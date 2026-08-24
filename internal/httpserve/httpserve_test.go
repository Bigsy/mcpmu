package httpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/server"
)

// TestHelperProcess implements the fake MCP upstream subprocess.
func TestHelperProcess(t *testing.T) {
	mcptest.RunHelperProcess(t)
}

func fakeUpstream(t *testing.T, fake mcptest.FakeServerConfig) config.ServerConfig {
	t.Helper()
	return config.ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env:     mcptest.FakeServerEnv(t, fake),
	}
}

// startServer builds a Core over the given config, wraps it in an httpserve
// Server listening on a real loopback socket, and returns a probe factory.
func startServer(t *testing.T, cfg *config.Config, mutate func(*Options)) (*Server, string) {
	t.Helper()
	core, err := server.NewCore(server.Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	opts := Options{
		Core:          core,
		Addr:          "127.0.0.1:0",
		ServerVersion: "test",
	}
	if mutate != nil {
		mutate(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv, "http://" + listener.Addr().String()
}

func singleServerConfig(t *testing.T, fake mcptest.FakeServerConfig) *config.Config {
	t.Helper()
	return &config.Config{Servers: map[string]config.ServerConfig{"fake": fakeUpstream(t, fake)}}
}

func TestInitializeIssuesSessionAndRoutesCalls(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}

	version := probe.Initialize(t)
	if version == "" {
		t.Fatal("initialize returned no protocolVersion")
	}

	list := probe.Call(t, 2, "tools/list", nil)
	if list.Error != nil {
		t.Fatalf("tools/list: %d %s", list.Error.Code, list.Error.Message)
	}
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &tools); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}

	call := probe.Call(t, 3, "tools/call", map[string]any{
		"name": tools.Tools[0].Name, "arguments": map[string]any{},
	})
	if call.Error != nil {
		t.Fatalf("tools/call: %d %s", call.Error.Code, call.Error.Message)
	}
}

func TestSecondInitializeOnSessionIs400(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	resp := probe.Post(t, `{"jsonrpc":"2.0","id":9,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	body := mcptest.ReadBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("initialize with session header: status %d, want 400 (body: %s)", resp.StatusCode, body)
	}
}

func TestFailedInitializeIsNotRetained(t *testing.T) {
	// Two namespaces, none selected: initialize resolves no namespace and
	// returns an RPC error — 200 with an error object, no session retained.
	cfg := singleServerConfig(t, mcptest.DefaultConfig())
	cfg.Namespaces = map[string]config.NamespaceConfig{
		"a": {ServerIDs: []string{"fake"}},
		"b": {ServerIDs: []string{"fake"}},
	}
	srv, base := startServer(t, cfg, nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}

	res := probe.Call(t, 1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if res.Status != http.StatusOK || res.Error == nil {
		t.Fatalf("ambiguous-namespace initialize: status %d error %v, want 200 + error object", res.Status, res.Error)
	}
	if probe.SessionID != "" {
		t.Fatalf("failed initialize issued a session ID %q", probe.SessionID)
	}
	srv.mu.Lock()
	n := len(srv.sessions)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("session table holds %d sessions after failed initialize, want 0", n)
	}
}

func TestPostStatusShapes(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	url := base + "/mcp"

	t.Run("request without session is 400", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
	t.Run("request with unknown session is 404", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url, SessionID: "deadbeefdeadbeefdeadbeefdeadbeef"}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d, want 404", resp.StatusCode)
		}
	})
	t.Run("notification is 202 with empty body", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		probe.Initialize(t)
		resp := probe.Post(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
		body := mcptest.ReadBody(t, resp)
		if resp.StatusCode != http.StatusAccepted || body != "" {
			t.Fatalf("status %d body %q, want 202 with empty body", resp.StatusCode, body)
		}
	})
	t.Run("client response is 202 with empty body", func(t *testing.T) {
		// A frame with an id and no method is a response to a server→client
		// request, not a request. mcpmu issues none, so it is acknowledged
		// and dropped rather than dispatched as a call to method "".
		probe := &mcptest.HTTPProbe{BaseURL: url}
		probe.Initialize(t)
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":77,"result":{}}`)
		body := mcptest.ReadBody(t, resp)
		if resp.StatusCode != http.StatusAccepted || body != "" {
			t.Fatalf("status %d body %q, want 202 with empty body", resp.StatusCode, body)
		}
	})
	t.Run("parse error is 400 with JSON-RPC error body", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		resp := probe.Post(t, `{not json`)
		body := mcptest.ReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
		var rpc struct {
			ID    json.RawMessage `json:"id"`
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &rpc); err != nil {
			t.Fatalf("400 body is not JSON-RPC: %s", body)
		}
		if rpc.Error == nil || rpc.Error.Code != -32700 {
			t.Fatalf("400 body error = %+v, want parse error -32700", rpc.Error)
		}
		if string(rpc.ID) != "null" && len(rpc.ID) != 0 {
			t.Fatalf("400 body id = %s, want null", rpc.ID)
		}
	})
	t.Run("empty object is 400 invalid request", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		probe.Initialize(t)
		resp := probe.Post(t, `{}`)
		body := mcptest.ReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400 (was previously accepted as a method-less notification)", resp.StatusCode)
		}
		var rpc struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &rpc); err != nil || rpc.Error == nil || rpc.Error.Code != -32600 {
			t.Fatalf("400 body = %s, want invalid request -32600", body)
		}
		if !strings.Contains(body, `"id":null`) {
			t.Fatalf("error body omits the spec-required id:null: %s", body)
		}
	})
	t.Run("wrong jsonrpc version is 400 echoing the id", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		probe.Initialize(t)
		resp := probe.Post(t, `{"jsonrpc":"1.0","id":9,"method":"ping"}`)
		body := mcptest.ReadBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, `"id":9`) {
			t.Fatalf("status %d body %s, want 400 echoing id 9", resp.StatusCode, body)
		}
	})
	t.Run("id without method or result is 400", func(t *testing.T) {
		// Not a request (no method) and not a response (no result/error):
		// it must not slip through the client-response 202 path.
		probe := &mcptest.HTTPProbe{BaseURL: url}
		probe.Initialize(t)
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":11}`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
	t.Run("batch array is 400", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		resp := probe.Post(t, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
	t.Run("wrong content type is 415", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Content-Type", "text/plain")
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("status %d, want 415", resp.StatusCode)
		}
	})
	t.Run("oversized body is 413", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		big := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":%q}}`,
			strings.Repeat("x", maxBodyBytes+1))
		resp := probe.Post(t, big)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d, want 413", resp.StatusCode)
		}
	})
}

