package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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
	serverName    string
	serverVersion string
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
func (c *Client) Initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo: clientInfo{
			Name:    "mcp-studio-go",
			Version: "0.1.0",
		},
	}

	var result initializeResult
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	c.serverName = result.ServerInfo.Name
	c.serverVersion = result.ServerInfo.Version

	// Send initialized notification
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("initialized notification: %w", err)
	}

	return nil
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
func (c *Client) call(ctx context.Context, method string, params interface{}, result interface{}) error {
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
