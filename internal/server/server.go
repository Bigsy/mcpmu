package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
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
	PIDTrackerDir      string        // Directory for PID tracking file (empty = derive from ConfigPath or default)
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
	ProtocolVersion    string
}

// SelectionMethod indicates how the active namespace was selected.
type SelectionMethod string

const (
	SelectionFlag    SelectionMethod = "flag"    // --namespace flag
	SelectionDefault SelectionMethod = "default" // config.defaultNamespaceId
	SelectionOnly    SelectionMethod = "only"    // only one namespace exists
	SelectionAll     SelectionMethod = "all"     // no namespaces, all servers exposed
)

// Server is an MCP server that aggregates tools from managed upstream servers.
type Server struct {
	opts       Options
	cfg        *config.Config
	bus        *events.Bus
	supervisor *process.Supervisor
	aggregator *Aggregator
	router     *Router

	// Active namespace (resolved at init)
	activeNamespaceName string          // Name of the active namespace
	activeServerNames   []string        // Server names in the active namespace (or all if no namespace)
	selectionMethod     SelectionMethod // How the namespace was selected

	// Protocol state
	initialized bool
	mu          sync.RWMutex

	// IO
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex

	// Tracks in-flight message-handler goroutines so Run() can wait for them
	// to drain before returning (otherwise stdout writes may race with the
	// caller reading the buffer after Run exits).
	handlersWG sync.WaitGroup

	// Background discovery
	bgDiscovering        atomic.Bool
	listToolsGracePeriod time.Duration // 0 means use ListToolsGracePeriod constant

	// Hot-reload
	reloadCh chan *config.Config // Serializes reload with request handling

	// Resource routing: maps original URI → server name (populated by resources/list)
	resourceMap sync.Map

	// Active resource subscriptions: URI → upstream server name. Populated
	// by successful resources/subscribe calls; cleared on unsubscribe and on
	// config reload. Guarded by subMu.
	subMu sync.Mutex
	subs  map[string]string
}

