package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultTimeout is the default timeout for RPC calls.
	DefaultTimeout = 30 * time.Second
	// MaxRetries is the maximum number of retries for connection.
	MaxRetries = 3
	// cancelNotifyTimeout bounds the best-effort notifications/cancelled write
	// that follows an abandoned request. The request's own context is already
	// done by then, so this needs a deadline of its own.
	cancelNotifyTimeout = 5 * time.Second
)

// NotificationHandler is invoked for each JSON-RPC notification received from
// the server. Handlers must be cheap — they are called inline on the reader
// goroutine. Dispatch to a goroutine if the work may block.
type NotificationHandler func(method string, params json.RawMessage)

// Client implements McpClient using a Transport. Messages are demultiplexed
// by a single reader goroutine so that responses and notifications can be
// delivered independently.
type Client struct {
	transport Transport
	nextID    atomic.Int64

	// mu guards pending and closed.
	mu      sync.Mutex
	closed  bool
	pending map[int64]chan rpcResponse

	// sendMu serializes transport.Send across call and notify so NDJSON
	// frames don't interleave on stdio.
	sendMu sync.Mutex

	// reinitMu single-flights session-expiry recovery. See reinitializeOnce.
	reinitMu sync.Mutex

	// Reader lifecycle. readerDone is initialized in NewClient and closed
	// after the reader has drained pending waiters on transport error.
	readerOnce sync.Once
	readerDone chan struct{}
	readerErr  atomic.Value // holds error; nil until set

	notifHandler atomic.Pointer[NotificationHandler]

	// negotiated holds what the initialize handshake settled on. Supervisor.Start
	// publishes a Handle before running the handshake on another goroutine, so
	// readers (Core.processNotification, Aggregator.shouldQueryCapability,
	// Router.handleServersList) can call the accessors while Initialize is still
	// writing. It is stored as one immutable struct so those readers always see a
	// consistent set rather than a half-updated one, and is nil until Initialize
	// succeeds.
	negotiated atomic.Pointer[negotiated]
}

