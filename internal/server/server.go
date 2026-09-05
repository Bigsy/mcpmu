package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

// DebugLogging enables verbose payload logging (Recv/Send messages).
var DebugLogging bool

// SessionOptions are the per-downstream-connection settings that every serve
// entry point accepts — embedded stdio, a daemon session, an HTTP session —
// and that the shim carries to the daemon verbatim. This is the single
// definition; the JSON tags are the daemon handshake encoding, so they must
// not be renamed without a SessionProtocol bump.
type SessionOptions struct {
	Namespace          string `json:"namespace,omitempty"`          // Namespace to expose (empty = auto-select)
	EagerStart         bool   `json:"eager,omitempty"`              // Pre-start all servers
	ExposeManagerTools bool   `json:"exposeManagerTools,omitempty"` // Include mcpmu.* tools in tools/list
	ExposeResources    bool   `json:"resources,omitempty"`          // Passthrough resources/* from upstream servers
	ExposePrompts      bool   `json:"prompts,omitempty"`            // Passthrough prompts/* from upstream servers
	// Compression replaces tools/list with the list_tools/get_tool_schema/
	// invoke_tool wrapper surface. Per-Session, not per-Core: two sessions
	// against one daemon can run different levels. It is the tri-state
	// --compress flag: unset defers to the active namespace's configured level,
	// an explicit level or an explicit off wins over it (see
	// Session.compressionLevel).
	//
	// The tag has no omitempty because omitempty never applied to a struct
	// anyway: an unset override is sent as "", and an absent key decodes to
	// unset too, so a shim that never sent the field reads correctly either way.
	Compression config.CompressionOverride `json:"compression"`
}

// Options configures the MCP server.
type Options struct {
	SessionOptions
	Config        *config.Config
	ConfigPath    string        // Expanded path for hot-reload watching (empty = no watching)
	PIDTrackerDir string        // Directory for per-owner PID registries (empty = derive from ConfigPath or default)
	DebounceDelay time.Duration // Delay before applying config changes (default: 150ms)
	LogLevel      string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	ServerName    string
	ServerVersion string
}

// SelectionMethod indicates how the active namespace was selected.
type SelectionMethod string

const (
	SelectionFlag    SelectionMethod = "flag"    // --namespace flag
	SelectionDefault SelectionMethod = "default" // config.defaultNamespaceId
	SelectionOnly    SelectionMethod = "only"    // only one namespace exists
	SelectionAll     SelectionMethod = "all"     // no namespaces, all servers exposed
)

// Session is one downstream MCP connection. It owns negotiated protocol
// state, namespace selection, resource routing/subscriptions, and its JSON-RPC
// read/write loop while embedding the shared Core it operates against.
type Session struct {
	*Core
	id                       string
	opts                     Options
	router                   *Router
	privateAggregatorMu      sync.RWMutex
	privateAggregator        *Aggregator
	unsubscribeNotifications func()
	ownsCore                 bool
	closeOnce                sync.Once
	instanceMu               sync.RWMutex
	closed                   atomic.Bool

	// lifetime is a child of Core.lifetime; Run's ctx ending and Close both
	// cancel it. Every Session.spawn goroutine and every session-scoped
	// background upstream call runs under it, so SIGTERM stops in-flight
	// work instead of letting it linger on context.Background().
	lifetime       context.Context
	cancelLifetime context.CancelFunc

	// Active namespace (resolved at init)
	activeNamespaceName string          // Name of the active namespace
	activeServerNames   []string        // Server names in the active namespace (or all if no namespace)
	selectionMethod     SelectionMethod // How the namespace was selected

	// Protocol state. protocolVersion is the revision this session settled on
	// during initialize — per-session, not per-process, because two daemon
	// sessions against one Core may negotiate different revisions.
	initialized     bool
	protocolVersion string
	mu              sync.RWMutex

	// IO
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex

	// Tracks in-flight message-handler goroutines so Run() can wait for them
	// to drain before returning (otherwise stdout writes may race with the
	// caller reading the buffer after Run exits).
	handlersWG sync.WaitGroup

	// Per-session request lifecycle: cancellable upstream calls keyed by the
	// client's JSON-RPC id, and the progress-token substitutions in force.
	// Both are session-scoped so a shared upstream instance can never let one
	// agent cancel — or eavesdrop on the progress of — another's call.
	inflight *inflightCalls
	progress *progressRoutes

	// Background discovery
	bgDiscovering        atomic.Bool
	listToolsGracePeriod time.Duration // 0 means use ListToolsGracePeriod constant

	// Resource routing is rebuilt atomically from each deterministic
	// resources/list merge: original URI → first server in namespace order.
	resourceMapMu sync.RWMutex
	resourceMap   map[string]process.InstanceID

	// Active resource subscriptions: URI → upstream instance. The Core owns
	// daemon-wide refcounts; this per-session view filters notifications and
	// drives disconnect cleanup. Guarded by subMu.
	subMu sync.Mutex
	subs  map[string]process.InstanceID
}

