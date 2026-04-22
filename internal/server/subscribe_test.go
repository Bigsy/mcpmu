package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// subscribeTestServer wires srv.Run over an io.Pipe so tests can write requests
// incrementally and leave stdin open long enough for async notifications to
// settle. The caller must call close() before reading stdout to force Run to
// return.
type subscribeTestServer struct {
	srv     *Server
	pw      *io.PipeWriter
	pr      *io.PipeReader
	stdout  *bytes.Buffer
	runDone chan struct{}
	cancel  context.CancelFunc
}

func (h *subscribeTestServer) write(frames ...string) {
	for _, f := range frames {
		if !strings.HasSuffix(f, "\n") {
			f += "\n"
		}
		_, _ = h.pw.Write([]byte(f))
	}
}

func (h *subscribeTestServer) settle(d time.Duration) { time.Sleep(d) }

func (h *subscribeTestServer) close(t *testing.T) {
	t.Helper()
	_ = h.pw.Close()
	select {
	case <-h.runDone:
	case <-time.After(10 * time.Second):
		h.cancel()
		<-h.runDone
		t.Fatal("srv.Run did not exit within timeout")
	}
}

func startSubscribeTestServer(t *testing.T, opts Options) *subscribeTestServer {
	t.Helper()
	pr, pw := io.Pipe()
	var stdout bytes.Buffer

	opts.Stdin = pr
	opts.Stdout = &stdout
	if opts.ServerName == "" {
		opts.ServerName = "mcpmu-test"
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "1.0.0"
	}
	if opts.ProtocolVersion == "" {
		opts.ProtocolVersion = "2024-11-05"
	}
	if opts.LogLevel == "" {
		opts.LogLevel = "error"
	}
	opts.PIDTrackerDir = t.TempDir()

	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = srv.Run(ctx)
	}()

	return &subscribeTestServer{
		srv:     srv,
		pw:      pw,
		pr:      pr,
		stdout:  &stdout,
		runDone: runDone,
		cancel:  cancel,
	}
}

// fakeSubprocessEnv returns env for a fake MCP server subprocess with the
// given config serialized as FAKE_MCP_CFG.
func fakeSubprocessEnv(t *testing.T, cfg map[string]any) map[string]string {
	t.Helper()
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal fake config: %v", err)
	}
	return map[string]string{
		"GO_WANT_HELPER_PROCESS": "1",
		"FAKE_MCP_CFG":           string(cfgJSON),
	}
}

func fakeServerConfig(t *testing.T, cfg map[string]any) config.ServerConfig {
	t.Helper()
	enabled := true
	return config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Enabled: &enabled,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     fakeSubprocessEnv(t, cfg),
	}
}

// collectNotifications returns all notifications/resources/updated frames
// seen in the stdout transcript along with their URIs, in order.
func collectUpdatedURIs(t *testing.T, out string) []string {
	t.Helper()
	var uris []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env struct {
			Method string `json:"method"`
			Params struct {
				URI string `json:"uri"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		if env.Method == "notifications/resources/updated" {
			uris = append(uris, env.Params.URI)
		}
	}
	return uris
}

// TestServer_Initialize_AdvertisesSubscribeCapability verifies the serve-mode
// server advertises subscribe: true in its resources capability when
// ExposeResources is enabled, regardless of upstream state.
func TestServer_Initialize_AdvertisesSubscribeCapability(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	var resp struct {
		Result struct {
			Capabilities struct {
				Resources *struct {
					Subscribe   bool `json:"subscribe"`
					ListChanged bool `json:"listChanged"`
				} `json:"resources"`
			} `json:"capabilities"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &resp); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, stdout.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.Result.Capabilities.Resources == nil {
		t.Fatal("expected resources capability")
	}
	if !resp.Result.Capabilities.Resources.Subscribe {
		t.Error("expected subscribe: true in capability")
	}
}

// TestServer_ResourcesSubscribe_HappyPath: init → list → subscribe →
// receive update notification with correct URI → unsubscribe.
func TestServer_ResourcesSubscribe_HappyPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeServerConfig(t, map[string]any{
				"tools":                    []any{},
				"resources":                []any{map[string]any{"uri": "file:///a.txt", "name": "a"}},
				"resourcesSubscribe":       true,
				"emitUpdateAfterSubscribe": true,
				// Give mcpmu time to register s.subs[uri] after the subscribe
				// RPC returns — the plan ordering (call → set) is otherwise
				// racy against an immediately-emitted update.
				"postSubscribeEmitDelayMs": 50,
			}),
		},
	}

	h := startSubscribeTestServer(t, Options{Config: cfg, ExposeResources: true})
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///a.txt"}}`,
	)
	h.settle(500 * time.Millisecond)
	h.write(`{"jsonrpc":"2.0","id":4,"method":"resources/unsubscribe","params":{"uri":"file:///a.txt"}}`)
	h.settle(200 * time.Millisecond)
	h.close(t)

	out := h.stdout.String()
	responses := parseResponsesByID(t, out)

	// Subscribe response should be empty-result success.
	var subResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[3], &subResp); err != nil {
		t.Fatalf("unmarshal subscribe response: %v", err)
	}
	if subResp.Error != nil {
		t.Fatalf("subscribe error: %v", subResp.Error)
	}

	// Unsubscribe response should be empty-result success.
	var unsubResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[4], &unsubResp); err != nil {
		t.Fatalf("unmarshal unsubscribe response: %v", err)
	}
	if unsubResp.Error != nil {
		t.Fatalf("unsubscribe error: %v", unsubResp.Error)
	}

	// Should have received notifications/resources/updated with the original URI.
	uris := collectUpdatedURIs(t, out)
	if len(uris) == 0 {
		t.Fatalf("expected at least one resources/updated notification, got none.\noutput: %s", out)
	}
	if !slices.Contains(uris, "file:///a.txt") {
		t.Errorf("expected URI file:///a.txt among updates, got %v", uris)
	}
}

