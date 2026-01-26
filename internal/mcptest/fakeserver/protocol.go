// Package fakeserver provides a fake MCP server for testing.
package fakeserver

import (
	"encoding/json"
	"io"
	"time"
)

// Config controls the fake server's behavior.
type Config struct {
	// Tools to return from tools/list
	Tools []Tool `json:"tools"`

	// Per-method delays (simulate slow responses)
	// NOTE: Use short delays (10-50ms) in tests to avoid slow suite.
	Delays map[string]time.Duration `json:"delays"`

	// Per-method forced errors (JSON-RPC error responses)
	Errors map[string]JSONRPCError `json:"errors"`

	// Crash behavior
	CrashOnMethod     string `json:"crashOnMethod"`     // crash when this method is called
	CrashOnNthRequest int    `json:"crashOnNthRequest"` // crash on Nth request (0 = never)
	CrashExitCode     int    `json:"crashExitCode"`     // exit code when crashing

	// Retry testing: fail on specific attempt, succeed on others
	FailOnAttempt map[string]int `json:"failOnAttempt"` // method -> attempt number to fail (1-indexed)

	// Protocol edge cases for stream realism
	// These options test that the client handles interleaved messages correctly.
	SendNotificationBeforeResponse bool `json:"sendNotificationBeforeResponse"` // send a notification before each response
	SendMismatchedIDFirst          bool `json:"sendMismatchedIDFirst"`          // send a response with wrong ID before correct one

	// Protocol edge cases
	Malformed bool `json:"malformed"` // write invalid JSON

	// Tool call handling
	ToolHandler ToolHandler `json:"-"` // Custom handler for tools/call (not JSON-serializable)
	EchoToolCalls bool `json:"echoToolCalls"` // If true, tools/call returns the tool name and arguments as text
}

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// rpcNotification is a JSON-RPC 2.0 notification.
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// InitializeResult is the result of the initialize request.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ServerInfo describes the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities describes server capabilities.
type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability indicates the server supports tools.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ToolsListResult is the result of tools/list.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolCallParams is the params for tools/call.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult is the result of tools/call.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents a content block in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolHandler is a function that handles a tool call.
type ToolHandler func(name string, arguments json.RawMessage) ([]ContentBlock, bool, error)

// writeResponse writes a JSON-RPC response with NDJSON framing.
func writeResponse(out io.Writer, id json.RawMessage, result any, cfg Config) error {
	// Stream realism: send notification before response if configured
	if cfg.SendNotificationBeforeResponse {
		notification := rpcNotification{JSONRPC: "2.0", Method: "test/noise"}
		data, _ := json.Marshal(notification)
		out.Write(data)
		out.Write([]byte("\n"))
	}

	// Stream realism: send mismatched ID first if configured
	if cfg.SendMismatchedIDFirst {
		// Create a fake ID by appending "999" to simulate wrong ID
		fakeResp := rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`99999`), Result: json.RawMessage(`{}`)}
		data, _ := json.Marshal(fakeResp)
		out.Write(data)
		out.Write([]byte("\n"))
	}

	// Actual response (NDJSON)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: id, Result: resultJSON}
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	out.Write(data)
	out.Write([]byte("\n"))
	return nil
}

// writeErrorResponse writes a JSON-RPC error response with NDJSON framing.
func writeErrorResponse(out io.Writer, id json.RawMessage, rpcErr JSONRPCError, cfg Config) error {
	// Stream realism options apply here too
	if cfg.SendNotificationBeforeResponse {
		notification := rpcNotification{JSONRPC: "2.0", Method: "test/noise"}
		data, _ := json.Marshal(notification)
		out.Write(data)
		out.Write([]byte("\n"))
	}

	if cfg.SendMismatchedIDFirst {
		fakeResp := rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`99999`), Error: &JSONRPCError{Code: -1, Message: "wrong"}}
		data, _ := json.Marshal(fakeResp)
		out.Write(data)
		out.Write([]byte("\n"))
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr}
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	out.Write(data)
	out.Write([]byte("\n"))
	return nil
}
