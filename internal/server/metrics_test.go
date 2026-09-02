package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/metrics"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

// fakeUpstream builds a ServerConfig that spawns the mcptest helper process
// with the given fake-server JSON config.
func fakeUpstream(fakeCfg string) config.ServerConfig {
	enabled := true
	return config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Enabled: &enabled,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"FAKE_MCP_CFG":           fakeCfg,
		},
	}
}

// runServeWithMetrics runs a full embedded serve session against a config
// saved in a temp dir, so the Core creates a real metrics recorder. Run's
// shutdown path closes the Core, which performs the final flush; the loaded
// store therefore also exercises Close-time flushing.
func runServeWithMetrics(t *testing.T, cfg *config.Config, namespace, script string, runTimeout time.Duration) (*metrics.Store, string) {
	t.Helper()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	var stdout bytes.Buffer
	srv, err := New(Options{
		SessionOptions: SessionOptions{
			Namespace: namespace,
		},
		Config:        cfg,
		ConfigPath:    configPath,
		PIDTrackerDir: dir,
		Stdin:         strings.NewReader(script),
		Stdout:        &stdout,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	_ = srv.Run(ctx)

	metricsPath := filepath.Join(dir, "metrics.json")
	store, err := metrics.Load(metricsPath)
	if err != nil {
		t.Fatalf("load metrics: %v", err)
	}
	return store, stdout.String()
}

// findBucket returns the counters for today's (server, tool) bucket.
func findBucket(t *testing.T, store *metrics.Store, namespace, server, tool string) *metrics.Counters {
	t.Helper()
	key := metrics.BucketKey{
		Date:      time.Now().Format("2006-01-02"),
		Namespace: namespace,
		Server:    server,
		Tool:      tool,
	}
	c, ok := store.Rows[key]
	if !ok {
		t.Fatalf("bucket %+v not found; rows: %+v", key, store.Rows)
	}
	return c
}

const initLine = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`

func TestMetrics_OutcomeOK(t *testing.T) {
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

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.read_file","arguments":{"path":"/x"}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "work", script, 15*time.Second)
	c := findBucket(t, store, "work", "srv1", "read_file")
	if c.Calls != 1 || c.Outcomes[metrics.OutcomeOK] != 1 {
		t.Errorf("counters = %+v, want 1 ok call", c)
	}
	if len(store.RecentCalls) != 1 || store.RecentCalls[0].Tool != "read_file" {
		t.Errorf("recent = %+v, want one read_file entry", store.RecentCalls)
	}
}

func TestMetrics_OutcomeToolError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"boom"}],"echoToolCalls":true,"toolCallIsError":true}`),
		},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.boom","arguments":{}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "", script, 15*time.Second)
	c := findBucket(t, store, "", "srv1", "boom")
	if c.Outcomes[metrics.OutcomeToolError] != 1 {
		t.Errorf("counters = %+v, want 1 tool_error", c)
	}
}

func TestMetrics_OutcomeTimeout(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	// tools/call sleeps 2s upstream; the per-tool timeout is 1s.
	srv := fakeUpstream(`{"tools":[{"name":"slow"}],"echoToolCalls":true,"delays":{"tools/call":2000000000}}`)
	srv.ToolTimeoutSec = 1
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{"srv1": srv},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.slow","arguments":{}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "", script, 20*time.Second)
	c := findBucket(t, store, "", "srv1", "slow")
	if c.Outcomes[metrics.OutcomeTimeout] != 1 {
		t.Errorf("counters = %+v, want 1 timeout", c)
	}
}

func TestMetrics_OutcomeDenied(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"secret"}],"echoToolCalls":true}`),
		},
		Namespaces: map[string]config.NamespaceConfig{
			"locked": {ServerIDs: []string{"srv1"}, DenyByDefault: true},
		},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.secret","arguments":{}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "locked", script, 15*time.Second)
	c := findBucket(t, store, "locked", "srv1", "secret")
	if c.Outcomes[metrics.OutcomeDenied] != 1 {
		t.Errorf("counters = %+v, want 1 denied", c)
	}
	if c.DurationMsSum != 0 {
		t.Errorf("denied call recorded duration %dms, want 0", c.DurationMsSum)
	}
}

func TestMetrics_OutcomeError_DeadServer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:              config.ServerKindStdio,
				Enabled:           &enabled,
				Command:           "/nonexistent-mcpmu-test-binary",
				StartupTimeoutSec: 2,
			},
		},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.any_tool","arguments":{}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "", script, 20*time.Second)
	c := findBucket(t, store, "", "srv1", "any_tool")
	if c.Outcomes[metrics.OutcomeError] != 1 {
		t.Errorf("counters = %+v, want 1 error", c)
	}
}

func TestMetrics_ManagerTool(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcpmu.servers_list","arguments":{}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "", script, 5*time.Second)
	c := findBucket(t, store, "", "mcpmu", "servers_list")
	if c.Outcomes[metrics.OutcomeOK] != 1 {
		t.Errorf("counters = %+v, want 1 ok", c)
	}
}

func TestMetrics_ServerNotFound_NotRecorded(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ghost.tool","arguments":{}}}` + "\n"

	store, _ := runServeWithMetrics(t, cfg, "", script, 5*time.Second)
	for key := range store.Rows {
		if key.Server == "ghost" {
			t.Errorf("misaddressed call was recorded: %+v", key)
		}
	}
}

