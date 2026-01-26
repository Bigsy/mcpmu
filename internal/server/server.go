package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/process"
)

// Options configures the MCP server.
type Options struct {
	Config          *config.Config
	Namespace       string // Namespace to expose (empty = auto-select)
	EagerStart      bool   // Pre-start all servers
	LogLevel        string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	ServerName      string
	ServerVersion   string
	ProtocolVersion string
}

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
	activeServerIDs   []string // Server IDs in the active namespace (or all if no namespace)

	// Protocol state
	initialized bool
	mu          sync.RWMutex

	// IO
	reader *bufio.Reader
	writer io.Writer
	writeMu sync.Mutex
}

// New creates a new MCP server.
func New(opts Options) (*Server, error) {
	// Create event bus
	bus := events.NewBus()

	// Create process supervisor
	supervisor := process.NewSupervisor(bus)

	s := &Server{
		opts:       opts,
		cfg:        opts.Config,
		bus:        bus,
		supervisor: supervisor,
		reader:     bufio.NewReader(opts.Stdin),
		writer:     opts.Stdout,
	}

	// Create aggregator and router (will be initialized after namespace selection)
	s.aggregator = NewAggregator(s.cfg, supervisor)
	s.router = NewRouter(s.cfg, supervisor, s.aggregator)

	return s, nil
}

// Run starts the server and processes requests until context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	defer s.shutdown()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read next request
		line, err := s.reader.ReadBytes('\n')

		// Process any data we got, even if there's an error (e.g., EOF without newline)
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			if msgErr := s.handleMessage(ctx, line); msgErr != nil {
				log.Printf("Error handling message: %v", msgErr)
			}
		}

		// Now handle the read error
		if err != nil {
			if err == io.EOF {
				log.Println("Client closed connection (EOF)")
				return nil
			}
			return fmt.Errorf("read request: %w", err)
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
	s.mu.RUnlock()

	// Get aggregated tools
	tools, err := s.aggregator.ListTools(ctx, s.activeServerIDs)
	if err != nil {
		return nil, ErrInternalError(err.Error())
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
	namespaceID := s.opts.Namespace

	// Rule 1: If --namespace provided, use it
	if namespaceID != "" {
		for i := range cfg.Namespaces {
			if cfg.Namespaces[i].ID == namespaceID {
				s.activeNamespace = &cfg.Namespaces[i]
				s.activeServerIDs = cfg.Namespaces[i].ServerIDs
				log.Printf("Using namespace %q with %d servers", namespaceID, len(s.activeServerIDs))
				return nil
			}
		}
		return ErrNamespaceNotFound(namespaceID)
	}

	// Rule 2: If config.defaultNamespaceId is set, use it
	if cfg.DefaultNamespaceID != "" {
		for i := range cfg.Namespaces {
			if cfg.Namespaces[i].ID == cfg.DefaultNamespaceID {
				s.activeNamespace = &cfg.Namespaces[i]
				s.activeServerIDs = cfg.Namespaces[i].ServerIDs
				log.Printf("Using default namespace %q with %d servers", cfg.DefaultNamespaceID, len(s.activeServerIDs))
				return nil
			}
		}
		return ErrNamespaceNotFound(cfg.DefaultNamespaceID)
	}

	// Rule 3: If exactly 1 namespace, use it
	if len(cfg.Namespaces) == 1 {
		s.activeNamespace = &cfg.Namespaces[0]
		s.activeServerIDs = cfg.Namespaces[0].ServerIDs
		log.Printf("Using only namespace %q with %d servers", cfg.Namespaces[0].ID, len(s.activeServerIDs))
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
		log.Printf("No namespaces configured, exposing all %d enabled servers", len(s.activeServerIDs))
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

	s.writer.Write(data)
	s.writer.Write([]byte("\n"))
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
