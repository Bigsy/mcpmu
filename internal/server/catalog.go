package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

type catalogState uint8

const (
	catalogUnknown catalogState = iota
	catalogDiscovering
	catalogVerified
	catalogFailed
)

type catalogEntry struct {
	state        catalogState
	generation   uint64
	capabilities mcp.ServerCapabilities
	tools        map[string]AggregatedTool
	lastGood     map[string]AggregatedTool
	err          error
}

type discoveryFlight struct {
	done chan struct{}
}

type verifiedCatalog struct {
	mu      sync.RWMutex
	entries map[process.InstanceID]catalogEntry
	flights map[process.InstanceID]*discoveryFlight
}

func newVerifiedCatalog() *verifiedCatalog {
	return &verifiedCatalog{
		entries: make(map[process.InstanceID]catalogEntry),
		flights: make(map[process.InstanceID]*discoveryFlight),
	}
}

func (c *verifiedCatalog) begin(id process.InstanceID) (*discoveryFlight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[id]; entry.state == catalogVerified {
		return nil, false
	}
	if flight := c.flights[id]; flight != nil {
		return flight, false
	}
	flight := &discoveryFlight{done: make(chan struct{})}
	c.flights[id] = flight
	entry := c.entries[id]
	entry.state = catalogDiscovering
	c.entries[id] = entry
	return flight, true
}

func (c *verifiedCatalog) finish(id process.InstanceID, flight *discoveryFlight) {
	c.mu.Lock()
	if c.flights[id] == flight {
		delete(c.flights, id)
		close(flight.done)
	}
	c.mu.Unlock()
}

func (c *verifiedCatalog) apply(result process.DiscoveryResult) (changed, hadPrior bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[result.Instance]
	if result.Generation < entry.generation {
		return false, entry.lastGood != nil
	}
	hadPrior = entry.lastGood != nil
	entry.generation = result.Generation
	entry.capabilities = result.Capabilities
	entry.err = result.Err

	if result.ToolDiscoverySucceeded() {
		tools := aggregateToolMap(result.Instance.Server, result.Tools)
		changed = !sameToolMap(entry.lastGood, tools)
		entry.state = catalogVerified
		entry.tools = tools
		entry.lastGood = cloneToolMap(tools)
		entry.err = nil
	} else {
		entry.state = catalogFailed
		// A failure remains retryable while retaining the last verified result.
		entry.tools = cloneToolMap(entry.lastGood)
	}
	c.entries[result.Instance] = entry
	return changed, hadPrior
}

func (c *verifiedCatalog) fail(id process.InstanceID, generation uint64, err error) {
	if generation == 0 {
		c.mu.RLock()
		generation = c.entries[id].generation
		c.mu.RUnlock()
	}
	c.apply(process.DiscoveryResult{Instance: id, Generation: generation, Err: err})
}

func (c *verifiedCatalog) invalidate(id process.InstanceID, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[id]
	if generation < entry.generation {
		return
	}
	entry.generation = generation
	entry.state = catalogUnknown
	entry.tools = cloneToolMap(entry.lastGood)
	c.entries[id] = entry
}

func (c *verifiedCatalog) snapshot(id process.InstanceID) catalogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry := c.entries[id]
	entry.tools = cloneToolMap(entry.tools)
	entry.lastGood = cloneToolMap(entry.lastGood)
	return entry
}

func (c *verifiedCatalog) toolsForInstances(serverNames []string, instanceFor func(string) process.InstanceID) []AggregatedTool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var result []AggregatedTool
	for _, name := range serverNames {
		entry := c.entries[instanceFor(name)]
		toolNames := make([]string, 0, len(entry.tools))
		for toolName := range entry.tools {
			toolNames = append(toolNames, toolName)
		}
		slices.Sort(toolNames)
		for _, toolName := range toolNames {
			result = append(result, exposeTool(name, entry.tools[toolName]))
		}
	}
	return result
}

func (c *verifiedCatalog) toolForInstance(id process.InstanceID, toolName string) (AggregatedTool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	tool, ok := c.entries[id].tools[toolName]
	return tool, ok
}

// aggregateToolMap copies an upstream tool definition into its stored form.
// Every modelled field plus the unknown-member catch-all comes across
// unchanged; `execution` is the sole deliberate omission (see
// mcp.Tool.Execution).
func aggregateToolMap(serverName string, tools []mcp.Tool) map[string]AggregatedTool {
	result := make(map[string]AggregatedTool, len(tools))
	for _, tool := range tools {
		result[tool.Name] = AggregatedTool{
			Name:         tool.Name,
			Title:        tool.Title,
			Description:  tool.Description,
			InputSchema:  tool.InputSchema,
			OutputSchema: tool.OutputSchema,
			Annotations:  tool.Annotations,
			Icons:        tool.Icons,
			Meta:         tool.Meta,
			Extra:        maps.Clone(tool.Extra),
			serverID:     serverName, serverName: serverName, origName: tool.Name,
		}
	}
	return result
}

func cloneToolMap(in map[string]AggregatedTool) map[string]AggregatedTool {
	if in == nil {
		return nil
	}
	out := make(map[string]AggregatedTool, len(in))
	for name, tool := range in {
		tool.InputSchema = slices.Clone(tool.InputSchema)
		tool.OutputSchema = slices.Clone(tool.OutputSchema)
		tool.Annotations = slices.Clone(tool.Annotations)
		tool.Icons = slices.Clone(tool.Icons)
		tool.Meta = slices.Clone(tool.Meta)
		tool.Extra = maps.Clone(tool.Extra)
		out[name] = tool
	}
	return out
}

// sameToolMap reports whether two catalogs are identical in every field mcpmu
// exposes downstream. Comparing only name/description/inputSchema would leave
// an agent holding a stale definition forever when a server changed nothing
// but, say, its annotations — no list_changed would ever fire.
func sameToolMap(a, b map[string]AggregatedTool) bool {
	if len(a) != len(b) {
		return false
	}
	for name, left := range a {
		right, ok := b[name]
		if !ok || !sameTool(left, right) {
			return false
		}
	}
	return true
}

func sameTool(left, right AggregatedTool) bool {
	return left.Name == right.Name &&
		left.Title == right.Title &&
		left.Description == right.Description &&
		slices.Equal(left.InputSchema, right.InputSchema) &&
		slices.Equal(left.OutputSchema, right.OutputSchema) &&
		slices.Equal(left.Annotations, right.Annotations) &&
		slices.Equal(left.Icons, right.Icons) &&
		slices.Equal(left.Meta, right.Meta) &&
		maps.EqualFunc(left.Extra, right.Extra, func(a, b json.RawMessage) bool {
			return slices.Equal(a, b)
		})
}

func waitForFlight(ctx context.Context, flight *discoveryFlight) error {
	select {
	case <-flight.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func catalogError(entry catalogEntry) error {
	if entry.state != catalogFailed {
		return nil
	}
	if entry.err != nil {
		return entry.err
	}
	return errors.New("tool discovery failed")
}

func (s catalogState) String() string {
	switch s {
	case catalogUnknown:
		return "unknown"
	case catalogDiscovering:
		return "discovering"
	case catalogVerified:
		return "verified"
	case catalogFailed:
		return "failed"
	default:
		return fmt.Sprintf("catalogState(%d)", s)
	}
}
