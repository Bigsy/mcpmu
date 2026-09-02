package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/fsnotify/fsnotify"
)

// DebugLogging enables verbose payload logging (Recv/Send messages).
var DebugLogging bool

// Options configures the MCP server.
type Options struct {
	Config             *config.Config
	ConfigPath         string // Expanded path for hot-reload watching (empty = no watching)
	PIDTrackerDir      string // Directory for per-owner PID registries (empty = derive from ConfigPath or default)
	Namespace          string // Namespace to expose (empty = auto-select)
	EagerStart         bool   // Pre-start all servers
	ExposeManagerTools bool   // Include mcpmu.* tools in tools/list
	ExposeResources    bool   // Passthrough resources/* from upstream servers
	ExposePrompts      bool   // Passthrough prompts/* from upstream servers
	// Compression replaces tools/list with the list_tools/get_tool_schema/
	// invoke_tool wrapper surface. Per-Session, not per-Core: two sessions
	// against one daemon can run different levels. It is the tri-state
	// --compress flag: unset defers to the active namespace's configured level,
	// an explicit level or an explicit off wins over it (see
	// Session.compressionLevel).
	Compression   config.CompressionOverride
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

// Server is retained as the embedded-serve API name. It is exactly one
// Session attached to one in-process Core.
type Server = Session

// New creates a new MCP server.
func New(opts Options) (*Server, error) {
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

func (s *Session) replacePrivateAggregator() {
	aggregator := s.newPrivateAggregator()
	s.privateAggregatorMu.Lock()
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

// readResult holds a line read from stdin and any error.
type readResult struct {
	line []byte
	err  error
}

// Run starts the server and processes requests until context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Run's ctx ending (SIGTERM, client hang-up) cancels the session
	// lifetime so in-flight handlers and background discovery stop promptly.
	stopLifetime := context.AfterFunc(ctx, s.cancelLifetime)
	defer func() {
		stopLifetime()
		s.cancelLifetime()
		// Detach from upstream notifications *before* waiting: a notification
		// arriving mid-drain would otherwise Add to handlersWG while Wait is
		// running. Unsubscribe returns only once no delivery is in progress.
		s.detachNotifications()
		// Wait for in-flight handler goroutines to finish before returning.
		// Callers (and tests) typically read the stdout buffer after Run
		// exits; if handlers were still writing, that would be a data race.
		s.handlersWG.Wait()
		s.shutdown()
	}()

	// Start config file watcher if ConfigPath is set
	s.startWatching(ctx)
	reloadCh := s.reloadCh
	if s.sharedReload.Load() {
		// Daemon mode has one Core-owned reload consumer. Letting every Session
		// receive from this channel would update only whichever Session won.
		reloadCh = nil
	}

	// Start a goroutine to read lines from stdin
	lines := make(chan readResult)
	goSafe("stdin reader", func() {
		defer close(lines)
		for {
			line, err := s.reader.ReadBytes('\n')
			if len(line) > 0 {
				// ReadBytes buffer is only valid until the next read, so clone it.
				line = append([]byte(nil), line...)
			}
			select {
			case lines <- readResult{line, err}:
				if err != nil {
					return // Stop reading on error (including EOF)
				}
			case <-ctx.Done():
				return // Stop reading when context is cancelled
			}
		}
	})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case newCfg := <-reloadCh:
			// Config file changed - apply reload
			s.applyReload(ctx, newCfg)

		case r, ok := <-lines:
			if !ok {
				// Channel closed, reader goroutine exited
				return nil
			}

			// Process any data we got, even if there's an error (e.g., EOF without newline)
			line := bytes.TrimSpace(r.line)
			if len(line) > 0 {
				if msgErr := s.handleMessage(ctx, line); msgErr != nil {
					log.Printf("Error handling message: %v", msgErr)
				}
			}

			// Handle the read error
			if r.err != nil {
				if r.err == io.EOF {
					log.Println("Client closed connection (EOF)")
					return nil
				}
				return fmt.Errorf("read request: %w", r.err)
			}
		}
	}
}

// isUpstreamMethod reports whether a JSON-RPC method dispatches a request to
// an upstream MCP server and therefore can block for an arbitrary time. We
// run these in goroutines so a slow upstream on one request doesn't freeze
// the main loop and starve every other pending request.
func isUpstreamMethod(method string) bool {
	switch method {
	case "tools/call", "resources/read", "prompts/get",
		"resources/subscribe", "resources/unsubscribe":
		return true
	}
	return false
}

// ParseMessage validates one JSON-RPC frame. A non-nil *RPCError means
// "reply with this parse error (null id)".
func ParseMessage(data []byte) (RPCMessage, *RPCError) {
	var msg RPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return RPCMessage{}, ErrParseError(err.Error())
	}
	// Shape validation beyond syntax: without it `{}` dispatches as a
	// notification for method "" and quietly succeeds (202 over HTTP). On a
	// shape error the partially-decoded msg is returned alongside, so the
	// caller can echo msg.ID in the error response when one was present.
	if msg.JSONRPC != "2.0" {
		return msg, ErrInvalidRequest(fmt.Sprintf("jsonrpc must be \"2.0\", got %q", msg.JSONRPC))
	}
	if msg.Method == "" && !msg.IsResponse() {
		return msg, ErrInvalidRequest("message has no method and is not a response")
	}
	return msg, nil
}

// Dispatch routes one parsed message and returns the response value.
// hasResponse is false for notifications. Dispatch never spawns goroutines
// and never writes to the session's writer — concurrency and delivery stay
// with the transport (the stdio Run loop, the HTTP POST handler).
func (s *Session) Dispatch(ctx context.Context, msg RPCMessage) (RPCResponse, bool) {
	if msg.ID == nil {
		if err := s.handleNotification(ctx, msg.Method, msg.Params); err != nil {
			log.Printf("Error handling notification %s: %v", msg.Method, err)
		}
		return RPCResponse{}, false
	}
	result, rpcErr := s.handleRequest(ctx, msg.Method, msg.Params)
	if rpcErr != nil {
		return RPCResponse{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}, true
	}
	resultJSON, _ := json.Marshal(result)
	return RPCResponse{JSONRPC: "2.0", ID: msg.ID, Result: resultJSON}, true
}

// TrackRequest registers a request with the session's in-flight table so a
// later notifications/cancelled naming its id can cancel the returned
// context. The release func unregisters the entry; callers must defer it.
// Register before dispatching so a cancellation that arrives immediately
// after the request cannot miss it.
func (s *Session) TrackRequest(ctx context.Context, id json.RawMessage) (context.Context, func()) {
	return s.inflight.track(ctx, id)
}

