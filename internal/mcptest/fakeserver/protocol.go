// Package fakeserver provides a fake MCP server for testing.
package fakeserver

import (
	"encoding/json"
	"io"
	"time"
)

// DefaultProtocolVersion is the revision the fake server reports when the
// initialize request carried none and Config.ProtocolVersion is unset.
const DefaultProtocolVersion = "2025-11-25"

// Config controls the fake server's behavior.
type Config struct {
	// ProtocolVersion pins the revision reported from initialize. Empty echoes
	// whatever the client requested.
	ProtocolVersion string `json:"protocolVersion,omitempty"`

	// Tools to return from tools/list
	Tools []Tool `json:"tools"`
	// AdvertiseTools controls the initialize capability independently of the
	// tools payload. Nil preserves the historical default (advertise tools).
	AdvertiseTools *bool `json:"advertiseTools,omitempty"`
	// ToolsAfterListChanged replaces Tools on the second and later tools/list
	// calls, for catalog invalidation tests.
	ToolsAfterListChanged []Tool `json:"toolsAfterListChanged,omitempty"`
	// EmitToolsListChangedAfterFirstList emits an upstream invalidation after
	// the initial tools/list response.
	EmitToolsListChangedAfterFirstList bool `json:"emitToolsListChangedAfterFirstList,omitempty"`

	// Resources to return from resources/list
	Resources          []Resource `json:"resources"`
	AdvertiseResources *bool      `json:"advertiseResources,omitempty"`
	// ResourceContents maps URI -> raw content for resources/read
	ResourceContents map[string]json.RawMessage `json:"resourceContents,omitempty"`

	// Prompts to return from prompts/list
	Prompts          []Prompt `json:"prompts"`
	AdvertisePrompts *bool    `json:"advertisePrompts,omitempty"`
	// PromptMessages maps prompt name -> raw messages for prompts/get
	PromptMessages map[string]json.RawMessage `json:"promptMessages,omitempty"`

	// Per-method delays (simulate slow responses)
	// NOTE: Use short delays (10-50ms) in tests to avoid slow suite.
	Delays map[string]time.Duration `json:"delays"`

	// Per-method forced errors (JSON-RPC error responses)
	Errors map[string]JSONRPCError `json:"errors"`

	// Crash behavior
	CrashOnMethod     string `json:"crashOnMethod"`     // crash when this method is called
	CrashOnNthRequest int    `json:"crashOnNthRequest"` // crash on Nth request (0 = never)
	CrashExitCode     int    `json:"crashExitCode"`     // exit code when crashing

	// Retry testing: fail on specific attempt, succeed on others
	FailOnAttempt map[string]int `json:"failOnAttempt"` // method -> attempt number to fail (1-indexed)

	// Protocol edge cases for stream realism
	// These options test that the client handles interleaved messages correctly.
	SendNotificationBeforeResponse bool `json:"sendNotificationBeforeResponse"` // send a notification before each response
	SendMismatchedIDFirst          bool `json:"sendMismatchedIDFirst"`          // send a response with wrong ID before correct one

	// Protocol edge cases
	Malformed bool `json:"malformed"` // write invalid JSON

	// Tool call handling
	ToolHandler   ToolHandler `json:"-"`             // Custom handler for tools/call (not JSON-serializable)
	EchoToolCalls bool        `json:"echoToolCalls"` // If true, tools/call returns the tool name and arguments as text

	// ToolResultStructured and ToolResultMeta are attached to every tools/call
	// result, for asserting that the result envelope survives the proxy hop.
	ToolResultStructured json.RawMessage `json:"toolResultStructured,omitempty"`
	ToolResultMeta       json.RawMessage `json:"toolResultMeta,omitempty"`

	// ProgressUpdatesPerCall emits this many notifications/progress frames,
	// carrying the caller's `_meta.progressToken`, before answering a
	// tools/call. Zero (the default) emits none. A call that arrives without a
	// progressToken never produces progress, exactly as the spec requires.
	ProgressUpdatesPerCall int `json:"progressUpdatesPerCall,omitempty"`

	// Resource subscription support (tests for resources/subscribe passthrough).
	// If true, the server advertises resources.subscribe: true and accepts
	// resources/subscribe and resources/unsubscribe requests.
	ResourcesSubscribe bool `json:"resourcesSubscribe"`

	// EmitUpdateAfterSubscribe emits a notifications/resources/updated frame
	// for the subscribed URI after responding to a successful
	// resources/subscribe. JSON-serializable trigger for integration tests;
	// zero delay intentionally stresses response/notification ordering.
	EmitUpdateAfterSubscribe bool `json:"emitUpdateAfterSubscribe"`

	// EmitUpdateAfterUnsubscribe emits a notifications/resources/updated frame
	// for the URI after responding to resources/unsubscribe. Used to verify
	// downstream clients don't receive updates for unsubscribed URIs.
	EmitUpdateAfterUnsubscribe bool `json:"emitUpdateAfterUnsubscribe"`

	// PostSubscribeEmitDelayMs optionally waits between writing the subscribe
	// response and emitting the update when EmitUpdateAfterSubscribe is set.
	PostSubscribeEmitDelayMs int `json:"postSubscribeEmitDelayMs,omitempty"`

	// PostUnsubscribeEmitDelayMs is the symmetric optional delay for
	// EmitUpdateAfterUnsubscribe.
	PostUnsubscribeEmitDelayMs int `json:"postUnsubscribeEmitDelayMs,omitempty"`

	// RequestLogPath is a file path the server appends every handled JSON-RPC
	// method name to, one per line. Tests use this to assert whether a
	// particular method (e.g., resources/unsubscribe) was invoked upstream.
	// Writes are best-effort and guarded against concurrent callers.
	RequestLogPath string `json:"requestLogPath,omitempty"`

	// EmitStartupUpdates lists URIs for which the server emits
	// notifications/resources/updated frames shortly after initialize. Used
	// to test stray-notification filtering in downstream code.
	EmitStartupUpdates []string `json:"emitStartupUpdates,omitempty"`

	// UpdateHook receives a function the test can call to emit an out-of-band
	// notifications/resources/updated{uri} frame on this server's output. The
	// hook is wired when Serve starts; the test should capture it via the
	// SetUpdateHook field. Only available when running in-process (not via
	// the subprocess helper pattern, since it isn't JSON-serializable).
	SetUpdateHook func(emit func(uri string)) `json:"-"`

	// OnSubscribe / OnUnsubscribe are optional test hooks invoked after the
	// server processes a subscribe/unsubscribe request (post-response).
	OnSubscribe   func(uri string) `json:"-"`
	OnUnsubscribe func(uri string) `json:"-"`
}

