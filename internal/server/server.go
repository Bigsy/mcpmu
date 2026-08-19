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
	ConfigPath         string        // Expanded path for hot-reload watching (empty = no watching)
	PIDTrackerDir      string        // Directory for per-owner PID registries (empty = derive from ConfigPath or default)
	Namespace          string        // Namespace to expose (empty = auto-select)
	EagerStart         bool          // Pre-start all servers
	ExposeManagerTools bool          // Include mcpmu.* tools in tools/list
	ExposeResources    bool          // Passthrough resources/* from upstream servers
	ExposePrompts      bool          // Passthrough prompts/* from upstream servers
	DebounceDelay      time.Duration // Delay before applying config changes (default: 150ms)
	LogLevel           string
	Stdin              io.Reader
	Stdout             io.Writer
	Stderr             io.Writer
	ServerName         string
	ServerVersion      string
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
	s := &Session{
		Core:        core,
		id:          core.newSessionID(),
		opts:        opts,
		reader:      bufio.NewReader(opts.Stdin),
		writer:      opts.Stdout,
		subs:        make(map[string]process.InstanceID),
		resourceMap: make(map[string]process.InstanceID),
		inflight:    newInflightCalls(),
		progress:    newProgressRoutes(),
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
		// Abandoning in-flight calls at disconnect would leave the upstream
		// working on results nobody will read. Only this session's calls are
		// cancelled; another session against the same shared instance keeps
		// running.
		s.inflight.cancelAll(errSessionClosed)
		s.progress.clear()
		s.resourceStateMu.RLock()
		s.cleanupSessionSubscriptions(s)
		s.resourceStateMu.RUnlock()
		if s.unsubscribeNotifications != nil {
			s.unsubscribeNotifications()
		}
		s.unregisterSession(s)
		s.instanceMu.Lock()
		s.supervisor.StopSessionInstances(s.id)
		s.instanceMu.Unlock()
	})
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
	defer s.shutdown()
	// Wait for in-flight handler goroutines to finish before returning.
	// Callers (and tests) typically read the stdout buffer after Run exits;
	// if handlers were still writing, that would be a data race.
	defer s.handlersWG.Wait()

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
	go func() {
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
	}()

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
		s.handlersWG.Go(func() {
			defer release()
			if resp, ok := s.Dispatch(callCtx, msg); ok {
				s.send(resp)
			}
		})
		return nil
	}

	if resp, ok := s.Dispatch(ctx, msg); ok {
		s.send(resp)
	}
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
			go s.startEagerServers(ctx)
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