// TestServer_ResourcesSubscribe_UnknownURI: subscribing to a URI not in the
// aggregator's resource map returns InvalidParams.
func TestServer_ResourcesSubscribe_UnknownURI(t *testing.T) {
	t.Parallel()
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"resources/subscribe","params":{"uri":"file:///never-listed"}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
		ExposeResources: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	responses := parseResponsesByID(t, stdout.String())
	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[2], &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected InvalidParams error for unknown URI")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("expected code %d, got %d", ErrCodeInvalidParams, resp.Error.Code)
	}
}

// TestServer_ResourcesSubscribe_UpstreamWithoutCapability: mcpmu still
// advertises subscribe: true, but a subscribe to a URI owned by an upstream
// that doesn't support subscribe returns MethodNotFound.
func TestServer_ResourcesSubscribe_UpstreamWithoutCapability(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeServerConfig(t, map[string]any{
				"tools":              []any{},
				"resources":          []any{map[string]any{"uri": "file:///a.txt", "name": "a"}},
				"resourcesSubscribe": false,
			}),
		},
	}

	h := startSubscribeTestServer(t, Options{Config: cfg, ExposeResources: true})
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///a.txt"}}`,
	)
	h.settle(300 * time.Millisecond)
	h.close(t)

	responses := parseResponsesByID(t, h.stdout.String())
	var resp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[3], &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected MethodNotFound error; upstream doesn't support subscribe")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("expected code %d (MethodNotFound), got %d (%s)",
			ErrCodeMethodNotFound, resp.Error.Code, resp.Error.Message)
	}
}

// TestServer_ResourcesSubscribe_MixedCapability: two upstreams, one supports
// subscribe and one doesn't. The supporting upstream's subscribe works; the
// non-supporting one returns MethodNotFound.
func TestServer_ResourcesSubscribe_MixedCapability(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"good": fakeServerConfig(t, map[string]any{
				"tools":                    []any{},
				"resources":                []any{map[string]any{"uri": "file:///good.txt", "name": "good"}},
				"resourcesSubscribe":       true,
				"emitUpdateAfterSubscribe": true,
			}),
			"bad": fakeServerConfig(t, map[string]any{
				"tools":              []any{},
				"resources":          []any{map[string]any{"uri": "file:///bad.txt", "name": "bad"}},
				"resourcesSubscribe": false,
			}),
		},
	}

	h := startSubscribeTestServer(t, Options{Config: cfg, ExposeResources: true})
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///good.txt"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/subscribe","params":{"uri":"file:///bad.txt"}}`,
	)
	h.settle(500 * time.Millisecond)
	h.close(t)

	responses := parseResponsesByID(t, h.stdout.String())

	var goodResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[3], &goodResp); err != nil {
		t.Fatalf("unmarshal good: %v", err)
	}
	if goodResp.Error != nil {
		t.Errorf("good subscribe returned error: %v", goodResp.Error)
	}

	var badResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[4], &badResp); err != nil {
		t.Fatalf("unmarshal bad: %v", err)
	}
	if badResp.Error == nil {
		t.Fatal("bad subscribe should have failed")
	}
	if badResp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("bad subscribe: expected code %d, got %d",
			ErrCodeMethodNotFound, badResp.Error.Code)
	}

	// Only the good URI should have yielded an update notification.
	uris := collectUpdatedURIs(t, h.stdout.String())
	if slices.Contains(uris, "file:///bad.txt") {
		t.Errorf("should not have received update for bad URI; got %v", uris)
	}
	if !slices.Contains(uris, "file:///good.txt") {
		t.Errorf("expected update for good URI, got %v", uris)
	}
}

