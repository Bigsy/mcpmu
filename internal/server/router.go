package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/mcp"
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
		r.recordMeta(strings.TrimPrefix(qualifiedName, "mcpmu."), start, rpcErr)
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
	// failureOutcome classifies a failure at recording time: a cancelled
	// parent context means the client hung up, wherever the failure surfaced
	// — during lazy startup, mid-call, reinitialization, or the retry — and
	// that is not an upstream error, so it stays out of the error rate.
	failureOutcome := func() metrics.Outcome {
		if ctx.Err() == context.Canceled {
			return metrics.OutcomeCancelled
		}
		return metrics.OutcomeError
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
		record(failureOutcome(), time.Since(start))
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
		// The client abandoned the call (notifications/cancelled, or an HTTP
		// client that dropped its connection). Not an upstream failure — keep
		// it out of the error rate.
		if ctx.Err() == context.Canceled {
			record(metrics.OutcomeCancelled, time.Since(start))
			return nil, ErrInternalError(fmt.Sprintf("tool call cancelled: %v", context.Cause(ctx)))
		}

		// A stale session (the transport's SessionExpiredError, usually after
		// the client layer's own recovery already failed) is the one failure
		// a reinit can fix. Other 4xx — 403, 429 — mean this call was
		// rejected; restarting the instance would punish unrelated sessions.
		if isRetriableHTTPError(err) {
			log.Printf("CallTool: stale session for %s.%s, reinitializing: %v", serverName, toolName, err)

			_ = r.session.supervisor.StopInstance(r.session.instanceID(serverName))

			reinitialized, reinitErr := r.session.getOrStartServer(ctx, serverName)
			if reinitErr != nil {
				record(failureOutcome(), time.Since(start))
				return nil, ErrInternalError(fmt.Sprintf("tool call failed (reinit: %v) (original: %v)", reinitErr, err))
			}
			client = reinitialized.client

			retryCtx, retryCancel := context.WithTimeout(ctx, timeout)
			defer retryCancel()

			result, err = client.CallToolWithMeta(retryCtx, toolName, arguments, meta)
			if err != nil {
				if retryCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
					record(metrics.OutcomeTimeout, time.Since(start))
					return nil, ErrToolCallTimeout(serverName, toolName)
				}
				record(failureOutcome(), time.Since(start))
				return nil, ErrInternalError(fmt.Sprintf("tool call failed after reinit: %v", err))
			}

			log.Printf("CallTool: retry succeeded for %s.%s after reinit", serverName, toolName)
		} else {
			record(failureOutcome(), time.Since(start))
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

// recordMeta records a call that mcpmu itself answered under server="mcpmu" —
// a manager tool (mcpmu.*) or a compressed-surface meta-call (list_tools,
// get_tool_schema) — so it is visible on the Metrics page without polluting
// per-server error rates. tool is the unqualified name. invoke_tool is
// deliberately absent: it dispatches through CallTool, which records the
// sample against the target tool.
func (r *Router) recordMeta(tool string, start time.Time, rpcErr *RPCError) {
	outcome := metrics.OutcomeOK
	if rpcErr != nil {
		outcome = metrics.OutcomeError
	}
	r.session.currentRecorder().Record(metrics.CallSample{
		Time:      start,
		Namespace: r.activeNamespaceName,
		Server:    "mcpmu",
		Tool:      tool,
		Duration:  time.Since(start),
		Outcome:   outcome,
	})
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

// isRetriableHTTPError reports whether an error justifies restarting the
// upstream instance: only a stale session does. The transport turns that
// shape — a 404 for a session the server once issued — into
// SessionExpiredError, so this reduces to errors.As. Every other 4xx must
// not restart: 403 and 429 mean this one call was rejected, and restarting a
// shared instance tears down sessions other callers depend on.
//
// Note the HTTP client layer has usually already recovered the session by
// the time this sees an error (sendWithSessionRecovery reinitializes and
// retries once); a SessionExpiredError reaching here means that recovery
// itself failed, and a full instance restart is the remaining remedy.
func isRetriableHTTPError(err error) bool {
	var expired *mcp.SessionExpiredError
	return errors.As(err, &expired)
}

// mustJSON marshals a value to JSON, panicking on error.
func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(b)
}
