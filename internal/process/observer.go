package process

import (
	"encoding/json"
	"maps"

	"github.com/Bigsy/mcpmu/internal/mcp"
)

// DiscoveryResult is the immutable result of one Supervisor-owned initialize
// and initial tools/list sequence. Generation prevents a late result from an
// old process from replacing catalog data for a newer process.
type DiscoveryResult struct {
	Instance     InstanceID
	Generation   uint64
	Initialized  bool
	Capabilities mcp.ServerCapabilities
	Tools        []mcp.Tool
	Err          error
}

// Clone returns a deep-enough copy for transfer between Supervisor and Core.
func (r DiscoveryResult) Clone() DiscoveryResult {
	r.Capabilities = cloneCapabilities(r.Capabilities)
	r.Tools = cloneTools(r.Tools)
	return r
}

// ToolDiscoverySucceeded reports whether the result verifies a tool catalog.
// Initialization anchors verification: a server that does not advertise tools
// is verified empty even though no tools/list request was made.
func (r DiscoveryResult) ToolDiscoverySucceeded() bool {
	return r.Initialized && (r.Capabilities.Tools == nil || r.Err == nil)
}

// UpstreamNotification identifies the exact process generation that emitted
// an MCP notification.
type UpstreamNotification struct {
	Instance   InstanceID
	Generation uint64
	Method     string
	Params     json.RawMessage
	Upstream   bool
}

// Clone isolates queued notification payloads from caller-owned buffers.
func (n UpstreamNotification) Clone() UpstreamNotification {
	n.Params = append(json.RawMessage(nil), n.Params...)
	return n
}

// Observer receives lifecycle output owned by Supervisor. Implementations
// must return promptly from OnUpstreamNotification because it is invoked by
// the MCP client's response-reader goroutine.
type Observer interface {
	OnDiscoveryResult(DiscoveryResult)
	OnInstanceStopped(InstanceID, uint64)
	OnUpstreamNotification(UpstreamNotification)
}

func cloneCapabilities(c mcp.ServerCapabilities) mcp.ServerCapabilities {
	out := c
	if c.Tools != nil {
		tools := *c.Tools
		out.Tools = &tools
	}
	if c.Resources != nil {
		resources := *c.Resources
		out.Resources = &resources
	}
	if c.Prompts != nil {
		prompts := *c.Prompts
		out.Prompts = &prompts
	}
	if c.Logging != nil {
		out.Logging = make(map[string]any, len(c.Logging))
		maps.Copy(out.Logging, c.Logging)
	}
	return out
}

func cloneTools(tools []mcp.Tool) []mcp.Tool {
	if tools == nil {
		return nil
	}
	out := make([]mcp.Tool, len(tools))
	copy(out, tools)
	return out
}
