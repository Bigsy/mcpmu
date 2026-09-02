package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/metrics"
)

// runCompressSession runs an embedded serve session to stdin exhaustion and
// returns the responses keyed by id.
func runCompressSession(t *testing.T, opts Options, script string) map[int]json.RawMessage {
	t.Helper()
	var stdout bytes.Buffer
	opts.Stdin = strings.NewReader(script)
	opts.Stdout = &stdout
	if opts.PIDTrackerDir == "" {
		opts.PIDTrackerDir = t.TempDir()
	}
	opts.ServerName = "mcpmu-test"
	opts.ServerVersion = "1.0.0"
	opts.LogLevel = "error"
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Run(ctx)
	return parseResponsesByID(t, stdout.String())
}

// wrapperListFromResponse decodes a tools/list response into name → tool.
func wrapperListFromResponse(t *testing.T, raw json.RawMessage) map[string]AggregatedTool {
	t.Helper()
	var resp struct {
		Result struct {
			Tools []AggregatedTool `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal tools/list response: %v\n%s", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("tools/list error: %v", resp.Error)
	}
	byName := make(map[string]AggregatedTool, len(resp.Result.Tools))
	for _, tool := range resp.Result.Tools {
		byName[tool.Name] = tool
	}
	return byName
}

// toolCallText extracts the first text content block of a tools/call response.
func toolCallText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal tools/call response: %v\n%s", err, raw)
	}
	if resp.Error != nil {
		t.Fatalf("tools/call error: %v", resp.Error)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("tools/call response has no content: %s", raw)
	}
	return resp.Result.Content[0].Text
}

func rpcErrorFromResponse(t *testing.T, raw json.RawMessage) *RPCError {
	t.Helper()
	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, raw)
	}
	return resp.Error
}

func TestCompress_ToolsListReturnsWrappers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"read_file","description":"Read a file. Extra detail.","inputSchema":{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"]}}],"echoToolCalls":true}`),
		},
	}

	script := initLine + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	responses := runCompressSession(t, Options{Config: cfg, Compression: config.CompressionForce(config.CompressionMedium)}, script)

	tools := wrapperListFromResponse(t, responses[2])
	if len(tools) != 3 {
		t.Fatalf("expected exactly 3 wrapper tools, got %d: %v", len(tools), tools)
	}
	for _, name := range []string{wrapperListTools, wrapperGetToolSchema, wrapperInvokeTool} {
		if _, ok := tools[name]; !ok {
			t.Errorf("tools/list missing wrapper %q", name)
		}
	}
	wantLine := "<tool>srv1.read_file(path, limit?): Read a file.</tool>"
	if !strings.Contains(tools[wrapperInvokeTool].Description, wantLine) {
		t.Errorf("invoke_tool description missing %q:\n%s", wantLine, tools[wrapperInvokeTool].Description)
	}
}