// handleMessage parses and routes a JSON-RPC message.
func (s *Server) handleMessage(ctx context.Context, data []byte) error {
	if DebugLogging {
		log.Printf("Recv: %s", string(data))
	}

	msg, parseErr := ParseMessage(data)
	if parseErr != nil {
		// Echo the request id when the shape was decodable enough to carry
		// one; a syntax error responds with id null, per spec.
		s.sendError(msg.ID, parseErr)
		return nil
	}

	if msg.IsResponse() {
		// A client response. The server issues no server→client requests, so
		// there is nothing to correlate it with; replying would be a protocol
		// violation (a response to a response), so drop it.
		log.Printf("Dropping unexpected client response (id %s)", msg.ID)
		return nil
	}

	// Requests that dispatch to an upstream MCP server can block for a long
	// time (up to the per-server tool timeout). Run them in a goroutine so
	// the main loop stays free to handle other requests — otherwise one
	// wedged upstream would freeze every other tool call, list, or ping.
	// JSON-RPC correlates responses by id and send() serializes stdout
	// writes via writeMu, so concurrent handlers are safe.
	if msg.ID != nil && isUpstreamMethod(msg.Method) {
		// Register the call before the goroutine starts so a cancellation that
		// arrives immediately after the request cannot miss it.
		callCtx, release := s.TrackRequest(ctx, msg.ID)
		s.spawnRequest(msg.ID, "handler "+msg.Method, func(context.Context) error {
			defer release()
			if resp, ok := s.Dispatch(callCtx, msg); ok {
				s.send(resp)
			}
			return nil
		})
		return nil
	}

	s.protect("handler "+msg.Method, msg.ID, func(context.Context) error {
		if resp, ok := s.Dispatch(ctx, msg); ok {
			s.send(resp)
		}
		return nil
	})
	return nil
}

// handleRequest processes a JSON-RPC request and returns a result or error.
func (s *Server) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case "initialize":
		return s.handleInitialize(ctx, params)
	case "ping":
		return s.handlePing(ctx)
	case "tools/list":
		return s.handleToolsList(ctx)
	case "tools/call":
		return s.handleToolsCall(ctx, params)
	case "resources/list":
		if !s.opts.ExposeResources {
			return nil, ErrMethodNotFound(method)
		}
		return s.handleResourcesList(ctx)
	case "resources/read":
		if !s.opts.ExposeResources {
			return nil, ErrMethodNotFound(method)
		}
		return s.handleResourcesRead(ctx, params)
	case "resources/subscribe":
		if !s.opts.ExposeResources {
			return nil, ErrMethodNotFound(method)
		}
		return s.handleResourcesSubscribe(ctx, params)
	case "resources/unsubscribe":
		if !s.opts.ExposeResources {
			return nil, ErrMethodNotFound(method)
		}
		return s.handleResourcesUnsubscribe(ctx, params)
	case "resources/templates/list":
		if !s.opts.ExposeResources {
			return nil, ErrMethodNotFound(method)
		}
		return struct {
			ResourceTemplates []any `json:"resourceTemplates"`
		}{ResourceTemplates: []any{}}, nil
	case "prompts/list":
		if !s.opts.ExposePrompts {
			return nil, ErrMethodNotFound(method)
		}
		return s.handlePromptsList(ctx)
	case "prompts/get":
		if !s.opts.ExposePrompts {
			return nil, ErrMethodNotFound(method)
		}
		return s.handlePromptsGet(ctx, params)
	default:
		return nil, ErrMethodNotFound(method)
	}
}

// handleNotification processes a JSON-RPC notification.
func (s *Server) handleNotification(ctx context.Context, method string, params json.RawMessage) error {
	switch method {
	case "notifications/initialized":
		log.Println("Client sent initialized notification")
		// Start eager servers if configured
		if s.opts.EagerStart {
			s.spawn("eager start", func(lifetime context.Context) error {
				startCtx, cancel := joinContext(ctx, lifetime)
				defer cancel()
				s.startEagerServers(startCtx)
				return nil
			})
		}
	case "notifications/cancelled":
		s.handleCancelled(params)
	default:
		log.Printf("Unknown notification: %s", method)
	}
	return nil
}

// handleCancelled acts on notifications/cancelled from the client.
//
// Cancelling the local context is only half the job: the upstream server is
// still working, still holding whatever the call reserved, and still on course
// to produce the side effect the user was trying to stop. mcp.Client sends its
// own notifications/cancelled upstream when a call's context ends, so
// cancelling here reaches the far end too.
func (s *Server) handleCancelled(params json.RawMessage) {
	var req struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(params, &req); err != nil || len(req.RequestID) == 0 {
		log.Printf("Ignoring malformed cancellation notification: %s", string(params))
		return
	}
	if s.inflight.cancel(req.RequestID, &cancelledError{reason: req.Reason}) {
		log.Printf("Cancelled request %s (reason: %s)", string(req.RequestID), reasonOrNone(req.Reason))
		return
	}
	// Not an error: the spec expects races here — the response may already
	// have been sent, or the id may never have named an upstream request.
	log.Printf("Cancellation for unknown or already-finished request %s (reason: %s)",
		string(req.RequestID), reasonOrNone(req.Reason))
}

func reasonOrNone(reason string) string {
	if reason == "" {
		return "none given"
	}
	return reason
}

// handleInitialize handles the initialize request.
func (s *Server) handleInitialize(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return nil, ErrInvalidRequest("already initialized")
	}

	var req initializeRequest
	if params != nil {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, ErrInvalidParams(err.Error())
		}
	}

	log.Printf("Initialize request from %s %s (protocol: %s)",
		req.ClientInfo.Name, req.ClientInfo.Version, req.ProtocolVersion)

	// Resolve namespace
	if err := s.resolveNamespace(); err != nil {
		return nil, err
	}

	// Update router with active namespace info
	s.router.SetActiveNamespace(s.activeNamespaceName, s.selectionMethod)

	s.protocolVersion = negotiateProtocolVersion(req.ProtocolVersion)
	s.initialized = true

	// Build capabilities
	caps := capabilities{
		Tools: &toolsCapability{ListChanged: true},
	}
	if s.opts.ExposeResources {
		// Advertise subscribe optimistically — capabilities are returned at
		// initialize before any upstream is started. Per-URI enforcement in
		// handleResourcesSubscribe returns a clean error if the owning
		// upstream doesn't support subscribe.
		caps.Resources = &resourcesCapability{ListChanged: true, Subscribe: true}
	}
	if s.opts.ExposePrompts {
		caps.Prompts = &promptsCapability{ListChanged: true}
	}

	// Return server capabilities
	return initializeResult{
		ProtocolVersion: s.protocolVersion,
		ServerInfo: serverInfo{
			Name:    s.opts.ServerName,
			Version: s.opts.ServerVersion,
		},
		Capabilities: caps,
	}, nil
}

// NegotiatedProtocolVersion returns the MCP revision this session settled on
// during initialize, or the empty string before initialize completes.
func (s *Session) NegotiatedProtocolVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.protocolVersion
}

