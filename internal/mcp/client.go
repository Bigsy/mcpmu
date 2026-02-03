package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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

// Client implements McpClient using a Transport.
type Client struct {
	transport Transport
	nextID    atomic.Int64
	mu        sync.Mutex
	closed    bool

	// Server info from initialization
	serverName        string
	serverVersion     string
	protocolVersion   string // Negotiated protocol version
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
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

// NewClient creates a new MCP client with the given transport.
func NewClient(transport Transport) *Client {
	return &Client{
		transport: transport,
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

// ListTools retrieves the list of tools from the server.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result toolsListResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	return result.Tools, nil
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

// Close closes the client connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	return c.transport.Close()
}

// call makes a JSON-RPC call and waits for the response.
// The call is serialized with a mutex to prevent concurrent transport access.
func (c *Client) call(ctx context.Context, method string, params interface{}, result interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("client closed")
	}

	id := c.nextID.Add(1)

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

	// Send the request
	if err := c.transport.Send(ctx, data); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	// Read responses until we get one with matching ID
	// (skip notifications which have no ID)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		respData, err := c.transport.Receive(ctx)
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}

		var resp rpcResponse
		if err := json.Unmarshal(respData, &resp); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}

		// Skip notifications (no ID) and responses for other requests
		if resp.ID == 0 {
			// This is a notification, skip it
			continue
		}
		if resp.ID != id {
			// Response for a different request, skip it
			// (shouldn't happen in our simple sequential model)
			continue
		}

		// Found our response
		if resp.Error != nil {
			return resp.Error
		}

		if result != nil && resp.Result != nil {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
		}

		return nil
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (c *Client) notify(ctx context.Context, method string, params interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("client closed")
	}

	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	return c.transport.Send(ctx, data)
}