// handleToolsList handles the tools/list request.
func (s *Server) handleToolsList(ctx context.Context) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
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
	graceCtx, cancel := context.WithTimeout(context.Background(), gracePeriod)
	defer cancel()
	tools := s.listTools(graceCtx, activeServerNames)
	if s.opts.ExposeManagerTools {
		tools = append(tools, s.currentAggregator().ManagerTools()...)
	}

	// If any servers didn't finish in time, continue in the background.
	// Pass the caller's snapshot of activeServerNames so the goroutine
	// doesn't re-read state that a concurrent reload could change.
	stillPending := s.pendingServers(activeServerNames)
	if len(stillPending) > 0 && s.bgDiscovering.CompareAndSwap(false, true) {
		go s.discoverAndNotify(stillPending)
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
	tools = filtered

	return toolsListResult{Tools: tools}, nil
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
		// writeMu and the reader must stay responsive. Tracked via
		// handlersWG so Run() doesn't return with a notification write
		// still in flight (otherwise callers reading the stdout buffer
		// after Run exits would race with the write).
		s.handlersWG.Go(func() {
			s.sendNotificationWithParams("notifications/resources/updated", map[string]string{"uri": p.URI})
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
		s.handlersWG.Go(func() {
			s.sendNotificationWithParams("notifications/progress", params)
		})
	default:
		if notification.Method == "notifications/tools/list_changed" ||
			(notification.Method == "notifications/resources/list_changed" && s.opts.ExposeResources) ||
			(notification.Method == "notifications/prompts/list_changed" && s.opts.ExposePrompts) {
			s.handlersWG.Go(func() { s.sendNotification(notification.Method) })
			return
		}
		if DebugLogging {
			log.Printf("OnUpstreamNotification: dropping %s from %s (relay not implemented)", notification.Method, serverName)
		}
	}
}

// discoverAndNotify continues tool discovery for straggling servers in the background.
// It discovers pending servers concurrently and sends a notifications/tools/list_changed
// as soon as the first straggler succeeds, so the client can refresh promptly instead of
// waiting for all servers (including broken ones) to time out.
//
// pendingNames is the set of servers that were still pending when the grace period expired.
func (s *Server) discoverAndNotify(pendingNames []string) {
	defer s.bgDiscovering.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultToolDiscoveryTimeout)
	defer cancel()

	// Channel signals when any single server finishes discovery successfully.
	notify := make(chan struct{}, 1)
	notified := false

	var wg sync.WaitGroup
	for _, name := range pendingNames {
		wg.Add(1)
		go func(serverName string) {
			defer wg.Done()

			tools, err := s.aggregatorForServer(serverName).DiscoverServer(ctx, serverName)
			if err != nil {
				log.Printf("Background discovery failed for %s: %v", serverName, err)
				return
			}
			log.Printf("Background discovery succeeded for %s (%d tools)", serverName, len(tools))

			// Signal that at least one server made progress
			select {
			case notify <- struct{}{}:
			default:
			}
		}(name)
	}

	// Wait for first success (or all to finish)
	go func() {
		wg.Wait()
		close(notify)
	}()

	for range notify {
		if !notified {
			notified = true
			s.sendNotification("notifications/tools/list_changed")
			log.Printf("Sent tools/list_changed notification (background discovery made progress)")
		}
	}

	if !notified {
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

	// Parse tool name to check namespace enforcement
	serverName, _, isManager := ParseToolName(req.Name)

	// Manager tools are always allowed
	if !isManager && serverName != "" {
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

// handleResourcesList handles the resources/list request.
func (s *Server) handleResourcesList(ctx context.Context) (any, *RPCError) {
	s.resourceStateMu.RLock()
	defer s.resourceStateMu.RUnlock()

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
		go func(resultIndex int, serverName string) {
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
		}(index, name)
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
	s.resourceMapMu.Lock()
	s.resourceMap = owners
	s.resourceMapMu.Unlock()
	return struct {
		Resources []listedResource `json:"resources"`
	}{Resources: allResources}, nil
}

// handleResourcesRead handles the resources/read request.
func (s *Server) handleResourcesRead(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.resourceStateMu.RLock()
	defer s.resourceStateMu.RUnlock()

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

// handleResourcesSubscribe handles the resources/subscribe request.
func (s *Server) handleResourcesSubscribe(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.resourceStateMu.RLock()
	defer s.resourceStateMu.RUnlock()

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
	s.resourceStateMu.RLock()
	defer s.resourceStateMu.RUnlock()

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
		go func(resultIndex int, serverName string) {
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
		}(index, name)
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
	c.resourceStateMu.Lock()
	defer c.resourceStateMu.Unlock()

	log.Printf("Applying config reload: %d servers, %d namespaces",
		len(newCfg.Servers), len(newCfg.Namespaces))

	// Clear subscription tracking before StopAll: closing the upstream
	// transport ends the upstream-side subscription cleanly, so we only
	// need to drop our local bookkeeping. No per-URI unsubscribe RPC is
	// attempted — it would race with shutdown.
	c.clearResourceStateForReload()

	// Advance the config generation before stopping instances. Any stale
	// get-or-start path must revalidate under its instance lifecycle lock.
	c.replaceConfig(newCfg)

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
		go session.startEagerServers(ctx)
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