// handlePing handles the ping request.
func (s *Server) handlePing(ctx context.Context) (any, *RPCError) {
	return struct{}{}, nil
}

// compressionLevel resolves the effective compression level for this session
// right now. An explicit --compress flag wins in both directions: a level
// forces compression on, `--compress off` forces it off. Otherwise the active
// namespace's configured "compression" applies. Resolved per request, not at
// session construction, because a hot reload can change both the active
// namespace and its configured level.
func (s *Session) compressionLevel() config.CompressionLevel {
	s.mu.RLock()
	activeNamespaceName := s.activeNamespaceName
	cfg := s.currentConfig()
	s.mu.RUnlock()
	ns, ok := cfg.GetNamespace(activeNamespaceName)
	if !ok {
		return s.opts.Compression.Resolve("")
	}
	return s.opts.Compression.Resolve(ns.Compression)
}

// handleToolsList handles the tools/list request.
func (s *Server) handleToolsList(ctx context.Context) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	s.mu.RUnlock()

	// Resolve the level once so the branch and the listing agree even if a
	// reload lands mid-request.
	compression := s.compressionLevel()

	// The listing runs under the session lifetime, not the request: tools/list
	// is handled synchronously on the main loop, and shutdown must cut the
	// discovery grace period short while a client cancelling its own tools/list
	// must not abort a server start other requests are waiting on.
	tools := s.visibleTools(s.lifetime)

	// Compressed surface: the client sees only the wrapper tools; the full
	// listing rides inside invoke_tool's description (and list_tools). Manager
	// tools stay real — they are already opt-in and tiny.
	if compression.Enabled() {
		wrappers := wrapperTools(formatListing(compression, tools))
		if s.opts.ExposeManagerTools {
			wrappers = append(wrappers, s.currentAggregator().ManagerTools()...)
		}
		return toolsListResult{Tools: wrappers}, nil
	}

	if s.opts.ExposeManagerTools {
		tools = append(tools, s.currentAggregator().ManagerTools()...)
	}
	return toolsListResult{Tools: tools}, nil
}

