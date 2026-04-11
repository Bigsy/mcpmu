package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := config.NewConfig()

	enabled := true
	_ = cfg.AddServer("test-stdio", config.ServerConfig{
		Command: "echo",
		Args:    []string{"hello"},
		Enabled: &enabled,
	})
	_ = cfg.AddServer("test-http", config.ServerConfig{
		URL:     "https://example.com/mcp",
		Enabled: &enabled,
	})
	_ = cfg.AddNamespace("default", config.NamespaceConfig{
		Description: "Default namespace",
		ServerIDs:   []string{"test-stdio"},
	})
	cfg.DefaultNamespace = "default"

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	supervisor := process.NewSupervisor(bus)

	srv, err := New(Options{
		Addr:       "127.0.0.1:0",
		Config:     cfg,
		ConfigPath: "",
		Supervisor: supervisor,
		Bus:        bus,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return srv
}

func TestServersPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Check key elements are present
	if !strings.Contains(html, "mcpmu") {
		t.Error("missing mcpmu branding")
	}
	if !strings.Contains(html, "test-stdio") {
		t.Error("missing test-stdio server")
	}
	if !strings.Contains(html, "test-http") {
		t.Error("missing test-http server")
	}
	if !strings.Contains(html, "Servers") {
		t.Error("missing Servers heading")
	}
}

func TestServerDetailPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/test-stdio", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "test-stdio") {
		t.Error("missing server name")
	}
	if !strings.Contains(html, "echo hello") {
		t.Error("missing command display")
	}
	if !strings.Contains(html, "Runtime") {
		t.Error("missing Runtime section")
	}
}

func TestServerDetailPage_NotFound(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/nonexistent", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestNamespacesPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/namespaces", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "default") {
		t.Error("missing default namespace")
	}
	if !strings.Contains(html, "Namespaces") {
		t.Error("missing Namespaces heading")
	}
}

func TestNamespaceDetailPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/namespaces/default", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "default") {
		t.Error("missing namespace name")
	}
	if !strings.Contains(html, "test-stdio") {
		t.Error("missing assigned server")
	}
}

func TestNamespaceDetailPage_NotFound(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/namespaces/nonexistent", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestStaticFiles(t *testing.T) {
	srv := newTestServer(t)

	for _, path := range []string{"/static/styles.css", "/static/htmx.min.js", "/static/sse.js"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)

		resp := rec.Result()
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", path, resp.StatusCode)
		}
	}
}

func TestRootRedirectsToServers(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Servers") {
		t.Error("root should show servers page")
	}
}

// flusherRecorder wraps httptest.ResponseRecorder to implement http.Flusher.
type flusherRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flusherRecorder) Flush() {
	f.ResponseRecorder.Flush()
}

func TestSSELogsEndpoint(t *testing.T) {
	srv := newTestServer(t)

	rec := &flusherRecorder{httptest.NewRecorder()}

	// Create a request with a cancelable context so the SSE handler unblocks
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/servers/test-stdio/logs/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.httpServer.Handler.ServeHTTP(rec, req)
	}()

	// Give the handler time to set headers, then cancel
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	ct := rec.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
}

func TestSSELogsEndpoint_NotFound(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/nonexistent/logs/stream", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestFragmentServerTable(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fragments/servers/table", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Should contain server names but NOT the full layout (no <html> tag)
	if strings.Contains(html, "<html") {
		t.Error("fragment should not contain full HTML layout")
	}
	if !strings.Contains(html, "test-stdio") {
		t.Error("fragment should contain server name")
	}
	if !strings.Contains(html, "<table") {
		t.Error("fragment should contain table element")
	}
}

func TestFragmentServerStatus(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fragments/servers/test-stdio/status", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "pill") {
		t.Error("fragment should contain status pill")
	}
}

func TestFragmentServerStatus_NotFound(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fragments/servers/nonexistent/status", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}