// New creates a new MCP server: one Session attached to one in-process Core,
// which is exactly what the embedded stdio serve path is.
func New(opts Options) (*Session, error) {
	core, err := NewCore(opts)
	if err != nil {
		return nil, err
	}
	s, err := NewSession(core, opts)
	if err != nil {
		core.Close()
		return nil, err
	}
	s.ownsCore = true
	return s, nil
}

// NewSession binds one downstream connection to an existing Core.
func NewSession(core *Core, opts Options) (*Session, error) {
	lifetime, cancelLifetime := context.WithCancel(core.lifetime)
	s := &Session{
		Core:           core,
		lifetime:       lifetime,
		cancelLifetime: cancelLifetime,
		id:             core.newSessionID(),
		opts:           opts,
		reader:         bufio.NewReader(opts.Stdin),
		writer:         opts.Stdout,
		subs:           make(map[string]process.InstanceID),
		resourceMap:    make(map[string]process.InstanceID),
		inflight:       newInflightCalls(),
		progress:       newProgressRoutes(),
	}
	s.privateAggregator = s.newPrivateAggregator()
	s.router = NewRouter(s)
	unsubscribe, err := core.notifications.Subscribe(s)
	if err != nil {
		return nil, err
	}
	s.unsubscribeNotifications = unsubscribe
	core.registerSession(s)
	return s, nil
}

// Close detaches the Session from Core notifications. Core lifetime remains
// independent unless the Session was created by New for embedded serve.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.cancelLifetime()
		// Abandoning in-flight calls at disconnect would leave the upstream
		// working on results nobody will read. Only this session's calls are
		// cancelled; another session against the same shared instance keeps
		// running.
		s.inflight.cancelAll(errSessionClosed)
		s.progress.clear()
		// Unsubscribe RPCs run without resourceStateMu: the subscription table
		// is internally synchronized and epoch-invalidated by the writers that
		// used to exclude this path, and holding a global read lock across
		// per-URI upstream round trips stalled reload and Core.Close for the
		// length of every HTTP DELETE, idle reap, and shutdown teardown.
		s.cleanupSessionSubscriptions(s)
		s.detachNotifications()
		s.unregisterSession(s)
		s.instanceMu.Lock()
		s.supervisor.StopSessionInstances(s.id)
		s.instanceMu.Unlock()
	})
}

// detachNotifications stops upstream notification delivery to this session.
// Idempotent; returns only once no delivery is in progress.
func (s *Session) detachNotifications() {
	if s.unsubscribeNotifications != nil {
		s.unsubscribeNotifications()
	}
}

func (s *Session) privateAggregatorSnapshot() *Aggregator {
	s.privateAggregatorMu.RLock()
	defer s.privateAggregatorMu.RUnlock()
	return s.privateAggregator
}

func (s *Session) newPrivateAggregator() *Aggregator {
	aggregator := NewAggregator(s.currentConfig(), s.supervisor, false)
	aggregator.instanceFor = func(serverName string) process.InstanceID {
		return process.PrivateInstanceID(serverName, s.id)
	}
	aggregator.serverConfig = func(serverName string) (config.ServerConfig, bool) {
		return s.currentConfig().GetServer(serverName)
	}
	aggregator.acquire = func(ctx context.Context, serverName string) (*process.Handle, config.ServerConfig, error) {
		return s.getOrStartSessionInstance(ctx, process.PrivateInstanceID(serverName, s.id), serverName)
	}
	return aggregator
}

