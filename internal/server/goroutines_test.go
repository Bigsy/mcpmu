package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// TestNoBareGoroutines is the lint-ish guard for the "one way to spawn a
// goroutine" rule: every `go` statement in package server (outside tests)
// must live in goroutines.go, where it is wrapped with panic recovery,
// lifetime context, and WaitGroup tracking.
func TestNoBareGoroutines(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	goStmt := regexp.MustCompile(`(?m)^\s*go\s+[\w.]+|^\s*go\s+func\b`)
	wgGo := regexp.MustCompile(`\bhandlersWG\.Go\(|\bbgWG\.Go\(`)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "goroutines.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for lineNo, line := range strings.Split(string(src), "\n") {
			if goStmt.MatchString(line) || wgGo.MatchString(line) {
				t.Errorf("%s:%d: bare goroutine %q — use Session.spawn/spawnRequest, Core.spawn, or goSafe",
					name, lineNo+1, strings.TrimSpace(line))
			}
		}
	}
}

// panicOnceWriter panics on the first Write whose payload contains trigger,
// then behaves as a plain buffer. It plants a panic inside the goroutine
// that is answering exactly one request.
type panicOnceWriter struct {
	lockedBuffer
	trigger string
	fired   bool
	mu      sync.Mutex
}

func (w *panicOnceWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	fire := !w.fired && strings.Contains(string(p), w.trigger)
	if fire {
		w.fired = true
	}
	w.mu.Unlock()
	if fire {
		panic("injected panic in request handler")
	}
	return w.lockedBuffer.Write(p)
}

// responsesByID parses NDJSON output into responses keyed by id string.
func responsesByID(t *testing.T, out string) map[string]RPCResponse {
	t.Helper()
	got := map[string]RPCResponse{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		var resp RPCResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil || resp.ID == nil {
			continue
		}
		got[string(resp.ID)] = resp
	}
	return got
}

// A panic in a per-request goroutine must not take the session down: the
// caller gets -32603 for that id, the next request is served, and shutdown
// still stops the upstreams.
func TestSession_HandlerPanicRepliesInternalError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv": {
				Kind: config.ServerKindStdio, Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"echo"}],"echoToolCalls":true}`,
				},
			},
		},
	}

	clientIn, clientOut := io.Pipe()
	// The tools/call response (id 2) is written by the spawned handler
	// goroutine; make that write panic.
	output := &panicOnceWriter{trigger: `"id":2`}

	srv, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: clientIn, Stdout: output,
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	write := func(line string) {
		t.Helper()
		if _, err := clientOut.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write to server: %v", err)
		}
	}
	waitFor := func(substr string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !strings.Contains(output.String(), substr) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in output:\n%s", substr, output.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}`)
	waitFor(`"id":1`)
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv.echo","arguments":{}}}`)
	waitFor(`"id":2`)
	// The daemon must still be serving: a plain request after the panic.
	write(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	waitFor(`"id":3`)

	_ = clientOut.Close()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the client disconnected")
	}

	responses := responsesByID(t, output.String())
	panicked, ok := responses["2"]
	if !ok || panicked.Error == nil {
		t.Fatalf("panicking request got no error response:\n%s", output.String())
	}
	if panicked.Error.Code != ErrCodeInternalError {
		t.Errorf("error code = %d, want %d (%s)", panicked.Error.Code, ErrCodeInternalError, panicked.Error.Message)
	}
	if !strings.Contains(panicked.Error.Message, "panicked") {
		t.Errorf("error message %q does not mention the panic", panicked.Error.Message)
	}
	if next, ok := responses["3"]; !ok || next.Error != nil {
		t.Errorf("ping after the panic was not served normally: %+v", next)
	}
	if running := srv.RunningServers(); len(running) != 0 {
		t.Errorf("upstreams still running after shutdown: %v", running)
	}
}