// TestServer_ResourcesSubscribe_StrayNotificationDropped: an upstream emits
// a notifications/resources/updated for a URI nobody subscribed to; mcpmu
// drops it silently.
func TestServer_ResourcesSubscribe_StrayNotificationDropped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeServerConfig(t, map[string]any{
				"tools":              []any{},
				"resources":          []any{map[string]any{"uri": "file:///a.txt", "name": "a"}},
				"resourcesSubscribe": true,
				// Fake emits update even though nobody subscribed.
				"emitStartupUpdates": []string{"file:///a.txt"},
			}),
		},
	}

	h := startSubscribeTestServer(t, Options{Config: cfg, ExposeResources: true})
	// Force the upstream to start so the startup-emit actually runs.
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
	)
	h.settle(500 * time.Millisecond)
	h.close(t)

	uris := collectUpdatedURIs(t, h.stdout.String())
	if len(uris) != 0 {
		t.Errorf("expected no resources/updated notifications (stray), got %v", uris)
	}
}

// TestServer_ResourcesSubscribe_PostUnsubscribeUpdateDropped: after
// unsubscribe, an upstream update for the same URI must not be forwarded
// downstream. The fake emits an update right after responding to
// unsubscribe; since the plan's ordering deletes s.subs after the RPC
// succeeds, the subsequent update is dropped as stray.
func TestServer_ResourcesSubscribe_PostUnsubscribeUpdateDropped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeServerConfig(t, map[string]any{
				"tools":                      []any{},
				"resources":                  []any{map[string]any{"uri": "file:///a.txt", "name": "a"}},
				"resourcesSubscribe":         true,
				"emitUpdateAfterUnsubscribe": true,
				// Give mcpmu time to delete s.subs[uri] after unsubscribe
				// RPC returns before the fake emits the post-unsubscribe
				// update (symmetric to the subscribe delay).
				"postUnsubscribeEmitDelayMs": 50,
			}),
		},
	}

	h := startSubscribeTestServer(t, Options{Config: cfg, ExposeResources: true})
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///a.txt"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/unsubscribe","params":{"uri":"file:///a.txt"}}`,
	)
	// Wait long enough for the post-unsubscribe update to make it all the way
	// through the upstream client's reader goroutine.
	h.settle(500 * time.Millisecond)
	h.close(t)

	uris := collectUpdatedURIs(t, h.stdout.String())
	// No emitUpdateAfterSubscribe here, so the ONLY possible update would be
	// the post-unsubscribe one. It must be dropped.
	if len(uris) != 0 {
		t.Errorf("expected no resources/updated downstream after unsubscribe; got %v", uris)
	}
}

// TestServer_ResourcesSubscribe_ReloadClearsSubs: after a config reload the
// subscription map is cleared, downstream gets list_changed for resources,
// and mcpmu does NOT emit a best-effort upstream resources/unsubscribe.
func TestServer_ResourcesSubscribe_ReloadClearsSubs(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	logPath := filepath.Join(t.TempDir(), "requests.log")

	makeCfg := func() *config.Config {
		return &config.Config{
			SchemaVersion: 1,
			Servers: map[string]config.ServerConfig{
				"srv1": fakeServerConfig(t, map[string]any{
					"tools":              []any{},
					"resources":          []any{map[string]any{"uri": "file:///a.txt", "name": "a"}},
					"resourcesSubscribe": true,
					"requestLogPath":     logPath,
				}),
			},
		}
	}

	h := startSubscribeTestServer(t, Options{Config: makeCfg(), ExposeResources: true})
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///a.txt"}}`,
	)
	h.settle(300 * time.Millisecond)

	// Reload rebuilds the supervisor set — upstream transports close, local
	// subs clear, list_changed notifications are emitted.
	h.srv.applyReload(context.Background(), makeCfg())
	h.settle(300 * time.Millisecond)
	h.close(t)

	h.srv.subMu.Lock()
	remaining := len(h.srv.subs)
	h.srv.subMu.Unlock()
	if remaining != 0 {
		t.Errorf("expected 0 subscriptions after reload, got %d", remaining)
	}

	// list_changed notifications must be visible downstream after the reload.
	out := h.stdout.String()
	if !strings.Contains(out, `"method":"notifications/resources/list_changed"`) {
		t.Errorf("expected notifications/resources/list_changed after reload; stdout:\n%s", out)
	}

	// Best-effort upstream resources/unsubscribe MUST NOT be attempted
	// during reload — transport close ends the upstream session cleanly.
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(logBytes), "resources/unsubscribe") {
		t.Errorf("reload must not call upstream resources/unsubscribe; log:\n%s", logBytes)
	}
	// Sanity: subscribe should have been called upstream before reload.
	if !strings.Contains(string(logBytes), "resources/subscribe") {
		t.Errorf("expected upstream resources/subscribe to have been called; log:\n%s", logBytes)
	}
}