func (s *Session) replacePrivateAggregatorKeeping(keep bool) {
	aggregator := s.newPrivateAggregator()
	s.privateAggregatorMu.Lock()
	if keep && s.privateAggregator != nil {
		aggregator.catalog = s.privateAggregator.catalog
	}
	s.privateAggregator = aggregator
	s.privateAggregatorMu.Unlock()
}

func (s *Session) instanceID(serverName string) process.InstanceID {
	srv, ok := s.currentConfig().GetServer(serverName)
	if ok && !srv.IsShared() {
		return process.PrivateInstanceID(serverName, s.id)
	}
	return process.SharedInstanceID(serverName)
}

func (s *Session) ownsInstance(id process.InstanceID) bool {
	return id.IsShared() || id.Session == s.id
}

func (s *Session) aggregatorForServer(serverName string) *Aggregator {
	if s.instanceID(serverName).IsShared() {
		return s.currentAggregator()
	}
	return s.privateAggregatorSnapshot()
}

func (s *Session) getOrStartHandle(ctx context.Context, serverName string) (*process.Handle, config.ServerConfig, error) {
	return s.getOrStartSessionInstance(ctx, s.instanceID(serverName), serverName)
}

func (s *Session) getOrStartSessionInstance(ctx context.Context, id process.InstanceID, serverName string) (*process.Handle, config.ServerConfig, error) {
	s.instanceMu.RLock()
	defer s.instanceMu.RUnlock()
	if s.closed.Load() {
		return nil, config.ServerConfig{}, fmt.Errorf("session is closed")
	}
	return s.getOrStartInstance(ctx, id, serverName)
}

func (s *Session) restartHandle(ctx context.Context, serverName string) (*process.Handle, error) {
	s.instanceMu.RLock()
	defer s.instanceMu.RUnlock()
	if s.closed.Load() {
		return nil, fmt.Errorf("session is closed")
	}
	return s.restartInstance(ctx, s.instanceID(serverName), serverName)
}

func (s *Session) getOrStartServer(ctx context.Context, serverName string) (serverClient, *RPCError) {
	handle, srv, err := s.getOrStartHandle(ctx, serverName)
	if err != nil {
		cfg := s.currentConfig()
		if _, ok := cfg.GetServer(serverName); !ok {
			return serverClient{}, ErrServerNotFound(serverName)
		}
		if existing, ok := cfg.GetServer(serverName); ok && !existing.IsEnabled() {
			return serverClient{}, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
		}
		return serverClient{}, ErrServerFailedToStart(serverName, err.Error())
	}
	client := handle.Client()
	if client == nil {
		return serverClient{}, ErrServerNotRunning(serverName)
	}
	return serverClient{
		handle: handle, client: client,
		timeout:      time.Duration(srv.ToolTimeout()) * time.Second,
		capabilities: handle.Capabilities(),
	}, nil
}

func (s *Session) setSubscription(key resourceSubscriptionKey) {
	s.subMu.Lock()
	s.subs[key.URI] = key.Instance
	s.subMu.Unlock()
}

func (s *Session) deleteSubscription(key resourceSubscriptionKey) {
	s.subMu.Lock()
	if s.subs[key.URI] == key.Instance {
		delete(s.subs, key.URI)
	}
	s.subMu.Unlock()
}

func (s *Session) subscriptionSnapshot() map[string]process.InstanceID {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return maps.Clone(s.subs)
}

func (s *Session) clearResourceState() {
	s.subMu.Lock()
	clear(s.subs)
	s.subMu.Unlock()
	s.resourceMapMu.Lock()
	clear(s.resourceMap)
	s.resourceMapMu.Unlock()
}

// startEagerServers starts all servers in the active namespace.
func (s *Session) startEagerServers(ctx context.Context) {
	s.mu.RLock()
	names := slices.Clone(s.activeServerNames)
	s.mu.RUnlock()
	log.Printf("Starting %d servers eagerly", len(names))
	for _, name := range names {
		_, ok := s.currentConfig().GetServer(name)
		if !ok {
			continue
		}
		if _, _, err := s.getOrStartHandle(ctx, name); err != nil {
			log.Printf("Failed to start server %s: %v", name, err)
		}
	}
}

// shutdown cleans up resources.
func (s *Session) shutdown() {
	log.Println("Shutting down server")
	s.Close()
	if s.ownsCore {
		s.Core.Close()
	}
}