// visibleTools returns this session's permission-filtered upstream tools in
// stable order (namespace server order, then upstream order), waiting up to
// the grace period for discovery and continuing stragglers in the background.
// Manager tools are not included — exposure is the caller's choice. ctx bounds
// the grace period: handleToolsList passes the session lifetime, the async
// list_tools wrapper the request context (like any other tools/call).
func (s *Session) visibleTools(ctx context.Context) []AggregatedTool {
	s.mu.RLock()
	activeNamespaceName := s.activeNamespaceName
	activeServerNames := s.activeServerNames
	cfg := s.currentConfig()
	s.mu.RUnlock()

	// Discover tools with a grace period. ListTools starts servers
	// concurrently and returns whatever succeeds within the deadline.
	// Already-running servers with tools return instantly.
	gracePeriod := s.listToolsGracePeriod
	if gracePeriod == 0 {
		gracePeriod = ListToolsGracePeriod
	}
	graceCtx, cancel := context.WithTimeout(ctx, gracePeriod)
	defer cancel()
	tools := s.listTools(graceCtx, activeServerNames)

	// If any servers didn't finish in time, continue in the background.
	// Pass the caller's snapshot of activeServerNames so the goroutine
	// doesn't re-read state that a concurrent reload could change.
	stillPending := s.pendingServers(activeServerNames)
	if len(stillPending) > 0 && s.bgDiscovering.CompareAndSwap(false, true) {
		s.spawn("background discovery", func(ctx context.Context) error {
			s.discoverAndNotify(ctx, stillPending)
			return nil
		})
	}

	// Filter tools based on permissions (always runs — IsToolAllowed handles
	// global deny even without a namespace, and returns true for everything
	// else when namespace is empty)
	filtered := make([]AggregatedTool, 0, len(tools))
	for _, tool := range tools {
		serverName, toolName, isManager := ParseToolName(tool.Name)
		// Manager tools are always shown
		if isManager {
			filtered = append(filtered, tool)
			continue
		}
		// Check permission for regular tools
		allowed, _ := IsToolAllowed(cfg, activeNamespaceName, serverName, toolName)
		if allowed {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (s *Session) splitServersBySharing(serverNames []string) (shared, private []string) {
	for _, name := range serverNames {
		if s.instanceID(name).IsShared() {
			shared = append(shared, name)
		} else {
			private = append(private, name)
		}
	}
	return shared, private
}

func (s *Session) listTools(ctx context.Context, serverNames []string) []AggregatedTool {
	shared, private := s.splitServersBySharing(serverNames)
	var sharedTools, privateTools []AggregatedTool
	var wg sync.WaitGroup
	if len(shared) > 0 {
		wg.Go(func() { sharedTools, _ = s.currentAggregator().ListTools(ctx, shared) })
	}
	if len(private) > 0 {
		wg.Go(func() { privateTools, _ = s.privateAggregatorSnapshot().ListTools(ctx, private) })
	}
	wg.Wait()

	byServer := make(map[string][]AggregatedTool, len(serverNames))
	for _, tool := range append(sharedTools, privateTools...) {
		byServer[tool.serverName] = append(byServer[tool.serverName], tool)
	}
	result := make([]AggregatedTool, 0, len(sharedTools)+len(privateTools))
	for _, name := range serverNames {
		result = append(result, byServer[name]...)
	}
	return result
}

func (s *Session) pendingServers(serverNames []string) []string {
	shared, private := s.splitServersBySharing(serverNames)
	pending := s.currentAggregator().PendingServers(shared)
	return append(pending, s.privateAggregatorSnapshot().PendingServers(private)...)
}

// sendNotification sends a JSON-RPC notification (no ID, no response expected).
func (s *Server) sendNotification(method string) {
	s.sendNotificationWithParams(method, nil)
}

// sendNotificationWithParams sends a JSON-RPC notification with optional
// params (pass nil to omit the field entirely).
func (s *Server) sendNotificationWithParams(method string, params any) {
	type notifMsg struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	s.send(notifMsg{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

// OnUpstreamNotification implements mcp.NotificationSink. It runs on the
// upstream client's reader goroutine — must not block on stdout writes, so
// any downstream emission happens in a goroutine.
func (s *Server) OnUpstreamNotification(notification process.UpstreamNotification) {
	if !s.ownsInstance(notification.Instance) {
		return
	}
	serverName := notification.Instance.Server
	switch notification.Method {
	case "notifications/resources/updated":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(notification.Params, &p); err != nil || p.URI == "" {
			if DebugLogging {
				log.Printf("resources/updated: malformed params from %s: %v", serverName, err)
			}
			return
		}
		s.subMu.Lock()
		owner, ok := s.subs[p.URI]
		s.subMu.Unlock()
		if !ok || owner != notification.Instance {
			if DebugLogging {
				log.Printf("resources/updated: dropping stray notification for %q from %s (owner=%q, subscribed=%t)",
					p.URI, serverName, owner, ok)
			}
			return
		}
		// Dispatch off the reader goroutine — writing to stdout blocks on
		// writeMu and the reader must stay responsive. spawn tracks it via
		// handlersWG so Run() doesn't return with a notification write
		// still in flight (otherwise callers reading the stdout buffer
		// after Run exits would race with the write).
		s.spawn("relay resources/updated", func(context.Context) error {
			s.sendNotificationWithParams("notifications/resources/updated", map[string]string{"uri": p.URI})
			return nil
		})
	case "notifications/progress":
		// Filtered at the sink rather than routed by the broadcaster: only the
		// session that minted the token has it, so a fan-out to every session
		// still lands in exactly one place, and the broadcaster's ordering
		// guarantees are preserved.
		params, ok := s.progressNotificationForSession(notification.Params)
		if !ok {
			return
		}
		s.spawn("relay progress", func(context.Context) error {
			s.sendNotificationWithParams("notifications/progress", params)
			return nil
		})
	default:
		if notification.Method == "notifications/tools/list_changed" ||
			(notification.Method == "notifications/resources/list_changed" && s.opts.ExposeResources) ||
			(notification.Method == "notifications/prompts/list_changed" && s.opts.ExposePrompts) {
			s.spawn("relay "+notification.Method, func(context.Context) error {
				s.sendNotification(notification.Method)
				return nil
			})
			return
		}
		if DebugLogging {
			log.Printf("OnUpstreamNotification: dropping %s from %s (relay not implemented)", notification.Method, serverName)
		}
	}
}

// discoverAndNotify continues tool discovery for straggling servers in the background.
// It discovers pending servers concurrently and sends a notifications/tools/list_changed
// each time a straggler succeeds, so the client can refresh promptly without missing
// later successes or waiting for broken servers to time out.
//
// pendingNames is the set of servers that were still pending when the grace
// period expired. ctx is the session lifetime: shutdown ends discovery
// promptly instead of letting it run out the full discovery timeout.
func (s *Server) discoverAndNotify(ctx context.Context, pendingNames []string) {
	defer s.bgDiscovering.Store(false)

	ctx, cancel := context.WithTimeout(ctx, DefaultToolDiscoveryTimeout)
	defer cancel()

	// Buffer one completion per pending server. A non-blocking, single-slot
	// signal loses later completions when several stragglers finish close
	// together, leaving clients unaware of tools discovered after the first
	// notification.
	completed := make(chan string, len(pendingNames))

	var wg sync.WaitGroup
	for _, name := range pendingNames {
		wg.Add(1)
		goSafe("background discovery "+name, func() {
			defer wg.Done()

			tools, err := s.aggregatorForServer(name).DiscoverServer(ctx, name)
			if err != nil {
				log.Printf("Background discovery failed for %s: %v", name, err)
				return
			}
			log.Printf("Background discovery succeeded for %s (%d tools)", name, len(tools))

			completed <- name
		})
	}

	// Close the completion stream after every discovery attempt has finished.
	goSafe("background discovery join", func() {
		wg.Wait()
		close(completed)
	})

	notified := 0
	for serverName := range completed {
		notified++
		s.sendNotification("notifications/tools/list_changed")
		log.Printf("Sent tools/list_changed notification (background discovery completed for %s)", serverName)
	}

	if notified == 0 {
		log.Printf("Background discovery made no progress (%d still pending), skipping notification",
			len(pendingNames))
	}
}

// handleToolsCall handles the tools/call request.
func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	router := s.router
	s.mu.RUnlock()

	var req toolsCallRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Compressed surface: intercept the wrapper tools before ParseToolName.
	// Resolved once per request so the intercept and the listing a wrapper
	// renders agree.
	compression := s.compressionLevel()
	if compression.Enabled() {
		switch req.Name {
		case wrapperListTools:
			return s.handleListToolsWrapper(ctx, router, compression)
		case wrapperGetToolSchema:
			return s.handleGetToolSchemaWrapper(ctx, router, req.Arguments)
		case wrapperInvokeTool:
			var args invokeToolArgs
			if len(req.Arguments) > 0 {
				if err := json.Unmarshal(req.Arguments, &args); err != nil {
					return nil, ErrInvalidParams(wrapperInvokeTool + ": " + err.Error())
				}
			}
			if args.Tool == "" {
				return nil, ErrInvalidParams(wrapperInvokeTool + `: "tool" is required (qualified name like "server.tool_name")`)
			}
			// The client can no longer schema-validate before sending, so
			// catch a non-object input here with an error the model can act
			// on, instead of forwarding it into an opaque upstream failure.
			if input := bytes.TrimSpace(args.Input); len(input) > 0 &&
				input[0] != '{' && !bytes.Equal(input, []byte("null")) {
				return nil, ErrInvalidParams(wrapperInvokeTool + `: "input" must be a JSON object holding the tool's arguments`)
			}
			// Rewrite to the target and fall through: namespace enforcement,
			// permission checks, the retry path, and metrics recording all run
			// on the target tool with zero changes.
			req.Name = args.Tool
			req.Arguments = args.Input
		}
	}

	// Parse tool name to check namespace enforcement
	serverName, _, isManager := ParseToolName(req.Name)

	// A dotless non-manager name can never route — aggregated tools are
	// always qualified as "server.tool" — and letting it fall through used to
	// produce `Server not found: ""`, useless to a model reading the error.
	// Name the actual problem instead (matching resolveToolSchema's
	// deliberate divergence). A wrapper name reaching this point means
	// compression is off — typically a client cache made stale by a reload
	// that disabled it — so that error additionally says how to recover.
	if !isManager && serverName == "" {
		msg := fmt.Sprintf("Tool not found: %s (tool names are qualified as \"server.tool\")", req.Name)
		switch req.Name {
		case wrapperListTools, wrapperGetToolSchema, wrapperInvokeTool:
			msg = fmt.Sprintf("Tool not found: %s (compression is off on this session; call tools/list for the available tools)", req.Name)
		}
		return nil, NewRPCError(ErrCodeToolNotFound, msg, map[string]string{"toolName": req.Name})
	}

	// Manager tools are always allowed
	if !isManager {
		// Check if the server is in the active namespace
		allowed := slices.Contains(activeServerNames, serverName)
		if !allowed {
			return nil, ErrServerNotFound(serverName)
		}

		// Check if server is enabled
		srv, ok := s.currentConfig().GetServer(serverName)
		if !ok {
			return nil, ErrServerNotFound(serverName)
		}
		if !srv.IsEnabled() {
			return nil, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
		}
	}

	// Forward the client's `_meta` upstream, substituting a unique token for
	// any progressToken it carried. Without this the server is never asked for
	// progress at all, because the whole `_meta` object used to be dropped
	// here at unmarshal time.
	meta, releaseProgress := s.rewriteRequestMeta(req.Meta)
	defer releaseProgress()

	// Route the call through the router
	result, rpcErr := router.CallTool(ctx, req.Name, req.Arguments, meta)
	if rpcErr != nil {
		return nil, rpcErr
	}

	return result, nil
}

// handleListToolsWrapper serves the list_tools wrapper: the compact listing as
// a text result, rebuilt per call from the current config and catalog. router
// and level are the caller's snapshots — a reload can swap s.router and change
// the effective compression concurrently, so neither is re-read here.
func (s *Session) handleListToolsWrapper(ctx context.Context, router *Router, level config.CompressionLevel) (any, *RPCError) {
	start := time.Now()
	listing := formatListing(level, s.visibleTools(ctx))
	router.recordMeta(wrapperListTools, start, nil)
	return textResult(listing), nil
}

// handleGetToolSchemaWrapper serves the get_tool_schema wrapper. A single
// "tool" returns the full AggregatedTool, or the same RPC error the direct
// tools/call path would produce. A "tools" array returns one entry per name,
// reporting unknown or denied names per entry without failing the call.
// router is the caller's snapshot (see handleListToolsWrapper).
func (s *Session) handleGetToolSchemaWrapper(ctx context.Context, router *Router, arguments json.RawMessage) (any, *RPCError) {
	start := time.Now()
	fail := func(rpcErr *RPCError) (any, *RPCError) {
		router.recordMeta(wrapperGetToolSchema, start, rpcErr)
		return nil, rpcErr
	}
	var args getToolSchemaArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return fail(ErrInvalidParams(wrapperGetToolSchema + ": " + err.Error()))
		}
	}
	if (args.Tool != "") == (len(args.Tools) > 0) {
		return fail(ErrInvalidParams(wrapperGetToolSchema + `: pass exactly one of "tool" or "tools"`))
	}
	if args.Tool != "" {
		tool, rpcErr := s.resolveToolSchema(ctx, args.Tool)
		if rpcErr != nil {
			return fail(rpcErr)
		}
		result, rpcErr := schemaResult(tool)
		if rpcErr != nil {
			return fail(rpcErr)
		}
		router.recordMeta(wrapperGetToolSchema, start, nil)
		return result, nil
	}
	type schemaError struct {
		Tool  string    `json:"tool"`
		Error *RPCError `json:"error"`
	}
	entries := make([]any, 0, len(args.Tools))
	for _, name := range args.Tools {
		tool, rpcErr := s.resolveToolSchema(ctx, name)
		if rpcErr != nil {
			entries = append(entries, schemaError{Tool: name, Error: rpcErr})
			continue
		}
		entries = append(entries, tool)
	}
	result, rpcErr := schemaResult(struct {
		Tools []any `json:"tools"`
	}{Tools: entries})
	if rpcErr != nil {
		return fail(rpcErr)
	}
	router.recordMeta(wrapperGetToolSchema, start, nil)
	return result, nil
}

// schemaResult renders v as an indented JSON text block plus structuredContent.
func schemaResult(v any) (any, *RPCError) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, ErrInternalError("marshal tool schema: " + err.Error())
	}
	result := textResult(string(data))
	result.StructuredContent = data
	return result, nil
}

// resolveToolSchema looks up one qualified tool for get_tool_schema, applying
// the same namespace, enabled, and permission checks the direct tools/call
// path performs — denied tools are refused so the model never learns about
// tools it cannot call. A server not discovered yet (lazy start, straggler)
// is discovered under a timeout instead of returning not-found — the
// compressed analogue of lazy startup.
func (s *Session) resolveToolSchema(ctx context.Context, qualifiedName string) (AggregatedTool, *RPCError) {
	serverName, toolName, isManager := ParseToolName(qualifiedName)
	if isManager || serverName == "" {
		// Deliberate divergence from the direct path, which answers a dotless
		// name with ErrServerNotFound("") — tool-not-found names the actual
		// problem for a model reading the error. Manager tools are excluded by
		// design: they stay real tools with full schemas in tools/list.
		return AggregatedTool{}, ErrToolNotFound(qualifiedName)
	}
	s.mu.RLock()
	activeNamespaceName := s.activeNamespaceName
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()
	if !slices.Contains(activeServerNames, serverName) {
		return AggregatedTool{}, ErrServerNotFound(serverName)
	}
	cfg := s.currentConfig()
	srv, ok := cfg.GetServer(serverName)
	if !ok {
		return AggregatedTool{}, ErrServerNotFound(serverName)
	}
	if !srv.IsEnabled() {
		return AggregatedTool{}, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
	}
	if allowed, reason := IsToolAllowed(cfg, activeNamespaceName, serverName, toolName); !allowed {
		return AggregatedTool{}, ErrToolDenied(qualifiedName, reason)
	}
	agg := s.aggregatorForServer(serverName)
	if tool, ok := agg.GetTool(qualifiedName); ok {
		return tool, nil
	}
	// Discovery runs under the request context, exactly like the direct
	// tools/call lazy-start path — the client can cancel its own call.
	discoverCtx, cancel := context.WithTimeout(ctx, DefaultToolDiscoveryTimeout)
	defer cancel()
	if _, err := agg.DiscoverServer(discoverCtx, serverName); err != nil {
		return AggregatedTool{}, ErrServerFailedToStart(serverName, err.Error())
	}
	if tool, ok := agg.GetTool(qualifiedName); ok {
		return tool, nil
	}
	return AggregatedTool{}, ErrToolNotFound(qualifiedName)
}

// handleResourcesList handles the resources/list request.
//
// No resourceStateMu hold across this handler: it starts upstreams (up to
// StartupTimeout) and issues RPCs, and a global read lock here starved hot
// reload and Core.Close daemon-wide. Every structure touched below
// synchronizes on its own lock (s.mu, resourceMapMu, instance lifecycle), and
// the subscription table's epoch check discards work a concurrent reload
// invalidates.
func (s *Server) handleResourcesList(ctx context.Context) (any, *RPCError) {
	// Snapshot for the install guard at the bottom: a list that gathered its
	// routing table under the old config must not land after a reload wiped
	// this state.
	genAtEntry := s.currentConfigGeneration()

	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	type listedResource struct {
		URI         string          `json:"uri"`
		Name        string          `json:"name"`
		Title       string          `json:"title,omitempty"`
		Description string          `json:"description,omitempty"`
		MimeType    string          `json:"mimeType,omitempty"`
		Size        *int64          `json:"size,omitempty"`
		Annotations json.RawMessage `json:"annotations,omitempty"`
		Icons       json.RawMessage `json:"icons,omitempty"`
		Meta        json.RawMessage `json:"_meta,omitempty"`
	}

	type serverResources struct {
		resources []mcp.Resource
	}
	results := make([]serverResources, len(activeServerNames))
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup

	for index, name := range activeServerNames {
		if !s.aggregatorForServer(name).shouldQueryCapability(name, catalogResources) {
			continue
		}
		wg.Add(1)
		resultIndex, serverName := index, name
		goSafe("resources/list "+name, func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sc, rpcErr := s.getOrStartServer(ctx, serverName)
			if rpcErr != nil {
				log.Printf("Failed to get client for %s (resources/list): %v", serverName, rpcErr)
				return
			}

			callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
			defer cancel()

			resources, err := sc.client.ListResources(callCtx)
			if err != nil {
				log.Printf("Failed to list resources from %s: %v", serverName, err)
				return
			}

			results[resultIndex].resources = resources
		})
	}

	wg.Wait()

	allResources := make([]listedResource, 0)
	owners := make(map[string]process.InstanceID)
	for index, result := range results {
		serverName := activeServerNames[index]
		instance := s.instanceID(serverName)
		for _, r := range result.resources {
			if firstOwner, exists := owners[r.URI]; exists {
				log.Printf("resources/list URI collision for %q: keeping %s, omitting %s", r.URI, firstOwner, serverName)
				continue
			}
			owners[r.URI] = instance
			allResources = append(allResources, listedResource{
				URI:         r.URI,
				Name:        r.Name,
				Title:       r.Title,
				Description: r.Description,
				MimeType:    r.MimeType,
				Size:        r.Size,
				Annotations: r.Annotations,
				Icons:       r.Icons,
				Meta:        r.Meta,
			})
		}
	}
	// Install the routing table only if the config is still the one this list
	// ran against. The generation check and the write share resourceMapMu with
	// the reload's wipe of this session's state, and the reload bumps the
	// generation strictly before wiping — so either the install precedes the
	// wipe (and is cleaned up by it) or the check fails. A stale table cannot
	// survive the reload.
	s.resourceMapMu.Lock()
	if s.currentConfigGeneration() == genAtEntry {
		s.resourceMap = owners
	}
	s.resourceMapMu.Unlock()
	return struct {
		Resources []listedResource `json:"resources"`
	}{Resources: allResources}, nil
}

