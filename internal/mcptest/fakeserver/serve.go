package fakeserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// logRequest appends method to the configured request log path if set. Used
// by integration tests that need to assert which upstream methods were
// actually invoked.
func logRequest(path, method string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(method + "\n")
}

// Serve runs the fake MCP server, reading requests from in and writing responses to out.
// It handles initialize and tools/list methods, with configurable delays, errors, and crashes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	reader := bufio.NewReader(in)
	requestCount := 0
	methodAttempts := make(map[string]int) // track attempts per method for FailOnAttempt

	// Serializes concurrent writes between the main request loop and any
	// out-of-band notification emitted by a SetUpdateHook caller.
	var writeMu sync.Mutex
	syncedOut := &syncedWriter{w: out, mu: &writeMu}

	// Track subscribed URIs so the fake can validate and optionally confirm.
	var subMu sync.Mutex
	subscribed := make(map[string]bool)

	// All subsequent response writes go through the synchronized wrapper so
	// they can't interleave with an emit() frame.
	out = syncedOut

	emitUpdate := func(uri string) {
		_ = writeFrame(syncedOut, rpcNotification{
			JSONRPC: "2.0",
			Method:  "notifications/resources/updated",
			Params:  map[string]string{"uri": uri},
		})
	}

	if cfg.SetUpdateHook != nil {
		cfg.SetUpdateHook(emitUpdate)
	}

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
		logRequest(cfg.RequestLogPath, req.Method)

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
			_, _ = out.Write([]byte("this is not valid json\n"))
			continue
		}

		// Check for FailOnAttempt (for retry testing)
		if failAttempt, ok := cfg.FailOnAttempt[req.Method]; ok {
			if methodAttempts[req.Method] == failAttempt {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32603, Message: "Simulated failure on attempt",
				}, cfg)
				continue
			}
		}

		// Check for forced error
		if rpcErr, ok := cfg.Errors[req.Method]; ok {
			_ = writeErrorResponse(out, req.ID, rpcErr, cfg)
			continue
		}

		// Handle methods
		switch req.Method {
		case "initialize":
			caps := Capabilities{Tools: &ToolsCapability{}}
			if len(cfg.Resources) > 0 || cfg.ResourceContents != nil || cfg.ResourcesSubscribe {
				caps.Resources = &ResourcesCapability{Subscribe: cfg.ResourcesSubscribe}
			}
			if len(cfg.Prompts) > 0 || cfg.PromptMessages != nil {
				caps.Prompts = &PromptsCapability{}
			}
			_ = writeResponse(out, req.ID, InitializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      ServerInfo{Name: "fake-server", Version: "1.0.0"},
				Capabilities:    caps,
			}, cfg)

		case "tools/list":
			tools := cfg.Tools
			if tools == nil {
				tools = []Tool{}
			}
			_ = writeResponse(out, req.ID, ToolsListResult{Tools: tools}, cfg)

		case "tools/call":
			var params ToolCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32602, Message: "Invalid params: " + err.Error(),
				}, cfg)
				continue
			}

			// Check if we have a custom handler
			if cfg.ToolHandler != nil {
				content, isError, err := cfg.ToolHandler(params.Name, params.Arguments)
				if err != nil {
					_ = writeErrorResponse(out, req.ID, JSONRPCError{
						Code: -32603, Message: err.Error(),
					}, cfg)
					continue
				}
				_ = writeResponse(out, req.ID, ToolCallResult{
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
				_ = writeResponse(out, req.ID, ToolCallResult{
					Content: []ContentBlock{{Type: "text", Text: text}},
				}, cfg)
				continue
			}

			// If no handler and no echo, return success with tool name
			_ = writeResponse(out, req.ID, ToolCallResult{
				Content: []ContentBlock{{Type: "text", Text: "Tool executed: " + params.Name}},
			}, cfg)

		case "resources/list":
			resources := cfg.Resources
			if resources == nil {
				resources = []Resource{}
			}
			_ = writeResponse(out, req.ID, ResourcesListResult{Resources: resources}, cfg)

		case "resources/read":
			var params ResourceReadParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32602, Message: "Invalid params: " + err.Error(),
				}, cfg)
				continue
			}
			if cfg.ResourceContents != nil {
				if content, ok := cfg.ResourceContents[params.URI]; ok {
					_ = writeResponse(out, req.ID, ResourceReadResult{Contents: content}, cfg)
					continue
				}
			}
			_ = writeErrorResponse(out, req.ID, JSONRPCError{
				Code: -32002, Message: "Resource not found: " + params.URI,
			}, cfg)

		case "prompts/list":
			prompts := cfg.Prompts
			if prompts == nil {
				prompts = []Prompt{}
			}
			_ = writeResponse(out, req.ID, PromptsListResult{Prompts: prompts}, cfg)

		case "prompts/get":
			var params PromptGetParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32602, Message: "Invalid params: " + err.Error(),
				}, cfg)
				continue
			}
			if cfg.PromptMessages != nil {
				if messages, ok := cfg.PromptMessages[params.Name]; ok {
					_ = writeResponse(out, req.ID, PromptGetResult{Messages: messages}, cfg)
					continue
				}
			}
			_ = writeErrorResponse(out, req.ID, JSONRPCError{
				Code: -32002, Message: "Prompt not found: " + params.Name,
			}, cfg)

		case "resources/subscribe":
			if !cfg.ResourcesSubscribe {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32601, Message: "Method not found",
				}, cfg)
				continue
			}
			var params ResourceReadParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32602, Message: "Invalid params: " + err.Error(),
				}, cfg)
				continue
			}
			subMu.Lock()
			subscribed[params.URI] = true
			subMu.Unlock()
			_ = writeResponse(out, req.ID, struct{}{}, cfg)
			if cfg.EmitUpdateAfterSubscribe {
				if cfg.PostSubscribeEmitDelayMs > 0 {
					time.Sleep(time.Duration(cfg.PostSubscribeEmitDelayMs) * time.Millisecond)
				}
				emitUpdate(params.URI)
			}
			if cfg.OnSubscribe != nil {
				cfg.OnSubscribe(params.URI)
			}

		case "resources/unsubscribe":
			if !cfg.ResourcesSubscribe {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32601, Message: "Method not found",
				}, cfg)
				continue
			}
			var params ResourceReadParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				_ = writeErrorResponse(out, req.ID, JSONRPCError{
					Code: -32602, Message: "Invalid params: " + err.Error(),
				}, cfg)
				continue
			}
			subMu.Lock()
			delete(subscribed, params.URI)
			subMu.Unlock()
			_ = writeResponse(out, req.ID, struct{}{}, cfg)
			if cfg.EmitUpdateAfterUnsubscribe {
				if cfg.PostUnsubscribeEmitDelayMs > 0 {
					time.Sleep(time.Duration(cfg.PostUnsubscribeEmitDelayMs) * time.Millisecond)
				}
				emitUpdate(params.URI)
			}
			if cfg.OnUnsubscribe != nil {
				cfg.OnUnsubscribe(params.URI)
			}

		case "notifications/initialized":
			// Startup-update emission: emit configured URIs after the client
			// signals it has finished initializing. Tests that exercise stray
			// or early notifications rely on this ordering.
			for _, uri := range cfg.EmitStartupUpdates {
				emitUpdate(uri)
			}

		default:
			_ = writeErrorResponse(out, req.ID, JSONRPCError{
				Code: -32601, Message: "Method not found",
			}, cfg)
		}
	}
}

// syncedWriter serializes writes from multiple goroutines (main request loop
// and the out-of-band update emitter) so NDJSON frames don't interleave.
type syncedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s *syncedWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