func TestGetStreamDeliversNotificationsAndBacklog(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.EmitToolsListChangedAfterFirstList = true
	_, base := startServer(t, singleServerConfig(t, fake), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	// tools/list starts the upstream, which then emits tools/list_changed.
	// No GET stream is attached yet, so the notification lands in the
	// backlog and must be delivered when the stream attaches.
	if res := probe.Call(t, 2, "tools/list", nil); res.Error != nil {
		t.Fatalf("tools/list: %+v", res.Error)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stream := probe.OpenStream(t)
		if stream.Status != http.StatusOK {
			t.Fatalf("GET stream status %d, want 200", stream.Status)
		}
		select {
		case ev := <-stream.Events:
			method, _ := ev.DecodeNotification(t)
			if method != "notifications/tools/list_changed" {
				t.Fatalf("SSE delivered %q, want notifications/tools/list_changed", method)
			}
			stream.Close()
			return
		case <-time.After(200 * time.Millisecond):
			// The upstream's list_changed may not have propagated into the
			// hub yet; retry until deadline.
			stream.Close()
			if time.Now().After(deadline) {
				t.Fatal("backlogged notification never arrived on the GET stream")
			}
		}
	}
}

func TestSecondGetStreamReplacesFirst(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	first := probe.OpenStream(t)
	if first.Status != http.StatusOK {
		t.Fatalf("first GET status %d", first.Status)
	}
	second := probe.OpenStream(t)
	if second.Status != http.StatusOK {
		t.Fatalf("second GET status %d, want 200 (replacement, not 409)", second.Status)
	}
	select {
	case _, ok := <-first.Events:
		if ok {
			t.Fatal("first stream received an event after replacement")
		}
		// closed — evicted as expected
	case <-time.After(5 * time.Second):
		t.Fatal("first stream was not closed after a replacement GET attached")
	}
}

func TestGetStreamKeepalive(t *testing.T) {
	old := keepaliveInterval
	keepaliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = old })

	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	stream := probe.OpenStream(t)
	if stream.Status != http.StatusOK {
		t.Fatalf("GET status %d", stream.Status)
	}
	ev := stream.Next(t, 5*time.Second)
	if ev.Comment == "" {
		t.Fatalf("expected a keepalive comment, got %+v", ev)
	}
}