// handleResourcesRead handles the resources/read request. Like
// handleResourcesList, it holds no global lock across its upstream I/O.
func (s *Server) handleResourcesRead(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Look up which server owns this URI (populated by resources/list)
	s.resourceMapMu.RLock()
	instance, ok := s.resourceMap[req.URI]
	s.resourceMapMu.RUnlock()
	if !ok {
		return nil, ErrInvalidParams("unknown resource URI (has resources/list been called?): " + req.URI)
	}
	if !slices.Contains(activeServerNames, instance.Server) {
		return nil, ErrServerNotFound(instance.Server)
	}

	sc, rpcErr := s.getOrStartServer(ctx, instance.Server)
	if rpcErr != nil {
		return nil, rpcErr
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	contents, err := sc.client.ReadResource(callCtx, req.URI)
	if err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/read from %s: %v", instance, err))
	}

	return struct {
		Contents json.RawMessage `json:"contents"`
	}{Contents: contents}, nil
}

// handleResourcesSubscribe handles the resources/subscribe request. The
// subscribe transition itself is serialized per key and epoch-checked inside
// resourceSubscriptions, so no global lock is held across the upstream RPC.
func (s *Server) handleResourcesSubscribe(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if req.URI == "" {
		return nil, ErrInvalidParams("missing uri")
	}

	s.resourceMapMu.RLock()
	instance, ok := s.resourceMap[req.URI]
	s.resourceMapMu.RUnlock()
	if !ok {
		return nil, ErrInvalidParams("unknown resource URI (has resources/list been called?): " + req.URI)
	}
	if !slices.Contains(activeServerNames, instance.Server) {
		return nil, ErrServerNotFound(instance.Server)
	}

	sc, rpcErr := s.getOrStartServer(ctx, instance.Server)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if sc.capabilities.Resources == nil || !sc.capabilities.Resources.Subscribe {
		return nil, ErrMethodNotFound(fmt.Sprintf("upstream %s does not support resources/subscribe", instance))
	}

	key := resourceSubscriptionKey{Instance: instance, URI: req.URI}
	if err := s.subscribeResource(ctx, s, key, sc); err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/subscribe on %s: %v", instance, err))
	}

	return struct{}{}, nil
}

