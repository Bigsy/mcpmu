package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

const (
	// LazyStartTimeout is the max time to wait for a lazy server start
	LazyStartTimeout = 10 * time.Second
)

// Router routes tool calls to the appropriate upstream server.
type Router struct {
	cfg        *config.Config
	supervisor *process.Supervisor
	aggregator *Aggregator

	// Active namespace info (set after initialize)
	activeNamespaceName string
	selectionMethod     SelectionMethod
}

// NewRouter creates a new tool call router.
func NewRouter(cfg *config.Config, supervisor *process.Supervisor, aggregator *Aggregator) *Router {
	return &Router{
		cfg:        cfg,
		supervisor: supervisor,
		aggregator: aggregator,
	}
}

// SetActiveNamespace sets the active namespace info for the router.
func (r *Router) SetActiveNamespace(namespaceName string, selection SelectionMethod) {
	r.activeNamespaceName = namespaceName
	r.selectionMethod = selection
}

// CallTool routes a tool call to the appropriate server and returns the result.
func (r *Router) CallTool(ctx context.Context, qualifiedName string, arguments json.RawMessage) (*ToolCallResult, *RPCError) {
	log.Printf("CallTool: %s", qualifiedName)

	// Parse the tool name
	serverName, toolName, isManager := ParseToolName(qualifiedName)

	// Handle manager tools (always allowed, no permission check)
	if isManager {
		return r.handleManagerTool(ctx, qualifiedName, arguments)
	}

	// Check permission (skip if no active namespace, i.e., selection=all)
	if r.activeNamespaceName != "" {
		allowed, reason := IsToolAllowed(r.cfg, r.activeNamespaceName, serverName, toolName)
		if !allowed {
			return nil, ErrToolDenied(qualifiedName, reason)
		}
	}

	// Validate server exists
	srv, ok := r.cfg.GetServer(serverName)
	if !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Get or start the server
	handle := r.supervisor.Get(serverName)
	if handle == nil || !handle.IsRunning() {
		// Lazy start the server
		var err error
		startCtx, cancel := context.WithTimeout(ctx, LazyStartTimeout)
		defer cancel()

		handle, err = r.supervisor.Start(startCtx, serverName, srv)
		if err != nil {
			return nil, ErrServerFailedToStart(serverName, err.Error())
		}
	}

	// Call the tool on the upstream server
	client := handle.Client()
	if client == nil {
		return nil, ErrServerNotRunning(serverName)
	}

	// Set timeout for the call using per-server config (defaults to 60s)
	timeout := time.Duration(srv.ToolTimeout()) * time.Second
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := client.CallTool(callCtx, toolName, arguments)
	if err != nil {
		if callCtx.Err() == context.DeadlineExceeded {
			return nil, ErrToolCallTimeout(serverName, toolName)
		}
		return nil, ErrInternalError(fmt.Sprintf("tool call failed: %v", err))
	}

	// Pass through the content blocks directly (they're already json.RawMessage)
	content := make([]json.RawMessage, len(result.Content))
	for i, c := range result.Content {
		content[i] = json.RawMessage(c)
	}

	return &ToolCallResult{
		Content: content,
		IsError: result.IsError,
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
	servers := make([]ServerInfo, 0, len(r.cfg.Servers))
	for name, srv := range r.cfg.Servers {
		info := ServerInfo{
			ID:      name, // Use name as ID for backwards compatibility in output
			Name:    name,
			Kind:    string(srv.GetKind()),
			Enabled: srv.IsEnabled(),
			Command: srv.Command,
		}

		// Check if running
		handle := r.supervisor.Get(name)
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
	srv, ok := r.cfg.GetServer(serverName)
	if !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Check if already running
	handle := r.supervisor.Get(serverName)
	if handle != nil && handle.IsRunning() {
		return textResult(fmt.Sprintf("Server %s is already running (PID: %d)", serverName, handle.PID())), nil
	}

	// Start the server
	handle, err := r.supervisor.Start(ctx, serverName, srv)
	if err != nil {
		return nil, ErrServerFailedToStart(serverName, err.Error())
	}

	// Refresh tools after starting
	if err := r.aggregator.RefreshServerTools(ctx, serverName); err != nil {
		log.Printf("Failed to refresh tools after start: %v", err)
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
	if _, ok := r.cfg.GetServer(serverName); !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Check if running
	handle := r.supervisor.Get(serverName)
	if handle == nil || !handle.IsRunning() {
		return textResult(fmt.Sprintf("Server %s is not running", serverName)), nil
	}

	// Stop the server
	if err := r.supervisor.Stop(serverName); err != nil {
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
	srv, ok := r.cfg.GetServer(serverName)
	if !ok {
		return nil, ErrServerNotFound(serverName)
	}

	// Stop if running
	handle := r.supervisor.Get(serverName)
	if handle != nil && handle.IsRunning() {
		if err := r.supervisor.Stop(serverName); err != nil {
			return nil, ErrInternalError(fmt.Sprintf("failed to stop server: %v", err))
		}
	}

	// Start the server
	handle, err := r.supervisor.Start(ctx, serverName, srv)
	if err != nil {
		return nil, ErrServerFailedToStart(serverName, err.Error())
	}

	// Refresh tools after restart
	if err := r.aggregator.RefreshServerTools(ctx, serverName); err != nil {
		log.Printf("Failed to refresh tools after restart: %v", err)
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
	if _, ok := r.cfg.GetServer(serverName); !ok {
		return nil, ErrServerNotFound(serverName)
	}

	handle := r.supervisor.Get(serverName)
	if handle == nil {
		return textResult(fmt.Sprintf("Server %s has not been started in this session", serverName)), nil
	}

	logs := handle.Logs()
	if len(logs) > args.Lines {
		logs = logs[len(logs)-args.Lines:]
	}

	result := fmt.Sprintf("Last %d log lines from %s:\n", len(logs), serverName)
	for _, line := range logs {
		result += line + "\n"
	}

	return textResult(result), nil
}

// handleNamespacesList returns the list of namespaces with active namespace info.
func (r *Router) handleNamespacesList(ctx context.Context) (*ToolCallResult, *RPCError) {
	namespaces := make([]NamespaceInfo, 0, len(r.cfg.Namespaces))
	for name, ns := range r.cfg.Namespaces {
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
	Content []json.RawMessage `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

// textResult creates a text content result.
func textResult(text string) *ToolCallResult {
	block, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	return &ToolCallResult{
		Content: []json.RawMessage{block},
	}
}

// mustJSON marshals a value to JSON, panicking on error.
func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(b)
}
