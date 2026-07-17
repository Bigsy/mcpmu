package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
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

	catalog *verifiedCatalog

	// Manager tools
	managerTools       []AggregatedTool
	exposeManagerTools bool
}

// NewAggregator creates a new tool aggregator.
func NewAggregator(cfg *config.Config, supervisor *process.Supervisor, exposeManagerTools bool) *Aggregator {
	a := &Aggregator{
		cfg:                cfg,
		supervisor:         supervisor,
		catalog:            newVerifiedCatalog(),
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
// Discovery is singleflight per InstanceID. Supervisor owns initialize and
// initial tools/list; Aggregator consumes that immutable result into the
// Core-owned verified catalog.
func (a *Aggregator) ListTools(ctx context.Context, serverNames []string) ([]AggregatedTool, error) {
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup
	errs := make([]error, len(serverNames))

	for i, name := range serverNames {
		wg.Add(1)
		go func(idx int, serverName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[idx] = a.ensureCatalog(ctx, serverName)
		}(i, name)
	}

	wg.Wait()
	for i, err := range errs {
		if err != nil {
			log.Printf("Failed to discover tools from %s: %v", serverNames[i], err)
		}
	}
	allTools := a.catalog.tools(serverNames)

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
		state := a.catalog.snapshot(process.SharedInstanceID(name)).state
		if state != catalogVerified {
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

	t, ok := a.catalog.tool(serverName, toolName)
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

func (a *Aggregator) ensureCatalog(ctx context.Context, serverName string) error {
	srv, ok := a.cfg.GetServer(serverName)
	if !ok {
		return fmt.Errorf("server not found: %s", serverName)
	}
	if !srv.IsEnabled() {
		return nil
	}
	id := process.SharedInstanceID(serverName)
	flight, owner := a.catalog.begin(id)
	if flight == nil {
		return nil
	}
	if !owner {
		if err := waitForFlight(ctx, flight); err != nil {
			return err
		}
		return catalogError(a.catalog.snapshot(id))
	}
	defer a.catalog.finish(id, flight)

	entry := a.catalog.snapshot(id)
	handle := a.supervisor.GetInstance(id)
	if entry.state == catalogDiscovering && entry.err != nil && handle != nil && handle.IsRunning() && handle.ToolsReady() &&
		handle.Generation() == entry.generation {
		return a.refreshHandleTools(ctx, handle)
	}

	handle, _, err := a.acquire(ctx, serverName)
	if err != nil {
		generation := uint64(0)
		if current := a.supervisor.GetInstance(id); current != nil {
			generation = current.Generation()
		}
		a.catalog.fail(id, generation, err)
		return err
	}
	result, exists := handle.DiscoveryResult()
	if !exists {
		err := errors.New("supervisor completed readiness without a discovery result")
		a.catalog.fail(id, handle.Generation(), err)
		return err
	}
	a.catalog.apply(result)
	return catalogError(a.catalog.snapshot(id))
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

// DiscoverServer verifies one server and returns its full catalog entry.
func (a *Aggregator) DiscoverServer(ctx context.Context, serverName string) ([]AggregatedTool, error) {
	if err := a.ensureCatalog(ctx, serverName); err != nil {
		return nil, err
	}
	return a.catalog.tools([]string{serverName}), nil
}

// RefreshServerTools refreshes the tool cache for a specific server (full
// per-server replace — old entries for that server are dropped).
func (a *Aggregator) RefreshServerTools(ctx context.Context, serverName string) error {
	handle := a.supervisor.Get(serverName)
	if handle == nil || !handle.IsRunning() {
		return a.ensureCatalog(ctx, serverName)
	}
	return a.refreshHandleTools(ctx, handle)
}

func (a *Aggregator) refreshHandleTools(ctx context.Context, handle *process.Handle) error {
	capabilities := handle.Capabilities()
	result := process.DiscoveryResult{
		Instance: handle.InstanceID(), Generation: handle.Generation(), Initialized: true,
		Capabilities: capabilities,
	}
	if capabilities.Tools == nil {
		result.Tools = []mcp.Tool{}
		a.catalog.apply(result)
		return nil
	}
	client := handle.Client()
	if client == nil {
		result.Err = errors.New("upstream client is unavailable")
		a.catalog.apply(result)
		return result.Err
	}
	tools, err := client.ListTools(ctx)
	result.Tools = tools
	result.Err = err
	if err == nil {
		handle.SetTools(tools)
	}
	a.catalog.apply(result)
	return err
}

func (a *Aggregator) applyDiscovery(result process.DiscoveryResult) (changed, hadPrior bool) {
	return a.catalog.apply(result)
}

func (a *Aggregator) invalidateInstance(id process.InstanceID, generation uint64) {
	a.catalog.invalidate(id, generation)
}

type catalogCapability uint8

const (
	catalogResources catalogCapability = iota
	catalogPrompts
)

// shouldQueryCapability implements list fan-out scoping. Running upstreams
// are queried regardless of catalog state; stopped verified upstreams that did
// not advertise the relevant capability are skipped.
func (a *Aggregator) shouldQueryCapability(serverName string, capability catalogCapability) bool {
	if handle := a.supervisor.Get(serverName); handle != nil && handle.IsRunning() {
		return true
	}
	entry := a.catalog.snapshot(process.SharedInstanceID(serverName))
	if entry.state != catalogVerified {
		return true
	}
	switch capability {
	case catalogResources:
		return entry.capabilities.Resources != nil
	case catalogPrompts:
		return entry.capabilities.Prompts != nil
	default:
		return true
	}
}

// ToolForServer returns the original tool info for routing a call.
func (a *Aggregator) ToolForServer(qualifiedName string) (serverID, origToolName string, ok bool) {
	serverName, toolName, isManager := ParseToolName(qualifiedName)
	if isManager {
		return "", "", false
	}

	t, ok := a.catalog.tool(serverName, toolName)
	if !ok {
		return "", "", false
	}
	return t.serverID, t.origName, true
}