// New creates a new MCP server.
func New(opts Options) (*Server, error) {
	// Create event bus
	bus := events.NewBus()

	// Derive PID tracker directory from config path to isolate instances
	pidTrackerDir := opts.PIDTrackerDir
	if pidTrackerDir == "" && opts.ConfigPath != "" {
		pidTrackerDir = filepath.Dir(opts.ConfigPath)
	}

	// Create process supervisor with config-specified credential store
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode:     opts.Config.MCPOAuthCredentialStore,
		PIDTrackerDir:           pidTrackerDir,
		GlobalOAuthCallbackPort: opts.Config.MCPOAuthCallbackPort,
	})

	if opts.ConfigPath != "" {
		toolCache, err := config.NewToolCache(opts.ConfigPath)
		if err != nil {
			log.Printf("Warning: failed to initialize tool cache: %v", err)
		} else {
			supervisor.SetToolCache(toolCache)
		}
	}

	s := &Server{
		opts:       opts,
		cfg:        opts.Config,
		bus:        bus,
		supervisor: supervisor,
		reader:     bufio.NewReader(opts.Stdin),
		writer:     opts.Stdout,
		reloadCh:   make(chan *config.Config, 1), // Buffered to avoid blocking watcher
		subs:       make(map[string]string),
	}

	// Wire the server as the supervisor's notification sink before any
	// upstream client is constructed so every handler is installed the
	// moment Initialize completes.
	supervisor.SetNotificationSink(s)

	// Create aggregator and router (will be initialized after namespace selection)
	s.aggregator = NewAggregator(s.cfg, supervisor, opts.ExposeManagerTools)
	s.router = NewRouter(s.cfg, supervisor, s.aggregator)

	return s, nil
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
	if s.opts.ConfigPath != "" {
		go s.watchConfig(ctx, s.opts.ConfigPath)
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

		case newCfg := <-s.reloadCh:
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

// handleMessage parses and routes a JSON-RPC message.
func (s *Server) handleMessage(ctx context.Context, data []byte) error {
	if DebugLogging {
		log.Printf("Recv: %s", string(data))
	}

	var msg rpcMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		// Parse error - respond if we can extract an ID
		s.sendError(nil, ErrParseError(err.Error()))
		return nil
	}

	// Check if it's a notification (no ID)
	if msg.ID == nil {
		return s.handleNotification(ctx, msg.Method, msg.Params)
	}

	// Requests that dispatch to an upstream MCP server can block for a long
	// time (up to the per-server tool timeout). Run them in a goroutine so
	// the main loop stays free to handle other requests — otherwise one
	// wedged upstream would freeze every other tool call, list, or ping.
	// JSON-RPC correlates responses by id and send() serializes stdout
	// writes via writeMu, so concurrent handlers are safe.
	if isUpstreamMethod(msg.Method) {
		s.handlersWG.Go(func() {
			result, rpcErr := s.handleRequest(ctx, msg.Method, msg.Params)
			if rpcErr != nil {
				s.sendError(msg.ID, rpcErr)
			} else {
				s.sendResult(msg.ID, result)
			}
		})
		return nil
	}

	// It's a request - handle and respond
	result, rpcErr := s.handleRequest(ctx, msg.Method, msg.Params)
	if rpcErr != nil {
		s.sendError(msg.ID, rpcErr)
	} else {
		s.sendResult(msg.ID, result)
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
		// Handle cancellation - for now just log it
		log.Printf("Received cancellation notification: %s", string(params))
	default:
		log.Printf("Unknown notification: %s", method)
	}
	return nil
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
		ProtocolVersion: s.opts.ProtocolVersion,
		ServerInfo: serverInfo{
			Name:    s.opts.ServerName,
			Version: s.opts.ServerVersion,
		},
		Capabilities: caps,
	}, nil
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
	aggregator := s.aggregator
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
	tools, _ := aggregator.ListTools(graceCtx, activeServerNames)

	// If any servers didn't finish in time, continue in the background.
	// Pass the caller's snapshot of activeServerNames so the goroutine
	// doesn't re-read state that a concurrent reload could change.
	stillPending := aggregator.PendingServers(activeServerNames)
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
		allowed, _ := IsToolAllowed(s.cfg, activeNamespaceName, serverName, toolName)
		if allowed {
			filtered = append(filtered, tool)
		}
	}
	tools = filtered

	return toolsListResult{Tools: tools}, nil
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
func (s *Server) OnUpstreamNotification(serverName, method string, params json.RawMessage) {
	switch method {
	case "notifications/resources/updated":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
			if DebugLogging {
				log.Printf("resources/updated: malformed params from %s: %v", serverName, err)
			}
			return
		}
		s.subMu.Lock()
		owner, ok := s.subs[p.URI]
		s.subMu.Unlock()
		if !ok || owner != serverName {
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
	default:
		if DebugLogging {
			log.Printf("OnUpstreamNotification: dropping %s from %s (relay not implemented)", method, serverName)
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

	s.mu.RLock()
	aggregator := s.aggregator
	s.mu.RUnlock()

	// Channel signals when any single server finishes discovery successfully.
	notify := make(chan struct{}, 1)
	notified := false

	var wg sync.WaitGroup
	for _, name := range pendingNames {
		wg.Add(1)
		go func(serverName string) {
			defer wg.Done()

			tools, err := aggregator.DiscoverServer(ctx, serverName)
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
		srv, ok := s.cfg.GetServer(serverName)
		if !ok {
			return nil, ErrServerNotFound(serverName)
		}
		if !srv.IsEnabled() {
			return nil, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
		}
	}

	// Route the call through the router
	result, rpcErr := router.CallTool(ctx, req.Name, req.Arguments)
	if rpcErr != nil {
		return nil, rpcErr
	}

	return result, nil
}

// handleResourcesList handles the resources/list request.
func (s *Server) handleResourcesList(ctx context.Context) (any, *RPCError) {
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
	}

	var allResources []listedResource
	var mu sync.Mutex
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup

	for _, name := range activeServerNames {
		wg.Add(1)
		go func(serverName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sc, rpcErr := s.ensureServerClient(ctx, serverName)
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

			mu.Lock()
			for _, r := range resources {
				// Pass through the original URI so clients can match URIs
				// referenced in tool descriptions. Store the mapping for
				// routing resources/read to the correct upstream server.
				s.resourceMap.Store(r.URI, serverName)
				allResources = append(allResources, listedResource{
					URI:         r.URI,
					Name:        r.Name,
					Title:       r.Title,
					Description: r.Description,
					MimeType:    r.MimeType,
					Size:        r.Size,
					Annotations: r.Annotations,
				})
			}
			mu.Unlock()
		}(name)
	}

	wg.Wait()

	if allResources == nil {
		allResources = []listedResource{}
	}
	return struct {
		Resources []listedResource `json:"resources"`
	}{Resources: allResources}, nil
}

// handleResourcesRead handles the resources/read request.
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
	val, ok := s.resourceMap.Load(req.URI)
	if !ok {
		return nil, ErrInvalidParams("unknown resource URI (has resources/list been called?): " + req.URI)
	}
	serverName := val.(string)

	if !slices.Contains(activeServerNames, serverName) {
		return nil, ErrServerNotFound(serverName)
	}

	sc, rpcErr := s.ensureServerClient(ctx, serverName)
	if rpcErr != nil {
		return nil, rpcErr
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	contents, err := sc.client.ReadResource(callCtx, req.URI)
	if err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/read from %s: %v", serverName, err))
	}

	return struct {
		Contents json.RawMessage `json:"contents"`
	}{Contents: contents}, nil
}

// handleResourcesSubscribe handles the resources/subscribe request.
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

	val, ok := s.resourceMap.Load(req.URI)
	if !ok {
		return nil, ErrInvalidParams("unknown resource URI (has resources/list been called?): " + req.URI)
	}
	serverName := val.(string)

	if !slices.Contains(activeServerNames, serverName) {
		return nil, ErrServerNotFound(serverName)
	}

	sc, rpcErr := s.ensureServerClient(ctx, serverName)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if sc.capabilities.Resources == nil || !sc.capabilities.Resources.Subscribe {
		return nil, ErrMethodNotFound(fmt.Sprintf("upstream %s does not support resources/subscribe", serverName))
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	if err := sc.client.SubscribeResource(callCtx, req.URI); err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/subscribe on %s: %v", serverName, err))
	}

	s.subMu.Lock()
	s.subs[req.URI] = serverName
	s.subMu.Unlock()

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

	// Prefer s.subs for lookup (client may unsubscribe after a list refresh
	// evicted resourceMap); fall back to resourceMap.
	s.subMu.Lock()
	serverName, known := s.subs[req.URI]
	s.subMu.Unlock()
	if !known {
		if val, ok := s.resourceMap.Load(req.URI); ok {
			serverName = val.(string)
			known = true
		}
	}
	if !known {
		// Idempotent: client cleanup on an unknown URI is not an error.
		return struct{}{}, nil
	}

	if !slices.Contains(activeServerNames, serverName) {
		// Server no longer in the active namespace — clear local tracking
		// and return success; there's no upstream to notify.
		s.subMu.Lock()
		delete(s.subs, req.URI)
		s.subMu.Unlock()
		return struct{}{}, nil
	}

	sc, rpcErr := s.ensureServerClient(ctx, serverName)
	if rpcErr != nil {
		return nil, rpcErr
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	if err := sc.client.UnsubscribeResource(callCtx, req.URI); err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/unsubscribe on %s: %v", serverName, err))
	}

	s.subMu.Lock()
	delete(s.subs, req.URI)
	s.subMu.Unlock()

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

	var allPrompts []qualifiedPrompt
	var mu sync.Mutex
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup

	for _, name := range activeServerNames {
		wg.Add(1)
		go func(serverName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sc, rpcErr := s.ensureServerClient(ctx, serverName)
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

			mu.Lock()
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
			mu.Unlock()
		}(name)
	}

	wg.Wait()

	if allPrompts == nil {
		allPrompts = []qualifiedPrompt{}
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

	sc, rpcErr := s.ensureServerClient(ctx, serverName)
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

// serverClient holds a client, its per-server timeout, and the upstream's
// advertised capabilities (snapshot at acquisition time).
type serverClient struct {
	client       *mcp.Client
	timeout      time.Duration
	capabilities mcp.ServerCapabilities
}

// ensureServerClient starts a server if needed and returns its MCP client
// along with the per-server tool timeout for wrapping upstream calls.
func (s *Server) ensureServerClient(ctx context.Context, serverName string) (serverClient, *RPCError) {
	srv, ok := s.cfg.GetServer(serverName)
	if !ok {
		return serverClient{}, ErrServerNotFound(serverName)
	}
	if !srv.IsEnabled() {
		return serverClient{}, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
	}

	handle := s.supervisor.Get(serverName)
	if handle == nil || !handle.IsRunning() {
		var err error
		handle, err = s.supervisor.Start(ctx, serverName, srv)
		if err != nil {
			return serverClient{}, ErrServerFailedToStart(serverName, err.Error())
		}
	}

	if err := handle.WaitForTools(ctx); err != nil {
		return serverClient{}, ErrServerFailedToStart(serverName, err.Error())
	}

	return serverClient{
		client:       handle.Client(),
		timeout:      time.Duration(srv.ToolTimeout()) * time.Second,
		capabilities: handle.Capabilities(),
	}, nil
}

// resolveNamespace determines which namespace to use and which servers are active.
func (s *Server) resolveNamespace() *RPCError {
	cfg := s.cfg
	namespaceArg := s.opts.Namespace

	// Rule 1: If --namespace provided, use it (lookup by name)
	if namespaceArg != "" {
		if ns, exists := cfg.Namespaces[namespaceArg]; exists {
			s.activeNamespaceName = namespaceArg
			s.activeServerNames = ns.ServerIDs
			s.selectionMethod = SelectionFlag
			log.Printf("Using namespace %q with %d servers (selection: flag)", namespaceArg, len(s.activeServerNames))
			return nil
		}
		return ErrNamespaceNotFound(namespaceArg)
	}

	// Rule 2: If config.defaultNamespace is set, use it
	if cfg.DefaultNamespace != "" {
		if ns, exists := cfg.Namespaces[cfg.DefaultNamespace]; exists {
			s.activeNamespaceName = cfg.DefaultNamespace
			s.activeServerNames = ns.ServerIDs
			s.selectionMethod = SelectionDefault
			log.Printf("Using default namespace %q with %d servers (selection: default)", cfg.DefaultNamespace, len(s.activeServerNames))
			return nil
		}
		return ErrNamespaceNotFound(cfg.DefaultNamespace)
	}

	// Rule 3: If exactly 1 namespace, use it
	if len(cfg.Namespaces) == 1 {
		for name, ns := range cfg.Namespaces {
			s.activeNamespaceName = name
			s.activeServerNames = ns.ServerIDs
			s.selectionMethod = SelectionOnly
			log.Printf("Using only namespace %q with %d servers (selection: only)", name, len(s.activeServerNames))
			return nil
		}
	}

	// Rule 4: If 0 namespaces, expose all enabled servers
	if len(cfg.Namespaces) == 0 {
		s.activeNamespaceName = ""
		s.activeServerNames = make([]string, 0, len(cfg.Servers))
		for name, srv := range cfg.Servers {
			if srv.IsEnabled() {
				s.activeServerNames = append(s.activeServerNames, name)
			}
		}
		s.selectionMethod = SelectionAll
		log.Printf("No namespaces configured, exposing all %d enabled servers (selection: all)", len(s.activeServerNames))
		return nil
	}

	// Rule 5: 2+ namespaces, none selected - fail
	return NewRPCError(ErrCodeInvalidRequest,
		fmt.Sprintf("Multiple namespaces configured (%d), but none selected. Use --namespace to specify which namespace to expose.", len(cfg.Namespaces)),
		nil)
}

// startEagerServers starts all servers in the active namespace.
func (s *Server) startEagerServers(ctx context.Context) {
	log.Printf("Starting %d servers eagerly", len(s.activeServerNames))
	for _, name := range s.activeServerNames {
		srv, ok := s.cfg.GetServer(name)
		if !ok {
			continue
		}
		if _, err := s.supervisor.Start(ctx, name, srv); err != nil {
			log.Printf("Failed to start server %s: %v", name, err)
		}
	}
}

// shutdown cleans up resources.
func (s *Server) shutdown() {
	log.Println("Shutting down server")
	s.supervisor.StopAll()
	s.bus.Close()
}

// watchConfig watches the config file for changes and sends new config to reloadCh.
// It watches the parent directory (not the file) to handle atomic renames.
func (s *Server) watchConfig(ctx context.Context, configPath string) {
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
	debounceDelay := s.opts.DebounceDelay
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
			case s.reloadCh <- newCfg:
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

// applyReload applies a new configuration, rebuilding all components.
// Must be called from the Run() goroutine to serialize with request handling.
func (s *Server) applyReload(ctx context.Context, newCfg *config.Config) {
	log.Printf("Applying config reload: %d servers, %d namespaces",
		len(newCfg.Servers), len(newCfg.Namespaces))

	// Clear subscription tracking before StopAll: closing the upstream
	// transport ends the upstream-side subscription cleanly, so we only
	// need to drop our local bookkeeping. No per-URI unsubscribe RPC is
	// attempted — it would race with shutdown.
	s.subMu.Lock()
	clear(s.subs)
	s.subMu.Unlock()

	// Stop all running servers
	s.supervisor.StopAll()

	// Swap config
	s.mu.Lock()
	oldNamespaceName := s.activeNamespaceName
	oldSelectionMethod := s.selectionMethod
	s.cfg = newCfg
	s.mu.Unlock()

	// Re-resolve namespace
	// If namespace was selected by flag and still exists, keep it
	// If namespace was auto-selected and still valid, keep it
	// If namespace no longer exists, re-auto-select
	s.mu.Lock()

	var keepNamespace bool
	if oldSelectionMethod == SelectionFlag && s.opts.Namespace != "" {
		// Try to find the namespace by the original flag value
		if ns, exists := newCfg.Namespaces[s.opts.Namespace]; exists {
			s.activeNamespaceName = s.opts.Namespace
			s.activeServerNames = ns.ServerIDs
			s.selectionMethod = SelectionFlag
			keepNamespace = true
		}
	} else if oldNamespaceName != "" {
		// Try to keep the same namespace by name
		if ns, exists := newCfg.Namespaces[oldNamespaceName]; exists {
			s.activeNamespaceName = oldNamespaceName
			s.activeServerNames = ns.ServerIDs
			s.selectionMethod = oldSelectionMethod
			keepNamespace = true
		}
	}

	if !keepNamespace {
		// Need to re-resolve namespace from scratch
		// Save previous state so we can restore on failure (fail-closed)
		oldActiveServerNames := s.activeServerNames
		s.activeNamespaceName = ""
		s.activeServerNames = nil
		s.mu.Unlock()

		// Re-run namespace resolution
		if err := s.resolveNamespace(); err != nil {
			log.Printf("WARN: namespace resolution failed after reload, keeping previous config: %v", err)
			s.mu.Lock()
			s.activeNamespaceName = oldNamespaceName
			s.activeServerNames = oldActiveServerNames
			s.selectionMethod = oldSelectionMethod
			s.mu.Unlock()
		}
	} else {
		log.Printf("Kept namespace %q after reload with %d servers",
			s.activeNamespaceName, len(s.activeServerNames))
		s.mu.Unlock()
	}

	// Rebuild aggregator and router with new config. Swap under the write
	// lock so concurrently-running handlers see either the whole old pair or
	// the whole new pair, never a torn read.
	newAgg := NewAggregator(s.cfg, s.supervisor, s.opts.ExposeManagerTools)
	newRouter := NewRouter(s.cfg, s.supervisor, newAgg)

	s.mu.Lock()
	s.aggregator = newAgg
	s.router = newRouter
	activeNsName := s.activeNamespaceName
	selMethod := s.selectionMethod
	s.mu.Unlock()

	newRouter.SetActiveNamespace(activeNsName, selMethod)

	// Restart servers if eager start is configured
	if s.opts.EagerStart {
		go s.startEagerServers(ctx)
	}

	// Notify client that lists may have changed
	s.sendNotification("notifications/tools/list_changed")
	if s.opts.ExposeResources {
		s.sendNotification("notifications/resources/list_changed")
	}
	if s.opts.ExposePrompts {
		s.sendNotification("notifications/prompts/list_changed")
	}

	log.Printf("Config reload complete")
}

// sendResult sends a successful JSON-RPC response.
func (s *Server) sendResult(id json.RawMessage, result any) {
	resultJSON, _ := json.Marshal(result)
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  resultJSON,
	}
	s.send(resp)
}

// sendError sends a JSON-RPC error response.
func (s *Server) sendError(id json.RawMessage, rpcErr *RPCError) {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   rpcErr,
	}
	s.send(resp)
}

// send writes a JSON-RPC message to stdout.
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

	if _, err := s.writer.Write(data); err != nil {
		log.Printf("Failed to write response: %v", err)
		return
	}
	if _, err := s.writer.Write([]byte("\n")); err != nil {
		log.Printf("Failed to write newline: %v", err)
	}
}

// JSON-RPC message types

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
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
}
