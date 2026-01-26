// Package server implements the MCP server that aggregates tools from managed upstream servers.
package server

import (
	"encoding/json"
	"fmt"
)

// MCP JSON-RPC error codes
const (
	// Standard JSON-RPC errors
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603

	// MCP-specific custom errors (-32000 to -32099)
	ErrCodeServerNotFound      = -32000
	ErrCodeServerFailedToStart = -32001
	ErrCodeToolCallTimeout     = -32002
	ErrCodeServerNotRunning    = -32003
	ErrCodeNamespaceNotFound   = -32004
	ErrCodeToolNotFound        = -32005
)

// RPCError represents a JSON-RPC 2.0 error.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

// NewRPCError creates a new RPC error with optional data.
func NewRPCError(code int, message string, data any) *RPCError {
	err := &RPCError{
		Code:    code,
		Message: message,
	}
	if data != nil {
		if dataBytes, jsonErr := json.Marshal(data); jsonErr == nil {
			err.Data = dataBytes
		}
	}
	return err
}

// Error constructors for common cases

func ErrParseError(detail string) *RPCError {
	return NewRPCError(ErrCodeParseError, "Parse error: "+detail, nil)
}

func ErrInvalidRequest(detail string) *RPCError {
	return NewRPCError(ErrCodeInvalidRequest, "Invalid Request: "+detail, nil)
}

func ErrMethodNotFound(method string) *RPCError {
	return NewRPCError(ErrCodeMethodNotFound, fmt.Sprintf("Method not found: %s", method), nil)
}

func ErrInvalidParams(detail string) *RPCError {
	return NewRPCError(ErrCodeInvalidParams, "Invalid params: "+detail, nil)
}

func ErrInternalError(detail string) *RPCError {
	return NewRPCError(ErrCodeInternalError, "Internal error: "+detail, nil)
}

func ErrServerNotFound(serverID string) *RPCError {
	return NewRPCError(ErrCodeServerNotFound, fmt.Sprintf("Server not found: %s", serverID), map[string]string{"serverId": serverID})
}

func ErrServerFailedToStart(serverID string, reason string) *RPCError {
	return NewRPCError(ErrCodeServerFailedToStart, fmt.Sprintf("Server %s failed to start: %s", serverID, reason), map[string]string{"serverId": serverID, "reason": reason})
}

func ErrToolCallTimeout(serverID, toolName string) *RPCError {
	return NewRPCError(ErrCodeToolCallTimeout, fmt.Sprintf("Tool call timeout: %s.%s", serverID, toolName), map[string]string{"serverId": serverID, "toolName": toolName})
}

func ErrServerNotRunning(serverID string) *RPCError {
	return NewRPCError(ErrCodeServerNotRunning, fmt.Sprintf("Server not running: %s", serverID), map[string]string{"serverId": serverID})
}

func ErrNamespaceNotFound(namespaceID string) *RPCError {
	return NewRPCError(ErrCodeNamespaceNotFound, fmt.Sprintf("Namespace not found: %s", namespaceID), map[string]string{"namespaceId": namespaceID})
}

func ErrToolNotFound(toolName string) *RPCError {
	return NewRPCError(ErrCodeToolNotFound, fmt.Sprintf("Tool not found: %s", toolName), map[string]string{"toolName": toolName})
}