// handleResourcesUnsubscribe handles the resources/unsubscribe request.
// Unknown URIs are treated as idempotent success — clients often unsubscribe
// defensively, and the URI may have been evicted by a concurrent resources/list.
func (s *Server) handleResourcesUnsubscribe(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	s.mu.RUnlock()

	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if req.URI == "" {
		return nil, ErrInvalidParams("missing uri")
	}

	// Prefer s.subs for lookup (client may unsubscribe after a list refresh
	// evicted resourceMap); fall back to resourceMap.
	s.subMu.Lock()
	instance, known := s.subs[req.URI]
	s.subMu.Unlock()
	if !known {
		s.resourceMapMu.RLock()
		mappedInstance, ok := s.resourceMap[req.URI]
		s.resourceMapMu.RUnlock()
		if ok {
			instance = mappedInstance
			known = true
		}
	}
	if !known {
		// Idempotent: client cleanup on an unknown URI is not an error.
		return struct{}{}, nil
	}

	// Unsubscribe is always a local success. The Core sends an upstream RPC
	// only for the 1→0 transition and logs (rather than surfacing) any failure.
	// If the namespace changed, the same removal path skips a dead/missing
	// upstream while still clearing retained intent.
	s.unsubscribeResource(ctx, s, resourceSubscriptionKey{Instance: instance, URI: req.URI})

	return struct{}{}, nil
}

// handlePromptsList handles the prompts/list request.
func (s *Server) handlePromptsList(ctx context.Context) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	type qualifiedPrompt struct {
		Name        string               `json:"name"`
		Description string               `json:"description,omitempty"`
		Arguments   []mcp.PromptArgument `json:"arguments,omitempty"`
	}

	results := make([][]mcp.Prompt, len(activeServerNames))
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup

	for index, name := range activeServerNames {
		if !s.aggregatorForServer(name).shouldQueryCapability(name, catalogPrompts) {
			continue
		}
		wg.Add(1)
		resultIndex, serverName := index, name
		goSafe("prompts/list "+name, func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sc, rpcErr := s.getOrStartServer(ctx, serverName)
			if rpcErr != nil {
				log.Printf("Failed to get client for %s (prompts/list): %v", serverName, rpcErr)
				return
			}

			callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
			defer cancel()

			prompts, err := sc.client.ListPrompts(callCtx)
			if err != nil {
				log.Printf("Failed to list prompts from %s: %v", serverName, err)
				return
			}

			results[resultIndex] = prompts
		})
	}

	wg.Wait()

	allPrompts := make([]qualifiedPrompt, 0)
	for index, prompts := range results {
		serverName := activeServerNames[index]
		for _, p := range prompts {
			desc := p.Description
			if desc != "" {
				desc = fmt.Sprintf("[%s] %s", serverName, desc)
			} else {
				desc = fmt.Sprintf("[%s]", serverName)
			}
			allPrompts = append(allPrompts, qualifiedPrompt{
				Name:        serverName + "." + p.Name,
				Description: desc,
				Arguments:   p.Arguments,
			})
		}
	}
	return struct {
		Prompts []qualifiedPrompt `json:"prompts"`
	}{Prompts: allPrompts}, nil
}