func TestGetRequiresAcceptAndSession(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	resp := probe.Request(t, http.MethodGet, "", "Accept", "application/json")
	if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotAcceptable {
		t.Fatalf("GET without event-stream Accept: status %d, want 406", resp.StatusCode)
	}

	anon := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	stream := anon.OpenStream(t)
	if stream.Status != http.StatusBadRequest {
		t.Fatalf("GET without session: status %d, want 400", stream.Status)
	}
	anon.SessionID = "deadbeefdeadbeefdeadbeefdeadbeef"
	stream = anon.OpenStream(t)
	if stream.Status != http.StatusNotFound {
		t.Fatalf("GET with unknown session: status %d, want 404", stream.Status)
	}
}

func TestDeleteTerminatesSession(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	resp := probe.Request(t, http.MethodDelete, "")
	if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: status %d, want 204", resp.StatusCode)
	}
	resp = probe.Post(t, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)
	if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST after DELETE: status %d, want 404", resp.StatusCode)
	}
	resp = probe.Request(t, http.MethodDelete, "")
	if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE: status %d, want 404", resp.StatusCode)
	}
}

func TestNamespaceRouting(t *testing.T) {
	cfg := singleServerConfig(t, mcptest.DefaultConfig())
	cfg.Namespaces = map[string]config.NamespaceConfig{
		"work":     {ServerIDs: []string{"fake"}},
		"personal": {ServerIDs: []string{}},
	}
	_, base := startServer(t, cfg, nil)

	t.Run("unknown namespace is 404", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp/nope"}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status %d, want 404", resp.StatusCode)
		}
	})
	t.Run("session is bound to its namespace route", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp/work"}
		probe.Initialize(t)

		crossed := &mcptest.HTTPProbe{BaseURL: base + "/mcp/personal", SessionID: probe.SessionID}
		resp := crossed.Post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("cross-namespace session reuse: status %d, want 404", resp.StatusCode)
		}
	})
}

func TestTokenAuth(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), func(o *Options) {
		o.Token = "sekrit"
	})
	url := base + "/mcp"

	t.Run("missing token is 401", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", resp.StatusCode)
		}
		if resp.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("WWW-Authenticate = %q, want Bearer", resp.Header.Get("WWW-Authenticate"))
		}
	})
	t.Run("wrong token is 401", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url, Token: "wrong"}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", resp.StatusCode)
		}
	})
	t.Run("right token works", func(t *testing.T) {
		probe := &mcptest.HTTPProbe{BaseURL: url, Token: "sekrit"}
		probe.Initialize(t)
	})
	t.Run("healthz is exempt", func(t *testing.T) {
		resp, err := http.Get(base + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("healthz status %d, want 200", resp.StatusCode)
		}
	})
}

func TestOriginPolicy(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), func(o *Options) {
		o.AllowedOrigins = []string{"https://myapp.example"}
	})
	url := base + "/mcp"
	cases := []struct {
		origin string
		want   int
	}{
		{"", http.StatusBadRequest}, // absent origin passes; 400 is the missing-session response
		{"http://localhost:3000", http.StatusBadRequest},
		{"http://127.0.0.1", http.StatusBadRequest},
		{"https://myapp.example", http.StatusBadRequest},
		{"https://evil.example", http.StatusForbidden},
		{"null", http.StatusForbidden},
	}
	for _, tc := range cases {
		probe := &mcptest.HTTPProbe{BaseURL: url}
		resp := probe.Post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "Origin", tc.origin)
		if mcptest.ReadBody(t, resp); resp.StatusCode != tc.want {
			t.Fatalf("Origin %q: status %d, want %d", tc.origin, resp.StatusCode, tc.want)
		}
	}
}

