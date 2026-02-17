// Package mcp provides MCP protocol client implementation.
package mcp

import (
	"context"
	"io"
)

// Transport is the interface for MCP transports.
type Transport interface {
	// Send sends a JSON-RPC message.
	Send(ctx context.Context, msg []byte) error
	// Receive reads the next JSON-RPC message.
	Receive(ctx context.Context) ([]byte, error)
	// Close closes the transport.
	Close() error
}

// McpClient is the interface for MCP clients.
type McpClient interface {
	// Initialize performs the MCP initialization handshake.
	Initialize(ctx context.Context) error
	// ListTools retrieves the list of tools from the server.
	ListTools(ctx context.Context) ([]Tool, error)
	// Close closes the client connection.
	Close() error
}

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema,omitempty"`
}

// StdioTransportConfig holds configuration for stdio transport.
type StdioTransportConfig struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}