// Tool represents an MCP tool definition. It carries the full 2025-11-25 field
// set (plus an Extra escape hatch) so tests can assert what survives the proxy
// hop. InputSchema stays `any` for the convenience of test literals.
type Tool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  any             `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
	Icons        json.RawMessage `json:"icons,omitempty"`
	Execution    json.RawMessage `json:"execution,omitempty"`
	Meta         json.RawMessage `json:"_meta,omitempty"`

	// Extra emits members the spec has not defined, for testing the
	// unknown-field catch-all.
	Extra map[string]json.RawMessage `json:"-"`
}

type toolAlias Tool

// MarshalJSON folds Extra into the emitted object.
func (t Tool) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(toolAlias(t))
	if err != nil || len(t.Extra) == 0 {
		return encoded, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, raw := range t.Extra {
		if _, exists := merged[key]; !exists {
			merged[key] = raw
		}
	}
	return json.Marshal(merged)
}

// JSONRPCError represents a JSON-RPC 2.0 error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// rpcNotification is a JSON-RPC 2.0 notification.
type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// InitializeResult is the result of the initialize request.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ServerInfo describes the server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities describes server capabilities.
type Capabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

// ResourcesCapability indicates the server supports resources.
type ResourcesCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
	Subscribe   bool `json:"subscribe,omitempty"`
}

// PromptsCapability indicates the server supports prompts.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ToolsCapability indicates the server supports tools.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ToolsListResult is the result of tools/list.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolCallParams is the params for tools/call.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

// ToolCallResult is the result of tools/call.
type ToolCallResult struct {
	Content           []ContentBlock  `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
	Meta              json.RawMessage `json:"_meta,omitempty"`
}

// ContentBlock represents a content block in a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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

// ResourcesListResult is the result of resources/list.
type ResourcesListResult struct {
	Resources []Resource `json:"resources"`
}

// ResourceReadParams is the params for resources/read.
type ResourceReadParams struct {
	URI string `json:"uri"`
}

// ResourceReadResult is the result of resources/read.
type ResourceReadResult struct {
	Contents json.RawMessage `json:"contents"`
}

// PromptsListResult is the result of prompts/list.
type PromptsListResult struct {
	Prompts []Prompt `json:"prompts"`
}

// PromptGetParams is the params for prompts/get.
type PromptGetParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// PromptGetResult is the result of prompts/get.
type PromptGetResult struct {
	Messages json.RawMessage `json:"messages"`
}

// ToolHandler is a function that handles a tool call.
type ToolHandler func(name string, arguments json.RawMessage) ([]ContentBlock, bool, error)

// writeFrame writes a single JSON value followed by a newline as one Write
// call so that a synchronized writer can treat the frame atomically.
func writeFrame(out io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = out.Write(data)
	return err
}

// writeResponse writes a JSON-RPC response with NDJSON framing.
func writeResponse(out io.Writer, id json.RawMessage, result any, cfg Config) error {
	// Stream realism: send notification before response if configured
	if cfg.SendNotificationBeforeResponse {
		_ = writeFrame(out, rpcNotification{JSONRPC: "2.0", Method: "test/noise"})
	}

	// Stream realism: send mismatched ID first if configured
	if cfg.SendMismatchedIDFirst {
		_ = writeFrame(out, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`99999`), Result: json.RawMessage(`{}`)})
	}

	// Actual response (NDJSON)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return writeFrame(out, rpcResponse{JSONRPC: "2.0", ID: id, Result: resultJSON})
}

// writeErrorResponse writes a JSON-RPC error response with NDJSON framing.
func writeErrorResponse(out io.Writer, id json.RawMessage, rpcErr JSONRPCError, cfg Config) error {
	// Stream realism options apply here too
	if cfg.SendNotificationBeforeResponse {
		_ = writeFrame(out, rpcNotification{JSONRPC: "2.0", Method: "test/noise"})
	}

	if cfg.SendMismatchedIDFirst {
		_ = writeFrame(out, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`99999`), Error: &JSONRPCError{Code: -1, Message: "wrong"}})
	}

	return writeFrame(out, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr})
}