func TestMetrics_NilRecorder_NoConfigPath(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcpmu.servers_list","arguments":{}}}` + "\n"

	// No ConfigPath → nil recorder; nothing must panic.
	srv, err := New(Options{
		Config:        cfg,
		PIDTrackerDir: t.TempDir(),
		Stdin:         strings.NewReader(script),
		Stdout:        &stdout,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv.currentRecorder() != nil {
		t.Fatal("expected nil recorder without a config path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	if strings.Contains(stdout.String(), `"error"`) {
		t.Errorf("manager tool call failed: %s", stdout.String())
	}
}

func TestMetrics_DisabledInConfig(t *testing.T) {
	t.Parallel()

	disabled := false
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Metrics:       &config.MetricsConfig{Enabled: &disabled},
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	core, err := NewCore(Options{Config: cfg, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()

	if core.currentRecorder() != nil {
		t.Error("expected nil recorder when metrics are disabled")
	}
}

func TestMetrics_ReloadFlipsRecorder(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	core, err := NewCore(Options{Config: cfg, ConfigPath: configPath})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	defer core.Close()

	if core.currentRecorder() == nil {
		t.Fatal("expected recorder with metrics enabled by default")
	}

	// Flip off.
	disabled := false
	off := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Metrics:       &config.MetricsConfig{Enabled: &disabled},
	}
	core.replaceConfig(off)
	if core.currentRecorder() != nil {
		t.Error("expected recorder to be dropped when metrics disabled via reload")
	}

	// Flip back on.
	on := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}
	core.replaceConfig(on)
	if core.currentRecorder() == nil {
		t.Error("expected recorder to be recreated when metrics re-enabled via reload")
	}
}

// A 4xx from the upstream makes CallTool reinitialize and retry once. That is
// still one tool call, so it must land exactly one sample carrying the final
// outcome — not one per attempt.
func TestMetrics_RetryAfter4xxRecordsOneSample(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping HTTP upstream test in short mode")
	}
	testutil.SetupTestHome(t)

	var toolCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No server→client stream on this upstream; the transport tolerates a
		// 405 and stops asking for one.
		if r.Method == http.MethodGet {
			http.Error(w, "no stream", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(body, &req)

		writeResult := func(result string) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
		}
		w.Header().Set("Mcp-Session-Id", "retry-session")

		switch req.Method {
		case "initialize":
			writeResult(`{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},` +
				`"serverInfo":{"name":"retry-upstream","version":"1"}}`)
		case "tools/list":
			writeResult(`{"tools":[{"name":"ping","inputSchema":{"type":"object"}}]}`)
		case "tools/call":
			if toolCalls.Add(1) == 1 {
				// A stale session — a 404 for a session ID the upstream once
				// issued (it sets Mcp-Session-Id on every response) — is the
				// one failure CallTool retries after reinit. The client layer
				// reinitializes first; this second 404-shaped path proves the
				// retry machinery end to end.
				http.Error(w, "stale session", http.StatusNotFound)
				return
			}
			writeResult(`{"content":[{"type":"text","text":"pong"}]}`)
		default:
			// Notifications carry no id and expect no body.
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer upstream.Close()

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"remote": {URL: upstream.URL + "/mcp", Enabled: &enabled},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"work": {ServerIDs: []string{"remote"}},
		},
	}

	script := initLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remote.ping","arguments":{}}}` + "\n"

	store, stdout := runServeWithMetrics(t, cfg, "work", script, 30*time.Second)

	if got := toolCalls.Load(); got != 2 {
		t.Fatalf("upstream saw %d tools/call requests, want 2 (the 4xx plus the retry):\n%s", got, stdout)
	}
	if !strings.Contains(stdout, "pong") {
		t.Fatalf("the retried call did not reach the client:\n%s", stdout)
	}

	c := findBucket(t, store, "work", "remote", "ping")
	if c.Calls != 1 || c.Outcomes[metrics.OutcomeOK] != 1 {
		t.Errorf("counters = %+v, want exactly 1 ok call for two attempts", c)
	}
	if c.Outcomes[metrics.OutcomeError] != 0 {
		t.Errorf("the failed attempt was recorded as its own error sample: %+v", c)
	}
	if len(store.RecentCalls) != 1 {
		t.Errorf("recent = %d entries, want 1", len(store.RecentCalls))
	}
}
