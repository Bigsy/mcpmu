package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/metrics"
)

// Router routes tool calls to the appropriate upstream server.
type Router struct {
	session *Session

	// Active namespace info (set after initialize)
	activeNamespaceName string
	selectionMethod     SelectionMethod
}

// NewRouter creates a new tool call router.
func NewRouter(session *Session) *Router {
	return &Router{session: session}
}

// SetActiveNamespace sets the active namespace info for the router.
func (r *Router) SetActiveNamespace(namespaceName string, selection SelectionMethod) {
	r.activeNamespaceName = namespaceName
	r.selectionMethod = selection
}

// CallTool routes a tool call to the appropriate server and returns the
// result. meta is the request's `_meta` object as it should reach the upstream
// server — already rewritten by the caller where mcpmu must not forward a
// value verbatim (progressToken).
//
// Every dispatched call is recorded as exactly one usage-metrics sample at
// exit, whatever the path. Misaddressed calls (server not found) are not tool
// usage and are not recorded; the internal 4xx-reinit retry is one call, so
// only the final outcome is recorded.
func (r *Router) CallTool(ctx context.Context, qualifiedName string, arguments, meta json.RawMessage) (*ToolCallResult, *RPCError) {
	log.Printf("CallTool: %s", qualifiedName)

	start := time.Now()

	// Parse the tool name
	serverName, toolName, isManager := ParseToolName(qualifiedName)

	// Handle manager tools (always allowed, no permission check)
	if isManager {
		result, rpcErr := r.handleManagerTool(ctx, qualifiedName, arguments)
		outcome := metrics.OutcomeOK
		if rpcErr != nil {
			outcome = metrics.OutcomeError
		}
		r.session.currentRecorder().Record(metrics.CallSample{
			Time:      start,
			Namespace: r.activeNamespaceName,
			Server:    "mcpmu",
			Tool:      strings.TrimPrefix(qualifiedName, "mcpmu."),
			Duration:  time.Since(start),
			Outcome:   outcome,
		})
		return result, rpcErr
	}

	record := func(outcome metrics.Outcome, duration time.Duration) {
		r.session.currentRecorder().Record(metrics.CallSample{
			Time:      start,
			Namespace: r.activeNamespaceName,
			Server:    serverName,
			Tool:      toolName,
			Duration:  duration,
			Outcome:   outcome,
		})
	}

	// Permission check — always runs. IsToolAllowed handles:
	// 1. Global deny (applies even without a namespace)
	// 2. Namespace-scoped permissions (when namespace is active)
	// 3. Returns true for everything else when namespace is empty
	cfg := r.session.currentConfig()
	allowed, reason := IsToolAllowed(cfg, r.activeNamespaceName, serverName, toolName)
	if !allowed {
		record(metrics.OutcomeDenied, 0)
		return nil, ErrToolDenied(qualifiedName, reason)
	}

	// Validate server exists
	srv, ok := cfg.GetServer(serverName)
	if !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Acquire through the Core's single lazy-start/readiness path.
	sc, rpcErr := r.session.getOrStartServer(ctx, serverName)
	if rpcErr != nil {
		record(metrics.OutcomeError, time.Since(start))
		return nil, rpcErr
	}
	client := sc.client

	// Set timeout for the call using per-server config (defaults to 60s)
	timeout := time.Duration(srv.ToolTimeout()) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := client.CallToolWithMeta(callCtx, toolName, arguments, meta)
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			record(metrics.OutcomeTimeout, time.Since(start))
			return nil, ErrToolCallTimeout(serverName, toolName)
		}

		// On 4xx errors (stale session, server reset, etc.), reinitialize and retry once.
		// 401 is excluded — the transport returns UnauthorizedError for that, not "request failed: 4xx".
		if isRetriableHTTPError(err) {
			log.Printf("CallTool: 4xx error for %s.%s, reinitializing: %v", serverName, toolName, err)

			_ = r.session.supervisor.StopInstance(r.session.instanceID(serverName))

			reinitialized, reinitErr := r.session.getOrStartServer(ctx, serverName)
			if reinitErr != nil {
				record(metrics.OutcomeError, time.Since(start))
				return nil, ErrInternalError(fmt.Sprintf("tool call failed (reinit: %v) (original: %v)", reinitErr, err))
			}
			client = reinitialized.client

			retryCtx, retryCancel := context.WithTimeout(ctx, timeout)
			defer retryCancel()

			result, err = client.CallToolWithMeta(retryCtx, toolName, arguments, meta)
			if err != nil {
				record(metrics.OutcomeError, time.Since(start))
				return nil, ErrInternalError(fmt.Sprintf("tool call failed after reinit: %v", err))
			}

			log.Printf("CallTool: retry succeeded for %s.%s after reinit", serverName, toolName)
		} else {
			record(metrics.OutcomeError, time.Since(start))
			return nil, ErrInternalError(fmt.Sprintf("tool call failed: %v", err))
		}
	}

	if result.IsError {
		record(metrics.OutcomeToolError, time.Since(start))
	} else {
		record(metrics.OutcomeOK, time.Since(start))
	}

	// Pass through the content blocks directly (they're already json.RawMessage)
	content := make([]json.RawMessage, len(result.Content))
	for i, c := range result.Content {
		content[i] = json.RawMessage(c)
	}

	return &ToolCallResult{
		Content:           content,
		StructuredContent: result.StructuredContent,
		IsError:           result.IsError,
		Meta:              result.Meta,
	}, nil
}