func TestCompress_ManagerToolsStayReal(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"read_file"}]}`),
		},
	}

	script := initLine + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	responses := runCompressSession(t, Options{
		Config: cfg, Compression: config.CompressionForce(config.CompressionMedium), ExposeManagerTools: true,
	}, script)

	tools := wrapperListFromResponse(t, responses[2])
	managerCount := 0
	for name := range tools {
		if strings.HasPrefix(name, "mcpmu.") {
			managerCount++
		}
	}
	if managerCount < 5 {
		t.Errorf("expected manager tools alongside wrappers, got %d manager tools: %v", managerCount, tools)
	}
	if _, ok := tools[wrapperInvokeTool]; !ok {
		t.Error("invoke_tool wrapper missing when manager tools exposed")
	}
	if got := len(tools); got != 3+managerCount {
		t.Errorf("expected wrappers + manager tools only, got %d tools", got)
	}
}

func TestCompress_DeniedToolsHidden(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"read_file","description":"Read a file"},{"name":"delete_file","description":"Delete a file"}],"echoToolCalls":true}`),
		},
		Namespaces: map[string]config.NamespaceConfig{
			// Explicit deny rather than deny-by-default: an unknown tool then
			// reaches the not-found check instead of being denied first, so the
			// multi-tool assertions below can distinguish the two errors — the
			// same behaviour the direct tools/call path has.
			"restricted": {ServerIDs: []string{"srv1"}},
		},
		ToolPermissions: []config.ToolPermission{
			{Namespace: "restricted", Server: "srv1", ToolName: "delete_file", Enabled: false},
		},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"srv1.delete_file"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"srv1.delete_file","input":{}}}}` + "\n" +
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tools":["srv1.read_file","srv1.delete_file","srv1.no_such_tool"]}}}` + "\n"

	responses := runCompressSession(t, Options{
		Config: cfg, Namespace: "restricted", Compression: config.CompressionForce(config.CompressionMedium),
	}, script)

	// Denied tools never appear in the embedded listing or list_tools output.
	tools := wrapperListFromResponse(t, responses[2])
	desc := tools[wrapperInvokeTool].Description
	if !strings.Contains(desc, "srv1.read_file") {
		t.Errorf("allowed tool missing from invoke_tool description:\n%s", desc)
	}
	if strings.Contains(desc, "delete_file") {
		t.Errorf("denied tool leaked into invoke_tool description:\n%s", desc)
	}
	listing := toolCallText(t, responses[3])
	if !strings.Contains(listing, "srv1.read_file") || strings.Contains(listing, "delete_file") {
		t.Errorf("list_tools output wrong: %q", listing)
	}

	// get_tool_schema refuses the denied tool with the direct path's error.
	if rpcErr := rpcErrorFromResponse(t, responses[4]); rpcErr == nil || rpcErr.Code != ErrCodeToolDenied {
		t.Errorf("get_tool_schema(denied) error = %v, want ErrCodeToolDenied", rpcErr)
	}
	// invoke_tool is denied on the target, exactly like a direct call.
	if rpcErr := rpcErrorFromResponse(t, responses[5]); rpcErr == nil || rpcErr.Code != ErrCodeToolDenied {
		t.Errorf("invoke_tool(denied) error = %v, want ErrCodeToolDenied", rpcErr)
	}

	// Multi-tool form reports per-entry results without failing the call.
	var multi struct {
		Result struct {
			StructuredContent struct {
				Tools []json.RawMessage `json:"tools"`
			} `json:"structuredContent"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[6], &multi); err != nil {
		t.Fatalf("unmarshal multi-tool response: %v\n%s", err, responses[6])
	}
	if multi.Error != nil {
		t.Fatalf("multi-tool get_tool_schema failed whole call: %v", multi.Error)
	}
	if len(multi.Result.StructuredContent.Tools) != 3 {
		t.Fatalf("expected 3 per-entry results, got %d", len(multi.Result.StructuredContent.Tools))
	}
	var okEntry struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(multi.Result.StructuredContent.Tools[0], &okEntry); err != nil || okEntry.Name != "srv1.read_file" {
		t.Errorf("first entry should be the resolved tool, got %s", multi.Result.StructuredContent.Tools[0])
	}
	for i, wantCode := range map[int]int{1: ErrCodeToolDenied, 2: ErrCodeToolNotFound} {
		var errEntry struct {
			Tool  string    `json:"tool"`
			Error *RPCError `json:"error"`
		}
		if err := json.Unmarshal(multi.Result.StructuredContent.Tools[i], &errEntry); err != nil {
			t.Fatalf("unmarshal entry %d: %v", i, err)
		}
		if errEntry.Error == nil || errEntry.Error.Code != wantCode {
			t.Errorf("entry %d error = %v, want code %d", i, errEntry.Error, wantCode)
		}
	}
}

func TestCompress_InvokeToolMatchesDirectCall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	fakeCfg := `{"tools":[{"name":"read_file","description":"Read a file"}],"echoToolCalls":true,` +
		`"toolResultStructured":{"a":1},"toolResultMeta":{"m":"v"}}`
	newConfig := func() *config.Config {
		return &config.Config{
			SchemaVersion: 1,
			Servers:       map[string]config.ServerConfig{"srv1": fakeUpstream(fakeCfg)},
		}
	}

	// Lazy start: invoke_tool is the session's first upstream interaction.
	compressed := runCompressSession(t, Options{Config: newConfig(), Compression: config.CompressionForce(config.CompressionMedium)},
		initLine+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"srv1.read_file","input":{"path":"/x"}}}}`+"\n")
	direct := runCompressSession(t, Options{Config: newConfig()},
		initLine+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.read_file","arguments":{"path":"/x"}}}`+"\n")

	var compressedResp, directResp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(compressed[2], &compressedResp); err != nil {
		t.Fatalf("unmarshal compressed response: %v", err)
	}
	if err := json.Unmarshal(direct[2], &directResp); err != nil {
		t.Fatalf("unmarshal direct response: %v", err)
	}
	if compressedResp.Error != nil || directResp.Error != nil {
		t.Fatalf("unexpected errors: compressed=%v direct=%v", compressedResp.Error, directResp.Error)
	}
	if !bytes.Equal(compressedResp.Result, directResp.Result) {
		t.Errorf("invoke_tool result differs from direct call:\ncompressed: %s\ndirect:     %s",
			compressedResp.Result, directResp.Result)
	}
}

func TestCompress_GetToolSchema_LazyDiscovery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"read_file","description":"Read a file","inputSchema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`),
		},
	}

	// get_tool_schema is the first upstream interaction — no tools/list has
	// primed the catalog, so this exercises the DiscoverServer fallback.
	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"srv1.read_file"}}}` + "\n"
	responses := runCompressSession(t, Options{Config: cfg, Compression: config.CompressionForce(config.CompressionMedium)}, script)

	var resp struct {
		Result struct {
			StructuredContent AggregatedTool `json:"structuredContent"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[2], &resp); err != nil {
		t.Fatalf("unmarshal get_tool_schema response: %v\n%s", err, responses[2])
	}
	if resp.Error != nil {
		t.Fatalf("get_tool_schema failed: %v", resp.Error)
	}
	tool := resp.Result.StructuredContent
	if tool.Name != "srv1.read_file" {
		t.Errorf("schema name = %q, want srv1.read_file", tool.Name)
	}
	if !strings.HasPrefix(tool.Description, "[srv1]") {
		t.Errorf("schema description missing [srv1] prefix: %q", tool.Description)
	}
	if !strings.Contains(string(tool.InputSchema), `"path"`) {
		t.Errorf("schema inputSchema missing path property: %s", tool.InputSchema)
	}
}