// handlePromptsGet handles the prompts/get request.
func (s *Server) handlePromptsGet(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	var req struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Split on first '.' to extract server name and original prompt name
	serverName, originalName, ok := strings.Cut(req.Name, ".")
	if !ok || serverName == "" || originalName == "" {
		return nil, ErrInvalidParams("invalid prompt name: " + req.Name)
	}

	if !slices.Contains(activeServerNames, serverName) {
		return nil, ErrServerNotFound(serverName)
	}

	sc, rpcErr := s.getOrStartServer(ctx, serverName)
	if rpcErr != nil {
		return nil, rpcErr
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	messages, err := sc.client.GetPrompt(callCtx, originalName, req.Arguments)
	if err != nil {
		return nil, ErrInternalError(fmt.Sprintf("prompts/get from %s: %v", serverName, err))
	}

	return struct {
		Messages json.RawMessage `json:"messages"`
	}{Messages: messages}, nil
}

// resolveNamespace determines which namespace to use and which servers are active.
func (s *Server) resolveNamespace() *RPCError {
	cfg := s.currentConfig()
	name, servers, method, rpcErr := resolveNamespaceSelection(cfg, s.opts.Namespace)
	if rpcErr != nil {
		return rpcErr
	}
	s.activeNamespaceName = name
	s.activeServerNames = servers
	s.selectionMethod = method
	switch method {
	case SelectionFlag:
		log.Printf("Using namespace %q with %d servers (selection: flag)", name, len(servers))
	case SelectionDefault:
		log.Printf("Using default namespace %q with %d servers (selection: default)", name, len(servers))
	case SelectionOnly:
		log.Printf("Using only namespace %q with %d servers (selection: only)", name, len(servers))
	case SelectionAll:
		log.Printf("No namespaces configured, exposing all %d enabled servers (selection: all)", len(servers))
	}
	return nil
}

func resolveNamespaceSelection(cfg *config.Config, namespaceArg string) (string, []string, SelectionMethod, *RPCError) {

	// Rule 1: If --namespace provided, use it (lookup by name)
	if namespaceArg != "" {
		if ns, exists := cfg.Namespaces[namespaceArg]; exists {
			return namespaceArg, slices.Clone(ns.ServerIDs), SelectionFlag, nil
		}
		return "", nil, "", ErrNamespaceNotFound(namespaceArg)
	}

	// Rule 2: If config.defaultNamespace is set, use it
	if cfg.DefaultNamespace != "" {
		if ns, exists := cfg.Namespaces[cfg.DefaultNamespace]; exists {
			return cfg.DefaultNamespace, slices.Clone(ns.ServerIDs), SelectionDefault, nil
		}
		return "", nil, "", ErrNamespaceNotFound(cfg.DefaultNamespace)
	}

	// Rule 3: If exactly 1 namespace, use it
	if len(cfg.Namespaces) == 1 {
		for name, ns := range cfg.Namespaces {
			return name, slices.Clone(ns.ServerIDs), SelectionOnly, nil
		}
	}

	// Rule 4: If 0 namespaces, expose all enabled servers
	if len(cfg.Namespaces) == 0 {
		servers := make([]string, 0, len(cfg.Servers))
		for name, srv := range cfg.Servers {
			if srv.IsEnabled() {
				servers = append(servers, name)
			}
		}
		slices.Sort(servers)
		return "", servers, SelectionAll, nil
	}

	// Rule 5: 2+ namespaces, none selected - fail
	return "", nil, "", NewRPCError(ErrCodeInvalidRequest,
		fmt.Sprintf("Multiple namespaces configured (%d), but none selected. Use --namespace to specify which namespace to expose.", len(cfg.Namespaces)),
		nil)
}

// startEagerServers starts all servers in the active namespace.
func (s *Server) startEagerServers(ctx context.Context) {
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
func (s *Server) shutdown() {
	log.Println("Shutting down server")
	s.Close()
	if s.ownsCore {
		s.Core.Close()
	}
}

// watchConfig watches the config file for changes and sends new config to reloadCh.
// It watches the parent directory (not the file) to handle atomic renames.
func (c *Core) watchConfig(ctx context.Context, configPath string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed to create config watcher: %v", err)
		return
	}
	defer func() { _ = watcher.Close() }()

	// Watch parent directory to catch atomic renames
	dir := filepath.Dir(configPath)
	filename := filepath.Base(configPath)

	if err := watcher.Add(dir); err != nil {
		log.Printf("Failed to watch config directory %s: %v", dir, err)
		return
	}

	log.Printf("Watching config file: %s", configPath)

	// Debounce timer
	debounceDelay := c.debounceDelay
	if debounceDelay == 0 {
		debounceDelay = 150 * time.Millisecond
	}
	var debounceTimer *time.Timer
	var debounceMu sync.Mutex

	triggerReload := func() {
		debounceMu.Lock()
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceDelay, func() {
			log.Printf("Config file changed, loading new config")

			// Load and parse before sending
			newCfg, err := config.LoadFrom(configPath)
			if err != nil {
				log.Printf("Failed to load config after change: %v (keeping current config)", err)
				return
			}

			// Send to reload channel (non-blocking with select to avoid deadlock if channel full)
			select {
			case c.reloadCh <- newCfg:
				log.Printf("Config reload queued")
			default:
				log.Printf("Config reload already pending, skipping")
			}
		})
		debounceMu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			debounceMu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceMu.Unlock()
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// Filter for our target file
			if filepath.Base(event.Name) != filename {
				continue
			}

			// React to write, create, rename, or remove events
			// Atomic writes show up as rename/create depending on OS/editor
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				log.Printf("Config file event: %s (%s)", event.Name, event.Op)
				triggerReload()
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Config watcher error: %v", err)
		}
	}
}

// applyReload applies a new configuration once at Core scope, then re-resolves
// every attached Session. Embedded mode has one Session; daemon mode's
// Core-owned watcher calls this directly for all live Sessions.
func (s *Server) applyReload(ctx context.Context, newCfg *config.Config) {
	s.Core.applyReload(ctx, newCfg, s)
}