// handleManagerTool handles mcpmu.* meta-tools.
func (r *Router) handleManagerTool(ctx context.Context, toolName string, arguments json.RawMessage) (*ToolCallResult, *RPCError) {
	switch toolName {
	case "mcpmu.servers_list":
		return r.handleServersList(ctx)
	case "mcpmu.servers_start":
		return r.handleServersStart(ctx, arguments)
	case "mcpmu.servers_stop":
		return r.handleServersStop(ctx, arguments)
	case "mcpmu.servers_restart":
		return r.handleServersRestart(ctx, arguments)
	case "mcpmu.server_logs":
		return r.handleServerLogs(ctx, arguments)
	case "mcpmu.namespaces_list":
		return r.handleNamespacesList(ctx)
	default:
		return nil, ErrToolNotFound(toolName)
	}
}

// handleServersList returns the list of configured servers with status.
func (r *Router) handleServersList(ctx context.Context) (*ToolCallResult, *RPCError) {
	cfg := r.session.currentConfig()
	servers := make([]ServerInfo, 0, len(cfg.Servers))
	for name, srv := range cfg.Servers {
		info := ServerInfo{
			ID:      name, // Use name as ID for backwards compatibility in output
			Name:    name,
			Kind:    string(srv.GetKind()),
			Enabled: srv.IsEnabled(),
			Command: srv.Command,
		}

		// Check if running
		handle := r.session.supervisor.GetInstance(r.session.instanceID(name))
		if handle != nil && handle.IsRunning() {
			info.Status = "running"
			info.PID = handle.PID()
			info.Uptime = handle.Uptime().String()
			info.ToolCount = len(handle.Tools())
		} else {
			info.Status = "stopped"
		}

		servers = append(servers, info)
	}

	return textResult(mustJSON(servers)), nil
}