func TestNonLoopbackWithoutTokenRefusesToStart(t *testing.T) {
	core, err := server.NewCore(server.Options{
		Config:        &config.Config{},
		PIDTrackerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	for _, addr := range []string{"0.0.0.0:8081", ":8081", "192.168.1.10:8081"} {
		if _, err := New(Options{Core: core, Addr: addr}); err == nil {
			t.Errorf("New with addr %q and no token: want error, got nil", addr)
		}
	}
	srv, err := New(Options{Core: core, Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New on loopback without token: %v", err)
	}
	_ = srv.Shutdown(context.Background())
}

func TestIdleReaperClosesSession(t *testing.T) {
	oldJanitor := janitorInterval
	janitorInterval = 25 * time.Millisecond
	t.Cleanup(func() { janitorInterval = oldJanitor })

	srv, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), func(o *Options) {
		o.SessionIdleTimeout = 50 * time.Millisecond
	})
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.sessions)
		srv.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle session was never reaped (%d still in table)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp := probe.Post(t, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`)
	if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST after reap: status %d, want 404", resp.StatusCode)
	}
}

// TestReaperSkipsInFlightRequest pins the reaper against a call that outlives
// the idle timeout. lastActive is stamped when a request arrives, so a slow
// tool call would otherwise have its session torn down — and its context
// cancelled — from underneath it.
func TestReaperSkipsInFlightRequest(t *testing.T) {
	oldJanitor := janitorInterval
	janitorInterval = 25 * time.Millisecond
	t.Cleanup(func() { janitorInterval = oldJanitor })

	fake := mcptest.DefaultConfig()
	fake.EchoToolCalls = true
	fake.Delays = map[string]time.Duration{"tools/call": 600 * time.Millisecond}
	srv, base := startServer(t, singleServerConfig(t, fake), func(o *Options) {
		o.SessionIdleTimeout = 150 * time.Millisecond
	})
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	list := probe.Call(t, 2, "tools/list", nil)
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &tools); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools/list: %v (%s)", err, list.Result)
	}

	res := probe.Call(t, 3, "tools/call", map[string]any{
		"name": tools.Tools[0].Name, "arguments": map[string]any{},
	})
	if res.Status != http.StatusOK || res.Error != nil {
		t.Fatalf("slow tools/call: status %d error %+v, want 200 with a result", res.Status, res.Error)
	}

	// Once it is no longer in flight the ordinary idle rules resume.
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		n := len(srv.sessions)
		srv.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session was never reaped after the call finished (%d in table)", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConcurrentPostsOnOneSession(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.EchoToolCalls = true
	_, base := startServer(t, singleServerConfig(t, fake), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	list := probe.Call(t, 2, "tools/list", nil)
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &tools); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools/list: %v (%s)", err, list.Result)
	}
	toolName := tools.Tools[0].Name

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Go(func() {
			p := &mcptest.HTTPProbe{BaseURL: probe.BaseURL, SessionID: probe.SessionID}
			res := p.Call(t, 100+i, "tools/call", map[string]any{
				"name": toolName, "arguments": map[string]any{"n": i},
			})
			if res.Error != nil {
				errs <- fmt.Errorf("caller %d: %d %s", i, res.Error.Code, res.Error.Message)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestVersionNegotiationShapes asserts the negotiation our own client cannot
// exercise (it records the version it requested, not the server's answer): a
// supported revision is echoed, an unsupported one gets our newest.
func TestVersionNegotiationShapes(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	cases := []struct {
		requested string
		want      string
	}{
		{"2024-11-05", "2024-11-05"},
		{"2025-06-18", "2025-06-18"},
		{"2099-01-01", server.LatestDownstreamProtocolVersion()},
		{"", server.LatestDownstreamProtocolVersion()},
	}
	for _, tc := range cases {
		probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
		res := probe.Call(t, 1, "initialize", map[string]any{"protocolVersion": tc.requested})
		if res.Error != nil {
			t.Fatalf("initialize(%q): %+v", tc.requested, res.Error)
		}
		var out struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(res.Result, &out); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if out.ProtocolVersion != tc.want {
			t.Errorf("initialize(%q) negotiated %q, want %q", tc.requested, out.ProtocolVersion, tc.want)
		}
	}
}

// TestSessionCapEvictsIdle pins the eviction policy: a full session table
// recycles the least-recently-active idle session for each new initialize
// instead of 503-ing healthy clients until the idle timeout lapses. A
// crash-looping client that re-initializes without DELETE-ing must not take
// the endpoint down for everyone.
func TestSessionCapEvictsIdle(t *testing.T) {
	// No servers configured: sessions stay lazy and cheap to mint.
	srv, base := startServer(t, &config.Config{}, nil)

	tableSize := func() int {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.sessions)
	}

	probes := make([]*mcptest.HTTPProbe, 0, maxSessions)
	for range maxSessions {
		p := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
		p.Initialize(t)
		probes = append(probes, p)
	}
	if n := tableSize(); n != maxSessions {
		t.Fatalf("session table holds %d sessions, want %d", n, maxSessions)
	}

	// One initialize past the cap must succeed via eviction, not 503.
	extra := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	extra.Initialize(t)

	if n := tableSize(); n > maxSessions {
		t.Fatalf("session table grew past the cap: %d", n)
	}

	// The very first session — the least-recently-active — was the victim.
	resp := probes[0].Post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oldest session survived eviction: status %d, want 404", resp.StatusCode)
	}

	// A recent survivor still works.
	if res := probes[maxSessions-1].Call(t, 2, "tools/list", nil); res.Error != nil {
		t.Fatalf("recent session evicted too: %d %s", res.Error.Code, res.Error.Message)
	}
}

// TestShutdownEndsStreamsAndKeepsSessionsThroughTheDrain pins the shutdown
// contract. With the previous ordering — http.Shutdown first, closed and
// teardown after — an attached standalone GET SSE stream never idled, so
// every Ctrl-C with a connected client burned the full grace period before
// anything was torn down. Now: streams end before the drain (so Shutdown is
// bounded by real in-flight work), and a POST round trip already in flight
// runs to completion inside a session that stays alive until the drain
// finishes. The closed-before-drain guarantee itself is pinned at unit level
// by TestBeginDrainRefusesRegisterAndStreamAttach — it is unobservable over
// HTTP because http.Shutdown closes both idle connections and the listener
// up front.
func TestShutdownEndsStreamsAndKeepsSessionsThroughTheDrain(t *testing.T) {
	requestLog := filepath.Join(t.TempDir(), "upstream-methods.log")
	cfg := singleServerConfig(t, mcptest.FakeServerConfig{
		Tools:          []mcptest.Tool{{Name: "slow_tool"}},
		EchoToolCalls:  true,
		Delays:         map[string]time.Duration{"tools/call": 3 * time.Second},
		RequestLogPath: requestLog,
	})
	srv, base := startServer(t, cfg, nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	postStatus := func(body string, headers ...string) int {
		req, err := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(body))
		if err != nil {
			return 0
		}
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return 0 // connection gone
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// Attach the standalone SSE stream and hold it open.
	getReq, err := http.NewRequest(http.MethodGet, base+"/mcp", nil)
	if err != nil {
		t.Fatalf("build GET: %v", err)
	}
	getReq.Header.Set("Accept", "text/event-stream")
	getReq.Header.Set("Mcp-Session-Id", probe.SessionID)
	attached := make(chan error, 1)
	streamEnded := make(chan struct{})
	go func() {
		defer close(streamEnded)
		resp, err := (&http.Client{}).Do(getReq)
		if err != nil {
			attached <- err
			return
		}
		attached <- nil
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	select {
	case err := <-attached:
		if err != nil {
			t.Fatalf("SSE attach: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSE stream never attached")
	}

	// A tools/call whose response is parked at the upstream represents the
	// legitimate in-flight work the drain exists to protect.
	callDone := make(chan int, 1)
	go func() {
		callDone <- postStatus(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"fake.slow_tool","arguments":{}}}`,
			"Mcp-Session-Id", probe.SessionID)
	}()
	waitDeadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(requestLog)
		if err == nil && strings.Contains(string(data), "tools/call") {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("upstream never received tools/call")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Shutdown(ctx) }()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Shutdown hit its context bound — the SSE stream still pins the drain")
	}

	// The in-flight round trip survived the drain: the session lived until
	// the drain finished and the client got its response.
	select {
	case status := <-callDone:
		if status != http.StatusOK {
			t.Errorf("in-flight tools/call got status %d, want 200 (session abandoned mid-call?)", status)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("in-flight tools/call never completed")
	}

	select {
	case <-streamEnded:
	case <-time.After(2 * time.Second):
		t.Error("SSE stream outlived Shutdown")
	}

	srv.mu.Lock()
	remaining := len(srv.sessions)
	srv.mu.Unlock()
	if remaining != 0 {
		t.Errorf("session table holds %d sessions after Shutdown, want 0 (teardown ran)", remaining)
	}
}

