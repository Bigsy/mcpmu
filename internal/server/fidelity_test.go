package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// richToolJSON is one tool definition carrying every field the 2025-11-25
// tools spec defines, plus an unknown member standing in for whatever the next
// revision adds.
const richToolJSON = `{
  "name": "read_file",
  "title": "Read File",
  "description": "Read a file from disk",
  "inputSchema": {"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},
  "outputSchema": {"type":"object","properties":{"contents":{"type":"string"}}},
  "annotations": {"title":"Read File","readOnlyHint":true,"idempotentHint":true},
  "icons": [{"src":"https://example.test/icon.png","mimeType":"image/png","sizes":["48x48"]}],
  "execution": {"taskSupport":"required"},
  "_meta": {"vendor.example/tier":"gold"},
  "futureField": {"revision":"2026-01-01"}
}`

func decodeRichTool(t *testing.T) mcp.Tool {
	t.Helper()
	var tool mcp.Tool
	if err := json.Unmarshal([]byte(richToolJSON), &tool); err != nil {
		t.Fatalf("unmarshal rich tool: %v", err)
	}
	return tool
}

// TestAggregatedTool_PreservesEveryUpstreamField is the core fidelity check:
// what an upstream server declared is what the agent sees. Before this,
// everything but name/description/inputSchema was discarded at unmarshal time
// and could not be recovered downstream at any later point.
func TestAggregatedTool_PreservesEveryUpstreamField(t *testing.T) {
	t.Parallel()
	tools := aggregateToolMap("srv", []mcp.Tool{decodeRichTool(t)})
	exposed := exposeTool("srv", tools["read_file"])

	encoded, err := json.Marshal(exposed)
	if err != nil {
		t.Fatalf("marshal exposed tool: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal exposed tool: %v", err)
	}

	var upstream map[string]json.RawMessage
	if err := json.Unmarshal([]byte(richToolJSON), &upstream); err != nil {
		t.Fatalf("unmarshal upstream tool: %v", err)
	}

	// name and description are deliberately rewritten at the exposure boundary.
	for _, field := range []string{"title", "outputSchema", "annotations", "icons", "_meta", "futureField"} {
		if _, ok := got[field]; !ok {
			t.Errorf("field %q was dropped; exposed tool = %s", field, encoded)
			continue
		}
		if !jsonEqual(t, got[field], upstream[field]) {
			t.Errorf("field %q changed in transit:\n  upstream: %s\n  exposed:  %s",
				field, upstream[field], got[field])
		}
	}
	if !jsonEqual(t, got["inputSchema"], upstream["inputSchema"]) {
		t.Errorf("inputSchema changed in transit:\n  upstream: %s\n  exposed:  %s",
			upstream["inputSchema"], got["inputSchema"])
	}
	if string(got["name"]) != `"srv.read_file"` {
		t.Errorf("name = %s, want %q", got["name"], "srv.read_file")
	}
}

// execution.taskSupport promises task-augmented execution. mcpmu implements no
// tasks/* methods, so forwarding the promise would invite a call it cannot
// service.
func TestAggregatedTool_StripsExecution(t *testing.T) {
	t.Parallel()
	tools := aggregateToolMap("srv", []mcp.Tool{decodeRichTool(t)})
	encoded, err := json.Marshal(exposeTool("srv", tools["read_file"]))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "execution") || strings.Contains(string(encoded), "taskSupport") {
		t.Errorf("execution was forwarded downstream: %s", encoded)
	}
}

