package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
)

// readResult holds a line read from stdin and any error.
type readResult struct {
	line []byte
	err  error
}

// Run starts the server and processes requests until context is cancelled.
func (s *Session) Run(ctx context.Context) error {
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
func (s *Session) handleMessage(ctx context.Context, data []byte) error {
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
func (s *Session) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
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
func (s *Session) handleNotification(ctx context.Context, method string, params json.RawMessage) error {
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
func (s *Session) handleCancelled(params json.RawMessage) {
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
func (s *Session) handleInitialize(ctx context.Context, params json.RawMessage) (any, *RPCError) {
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
func (s *Session) handlePing(ctx context.Context) (any, *RPCError) {
	return struct{}{}, nil
}

// sendError sends a JSON-RPC error response.
func (s *Session) sendError(id json.RawMessage, rpcErr *RPCError) {
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
func (s *Session) send(msg any) {
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