// TestBeginDrainRefusesRegisterAndStreamAttach pins the ordering guarantees
// of beginDrain directly: from the moment it returns, register refuses new
// sessions (nothing can slip past the shutdown snapshot to leak its private
// instances) and no hub accepts a new standalone stream.
func TestBeginDrainRefusesRegisterAndStreamAttach(t *testing.T) {
	srv, base := startServer(t, &config.Config{}, nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)

	sessions := srv.beginDrain()
	if len(sessions) != 1 {
		t.Fatalf("beginDrain snapshot holds %d sessions, want 1", len(sessions))
	}

	if ok, _ := srv.register("fresh-session", &httpSession{}); ok {
		srv.mu.Lock()
		delete(srv.sessions, "fresh-session")
		srv.mu.Unlock()
		t.Error("register succeeded after beginDrain; an initialize during the drain could leak its session")
	}

	for id, hs := range sessions {
		if _, _, ok := hs.hub.attach(); ok {
			t.Errorf("hub for session %s accepted a new stream after beginDrain", id)
		}
	}

	// Finish what beginDrain started so cleanup has nothing wedged.
	for id, hs := range sessions {
		srv.teardown(id, hs)
	}
}

func TestHubCoalescesBacklog(t *testing.T) {
	hub := newSSEHub()
	own, _, ok := hub.attach()
	if !ok {
		t.Fatal("attach on a fresh hub failed")
	}
	for range 10 {
		_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n"))
	}
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"a://x"}}` + "\n"))
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"a://y"}}` + "\n"))
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"a://x"}}` + "\n"))

	frames := hub.takeAll(own)
	if len(frames) != 3 {
		keys := make([]string, len(frames))
		for i, f := range frames {
			keys[i] = f.key
		}
		t.Fatalf("backlog holds %d frames %v, want 3 (list_changed coalesced, updated per-URI)", len(frames), keys)
	}
}

// TestHubTakeAllRefusesEvictedStream pins the ownership check: after a newer
// GET attaches, an evicted handler can still be scheduled and call takeAll —
// its drain must come up empty or the frames land on the abandoned connection.
func TestHubTakeAllRefusesEvictedStream(t *testing.T) {
	hub := newSSEHub()
	old, _, ok := hub.attach()
	if !ok {
		t.Fatal("first attach failed")
	}
	cur, _, ok := hub.attach() // evicts old
	if !ok {
		t.Fatal("second attach failed")
	}
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n"))

	if frames := hub.takeAll(old); len(frames) != 0 {
		t.Fatalf("evicted stream drained %d frames; they belong to its replacement", len(frames))
	}
	if frames := hub.takeAll(cur); len(frames) != 1 {
		t.Fatalf("current stream drained %d frames, want 1", len(frames))
	}
}

// TestHubReplacementSeesBacklogAfterEvictedSignal pins the lost-wakeup fix.
// With a single shared wake channel, this sequence stranded a frame: write
// signals wake; the evicted stream consumes that signal in its select race,
// drains nothing (it no longer owns the queue), and exits; the replacement
// then sits on an empty channel with the frame still queued. Per-attach drain
// channels prime the replacement at attach time instead.
func TestHubReplacementSeesBacklogAfterEvictedSignal(t *testing.T) {
	hub := newSSEHub()
	_, oldDrain, ok := hub.attach()
	if !ok {
		t.Fatal("first attach failed")
	}

	// A frame arrives while the old stream is attached; its signal lands on
	// the old stream's drain channel.
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n"))
	select {
	case <-oldDrain:
	default:
		t.Fatal("old stream was not signalled for the frame")
	}

	// The replacement attaches — the evicted handler has consumed its signal
	// but not yet exited. The backlog must already be primed on the new
	// stream's own channel.
	newOwn, newDrain, ok := hub.attach()
	if !ok {
		t.Fatal("second attach failed")
	}
	select {
	case <-newDrain:
	default:
		t.Fatal("replacement stream sits on an empty drain with a queued frame — lost wakeup")
	}
	if frames := hub.takeAll(newOwn); len(frames) != 1 {
		t.Fatalf("replacement drained %d frames, want the stranded one", len(frames))
	}

	// Later writes signal only the current stream's channel.
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n"))
	select {
	case <-newDrain:
	default:
		t.Fatal("current stream not signalled after later write")
	}
}

func TestHubWriteAfterCloseIsNoop(t *testing.T) {
	hub := newSSEHub()
	hub.close()
	n, err := hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n"))
	if err != nil || n == 0 {
		t.Fatalf("Write after close = (%d, %v), want full-length no-op", n, err)
	}
	if frames := hub.takeAll(nil); len(frames) != 0 {
		t.Fatalf("closed hub queued %d frames", len(frames))
	}
}
