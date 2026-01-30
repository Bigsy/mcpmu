package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

const (
	// ToolDiscoveryTimeout is the max time to wait for tool discovery per server
	ToolDiscoveryTimeout = 5 * time.Second
	// MaxConcurrentDiscovery is the max number of servers to discover tools from concurrently
	MaxConcurrentDiscovery = 4
)

// AggregatedTool represents a tool with qualified name and server info.
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

	// Tool cache
	tools   map[string]AggregatedTool // qualified name -> tool
	toolsMu sync.RWMutex

	// Manager tools
	managerTools []AggregatedTool
}

// NewAggregator creates a new tool aggregator.
func NewAggregator(cfg *config.Config, supervisor *process.Supervisor) *Aggregator {
	a := &Aggregator{
		cfg:        cfg,
		supervisor: supervisor,
		tools:      make(map[string]AggregatedTool),
	}
	a.managerTools = a.buildManagerTools()
	return a
}

// ListTools discovers and returns all tools from the specified servers.
// This may start servers lazily if they're not running.
func (a *Aggregator) ListTools(ctx context.Context, serverIDs []string) ([]AggregatedTool, error) {
	// Discover tools from servers concurrently with bounded parallelism
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allTools []AggregatedTool

	for _, id := range serverIDs {
		wg.Add(1)
		go func(serverID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			tools, err := a.discoverServerTools(ctx, serverID)
			if err != nil {
				log.Printf("Failed to discover tools from %s: %v", serverID, err)
				return
			}

			mu.Lock()
			allTools = append(allTools, tools...)
			mu.Unlock()
		}(id)
	}

	wg.Wait()

	// Update cache
	a.toolsMu.Lock()
	a.tools = make(map[string]AggregatedTool)
	for _, t := range allTools {
		a.tools[t.Name] = t
	}
	a.toolsMu.Unlock()

	// Add manager tools
	result := make([]AggregatedTool, 0, len(allTools)+len(a.managerTools))
	result = append(result, allTools...)
	result = append(result, a.managerTools...)

	return result, nil
}

// GetTool returns a tool by its qualified name.
func (a *Aggregator) GetTool(name string) (AggregatedTool, bool) {
	// Check manager tools first
	for _, t := range a.managerTools {
		if t.Name == name {
			return t, true
		}
	}

	a.toolsMu.RLock()
	defer a.toolsMu.RUnlock()
	t, ok := a.tools[name]
	return t, ok
}

// discoverServerTools starts a server (if needed) and retrieves its tools.
func (a *Aggregator) discoverServerTools(ctx context.Context, serverID string) ([]AggregatedTool, error) {
	srv := a.cfg.GetServer(serverID)
	if srv == nil {
		return nil, fmt.Errorf("server not found: %s", serverID)
	}

	if !srv.IsEnabled() {
		log.Printf("Server %s is disabled, skipping", serverID)
		return nil, nil
	}

	// Check if server is already running
	handle := a.supervisor.Get(serverID)
	if handle == nil || !handle.IsRunning() {
		// Start the server
		var err error
		timeoutCtx, cancel := context.WithTimeout(ctx, ToolDiscoveryTimeout)
		defer cancel()

		handle, err = a.supervisor.Start(timeoutCtx, *srv)
		if err != nil {
			return nil, fmt.Errorf("start server: %w", err)
		}
	}

	// Get tools from the running server
	mcpTools := handle.Tools()
	serverName := srv.Name
	if serverName == "" {
		serverName = serverID
	}

	tools := make([]AggregatedTool, len(mcpTools))
	for i, t := range mcpTools {
		// Qualify tool name: serverId.toolName
		qualifiedName := serverID + "." + t.Name

		// Prefix description with server name
		desc := t.Description
		if desc != "" {
			desc = fmt.Sprintf("[%s] %s", serverName, desc)
		} else {
			desc = fmt.Sprintf("[%s]", serverName)
		}

		// Convert InputSchema
		var schemaJSON json.RawMessage
		if t.InputSchema != nil {
			if b, err := json.Marshal(t.InputSchema); err == nil {
				schemaJSON = b
			}
		}

		tools[i] = AggregatedTool{
			Name:        qualifiedName,
			Description: desc,
			InputSchema: schemaJSON,
			serverID:    serverID,
			serverName:  serverName,
			origName:    t.Name,
		}
	}

	return tools, nil
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

// RefreshServerTools refreshes the tool cache for a specific server.
func (a *Aggregator) RefreshServerTools(ctx context.Context, serverID string) error {
	tools, err := a.discoverServerTools(ctx, serverID)
	if err != nil {
		return err
	}

	a.toolsMu.Lock()
	defer a.toolsMu.Unlock()

	// Remove old tools from this server
	for name, t := range a.tools {
		if t.serverID == serverID {
			delete(a.tools, name)
		}
	}

	// Add new tools
	for _, t := range tools {
		a.tools[t.Name] = t
	}

	return nil
}

// ToolForServer returns the original tool info for routing a call.
func (a *Aggregator) ToolForServer(qualifiedName string) (serverID, origToolName string, ok bool) {
	a.toolsMu.RLock()
	defer a.toolsMu.RUnlock()

	t, ok := a.tools[qualifiedName]
	if !ok {
		return "", "", false
	}
	return t.serverID, t.origName, true
}