// TestServer_ResourcesSubscribe_DuplicateURI: when two upstreams advertise
// the same URI, resourceMap is last-writer-wins (pre-existing limitation).
// Subscribe must route to the last writer; a stray update from the other
// upstream for that URI must be dropped.
//
// The test forces deterministic "last-writer" ordering by delaying srv_b's
// resources/list response, so srv_b stores into resourceMap after srv_a.
// Assertions are then exact, not "at most one update".
func TestServer_ResourcesSubscribe_DuplicateURI(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	logA := filepath.Join(t.TempDir(), "srv_a.log")
	logB := filepath.Join(t.TempDir(), "srv_b.log")

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv_a": fakeServerConfig(t, map[string]any{
				"tools":              []any{},
				"resources":          []any{map[string]any{"uri": "file:///shared.txt", "name": "shared"}},
				"resourcesSubscribe": true,
				"requestLogPath":     logA,
				// Emits an unsolicited update at startup — represents the
				// "losing" upstream trying to push updates for a URI it
				// doesn't own in mcpmu's routing map.
				"emitStartupUpdates": []string{"file:///shared.txt"},
			}),
			"srv_b": fakeServerConfig(t, map[string]any{
				"tools":                    []any{},
				"resources":                []any{map[string]any{"uri": "file:///shared.txt", "name": "shared"}},
				"resourcesSubscribe":       true,
				"requestLogPath":           logB,
				"emitUpdateAfterSubscribe": true,
				"postSubscribeEmitDelayMs": 50,
				// Delay resources/list so srv_b responds AFTER srv_a. The
				// aggregator's per-server goroutines store into resourceMap
				// as their ListResources calls return, so srv_b stores last
				// and wins the URI in resourceMap.
				"delays": map[string]int64{
					"resources/list": int64(200 * time.Millisecond),
				},
			}),
		},
	}

	h := startSubscribeTestServer(t, Options{Config: cfg, ExposeResources: true})
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
	)
	// Wait for resources/list to fully complete on both upstreams before
	// subscribing, so the last-writer-wins ordering is stable.
	h.settle(400 * time.Millisecond)

	// Verify routing: the shared URI must be owned by srv_b (the delayed one).
	if val, ok := h.srv.resourceMap.Load("file:///shared.txt"); !ok {
		t.Fatal("expected shared URI in resourceMap after list, missing")
	} else if got := val.(string); got != "srv_b" {
		t.Fatalf("last-writer routing: expected srv_b to own shared URI, got %q", got)
	}

	h.write(`{"jsonrpc":"2.0","id":3,"method":"resources/subscribe","params":{"uri":"file:///shared.txt"}}`)
	h.settle(400 * time.Millisecond)
	h.close(t)

	// Subscribe succeeded.
	responses := parseResponsesByID(t, h.stdout.String())
	var subResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(responses[3], &subResp); err != nil {
		t.Fatalf("unmarshal subscribe: %v", err)
	}
	if subResp.Error != nil {
		t.Fatalf("subscribe on duplicated URI should have succeeded, got: %v", subResp.Error)
	}

	// Routing: subscribe hit srv_b only, not srv_a.
	logABytes, _ := os.ReadFile(logA)
	logBBytes, _ := os.ReadFile(logB)
	if strings.Contains(string(logABytes), "resources/subscribe") {
		t.Errorf("subscribe must not be sent to losing upstream srv_a; log:\n%s", logABytes)
	}
	if !strings.Contains(string(logBBytes), "resources/subscribe") {
		t.Errorf("subscribe should have been sent to winning upstream srv_b; log:\n%s", logBBytes)
	}

	// Downstream: exactly one update for the shared URI — from srv_b
	// (post-subscribe). srv_a's startup emit must have been filtered as stray.
	uris := collectUpdatedURIs(t, h.stdout.String())
	count := 0
	for _, u := range uris {
		if u == "file:///shared.txt" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 downstream update for shared URI (srv_b's post-subscribe, srv_a's stray filtered); got %d: %v",
			count, uris)
	}
}