func TestCompress_GetToolSchema_InvalidArgs(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"a.b","tools":["c.d"]}}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"invoke_tool","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"a.b","input":"not an object"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"a.b","input":[1,2]}}}` + "\n"
	responses := runCompressSession(t, Options{Config: cfg, Compression: config.CompressionForce(config.CompressionMedium)}, script)

	for id := 2; id <= 6; id++ {
		if rpcErr := rpcErrorFromResponse(t, responses[id]); rpcErr == nil || rpcErr.Code != ErrCodeInvalidParams {
			t.Errorf("response %d error = %v, want ErrCodeInvalidParams", id, rpcErr)
		}
	}
}

func TestCompress_WrappersUnknownWhenOff(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{}}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"a.b"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"a.b"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"someword","arguments":{}}}` + "\n"
	responses := runCompressSession(t, Options{Config: cfg}, script)

	// A wrapper name with compression off is the error a model actually hits
	// when a reload disabled compression under its cached tools — it must be
	// tool-not-found and say how to recover, not `Server not found: ""`.
	for id := 2; id <= 4; id++ {
		rpcErr := rpcErrorFromResponse(t, responses[id])
		if rpcErr == nil || rpcErr.Code != ErrCodeToolNotFound {
			t.Errorf("response %d error = %v, want ErrCodeToolNotFound", id, rpcErr)
			continue
		}
		if !strings.Contains(rpcErr.Message, "compression is off") {
			t.Errorf("response %d message %q missing recovery hint", id, rpcErr.Message)
		}
	}

	// Any other dotless name gets tool-not-found with the qualified-name hint.
	rpcErr := rpcErrorFromResponse(t, responses[5])
	if rpcErr == nil || rpcErr.Code != ErrCodeToolNotFound {
		t.Fatalf("dotless name error = %v, want ErrCodeToolNotFound", rpcErr)
	}
	if !strings.Contains(rpcErr.Message, `qualified as "server.tool"`) {
		t.Errorf("dotless name message %q missing qualified-name hint", rpcErr.Message)
	}
}

func TestCompress_UpstreamToolNamedInvokeTool(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	fakeCfg := `{"tools":[{"name":"invoke_tool","description":"An upstream tool that shares a wrapper name"}],"echoToolCalls":true}`
	newConfig := func() *config.Config {
		return &config.Config{
			SchemaVersion: 1,
			Servers:       map[string]config.ServerConfig{"srv1": fakeUpstream(fakeCfg)},
		}
	}

	directCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.invoke_tool","arguments":{"x":1}}}`
	wrapped := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"srv1.invoke_tool","input":{"x":1}}}}`

	// Compression on: qualified name resolves directly and through the wrapper.
	on := runCompressSession(t, Options{Config: newConfig(), Compression: config.CompressionForce(config.CompressionMedium)},
		initLine+"\n"+directCall+"\n"+wrapped+"\n")
	for id := 2; id <= 3; id++ {
		if text := toolCallText(t, on[id]); !strings.Contains(text, "invoke_tool") {
			t.Errorf("compression on, response %d: unexpected text %q", id, text)
		}
	}

	// Compression off: the qualified name works the same.
	off := runCompressSession(t, Options{Config: newConfig()}, initLine+"\n"+directCall+"\n")
	if text := toolCallText(t, off[2]); !strings.Contains(text, "invoke_tool") {
		t.Errorf("compression off: unexpected text %q", text)
	}
}

func TestCompress_MetricsRecording(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"read_file"}],"echoToolCalls":true}`),
		},
		Namespaces: map[string]config.NamespaceConfig{
			"work": {ServerIDs: []string{"srv1"}},
		},
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"srv1.read_file","input":{"path":"/x"}}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"srv1.read_file"}}}` + "\n"

	_ = runCompressSession(t, Options{
		Config: cfg, ConfigPath: configPath, PIDTrackerDir: dir,
		Namespace: "work", Compression: config.CompressionForce(config.CompressionMedium),
	}, script)

	store, err := metrics.Load(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatalf("load metrics: %v", err)
	}

	// invoke_tool records against the target tool, not the wrapper.
	c := findBucket(t, store, "work", "srv1", "read_file")
	if c.Calls != 1 || c.Outcomes[metrics.OutcomeOK] != 1 {
		t.Errorf("target counters = %+v, want 1 ok call", c)
	}
	// The meta wrappers record under server="mcpmu" like manager tools.
	for _, tool := range []string{"list_tools", "get_tool_schema"} {
		c := findBucket(t, store, "work", "mcpmu", tool)
		if c.Calls != 1 || c.Outcomes[metrics.OutcomeOK] != 1 {
			t.Errorf("%s counters = %+v, want 1 ok call", tool, c)
		}
	}
}

func TestCompress_SharedAndPrivateAggregatorsMerge(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	private := false
	privateServer := fakeUpstream(`{"tools":[{"name":"private_tool","description":"Private tool"}],"echoToolCalls":true}`)
	privateServer.Shared = &private

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"shared-srv":  fakeUpstream(`{"tools":[{"name":"shared_tool","description":"Shared tool"}],"echoToolCalls":true}`),
			"private-srv": privateServer,
		},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"private-srv.private_tool"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"private-srv.private_tool","input":{}}}}` + "\n"

	responses := runCompressSession(t, Options{Config: cfg, Compression: config.CompressionForce(config.CompressionMedium)}, script)

	listing := toolCallText(t, responses[2])
	for _, want := range []string{"shared-srv.shared_tool", "private-srv.private_tool"} {
		if !strings.Contains(listing, want) {
			t.Errorf("list_tools missing %q:\n%s", want, listing)
		}
	}
	if rpcErr := rpcErrorFromResponse(t, responses[3]); rpcErr != nil {
		t.Errorf("get_tool_schema against private instance failed: %v", rpcErr)
	}
	if rpcErr := rpcErrorFromResponse(t, responses[4]); rpcErr != nil {
		t.Errorf("invoke_tool against private instance failed: %v", rpcErr)
	}
}

