package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

const (
	// DefaultToolDiscoveryTimeout is the fallback timeout for tool discovery per server.
	// Per-server StartupTimeout (from config) is preferred when available.
	DefaultToolDiscoveryTimeout = 30 * time.Second
	// MaxConcurrentDiscovery is the max number of servers to discover tools from concurrently
	MaxConcurrentDiscovery = 8
	// ListToolsGracePeriod is the max time tools/list will block waiting for
	// server discovery before returning partial results. Kept under typical
	// client timeouts (Codex defaults to 10s).
	ListToolsGracePeriod = 8 * time.Second
)

// AggregatedTool represents a tool with qualified name and server info.
//
// Storage shape: internally we keep the raw upstream values — `Name` is the
// unqualified upstream tool name and `Description` is the upstream string with
// no prefix. Qualified names (`{server}.{tool}`) and the `[server]` description
// prefix are applied at the exposure boundary in `ListTools`/`GetTool`.
type AggregatedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`

	// Internal metadata (not serialized to MCP)
	serverID   string
	serverName string
	origName   string
}

// Aggregator collects and manages tools from multiple upstream servers.
type Aggregator struct {
	cfg        *config.Config
	supervisor *process.Supervisor
	acquire    func(context.Context, string) (*process.Handle, config.ServerConfig, error)

	// Tool cache: serverName -> unqualified toolName -> tool (raw, no prefix).
	// Per-server map so RefreshServerTools / DiscoverServer / partial-failure
	// handling are O(1) per server rather than scanning a flat map.
	tools   map[string]map[string]AggregatedTool
	toolsMu sync.RWMutex

	// Manager tools
	managerTools       []AggregatedTool
	exposeManagerTools bool
}

// NewAggregator creates a new tool aggregator.
func NewAggregator(cfg *config.Config, supervisor *process.Supervisor, exposeManagerTools bool) *Aggregator {
	a := &Aggregator{
		cfg:                cfg,
		supervisor:         supervisor,
		tools:              make(map[string]map[string]AggregatedTool),
		exposeManagerTools: exposeManagerTools,
	}
	a.acquire = a.getOrStartHandle
	a.managerTools = a.buildManagerTools()
	return a
}

// ListTools discovers and returns all tools from the specified servers.
// This may start servers lazily if they're not running.
// serverNames is a list of server names (map keys).
//
// Discovery is per-server: a failure for one server marks that server's cache
// entry as empty (not poisoned with stale data) but does not affect entries
// for other servers — including servers that were populated by an earlier
// DiscoverServer / RefreshServerTools call and are not in serverNames now.
func (a *Aggregator) ListTools(ctx context.Context, serverNames []string) ([]AggregatedTool, error) {
	type discoveryResult struct {
		serverName string
		tools      []AggregatedTool
		err        error
	}

	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup
	results := make([]discoveryResult, len(serverNames))

	for i, name := range serverNames {
		wg.Add(1)
		go func(idx int, serverName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			tools, err := a.discoverServerTools(ctx, serverName)
			results[idx] = discoveryResult{serverName: serverName, tools: tools, err: err}
		}(i, name)
	}

	wg.Wait()

	a.toolsMu.Lock()
	for _, r := range results {
		if r.err != nil {
			log.Printf("Failed to discover tools from %s: %v", r.serverName, r.err)
			// Empty entry — explicit "no tools" rather than stale data from
			// a previous discovery.
			a.tools[r.serverName] = map[string]AggregatedTool{}
			continue
		}
		m := make(map[string]AggregatedTool, len(r.tools))
		for _, t := range r.tools {
			m[t.origName] = t
		}
		a.tools[r.serverName] = m
	}

	// Build the exposed list from cache for the requested servers only.
	var allTools []AggregatedTool
	for _, name := range serverNames {
		for _, t := range a.tools[name] {
			allTools = append(allTools, exposeTool(name, t))
		}
	}
	a.toolsMu.Unlock()

	// Add manager tools only if exposed
	if a.exposeManagerTools {
		result := make([]AggregatedTool, 0, len(allTools)+len(a.managerTools))
		result = append(result, allTools...)
		result = append(result, a.managerTools...)
		return result, nil
	}

	return allTools, nil
}

// PendingServers returns enabled servers that have not yet finished tool discovery.
func (a *Aggregator) PendingServers(serverNames []string) []string {
	var pending []string
	for _, name := range serverNames {
		srv, ok := a.cfg.GetServer(name)
		if !ok || !srv.IsEnabled() {
			continue
		}
		handle := a.supervisor.Get(name)
		if handle == nil || !handle.IsRunning() || !handle.ToolsReady() {
			pending = append(pending, name)
		}
	}
	return pending
}

// GetTool returns a tool by its qualified name (`{server}.{tool}`) or manager
// tool name. The returned tool has the qualified name and `[server]` prefix
// applied to the description.
func (a *Aggregator) GetTool(name string) (AggregatedTool, bool) {
	// Check manager tools first
	for _, t := range a.managerTools {
		if t.Name == name {
			return t, true
		}
	}

	serverName, toolName, isManager := ParseToolName(name)
	if isManager {
		return AggregatedTool{}, false
	}

	a.toolsMu.RLock()
	defer a.toolsMu.RUnlock()
	m, ok := a.tools[serverName]
	if !ok {
		return AggregatedTool{}, false
	}
	t, ok := m[toolName]
	if !ok {
		return AggregatedTool{}, false
	}
	return exposeTool(serverName, t), true
}

// ManagerTools returns the built-in management tools. Exposure is a Session
// choice, so Core-backed sessions append these at their tools/list boundary.
func (a *Aggregator) ManagerTools() []AggregatedTool {
	return slices.Clone(a.managerTools)
}

// discoverServerTools acquires a ready server through the Core-provided
// get-or-start helper and retrieves its tools.
// Returns tools with raw (unqualified, unprefixed) values; the caller stores
// them as-is and applies qualification/prefix at the exposure boundary.
func (a *Aggregator) discoverServerTools(ctx context.Context, serverName string) ([]AggregatedTool, error) {
	srv, ok := a.cfg.GetServer(serverName)
	if !ok {
		return nil, fmt.Errorf("server not found: %s", serverName)
	}

	if !srv.IsEnabled() {
		log.Printf("Server %s is disabled, skipping", serverName)
		return nil, nil
	}

	handle, _, err := a.acquire(ctx, serverName)
	if err != nil {
		return nil, err
	}

	// Get tools from the running server
	mcpTools := handle.Tools()

	tools := make([]AggregatedTool, len(mcpTools))
	for i, t := range mcpTools {
		// Convert InputSchema
		var schemaJSON json.RawMessage
		if t.InputSchema != nil {
			if b, err := json.Marshal(t.InputSchema); err == nil {
				schemaJSON = b
			}
		}

		tools[i] = AggregatedTool{
			Name:        t.Name,        // unqualified, internal
			Description: t.Description, // raw, internal
			InputSchema: schemaJSON,
			serverID:    serverName,
			serverName:  serverName,
			origName:    t.Name,
		}
	}

	return tools, nil
}

// getOrStartHandle preserves NewAggregator's standalone test/API behavior.
// Core replaces acquire with its config-snapshotting helper in production.
func (a *Aggregator) getOrStartHandle(ctx context.Context, serverName string) (*process.Handle, config.ServerConfig, error) {
	return getOrStartHandle(ctx, a.cfg, a.supervisor, serverName)
}

// exposeTool converts an internally-stored tool into its client-visible form:
// qualified name and `[server]` description prefix.
func exposeTool(serverName string, t AggregatedTool) AggregatedTool {
	out := t
	out.Name = serverName + "." + t.origName
	if t.Description != "" {
		out.Description = fmt.Sprintf("[%s] %s", serverName, t.Description)
	} else {
		out.Description = fmt.Sprintf("[%s]", serverName)
	}
	return out
}

// ParseToolName extracts serverID and tool name from a qualified tool name.
func ParseToolName(qualifiedName string) (serverID, toolName string, isManager bool) {
	// Manager tools have "mcpmu." prefix
	if strings.HasPrefix(qualifiedName, "mcpmu.") {
		return "", qualifiedName, true
	}

	// Regular tools: serverId.toolName
	parts := strings.SplitN(qualifiedName, ".", 2)
	if len(parts) != 2 {
		return "", qualifiedName, false
	}
	return parts[0], parts[1], false
}

// buildManagerTools creates the mcpmu.* meta-tools.
func (a *Aggregator) buildManagerTools() []AggregatedTool {
	return []AggregatedTool{
		{
			Name:        "mcpmu.servers_list",
			Description: "List all configured MCP servers and their status",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
		{
			Name:        "mcpmu.servers_start",
			Description: "Start a specific MCP server by ID",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"server_id": {"type": "string", "description": "The ID of the server to start"}}, "required": ["server_id"]}`),
		},
		{
			Name:        "mcpmu.servers_stop",
			Description: "Stop a specific MCP server by ID",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"server_id": {"type": "string", "description": "The ID of the server to stop"}}, "required": ["server_id"]}`),
		},
		{
			Name:        "mcpmu.servers_restart",
			Description: "Restart a specific MCP server by ID",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"server_id": {"type": "string", "description": "The ID of the server to restart"}}, "required": ["server_id"]}`),
		},
		{
			Name:        "mcpmu.server_logs",
			Description: "Get recent log lines from a server's stderr",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"server_id": {"type": "string", "description": "The ID of the server"}, "lines": {"type": "integer", "description": "Number of lines to return (default: 50)", "default": 50}}, "required": ["server_id"]}`),
		},
		{
			Name:        "mcpmu.namespaces_list",
			Description: "List all namespaces and show which is active",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
	}
}

// DiscoverServer discovers tools from a single server and merges them into
// the cache. Existing entries for tools whose names appear in the new
// discovery are overwritten; entries for other tools from this server are
// left in place (per-server append semantics, kept for parity with prior
// behavior).
func (a *Aggregator) DiscoverServer(ctx context.Context, serverName string) ([]AggregatedTool, error) {
	tools, err := a.discoverServerTools(ctx, serverName)
	if err != nil {
		return nil, err
	}

	a.toolsMu.Lock()
	m, ok := a.tools[serverName]
	if !ok {
		m = make(map[string]AggregatedTool, len(tools))
		a.tools[serverName] = m
	}
	for _, t := range tools {
		m[t.origName] = t
	}
	a.toolsMu.Unlock()

	// Return tools in their exposed form so callers see qualified names.
	out := make([]AggregatedTool, len(tools))
	for i, t := range tools {
		out[i] = exposeTool(serverName, t)
	}
	return out, nil
}

// RefreshServerTools refreshes the tool cache for a specific server (full
// per-server replace — old entries for that server are dropped).
func (a *Aggregator) RefreshServerTools(ctx context.Context, serverName string) error {
	tools, err := a.discoverServerTools(ctx, serverName)
	if err != nil {
		return err
	}

	m := make(map[string]AggregatedTool, len(tools))
	for _, t := range tools {
		m[t.origName] = t
	}

	a.toolsMu.Lock()
	a.tools[serverName] = m
	a.toolsMu.Unlock()

	return nil
}

// ToolForServer returns the original tool info for routing a call.
func (a *Aggregator) ToolForServer(qualifiedName string) (serverID, origToolName string, ok bool) {
	serverName, toolName, isManager := ParseToolName(qualifiedName)
	if isManager {
		return "", "", false
	}

	a.toolsMu.RLock()
	defer a.toolsMu.RUnlock()
	m, ok := a.tools[serverName]
	if !ok {
		return "", "", false
	}
	t, ok := m[toolName]
	if !ok {
		return "", "", false
	}
	return t.serverID, t.origName, true
}
