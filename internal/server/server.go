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
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/fsnotify/fsnotify"
)

// Options configures the MCP server.
type Options struct {
	Config             *config.Config
	ConfigPath         string // Expanded path for hot-reload watching (empty = no watching)
	Namespace          string // Namespace to expose (empty = auto-select)
	EagerStart         bool   // Pre-start all servers
	ExposeManagerTools bool   // Include mcpmu.* tools in tools/list
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
	activeNamespace   *config.NamespaceConfig
	activeServerIDs   []string        // Server IDs in the active namespace (or all if no namespace)
	selectionMethod   SelectionMethod // How the namespace was selected

	// Protocol state
	initialized bool
	mu          sync.RWMutex

	// IO
	reader  *bufio.Reader
	writer  io.Writer
	writeMu sync.Mutex

	// Hot-reload
	reloadCh chan *config.Config // Serializes reload with request handling
}

// New creates a new MCP server.
func New(opts Options) (*Server, error) {
	// Create event bus
	bus := events.NewBus()

	// Create process supervisor with config-specified credential store
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: opts.Config.MCPOAuthCredentialStore,
	})

	s := &Server{
		opts:       opts,
		cfg:        opts.Config,
		bus:        bus,
		supervisor: supervisor,
		reader:     bufio.NewReader(opts.Stdin),
		writer:     opts.Stdout,
		reloadCh:   make(chan *config.Config, 1), // Buffered to avoid blocking watcher
	}

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