// A schema with an integer too large for float64 used to be mangled by the
// any → map[string]any → re-marshal round trip.
func TestAggregatedTool_LargeIntegersSurviveSchemaRoundTrip(t *testing.T) {
	t.Parallel()
	const schema = `{"type":"object","properties":{"id":{"type":"integer","maximum":9007199254740993}}}`
	var tool mcp.Tool
	if err := json.Unmarshal([]byte(`{"name":"t","inputSchema":`+schema+`}`), &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools := aggregateToolMap("srv", []mcp.Tool{tool})
	if got := string(tools["t"].InputSchema); got != schema {
		t.Errorf("inputSchema round trip changed the bytes:\n got: %s\nwant: %s", got, schema)
	}
}

func TestSameToolMap_DetectsMetadataOnlyChanges(t *testing.T) {
	t.Parallel()
	base := decodeRichTool(t)

	mutations := map[string]func(mcp.Tool) mcp.Tool{
		"annotations": func(tool mcp.Tool) mcp.Tool {
			tool.Annotations = json.RawMessage(`{"readOnlyHint":false}`)
			return tool
		},
		"outputSchema": func(tool mcp.Tool) mcp.Tool {
			tool.OutputSchema = json.RawMessage(`{"type":"string"}`)
			return tool
		},
		"title": func(tool mcp.Tool) mcp.Tool {
			tool.Title = "Read A File"
			return tool
		},
		"icons": func(tool mcp.Tool) mcp.Tool {
			tool.Icons = json.RawMessage(`[]`)
			return tool
		},
		"_meta": func(tool mcp.Tool) mcp.Tool {
			tool.Meta = json.RawMessage(`{"vendor.example/tier":"silver"}`)
			return tool
		},
		"unknown field": func(tool mcp.Tool) mcp.Tool {
			tool.Extra = map[string]json.RawMessage{"futureField": json.RawMessage(`{"revision":"2027-01-01"}`)}
			return tool
		},
	}

	original := aggregateToolMap("srv", []mcp.Tool{base})
	if !sameToolMap(original, aggregateToolMap("srv", []mcp.Tool{decodeRichTool(t)})) {
		t.Fatal("identical catalogs compared unequal")
	}
	for field, mutate := range mutations {
		changed := aggregateToolMap("srv", []mcp.Tool{mutate(decodeRichTool(t))})
		if sameToolMap(original, changed) {
			t.Errorf("a change to %s did not register: no list_changed would ever fire", field)
		}
	}
}

// End-to-end: a rich tool definition from a real upstream process reaches the
// client's tools/list intact, and a tools/call carries both halves of the
// result envelope plus the client's own request metadata.
func TestServer_ToolFidelityEndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	fakeCfg := `{
	  "tools": [{
	    "name": "read_file",
	    "title": "Read File",
	    "description": "Read a file from disk",
	    "inputSchema": {"type":"object","properties":{"path":{"type":"string"}}},
	    "outputSchema": {"type":"object","properties":{"contents":{"type":"string"}}},
	    "annotations": {"readOnlyHint":true},
	    "icons": [{"src":"https://example.test/i.png","mimeType":"image/png"}],
	    "execution": {"taskSupport":"required"},
	    "_meta": {"vendor.example/tier":"gold"}
	  }],
	  "echoToolCalls": true,
	  "toolResultStructured": {"contents":"hello"},
	  "toolResultMeta": {"vendor.example/duration":12}
	}`

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv": {
				Kind: config.ServerKindStdio, Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           fakeCfg,
				},
			},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"srv.read_file","arguments":{"path":"/x"},"_meta":{"vendor.example/trace":"abc123"}}}` + "\n",
	)

	srv, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: stdin, Stdout: &stdout,
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	responses := parseResponsesByID(t, stdout.String())

	var listResp struct {
		Result struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responses[2], &listResp); err != nil {
		t.Fatalf("unmarshal tools/list: %v (raw: %s)", err, responses[2])
	}
	if len(listResp.Result.Tools) != 1 {
		t.Fatalf("tools/list returned %d tools, want 1: %s", len(listResp.Result.Tools), responses[2])
	}
	tool := listResp.Result.Tools[0]
	for field, want := range map[string]string{
		"title":        `"Read File"`,
		"outputSchema": `{"type":"object","properties":{"contents":{"type":"string"}}}`,
		"annotations":  `{"readOnlyHint":true}`,
		"icons":        `[{"src":"https://example.test/i.png","mimeType":"image/png"}]`,
		"_meta":        `{"vendor.example/tier":"gold"}`,
	} {
		got, ok := tool[field]
		if !ok {
			t.Errorf("tools/list dropped %q: %s", field, responses[2])
			continue
		}
		if !jsonEqual(t, got, json.RawMessage(want)) {
			t.Errorf("tools/list %q = %s, want %s", field, got, want)
		}
	}
	if _, ok := tool["execution"]; ok {
		t.Errorf("tools/list forwarded execution: %s", responses[2])
	}

	var callResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent json.RawMessage `json:"structuredContent"`
			Meta              json.RawMessage `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(responses[3], &callResp); err != nil {
		t.Fatalf("unmarshal tools/call: %v (raw: %s)", err, responses[3])
	}
	if !jsonEqual(t, callResp.Result.StructuredContent, json.RawMessage(`{"contents":"hello"}`)) {
		t.Errorf("structuredContent = %s, want {\"contents\":\"hello\"}", callResp.Result.StructuredContent)
	}
	if !jsonEqual(t, callResp.Result.Meta, json.RawMessage(`{"vendor.example/duration":12}`)) {
		t.Errorf("result _meta = %s, want {\"vendor.example/duration\":12}", callResp.Result.Meta)
	}
	if len(callResp.Result.Content) == 0 || !strings.Contains(callResp.Result.Content[0].Text, "vendor.example/trace") {
		t.Errorf("the client's request _meta never reached the upstream server: %s", responses[3])
	}
}

// A change to a tool's annotations must produce notifications/tools/list_changed,
// or the agent keeps a stale definition indefinitely.
func TestCatalog_AnnotationChangeEmitsListChanged(t *testing.T) {
	t.Parallel()
	catalog := newVerifiedCatalog()
	instance := process.SharedInstanceID("srv")

	base := decodeRichTool(t)
	first := process.DiscoveryResult{
		Instance: instance, Generation: 1, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}},
		Tools:        []mcp.Tool{base},
	}
	if changed, hadPrior := catalog.apply(first); hadPrior || !changed {
		t.Fatalf("first discovery: changed=%v hadPrior=%v, want true/false", changed, hadPrior)
	}

	annotated := decodeRichTool(t)
	annotated.Annotations = json.RawMessage(`{"readOnlyHint":false}`)
	second := first
	second.Generation = 2
	second.Tools = []mcp.Tool{annotated}
	changed, hadPrior := catalog.apply(second)
	if !changed || !hadPrior {
		t.Fatalf("annotations-only change: changed=%v hadPrior=%v, want true/true", changed, hadPrior)
	}
}

func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	leftEncoded, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightEncoded, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftEncoded, rightEncoded)
}
