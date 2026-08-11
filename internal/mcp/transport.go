// Package mcp provides MCP protocol client implementation.
package mcp

import (
	"context"
	"encoding/json"
	"io"
)

// Transport is the interface for MCP transports.
type Transport interface {
	// Send sends a JSON-RPC message.
	Send(ctx context.Context, msg []byte) error
	// Receive reads the next JSON-RPC message.
	Receive(ctx context.Context) ([]byte, error)
	// Close closes the transport.
	Close() error
}

// McpClient is the interface for MCP clients.
type McpClient interface {
	// Initialize performs the MCP initialization handshake.
	Initialize(ctx context.Context) error
	// ListTools retrieves the list of tools from the server.
	ListTools(ctx context.Context) ([]Tool, error)
	// ListResources retrieves the list of resources from the server.
	ListResources(ctx context.Context) ([]Resource, error)
	// ReadResource reads a specific resource by URI.
	ReadResource(ctx context.Context, uri string) (json.RawMessage, error)
	// ListPrompts retrieves the list of prompts from the server.
	ListPrompts(ctx context.Context) ([]Prompt, error)
	// GetPrompt retrieves a specific prompt with arguments.
	GetPrompt(ctx context.Context, name string, arguments map[string]string) (json.RawMessage, error)
	// Close closes the client connection.
	Close() error
}

// ServerCapabilities is the typed form of the `capabilities` object returned
// by an MCP server during initialization. Nil pointer fields indicate the
// corresponding capability was not advertised by the server.
type ServerCapabilities struct {
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
	Logging   map[string]any       `json:"logging,omitempty"`
}

// ResourcesCapability describes the resources-related features a server
// supports.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// ToolsCapability describes the tools-related features a server supports.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability describes the prompts-related features a server supports.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// Tool represents an MCP tool definition (2025-11-25).
//
// Everything mcpmu does not itself interpret is held as json.RawMessage so it
// survives the proxy hop byte-for-byte; only `annotations` gets typed access,
// and that is on demand via ParseToolAnnotations rather than a struct field.
// Unknown members land in Extra, so a tool field added by a future revision is
// forwarded rather than silently dropped at unmarshal time.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	Icons        json.RawMessage `json:"icons,omitempty"`
	Meta         json.RawMessage `json:"_meta,omitempty"`

	// Execution carries `execution.taskSupport`. It is captured here — rather
	// than left to Extra — precisely so it can be dropped on the way
	// downstream: it advertises that a tool supports task-augmented execution,
	// and forwarding that promise while mcpmu implements no `tasks/*` methods
	// would invite an agent to make a call mcpmu cannot service. Forward it
	// once tasks are supported. The same reasoning applies to any future field
	// that implies behaviour the proxy must itself provide.
	Execution json.RawMessage `json:"execution,omitempty"`

	// Extra holds members not named above, keyed by their JSON name.
	Extra map[string]json.RawMessage `json:"-"`
}

// toolKnownFields are the members Tool models explicitly; everything else is
// collected into Extra.
var toolKnownFields = map[string]struct{}{
	"name": {}, "title": {}, "description": {}, "inputSchema": {},
	"outputSchema": {}, "annotations": {}, "icons": {}, "_meta": {},
	"execution": {},
}

// toolAlias exists so Tool's own (Un)MarshalJSON can defer to the default
// struct codec without recursing.
type toolAlias Tool

// UnmarshalJSON decodes the modelled fields and routes every other member into
// Extra.
func (t *Tool) UnmarshalJSON(data []byte) error {
	var alias toolAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*t = Tool(alias)

	extra, err := UnknownToolFields(data)
	if err != nil {
		return err
	}
	t.Extra = extra
	return nil
}

// UnknownToolFields returns the members of a tool definition that Tool does not
// model. Downstream types that mirror Tool use it so the catch-all is defined
// in exactly one place.
func UnknownToolFields(data []byte) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	for key, value := range all {
		if _, known := toolKnownFields[key]; known {
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage, len(all))
		}
		extra[key] = value
	}
	return extra, nil
}

// MarshalJSON re-emits the modelled fields plus anything held in Extra.
func (t Tool) MarshalJSON() ([]byte, error) {
	return MarshalWithExtra(toolAlias(t), t.Extra)
}

// MarshalWithExtra encodes value and folds extra's members into the resulting
// object. Modelled fields win on collision, so Extra can never forge a `name`.
func MarshalWithExtra(value any, extra map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return encoded, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	if merged == nil {
		merged = make(map[string]json.RawMessage, len(extra))
	}
	for key, raw := range extra {
		if _, exists := merged[key]; exists {
			continue
		}
		merged[key] = raw
	}
	return json.Marshal(merged)
}

// ToolAnnotations is the typed view of a tool's `annotations` object. Pointer
// fields distinguish "the server said false" from "the server said nothing",
// which is the whole point — an absent hint falls back to a heuristic, a
// present one is ground truth.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// ParseToolAnnotations decodes an `annotations` object. Malformed or absent
// annotations yield the zero value and ok=false — real servers do send
// nonsense here, and a proxy must not fail a whole tools/list over it.
func ParseToolAnnotations(raw json.RawMessage) (ToolAnnotations, bool) {
	if len(raw) == 0 {
		return ToolAnnotations{}, false
	}
	var annotations ToolAnnotations
	if err := json.Unmarshal(raw, &annotations); err != nil {
		return ToolAnnotations{}, false
	}
	return annotations, true
}

// Resource represents an MCP resource definition.
type Resource struct {
	URI         string          `json:"uri"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	MimeType    string          `json:"mimeType,omitempty"`
	Size        *int64          `json:"size,omitempty"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Icons       json.RawMessage `json:"icons,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
}

// Prompt represents an MCP prompt definition.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument represents an argument for an MCP prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// StdioTransportConfig holds configuration for stdio transport.
type StdioTransportConfig struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}