// handleMessage parses and routes a JSON-RPC message.
func (s *Server) handleMessage(ctx context.Context, data []byte) error {
	log.Printf("Recv: %s", string(data))

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
	activeID := ""
	if s.activeNamespace != nil {
		activeID = s.activeNamespace.ID
	}
	s.router.SetActiveNamespace(activeID, s.selectionMethod)

	s.initialized = true

	// Return server capabilities
	return initializeResult{
		ProtocolVersion: s.opts.ProtocolVersion,
		ServerInfo: serverInfo{
			Name:    s.opts.ServerName,
			Version: s.opts.ServerVersion,
		},
		Capabilities: capabilities{
			Tools: &toolsCapability{},
		},
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
	activeNamespaceID := ""
	if s.activeNamespace != nil {
		activeNamespaceID = s.activeNamespace.ID
	}
	s.mu.RUnlock()

	// Get aggregated tools
	tools, err := s.aggregator.ListTools(ctx, s.activeServerIDs)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	// Filter tools based on permissions (if namespace is active)
	if activeNamespaceID != "" {
		filtered := make([]AggregatedTool, 0, len(tools))
		for _, tool := range tools {
			serverID, toolName, isManager := ParseToolName(tool.Name)
			// Manager tools are always shown
			if isManager {
				filtered = append(filtered, tool)
				continue
			}
			// Check permission for regular tools
			allowed, _ := IsToolAllowed(s.cfg, activeNamespaceID, serverID, toolName)
			if allowed {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	return toolsListResult{Tools: tools}, nil
}

// handleToolsCall handles the tools/call request.
func (s *Server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerIDs := s.activeServerIDs
	s.mu.RUnlock()

	var req toolsCallRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Parse tool name to check namespace enforcement
	serverID, _, isManager := ParseToolName(req.Name)

	// Manager tools are always allowed
	if !isManager && serverID != "" {
		// Check if the server is in the active namespace
		allowed := false
		for _, id := range activeServerIDs {
			if id == serverID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrServerNotFound(serverID)
		}

		// Check if server is enabled
		srv := s.cfg.GetServer(serverID)
		if srv == nil {
			return nil, ErrServerNotFound(serverID)
		}
		if !srv.IsEnabled() {
			return nil, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverID, nil)
		}
	}

	// Route the call through the router
	result, rpcErr := s.router.CallTool(ctx, req.Name, req.Arguments)
	if rpcErr != nil {
		return nil, rpcErr
	}

	return result, nil
}

// resolveNamespace determines which namespace to use and which servers are active.
func (s *Server) resolveNamespace() *RPCError {
	cfg := s.cfg
	namespaceArg := s.opts.Namespace

	// Rule 1: If --namespace provided, use it (lookup by ID or name)
	if namespaceArg != "" {
		// Try lookup by ID first
		for i := range cfg.Namespaces {
			if cfg.Namespaces[i].ID == namespaceArg {
				s.activeNamespace = &cfg.Namespaces[i]
				s.activeServerIDs = cfg.Namespaces[i].ServerIDs
				s.selectionMethod = SelectionFlag
				log.Printf("Using namespace %q with %d servers (selection: flag)", namespaceArg, len(s.activeServerIDs))
				return nil
			}
		}
		// Try lookup by name
		for i := range cfg.Namespaces {
			if cfg.Namespaces[i].Name == namespaceArg {
				s.activeNamespace = &cfg.Namespaces[i]
				s.activeServerIDs = cfg.Namespaces[i].ServerIDs
				s.selectionMethod = SelectionFlag
				log.Printf("Using namespace %q with %d servers (selection: flag)", cfg.Namespaces[i].Name, len(s.activeServerIDs))
				return nil
			}
		}
		return ErrNamespaceNotFound(namespaceArg)
	}

	// Rule 2: If config.defaultNamespaceId is set, use it
	if cfg.DefaultNamespaceID != "" {
		for i := range cfg.Namespaces {
			if cfg.Namespaces[i].ID == cfg.DefaultNamespaceID {
				s.activeNamespace = &cfg.Namespaces[i]
				s.activeServerIDs = cfg.Namespaces[i].ServerIDs
				s.selectionMethod = SelectionDefault
				log.Printf("Using default namespace %q with %d servers (selection: default)", cfg.DefaultNamespaceID, len(s.activeServerIDs))
				return nil
			}
		}
		return ErrNamespaceNotFound(cfg.DefaultNamespaceID)
	}

	// Rule 3: If exactly 1 namespace, use it
	if len(cfg.Namespaces) == 1 {
		s.activeNamespace = &cfg.Namespaces[0]
		s.activeServerIDs = cfg.Namespaces[0].ServerIDs
		s.selectionMethod = SelectionOnly
		log.Printf("Using only namespace %q with %d servers (selection: only)", cfg.Namespaces[0].ID, len(s.activeServerIDs))
		return nil
	}

	// Rule 4: If 0 namespaces, expose all enabled servers
	if len(cfg.Namespaces) == 0 {
		s.activeNamespace = nil
		s.activeServerIDs = make([]string, 0, len(cfg.Servers))
		for id, srv := range cfg.Servers {
			if srv.IsEnabled() {
				s.activeServerIDs = append(s.activeServerIDs, id)
			}
		}
		s.selectionMethod = SelectionAll
		log.Printf("No namespaces configured, exposing all %d enabled servers (selection: all)", len(s.activeServerIDs))
		return nil
	}

	// Rule 5: 2+ namespaces, none selected - fail
	return NewRPCError(ErrCodeInvalidRequest,
		fmt.Sprintf("Multiple namespaces configured (%d), but none selected. Use --namespace to specify which namespace to expose.", len(cfg.Namespaces)),
		nil)
}

// startEagerServers starts all servers in the active namespace.
func (s *Server) startEagerServers(ctx context.Context) {
	log.Printf("Starting %d servers eagerly", len(s.activeServerIDs))
	for _, id := range s.activeServerIDs {
		srv := s.cfg.GetServer(id)
		if srv == nil {
			continue
		}
		if _, err := s.supervisor.Start(ctx, *srv); err != nil {
			log.Printf("Failed to start server %s: %v", id, err)
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
	defer watcher.Close()

	// Watch parent directory to catch atomic renames
	dir := filepath.Dir(configPath)
	filename := filepath.Base(configPath)

	if err := watcher.Add(dir); err != nil {
		log.Printf("Failed to watch config directory %s: %v", dir, err)
		return
	}

	log.Printf("Watching config file: %s", configPath)

	// Debounce timer
	const debounceDelay = 150 * time.Millisecond
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

	// Stop all running servers
	s.supervisor.StopAll()

	// Swap config
	s.mu.Lock()
	oldNamespaceID := ""
	if s.activeNamespace != nil {
		oldNamespaceID = s.activeNamespace.ID
	}
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
		for i := range newCfg.Namespaces {
			if newCfg.Namespaces[i].ID == s.opts.Namespace || newCfg.Namespaces[i].Name == s.opts.Namespace {
				s.activeNamespace = &newCfg.Namespaces[i]
				s.activeServerIDs = newCfg.Namespaces[i].ServerIDs
				s.selectionMethod = SelectionFlag
				keepNamespace = true
				break
			}
		}
	} else if oldNamespaceID != "" {
		// Try to keep the same namespace by ID
		for i := range newCfg.Namespaces {
			if newCfg.Namespaces[i].ID == oldNamespaceID {
				s.activeNamespace = &newCfg.Namespaces[i]
				s.activeServerIDs = newCfg.Namespaces[i].ServerIDs
				s.selectionMethod = oldSelectionMethod
				keepNamespace = true
				break
			}
		}
	}

	if !keepNamespace {
		// Need to re-resolve namespace from scratch
		// Clear current state first
		s.activeNamespace = nil
		s.activeServerIDs = nil
		s.mu.Unlock()

		// Re-run namespace resolution
		if err := s.resolveNamespace(); err != nil {
			log.Printf("Failed to resolve namespace after reload: %v", err)
			// Fall back to exposing all enabled servers
			s.mu.Lock()
			s.activeNamespace = nil
			s.activeServerIDs = make([]string, 0, len(newCfg.Servers))
			for id, srv := range newCfg.Servers {
				if srv.IsEnabled() {
					s.activeServerIDs = append(s.activeServerIDs, id)
				}
			}
			s.selectionMethod = SelectionAll
			s.mu.Unlock()
			log.Printf("Fell back to exposing all %d enabled servers", len(s.activeServerIDs))
		}
	} else {
		log.Printf("Kept namespace %q after reload with %d servers",
			s.activeNamespace.ID, len(s.activeServerIDs))
		s.mu.Unlock()
	}

	// Rebuild aggregator and router with new config
	s.aggregator = NewAggregator(s.cfg, s.supervisor, s.opts.ExposeManagerTools)
	s.router = NewRouter(s.cfg, s.supervisor, s.aggregator)

	// Update router with active namespace info
	s.mu.RLock()
	activeID := ""
	if s.activeNamespace != nil {
		activeID = s.activeNamespace.ID
	}
	selMethod := s.selectionMethod
	s.mu.RUnlock()
	s.router.SetActiveNamespace(activeID, selMethod)

	// Restart servers if eager start is configured
	if s.opts.EagerStart {
		go s.startEagerServers(ctx)
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

	log.Printf("Send: %s", string(data))

	_, _ = s.writer.Write(data)
	_, _ = s.writer.Write([]byte("\n"))
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
	Tools *toolsCapability `json:"tools,omitempty"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type toolsListResult struct {
	Tools []AggregatedTool `json:"tools"`
}

type toolsCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}
