package mcp

import (
	"context"
	"encoding/json"
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

	// Reader lifecycle. readerDone is initialized in NewClient and closed
	// after the reader has drained pending waiters on transport error.
	readerOnce sync.Once
	readerDone chan struct{}
	readerErr  atomic.Value // holds error; nil until set

	notifHandler atomic.Pointer[NotificationHandler]

	// Server info from initialization
	serverName      string
	serverVersion   string
	protocolVersion string // Negotiated protocol version
	capabilities    any    // Raw capabilities from initialize; Stage 2 narrows this to a typed ServerCapabilities.
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
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ServerInfo      serverInfo `json:"serverInfo"`
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

		// Success!
		c.serverName = result.ServerInfo.Name
		c.serverVersion = result.ServerInfo.Version
		c.protocolVersion = version
		c.capabilities = result.Capabilities

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

// ProtocolVersion returns the negotiated protocol version.
func (c *Client) ProtocolVersion() string {
	return c.protocolVersion
}

// Capabilities returns the capabilities advertised by the server during
// initialize, or nil if Initialize has not completed. Stage 1 returns the
// raw decoded value (typically map[string]any) as a stub; Stage 2 narrows
// this to a typed ServerCapabilities struct.
func (c *Client) Capabilities() any {
	return c.capabilities
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

// ServerInfo returns information about the connected server.
func (c *Client) ServerInfo() (name, version string) {
	return c.serverName, c.serverVersion
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
	params := toolCallParams{
		Name:      name,
		Arguments: arguments,
	}

	var result toolCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}

	return &ToolResult{
		Content: result.Content,
		IsError: result.IsError,
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
}

// toolCallResult is the result of tools/call.
type toolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ToolResult represents the result of a tool call.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
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

	c.sendMu.Lock()
	sendErr := c.transport.Send(ctx, data)
	c.sendMu.Unlock()
	if sendErr != nil {
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
		return ctx.Err()
	case <-c.readerDone:
		if errVal, ok := c.readerErr.Load().(error); ok && errVal != nil {
			return fmt.Errorf("transport closed: %w", errVal)
		}
		return fmt.Errorf("transport closed")
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

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.transport.Send(ctx, data)
}