// TestCompress_WrapperMetaRecording_RaceWithReload hammers the wrapper
// handlers while config reloads swap s.router underneath them. The wrappers
// must use the router snapshot handleToolsCall took under s.mu — a late,
// unsynchronized re-read of s.router races with applyReloadConfig's swap and
// SetActiveNamespace's field write. The race detector is the assertion.
func TestCompress_WrapperMetaRecording_RaceWithReload(t *testing.T) {
	t.Parallel()

	newConfig := func() *config.Config {
		return &config.Config{
			SchemaVersion: 1,
			Servers:       map[string]config.ServerConfig{},
			Namespaces:    map[string]config.NamespaceConfig{"ns1": {}},
		}
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:        newConfig(),
		PIDTrackerDir: t.TempDir(),
		Namespace:     "ns1",
		Compression:   config.CompressionForce(config.CompressionMedium),
		Stdin:         stdinReader,
		Stdout:        stdoutWriter,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	lines := make(chan string, 256)
	go func() {
		r := bufio.NewReader(stdoutReader)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- strings.TrimSpace(line)
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	if _, err := stdinWriter.WriteString(initLine + "\n"); err != nil {
		t.Fatalf("write init: %v", err)
	}
	select {
	case <-lines:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for initialize response")
	}
	// Responses and reload-driven list_changed notifications interleave from
	// here on; drain without correlating.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range lines {
		}
	}()

	const rounds = 30
	reloadsDone := make(chan struct{})
	go func() {
		defer close(reloadsDone)
		for range rounds {
			srv.applyReload(ctx, newConfig())
		}
	}()
	for i := range rounds {
		calls := `{"jsonrpc":"2.0","id":` + strconv.Itoa(100+2*i) + `,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}` + "\n" +
			`{"jsonrpc":"2.0","id":` + strconv.Itoa(101+2*i) + `,"method":"tools/call","params":{"name":"get_tool_schema","arguments":{"tool":"nosuch.tool"}}}` + "\n"
		if _, err := stdinWriter.WriteString(calls); err != nil {
			t.Fatalf("write calls: %v", err)
		}
	}
	<-reloadsDone

	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("server did not stop")
	}
	// Run has drained its handlers, so nothing writes to stdout any more;
	// closing the write end EOFs the reader, which closes lines and lets the
	// drain goroutine finish.
	_ = stdoutWriter.Close()
	<-drained
}