func (c *Core) applyReload(ctx context.Context, newCfg *config.Config, initiator *Session) {
	// resourceStateMu excludes only the other writer (Core.Close): handlers
	// never take it, because they run upstream I/O that must not stall a
	// reload. The clearing below is internally synchronized; the epoch bump
	// inside subscriptions.clear() invalidates any operation still in flight
	// against the old generation.
	//
	// Ordering matters twice over. The generation advance must precede the
	// clearing: handleResourcesList guards its resourceMap install with an
	// entry-time generation snapshot, and "generation unchanged" implying
	// "not yet wiped" is what makes that guard sound. The clearing itself
	// must precede StopAll: closing the upstream transport ends the
	// upstream-side subscription cleanly, so only local bookkeeping is
	// dropped here — no per-URI unsubscribe RPC is attempted, it would race
	// with shutdown.
	log.Printf("Applying config reload: %d servers, %d namespaces",
		len(newCfg.Servers), len(newCfg.Namespaces))

	// Advance the config generation before stopping instances. Any stale
	// get-or-start path must revalidate under its instance lifecycle lock.
	c.replaceConfig(newCfg)

	c.resourceStateMu.Lock()
	c.clearResourceStateForReload()
	c.resourceStateMu.Unlock()

	// Stop all running servers after the generation barrier is visible.
	c.supervisor.StopAll()

	sessions := c.sessionSnapshot()
	if initiator != nil && !slices.Contains(sessions, initiator) {
		// A few focused tests construct a Session directly around a Core. Keep
		// the internal applyReload hook correct for that legacy arrangement.
		sessions = append(sessions, initiator)
	}
	eagerSessions := make([]*Session, 0, len(sessions))
	for _, session := range sessions {
		if len(session.applyReloadConfig(newCfg)) > 0 {
			eagerSessions = append(eagerSessions, session)
		}
	}

	for _, session := range eagerSessions {
		session.spawn("eager start after reload", func(lifetime context.Context) error {
			startCtx, cancel := joinContext(ctx, lifetime)
			defer cancel()
			session.startEagerServers(startCtx)
			return nil
		})
	}

	for _, session := range sessions {
		session.sendNotification("notifications/tools/list_changed")
		if session.opts.ExposeResources {
			session.sendNotification("notifications/resources/list_changed")
		}
		if session.opts.ExposePrompts {
			session.sendNotification("notifications/prompts/list_changed")
		}
	}

	log.Printf("Config reload complete")
}

func (s *Session) applyReloadConfig(newCfg *config.Config) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldNamespaceName := s.activeNamespaceName
	oldServerNames := slices.Clone(s.activeServerNames)
	oldSelectionMethod := s.selectionMethod

	// Re-resolve namespace
	// If namespace was selected by flag and still exists, keep it
	// If namespace was auto-selected and still valid, keep it
	// If namespace no longer exists, re-auto-select
	var keepNamespace bool
	if oldSelectionMethod == SelectionFlag && s.opts.Namespace != "" {
		// Try to find the namespace by the original flag value
		if ns, exists := newCfg.Namespaces[s.opts.Namespace]; exists {
			s.activeNamespaceName = s.opts.Namespace
			s.activeServerNames = slices.Clone(ns.ServerIDs)
			s.selectionMethod = SelectionFlag
			keepNamespace = true
		}
	} else if oldNamespaceName != "" {
		// Try to keep the same namespace by name
		if ns, exists := newCfg.Namespaces[oldNamespaceName]; exists {
			s.activeNamespaceName = oldNamespaceName
			s.activeServerNames = slices.Clone(ns.ServerIDs)
			s.selectionMethod = oldSelectionMethod
			keepNamespace = true
		}
	}

	if !keepNamespace {
		name, servers, method, err := resolveNamespaceSelection(newCfg, s.opts.Namespace)
		if err != nil {
			log.Printf("WARN: namespace resolution failed after reload, keeping previous config: %v", err)
			s.activeNamespaceName = oldNamespaceName
			s.activeServerNames = oldServerNames
			s.selectionMethod = oldSelectionMethod
		} else {
			s.activeNamespaceName = name
			s.activeServerNames = servers
			s.selectionMethod = method
		}
	} else {
		log.Printf("Kept namespace %q after reload with %d servers",
			s.activeNamespaceName, len(s.activeServerNames))
	}

	// Rebuild aggregator and router with new config. Swap under the write
	// lock so concurrently-running handlers see either the whole old pair or
	// the whole new pair, never a torn read.
	s.replacePrivateAggregator()
	newRouter := NewRouter(s)

	s.router = newRouter
	activeNsName := s.activeNamespaceName
	selMethod := s.selectionMethod

	newRouter.SetActiveNamespace(activeNsName, selMethod)
	if !s.opts.EagerStart {
		return nil
	}
	return slices.Clone(s.activeServerNames)
}

// sendError sends a JSON-RPC error response.
func (s *Server) sendError(id json.RawMessage, rpcErr *RPCError) {
	resp := RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcErr,
	}
	s.send(resp)
}

// send writes a JSON-RPC message to the session writer as exactly one Write
// call per frame (payload + trailing newline together), so a non-stdio writer
// like the daemon's queuedWriter or the HTTP sseHub receives whole frames.
func (s *Server) send(msg any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal response: %v", err)
		return
	}

	if DebugLogging {
		log.Printf("Send: %s", string(data))
	}

	if _, err := s.writer.Write(append(data, '\n')); err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}

// JSON-RPC message types

// RPCMessage is one incoming JSON-RPC frame. A nil ID marks a notification.
type RPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	// Result and Error are captured only to classify the frame: a message
	// with an id, no method, and one of these is a client's *response* to a
	// server→client request, not a request to dispatch.
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// IsResponse reports whether the frame is a JSON-RPC response.
func (m RPCMessage) IsResponse() bool {
	return m.Method == "" && m.ID != nil && (len(m.Result) > 0 || len(m.Error) > 0)
}

// RPCResponse is one outgoing JSON-RPC response frame.
type RPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	// No omitempty: a parse-error response has no id to echo and the spec
	// requires an explicit "id": null there, which is exactly how a nil
	// RawMessage marshals.
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type initializeRequest struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Capabilities    capabilities `json:"capabilities"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type capabilities struct {
	Tools     *toolsCapability     `json:"tools,omitempty"`
	Resources *resourcesCapability `json:"resources,omitempty"`
	Prompts   *promptsCapability   `json:"prompts,omitempty"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type resourcesCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
	Subscribe   bool `json:"subscribe,omitempty"`
}

type promptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type toolsListResult struct {
	Tools []AggregatedTool `json:"tools"`
}

type toolsCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Meta is the request envelope's `_meta`, most importantly progressToken.
	// It is forwarded upstream (with progressToken rewritten) rather than
	// dropped — a server that is never told a token can never report progress.
	Meta json.RawMessage `json:"_meta,omitempty"`
}
