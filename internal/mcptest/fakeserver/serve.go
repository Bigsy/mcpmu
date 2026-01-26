package fakeserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"
)

// Serve runs the fake MCP server, reading requests from in and writing responses to out.
// It handles initialize and tools/list methods, with configurable delays, errors, and crashes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	reader := bufio.NewReader(in)
	requestCount := 0
	methodAttempts := make(map[string]int) // track attempts per method for FailOnAttempt

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read JSON-RPC request (NDJSON framing - read until newline)
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		var req rpcRequest
		if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
			return err
		}

		requestCount++
		methodAttempts[req.Method]++

		// Check crash conditions
		if cfg.CrashOnNthRequest > 0 && requestCount >= cfg.CrashOnNthRequest {
			os.Exit(cfg.CrashExitCode)
		}
		if cfg.CrashOnMethod != "" && req.Method == cfg.CrashOnMethod {
			os.Exit(cfg.CrashExitCode)
		}

		// Apply delay if configured
		if delay, ok := cfg.Delays[req.Method]; ok {
			time.Sleep(delay)
		}

		// Check for Malformed response mode
		if cfg.Malformed {
			out.Write([]byte("this is not valid json\n"))
			continue
		}

		// Check for FailOnAttempt (for retry testing)
		if failAttempt, ok := cfg.FailOnAttempt[req.Method]; ok {
			if methodAttempts[req.Method] == failAttempt {
				writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32603, Message: "Simulated failure on attempt",
				}, cfg)
				continue
			}
		}

		// Check for forced error
		if rpcErr, ok := cfg.Errors[req.Method]; ok {
			writeErrorResponse(out, req.ID, rpcErr, cfg)
			continue
		}

		// Handle methods
		switch req.Method {
		case "initialize":
			writeResponse(out, req.ID, InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      ServerInfo{Name: "fake-server", Version: "1.0.0"},
				Capabilities:    Capabilities{Tools: &ToolsCapability{}},
			}, cfg)

		case "tools/list":
			tools := cfg.Tools
			if tools == nil {
				tools = []Tool{}
			}
			writeResponse(out, req.ID, ToolsListResult{Tools: tools}, cfg)

		case "tools/call":
			var params ToolCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32602, Message: "Invalid params: " + err.Error(),
				}, cfg)
				continue
			}

			// Check if we have a custom handler
			if cfg.ToolHandler != nil {
				content, isError, err := cfg.ToolHandler(params.Name, params.Arguments)
				if err != nil {
					writeErrorResponse(out, req.ID, JSONRPCError{
						Code: -32603, Message: err.Error(),
					}, cfg)
					continue
				}
				writeResponse(out, req.ID, ToolCallResult{
					Content: content,
					IsError: isError,
				}, cfg)
				continue
			}

			// Default: echo the call
			if cfg.EchoToolCalls {
				text := "Called tool: " + params.Name
				if params.Arguments != nil {
					text += "\nArguments: " + string(params.Arguments)
				}
				writeResponse(out, req.ID, ToolCallResult{
					Content: []ContentBlock{{Type: "text", Text: text}},
				}, cfg)
				continue
			}

			// If no handler and no echo, return success with tool name
			writeResponse(out, req.ID, ToolCallResult{
				Content: []ContentBlock{{Type: "text", Text: "Tool executed: " + params.Name}},
			}, cfg)

		case "notifications/initialized":
			// No response needed for notifications

		default:
			writeErrorResponse(out, req.ID, JSONRPCError{
				Code: -32601, Message: "Method not found",
			}, cfg)
		}
	}
}