// discoverAndNotify runs under the session lifetime: cancelling Run's ctx
// must end it promptly rather than letting it run out the discovery timeout
// against an upstream that is slow to answer tools/list.
func TestSession_BackgroundDiscoveryEndsOnCancel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"slow": {
				Kind: config.ServerKindStdio, Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// tools/list takes 4s: well past the grace period below, and
					// long enough that a prompt Run return proves cancellation.
					"FAKE_MCP_CFG": `{"tools":[{"name":"t"}],"delays":{"tools/list":4000000000}}`,
				},
			},
		},
	}

	clientIn, clientOut := io.Pipe()
	output := &lockedBuffer{}
	srv, err := New(Options{
		Config: cfg, PIDTrackerDir: t.TempDir(),
		Stdin: clientIn, Stdout: output,
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.listToolsGracePeriod = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	} {
		if _, err := clientOut.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write to server: %v", err)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(output.String(), `"id":2`) {
		if time.Now().After(deadline) {
			t.Fatalf("no tools/list response:\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !srv.bgDiscovering.Load() {
		t.Fatal("expected background discovery to be in progress after the grace period expired")
	}

	// SIGTERM-equivalent: cancel Run's ctx while discovery is still waiting.
	cancelled := time.Now()
	cancel()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	if elapsed := time.Since(cancelled); elapsed > 2*time.Second {
		t.Errorf("Run took %v to return after cancel; background discovery did not stop with the session", elapsed)
	}
	if srv.bgDiscovering.Load() {
		t.Error("background discovery still marked in progress after Run returned")
	}
	_ = clientOut.Close()
}

// In-process variant of TestServer_ReloadDuringActiveRequest so the race
// detector can see the shared state: a hot reload lands while a tools/call is
// in flight against the upstream it replaces. The request must get exactly
// one reply (result or error), the session must keep serving, and Run must
// return cleanly.
func TestServer_ReloadDuringActiveRequestInProcess(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	enabled := true
	serverWithDelay := func(fakeCfg string) *config.Config {
		return &config.Config{
			SchemaVersion: 1,
			Servers: map[string]config.ServerConfig{
				"slow-srv": {
					Kind: config.ServerKindStdio, Enabled: &enabled,
					Command: os.Args[0],
					Args:    []string{"-test.run=TestHelperProcess", "--"},
					Env: map[string]string{
						"GO_WANT_HELPER_PROCESS": "1",
						"FAKE_MCP_CFG":           fakeCfg,
					},
				},
			},
			Namespaces: map[string]config.NamespaceConfig{},
		}
	}
	initialCfg := serverWithDelay(`{"tools":[{"name":"slow_tool"}],"delays":{"tools/call":2000000000},"echoToolCalls":true}`)
	if err := config.SaveTo(initialCfg, configPath); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	clientIn, clientOut := io.Pipe()
	output := &lockedBuffer{}
	srv, err := New(Options{
		Config: initialCfg, ConfigPath: configPath, PIDTrackerDir: dir,
		DebounceDelay: 50 * time.Millisecond,
		Stdin:         clientIn, Stdout: output,
		ServerName: "mcpmu-test", ServerVersion: "1.0.0", LogLevel: "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	write := func(line string) {
		t.Helper()
		if _, err := clientOut.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write to server: %v", err)
		}
	}
	waitFor := func(substr string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for !strings.Contains(output.String(), substr) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in output:\n%s", substr, output.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}`)
	waitFor(`"id":1`, 15*time.Second)
	// tools/list starts the upstream so the tools/call below is in flight
	// against a running process when the reload lands.
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	waitFor(`"id":2`, 15*time.Second)
	write(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"slow-srv.slow_tool","arguments":{}}}`)
	time.Sleep(300 * time.Millisecond)

	// Hot reload through the real watcher path while the call is in flight.
	reloadCfg := serverWithDelay(`{"tools":[{"name":"slow_tool"}],"echoToolCalls":true}`)
	if err := config.SaveTo(reloadCfg, configPath); err != nil {
		t.Fatalf("save reload config: %v", err)
	}

	waitFor(`"id":3`, 20*time.Second)
	waitFor(`notifications/tools/list_changed`, 15*time.Second)

	// The session must still be usable after the reload: the replaced
	// upstream answers on the new config.
	write(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"slow-srv.slow_tool","arguments":{}}}`)
	waitFor(`"id":4`, 20*time.Second)

	_ = clientOut.Close()
	select {
	case err := <-runDone:
		if err != nil && err != context.Canceled {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the client disconnected")
	}

	responses := responsesByID(t, output.String())
	if _, ok := responses["3"]; !ok {
		t.Errorf("in-flight tools/call got no reply:\n%s", output.String())
	}
	if resp, ok := responses["4"]; !ok || resp.Error != nil {
		t.Errorf("tools/call after reload failed: %+v", resp)
	}
	if n := bytes.Count([]byte(output.String()), []byte(`"id":3`)); n != 1 {
		t.Errorf("in-flight request answered %d times, want exactly once", n)
	}
}