// TestCompress_ReloadDropsRemovedServer verifies the listing is rebuilt per
// call from the current config: a reload that removes a server drops it from
// the next list_tools without extra plumbing, and invoke_tool against it
// fails the same way a direct call would.
func TestCompress_ReloadDropsRemovedServer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"tool_a","description":"Tool A"}],"echoToolCalls":true}`),
			"srv2": fakeUpstream(`{"tools":[{"name":"tool_b","description":"Tool B"}],"echoToolCalls":true}`),
		},
	}
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"tool_a","description":"Tool A"}],"echoToolCalls":true}`),
		},
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:        oldCfg,
		PIDTrackerDir: t.TempDir(),
		Compression:   config.CompressionForce(config.CompressionMedium),
		Stdin:         stdinReader,
		Stdout:        stdoutWriter,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	send := func(msg string) {
		t.Helper()
		if _, err := stdinWriter.WriteString(msg + "\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	lines := make(chan string, 10)
	go func() {
		r := bufio.NewReader(stdoutReader)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- strings.TrimSpace(line)
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	readLine := func() string {
		t.Helper()
		select {
		case line := <-lines:
			return line
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for output line")
			return ""
		}
	}

	send(initLine)
	_ = readLine()

	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}`)
	listing1 := toolCallText(t, json.RawMessage(readLine()))
	for _, want := range []string{"srv1.tool_a", "srv2.tool_b"} {
		if !strings.Contains(listing1, want) {
			t.Fatalf("pre-reload listing missing %q:\n%s", want, listing1)
		}
	}

	srv.applyReload(ctx, newCfg)
	_ = readLine() // notifications/tools/list_changed from the reload

	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tools","arguments":{}}}`)
	listing2 := toolCallText(t, json.RawMessage(readLine()))
	if !strings.Contains(listing2, "srv1.tool_a") {
		t.Errorf("post-reload listing missing srv1.tool_a:\n%s", listing2)
	}
	if strings.Contains(listing2, "srv2.tool_b") {
		t.Errorf("post-reload listing still contains removed server:\n%s", listing2)
	}

	send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"tool":"srv2.tool_b","input":{}}}}`)
	if rpcErr := rpcErrorFromResponse(t, json.RawMessage(readLine())); rpcErr == nil || rpcErr.Code != ErrCodeServerNotFound {
		t.Errorf("invoke_tool against removed server = %v, want ErrCodeServerNotFound", rpcErr)
	}

	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not stop")
	}
}

// TestCompress_ListingChangesAfterDiscovery verifies the compressed analogue
// of straggler discovery: the client re-lists after tools/list_changed and the
// wrapper *description* — the thing the client caches — now contains the late
// server's tools.
func TestCompress_ListingChangesAfterDiscovery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"fast-srv": fakeUpstream(`{"tools":[{"name":"fast_tool","description":"Fast tool"}]}`),
			"slow-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// 400ms init delay — exceeds the 100ms grace period below.
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_tool","description":"Slow tool"}],"delays":{"initialize":400000000}}`,
				},
			},
		},
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:        cfg,
		PIDTrackerDir: t.TempDir(),
		Compression:   config.CompressionForce(config.CompressionMedium),
		Stdin:         stdinReader,
		Stdout:        stdoutWriter,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.listToolsGracePeriod = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	send := func(msg string) {
		t.Helper()
		if _, err := stdinWriter.WriteString(msg + "\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	lines := make(chan string, 10)
	go func() {
		r := bufio.NewReader(stdoutReader)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- strings.TrimSpace(line)
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	readLine := func() string {
		t.Helper()
		select {
		case line := <-lines:
			return line
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for output line")
			return ""
		}
	}
	invokeDescription := func(line string) string {
		t.Helper()
		tools := wrapperListFromResponse(t, json.RawMessage(line))
		return tools[wrapperInvokeTool].Description
	}

	send(initLine)
	_ = readLine() // initialize response

	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	desc1 := invokeDescription(readLine())
	if !strings.Contains(desc1, "fast-srv.fast_tool") {
		t.Fatalf("first listing missing fast tool:\n%s", desc1)
	}
	if strings.Contains(desc1, "slow-srv.slow_tool") {
		t.Fatalf("first listing should not contain the straggler's tool:\n%s", desc1)
	}

	// Background discovery completes and notifies.
	notifLine := readLine()
	var notif struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(notifLine), &notif); err != nil || notif.Method != "notifications/tools/list_changed" {
		t.Fatalf("expected tools/list_changed notification, got %q", notifLine)
	}

	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	desc2 := invokeDescription(readLine())
	if !strings.Contains(desc2, "slow-srv.slow_tool") {
		t.Errorf("re-listed description missing the straggler's tool:\n%s", desc2)
	}
	if desc1 == desc2 {
		t.Error("wrapper description did not change after background discovery")
	}

	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not stop")
	}
}