// negotiated is the immutable result of a successful initialize handshake.
// Fields are never mutated after the struct is published; a re-initialize
// replaces the whole value.
type negotiated struct {
	serverName      string
	serverVersion   string
	protocolVersion string
	capabilities    ServerCapabilities
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response as delivered from the reader.
type rpcResponse struct {
	ID     int64
	Result json.RawMessage
	Error  *rpcError
}

// rpcError is a JSON-RPC 2.0 error.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// rawMessage is the envelope used to classify incoming JSON-RPC frames.
// ID uses *json.RawMessage so a concrete id value can be distinguished from
// the field being absent. Note that encoding/json decodes JSON literal null
// into a nil *json.RawMessage, so absent id and "id": null are
// indistinguishable here — both are treated as "no usable response ID". See
// the classification caveat in the Stage 1 plan.
type rawMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method *string          `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *rpcError        `json:"error,omitempty"`
}

// initializeParams is the params for the initialize request.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult is the result of the initialize request.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// toolsListResult is the result of tools/list.
type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

// NewClient creates a new MCP client with the given transport. The reader
// goroutine starts immediately so that Close is safe even if Initialize is
// never called.
func NewClient(transport Transport) *Client {
	c := &Client{
		transport:  transport,
		pending:    make(map[int64]chan rpcResponse),
		readerDone: make(chan struct{}),
	}
	c.readerOnce.Do(func() { go c.readLoop() })
	return c
}

// SetNotificationHandler installs a handler invoked for each notification
// received from the server. Pass nil to clear. Handlers run inline on the
// reader goroutine — dispatch to another goroutine if the work may block.
func (c *Client) SetNotificationHandler(h NotificationHandler) {
	if h == nil {
		c.notifHandler.Store(nil)
		return
	}
	c.notifHandler.Store(&h)
}

// readLoop is the demultiplexing reader. It runs until the transport's
// Receive returns an error, at which point it delivers a transport-closed
// response to every pending waiter and closes readerDone.
func (c *Client) readLoop() {
	defer close(c.readerDone)

	ctx := context.Background()
	for {
		data, err := c.transport.Receive(ctx)
		if err != nil {
			c.readerErr.Store(err)
			c.mu.Lock()
			pending := c.pending
			c.pending = make(map[int64]chan rpcResponse)
			c.mu.Unlock()
			errResp := rpcResponse{Error: &rpcError{
				Code:    -32000,
				Message: "transport closed: " + err.Error(),
			}}
			for _, ch := range pending {
				select {
				case ch <- errResp:
				default:
				}
			}
			return
		}

		var env rawMessage
		if err := json.Unmarshal(data, &env); err != nil {
			if DebugLogging {
				log.Printf("MCP Recv: malformed frame dropped: %v", err)
			}
			continue
		}

		hasID := env.ID != nil
		hasMethod := env.Method != nil

		switch {
		case hasID && !hasMethod:
			var id int64
			if err := json.Unmarshal(*env.ID, &id); err != nil {
				if DebugLogging {
					log.Printf("MCP Recv: non-numeric response id dropped: %s", string(*env.ID))
				}
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch == nil {
				if DebugLogging {
					log.Printf("MCP Recv: unknown response id %d dropped", id)
				}
				continue
			}
			select {
			case ch <- rpcResponse{ID: id, Result: env.Result, Error: env.Error}:
			default:
				// Buffered size 1 and map entry already deleted — no other sender.
			}

		case !hasID && hasMethod:
			if h := c.notifHandler.Load(); h != nil && *h != nil {
				(*h)(*env.Method, env.Params)
			}

		case hasID && hasMethod:
			if DebugLogging {
				log.Printf("MCP Recv: server->client request dropped: method=%s id=%s",
					*env.Method, string(*env.ID))
			}

		default:
			if DebugLogging {
				log.Printf("MCP Recv: malformed frame (no id, no method) dropped")
			}
		}
	}
}

// Initialize performs the MCP initialization handshake.
// For stdio transports, it tries protocol versions in order until one is accepted.
// For HTTP transports, version negotiation is handled by the transport layer.
func (c *Client) Initialize(ctx context.Context) error {
	// Try each supported version until one works
	var lastErr error
	for _, version := range SupportedProtocolVersions {
		params := initializeParams{
			ProtocolVersion: version,
			Capabilities:    map[string]any{},
			ClientInfo: clientInfo{
				Name:    "mcpmu-go",
				Version: "0.1.0",
			},
		}

		var result initializeResult
		err := c.call(ctx, "initialize", params, &result)
		if err != nil {
			// Check if this is a version rejection error
			if isProtocolVersionError(err) {
				lastErr = err
				continue // Try next version
			}
			// Other errors are fatal
			return fmt.Errorf("initialize: %w", err)
		}

		// Success! Publish all four values at once so a concurrent reader cannot
		// observe, say, a server name without its capabilities.
		c.negotiated.Store(&negotiated{
			serverName:      result.ServerInfo.Name,
			serverVersion:   result.ServerInfo.Version,
			protocolVersion: version,
			capabilities:    result.Capabilities,
		})

		// Send initialized notification
		if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
			return fmt.Errorf("initialized notification: %w", err)
		}

		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("all protocol versions rejected: %w", lastErr)
	}
	return fmt.Errorf("initialize: no protocol versions to try")
}

// isProtocolVersionError checks if an error indicates a protocol version rejection.
func isProtocolVersionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Common patterns in version rejection errors
	return strings.Contains(errStr, "protocol") && strings.Contains(errStr, "version") ||
		strings.Contains(errStr, "protocolVersion") ||
		strings.Contains(errStr, "unsupported version")
}

// ProtocolVersion returns the negotiated protocol version, or the empty string
// if Initialize has not completed.
func (c *Client) ProtocolVersion() string {
	if n := c.negotiated.Load(); n != nil {
		return n.protocolVersion
	}
	return ""
}

// Capabilities returns the capabilities advertised by the server during
// initialize, or the zero value if Initialize has not completed.
func (c *Client) Capabilities() ServerCapabilities {
	if n := c.negotiated.Load(); n != nil {
		return n.capabilities
	}
	return ServerCapabilities{}
}

// ListTools retrieves the list of tools from the server.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result toolsListResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	return result.Tools, nil
}

// ListResources retrieves the list of resources from the server.
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
	var result resourcesListResult
	if err := c.call(ctx, "resources/list", nil, &result); err != nil {
		return nil, fmt.Errorf("resources/list: %w", err)
	}
	return result.Resources, nil
}

// ReadResource reads a specific resource by URI.
func (c *Client) ReadResource(ctx context.Context, uri string) (json.RawMessage, error) {
	params := resourceReadParams{URI: uri}
	var result resourceReadResult
	if err := c.call(ctx, "resources/read", params, &result); err != nil {
		return nil, fmt.Errorf("resources/read: %w", err)
	}
	return result.Contents, nil
}

// SubscribeResource subscribes the client to update notifications for the
// given resource URI. The server must advertise resources.subscribe: true.
func (c *Client) SubscribeResource(ctx context.Context, uri string) error {
	params := resourceReadParams{URI: uri}
	if err := c.call(ctx, "resources/subscribe", params, nil); err != nil {
		return fmt.Errorf("resources/subscribe: %w", err)
	}
	return nil
}

// UnsubscribeResource removes a previously-registered subscription for the
// given resource URI.
func (c *Client) UnsubscribeResource(ctx context.Context, uri string) error {
	params := resourceReadParams{URI: uri}
	if err := c.call(ctx, "resources/unsubscribe", params, nil); err != nil {
		return fmt.Errorf("resources/unsubscribe: %w", err)
	}
	return nil
}

// ListPrompts retrieves the list of prompts from the server.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var result promptsListResult
	if err := c.call(ctx, "prompts/list", nil, &result); err != nil {
		return nil, fmt.Errorf("prompts/list: %w", err)
	}
	return result.Prompts, nil
}

// GetPrompt retrieves a specific prompt with arguments.
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) (json.RawMessage, error) {
	params := promptGetParams{Name: name, Arguments: arguments}
	var result promptGetResult
	if err := c.call(ctx, "prompts/get", params, &result); err != nil {
		return nil, fmt.Errorf("prompts/get: %w", err)
	}
	return result.Messages, nil
}

// ServerInfo returns information about the connected server. Both values are
// empty if Initialize has not completed.
func (c *Client) ServerInfo() (name, version string) {
	if n := c.negotiated.Load(); n != nil {
		return n.serverName, n.serverVersion
	}
	return "", ""
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
	return c.CallToolWithMeta(ctx, name, arguments, nil)
}

// CallToolWithMeta invokes a tool, attaching the caller's `_meta` object to the
// request. mcpmu forwards the client's `_meta` verbatim except for
// progressToken, which it rewrites first — see the Session progress table for
// why a shared upstream cannot be handed two sessions' tokens unchanged.
func (c *Client) CallToolWithMeta(ctx context.Context, name string, arguments, meta json.RawMessage) (*ToolResult, error) {
	params := toolCallParams{
		Name:      name,
		Arguments: arguments,
		Meta:      meta,
	}

	var result toolCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}

	return &ToolResult{
		Content:           result.Content,
		StructuredContent: result.StructuredContent,
		IsError:           result.IsError,
		Meta:              result.Meta,
	}, nil
}

// resourcesListResult is the result of resources/list.
type resourcesListResult struct {
	Resources []Resource `json:"resources"`
}

type resourceReadParams struct {
	URI string `json:"uri"`
}

type resourceReadResult struct {
	Contents json.RawMessage `json:"contents"`
}

// promptsListResult is the result of prompts/list.
type promptsListResult struct {
	Prompts []Prompt `json:"prompts"`
}

type promptGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

type promptGetResult struct {
	Messages json.RawMessage `json:"messages"`
}

// toolCallParams is the params for tools/call.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

// toolCallResult is the result of tools/call. StructuredContent is the other
// half of a tool's outputSchema contract; dropping it while forwarding the
// schema would leave the server advertising a promise mcpmu had erased.
type toolCallResult struct {
	Content           []ContentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	Meta              json.RawMessage `json:"_meta,omitempty"`
}

// ToolResult represents the result of a tool call.
type ToolResult struct {
	Content           []ContentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	Meta              json.RawMessage `json:"_meta,omitempty"`
}

// ContentBlock represents a content block in a tool result.
// Uses json.RawMessage to preserve all fields from upstream servers,
// including non-text content types (images, resources, etc.).
type ContentBlock json.RawMessage

// MarshalJSON implements json.Marshaler.
func (c ContentBlock) MarshalJSON() ([]byte, error) {
	return json.RawMessage(c), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *ContentBlock) UnmarshalJSON(data []byte) error {
	*c = ContentBlock(data)
	return nil
}

// Close closes the client connection. It is safe to call Close on a client
// that was never Initialize'd — the reader goroutine started in NewClient
// will be torn down cleanly by the transport close.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	err := c.transport.Close()

	// Wait for the reader to finish draining pending waiters.
	<-c.readerDone
	return err
}

// call makes a JSON-RPC call and waits for the response on a per-call
// channel populated by the reader goroutine.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("client closed")
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	// Cleanup: remove pending entry if still present. Channel is never
	// closed — a late sender from the reader is impossible because the
	// reader deletes the entry under mu before sending.
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	if sendErr := c.sendWithSessionRecovery(ctx, method, data); sendErr != nil {
		return fmt.Errorf("send: %w", sendErr)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && resp.Result != nil {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.cancelUpstream(id, ctx)
		return ctx.Err()
	case <-c.readerDone:
		if errVal, ok := c.readerErr.Load().(error); ok && errVal != nil {
			return fmt.Errorf("transport closed: %w", errVal)
		}
		return fmt.Errorf("transport closed")
	}
}

// cancelUpstream tells the server that an in-flight request has been abandoned.
//
// Without this, cancelling only unblocks mcpmu: the server keeps working, keeps
// holding whatever the call reserved, and still performs the side effect the
// user was trying to stop. The cancellation spec asks both for cancelled and
// for timed-out requests to be withdrawn, so this covers deadline expiry too.
//
// Best effort by design — the request's own context is already done, so the
// notification goes out under a short independent deadline, and a failure to
// deliver it is not worth surfacing over the cancellation itself.
func (c *Client) cancelUpstream(id int64, ctx context.Context) {
	params := map[string]any{"requestId": id}
	if cause := context.Cause(ctx); cause != nil {
		params["reason"] = cause.Error()
	}

	sendCtx, cancel := context.WithTimeout(context.Background(), cancelNotifyTimeout)
	defer cancel()
	if err := c.notify(sendCtx, "notifications/cancelled", params); err != nil && DebugLogging {
		log.Printf("MCP Send: cancellation for request %d not delivered: %v", id, err)
	}
}

// notify sends a JSON-RPC notification (no response expected). Serialized
// with call via sendMu so NDJSON frames cannot interleave on stdio.
func (c *Client) notify(ctx context.Context, method string, params any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("client closed")
	}
	c.mu.Unlock()

	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	return c.sendWithSessionRecovery(ctx, method, data)
}

// sendWithSessionRecovery sends one frame, recovering once from an expired
// HTTP session. The Streamable HTTP spec requires clients to reinitialize
// when a request carrying a session ID comes back 404; the transport has
// already cleared its session state (and stopped the stale standalone SSE
// stream) by the time it returns SessionExpiredError, so a fresh Initialize
// mints a new session — and its first successful POST reopens the stream.
//
// initialize and its follow-up notification are exempt: they are how a
// session is created, so recovering through them could recurse.
func (c *Client) sendWithSessionRecovery(ctx context.Context, method string, data []byte) error {
	c.sendMu.Lock()
	sendErr := c.transport.Send(ctx, data)
	c.sendMu.Unlock()

	var expired *SessionExpiredError
	if !errors.As(sendErr, &expired) || method == "initialize" || method == "notifications/initialized" {
		return sendErr
	}

	log.Printf("MCP session expired before %s; reinitializing and retrying once", method)
	if initErr := c.reinitializeOnce(ctx); initErr != nil {
		return fmt.Errorf("reinitialize after session expiry: %w", initErr)
	}
	c.sendMu.Lock()
	sendErr = c.transport.Send(ctx, data)
	c.sendMu.Unlock()
	return sendErr
}

// sessionIDer is implemented by transports carrying a server-issued session
// (Streamable HTTP). It is how recovery tells "nobody has reconnected yet"
// from "another goroutine already did".
type sessionIDer interface {
	SessionID() string
}

// reinitializeOnce runs at most one Initialize per expired session, however
// many callers raced into recovery. Concurrent calls all get
// SessionExpiredError for the same dead session; without the guard each would
// mint its own server-side session — every one of them spinning up private
// upstream instances for shared:false servers — and only the last session ID
// would survive in the transport, orphaning the rest until the server's idle
// reaper notices.
func (c *Client) reinitializeOnce(ctx context.Context) error {
	c.reinitMu.Lock()
	defer c.reinitMu.Unlock()
	if s, ok := c.transport.(sessionIDer); ok && s.SessionID() != "" {
		// Recovered while this goroutine waited for the lock; the caller's
		// retry rides the new session.
		return nil
	}
	return c.Initialize(ctx)
}
