// Package mcp provides MCP protocol client implementation.
package mcp

import (
	"context"
	"encoding/json"
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
	// ListResources retrieves the list of resources from the server.
	ListResources(ctx context.Context) ([]Resource, error)
	// ReadResource reads a specific resource by URI.
	ReadResource(ctx context.Context, uri string) (json.RawMessage, error)
	// ListPrompts retrieves the list of prompts from the server.
	ListPrompts(ctx context.Context) ([]Prompt, error)
	// GetPrompt retrieves a specific prompt with arguments.
	GetPrompt(ctx context.Context, name string, arguments map[string]string) (json.RawMessage, error)
	// Close closes the client connection.
	Close() error
}

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema,omitempty"`
}

// Resource represents an MCP resource definition.
type Resource struct {
	URI         string          `json:"uri"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	Size        *int64          `json:"size,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// Prompt represents an MCP prompt definition.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument represents an argument for an MCP prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// StdioTransportConfig holds configuration for stdio transport.
type StdioTransportConfig struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}