// handleServersStart starts a server by name.
func (r *Router) handleServersStart(ctx context.Context, arguments json.RawMessage) (*ToolCallResult, *RPCError) {
	var args struct {
		ServerID string `json:"server_id"` // Keep JSON field name for API compatibility
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	serverName := args.ServerID // server_id now means server name
	cfg := r.session.currentConfig()
	_, ok := cfg.GetServer(serverName)
	if !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Check if already running
	instance := r.session.instanceID(serverName)
	handle := r.session.supervisor.GetInstance(instance)
	if handle != nil && handle.IsRunning() {
		return textResult(fmt.Sprintf("Server %s is already running (PID: %d)", serverName, handle.PID())), nil
	}

	// Start the server
	handle, _, err := r.session.getOrStartHandle(ctx, serverName)
	if err != nil {
		return nil, ErrServerFailedToStart(serverName, err.Error())
	}

	// Wait for init + tool discovery
	if err := handle.WaitForTools(ctx); err != nil {
		return nil, ErrServerFailedToStart(serverName, err.Error())
	}

	return textResult(fmt.Sprintf("Started server %s (PID: %d, tools: %d)", serverName, handle.PID(), len(handle.Tools()))), nil
}

// handleServersStop stops a server by name.
func (r *Router) handleServersStop(ctx context.Context, arguments json.RawMessage) (*ToolCallResult, *RPCError) {
	var args struct {
		ServerID string `json:"server_id"` // Keep JSON field name for API compatibility
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	serverName := args.ServerID
	if _, ok := r.session.currentConfig().GetServer(serverName); !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Check if running
	instance := r.session.instanceID(serverName)
	handle := r.session.supervisor.GetInstance(instance)
	if handle == nil || !handle.IsRunning() {
		return textResult(fmt.Sprintf("Server %s is not running", serverName)), nil
	}

	// Stop the server
	if err := r.session.supervisor.StopInstance(instance); err != nil {
		return nil, ErrInternalError(fmt.Sprintf("failed to stop server: %v", err))
	}

	return textResult(fmt.Sprintf("Stopped server %s", serverName)), nil
}

// handleServersRestart restarts a server by name.
func (r *Router) handleServersRestart(ctx context.Context, arguments json.RawMessage) (*ToolCallResult, *RPCError) {
	var args struct {
		ServerID string `json:"server_id"` // Keep JSON field name for API compatibility
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	serverName := args.ServerID
	if _, ok := r.session.currentConfig().GetServer(serverName); !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Stop and start under one per-instance lifecycle lock.
	handle, err := r.session.restartHandle(ctx, serverName)
	if err != nil {
		return nil, ErrServerFailedToStart(serverName, err.Error())
	}

	// Wait for init + tool discovery
	if err := handle.WaitForTools(ctx); err != nil {
		return nil, ErrServerFailedToStart(serverName, err.Error())
	}

	return textResult(fmt.Sprintf("Restarted server %s (PID: %d, tools: %d)", serverName, handle.PID(), len(handle.Tools()))), nil
}

// handleServerLogs returns recent log lines from a server.
func (r *Router) handleServerLogs(ctx context.Context, arguments json.RawMessage) (*ToolCallResult, *RPCError) {
	var args struct {
		ServerID string `json:"server_id"` // Keep JSON field name for API compatibility
		Lines    int    `json:"lines"`
	}
	args.Lines = 50 // default
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Validate line count
	if args.Lines < 0 {
		return nil, ErrInvalidParams("lines must be non-negative")
	}
	if args.Lines == 0 {
		args.Lines = 50 // treat 0 as default
	}

	serverName := args.ServerID
	if _, ok := r.session.currentConfig().GetServer(serverName); !ok {
		return nil, ErrServerNotFound(serverName)
	}

	handle := r.session.supervisor.GetInstance(r.session.instanceID(serverName))
	if handle == nil {
		return textResult(fmt.Sprintf("Server %s has not been started in this session", serverName)), nil
	}

	logs := handle.Logs()
	if len(logs) > args.Lines {
		logs = logs[len(logs)-args.Lines:]
	}

	var result strings.Builder
	_, _ = fmt.Fprintf(&result, "Last %d log lines from %s:\n", len(logs), serverName)
	for _, line := range logs {
		result.WriteString(line + "\n")
	}

	return textResult(result.String()), nil
}

// handleNamespacesList returns the list of namespaces with active namespace info.
func (r *Router) handleNamespacesList(ctx context.Context) (*ToolCallResult, *RPCError) {
	cfg := r.session.currentConfig()
	namespaces := make([]NamespaceInfo, 0, len(cfg.Namespaces))
	for name, ns := range cfg.Namespaces {
		namespaces = append(namespaces, NamespaceInfo{
			ID:          name, // Use name as ID for backwards compatibility
			Name:        name,
			Description: ns.Description,
			ServerCount: len(ns.ServerIDs),
			ServerIDs:   ns.ServerIDs,
		})
	}

	// Return envelope with active namespace info
	result := NamespacesListResult{
		ActiveNamespaceID: r.activeNamespaceName,
		Selection:         string(r.selectionMethod),
		Namespaces:        namespaces,
	}

	return textResult(mustJSON(result)), nil
}

// ServerInfo represents server status information.
type ServerInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	Command   string `json:"command,omitempty"`
	Status    string `json:"status"`
	PID       int    `json:"pid,omitempty"`
	Uptime    string `json:"uptime,omitempty"`
	ToolCount int    `json:"toolCount,omitempty"`
}

// NamespaceInfo represents namespace information.
type NamespaceInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	ServerCount int      `json:"serverCount"`
	ServerIDs   []string `json:"serverIds"`
}

// NamespacesListResult is the envelope for the namespaces_list response.
type NamespacesListResult struct {
	ActiveNamespaceID string          `json:"activeNamespaceId"`
	Selection         string          `json:"selection"` // "flag", "default", "only", or "all"
	Namespaces        []NamespaceInfo `json:"namespaces"`
}

// ToolCallResult represents the result of a tool call.
type ToolCallResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
	Meta              json.RawMessage   `json:"_meta,omitempty"`
}

// textResult creates a text content result.
func textResult(text string) *ToolCallResult {
	block, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	return &ToolCallResult{
		Content: []json.RawMessage{block},
	}
}

// isRetriableHTTPError checks if an error is a 4xx HTTP error that might
// be resolved by reinitializing the server (e.g., stale session).
// 401 Unauthorized is excluded as it returns a distinct UnauthorizedError type
// and has its own OAuth handling flow.
func isRetriableHTTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "request failed: 4")
}

// mustJSON marshals a value to JSON, panicking on error.
func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(b)
}
