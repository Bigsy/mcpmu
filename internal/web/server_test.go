package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/registry"
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

	// Write config to temp file so mutateConfig can reload from disk
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save test config: %v", err)
	}

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	supervisor := process.NewSupervisor(bus)

	srv, err := New(Options{
		Addr:       "127.0.0.1:0",
		Config:     cfg,
		ConfigPath: configPath,
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

// --- Phase 2: CRUD tests ---

func TestServerAddPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/add", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "Add Server") {
		t.Error("missing Add Server heading")
	}
	if !strings.Contains(html, `name="command"`) {
		t.Error("missing command input")
	}
}

func TestServerCreate(t *testing.T) {
	srv := newTestServer(t)

	form := strings.NewReader("name=new-server&kind=stdio&command=echo&args=test&enabled=true&startup_timeout=10&tool_timeout=60&auth_mode=none")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 303, got %d: %s", resp.StatusCode, body)
	}

	loc := resp.Header.Get("Location")
	if loc != "/servers/new-server" {
		t.Errorf("expected redirect to /servers/new-server, got %q", loc)
	}

	// Verify server was persisted
	if _, ok := srv.cfg.GetServer("new-server"); !ok {
		t.Error("new-server not found in config")
	}
}

func TestServerCreate_Duplicate(t *testing.T) {
	srv := newTestServer(t)

	form := strings.NewReader("name=test-stdio&kind=stdio&command=echo&enabled=true&startup_timeout=10&tool_timeout=60&auth_mode=none")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "already exists") {
		t.Error("error page should mention already exists")
	}
}

func TestServerEditPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/test-stdio/edit", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "Edit Server") {
		t.Error("missing Edit Server heading")
	}
	if !strings.Contains(html, "echo") {
		t.Error("missing pre-filled command value")
	}
}

func TestServerUpdate(t *testing.T) {
	srv := newTestServer(t)

	form := strings.NewReader("kind=stdio&command=cat&args=--help&enabled=true&autostart=true&startup_timeout=15&tool_timeout=30&auth_mode=none")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers/test-stdio/edit", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 303, got %d: %s", resp.StatusCode, body)
	}

	// Verify update was applied
	updated, ok := srv.cfg.GetServer("test-stdio")
	if !ok {
		t.Fatal("test-stdio not found after update")
	}
	if updated.Command != "cat" {
		t.Errorf("expected command 'cat', got %q", updated.Command)
	}
	if !updated.Autostart {
		t.Error("expected autostart to be true")
	}
}

func TestServerDelete(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers/test-http/delete", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	if _, ok := srv.cfg.GetServer("test-http"); ok {
		t.Error("test-http should be deleted")
	}
}

func TestServerToggle(t *testing.T) {
	srv := newTestServer(t)

	// test-stdio starts enabled; toggle should disable it
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers/test-stdio/toggle", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	s, _ := srv.cfg.GetServer("test-stdio")
	if s.IsEnabled() {
		t.Error("server should be disabled after toggle")
	}
}

func TestNamespaceAddPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/namespaces/add", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Add Namespace") {
		t.Error("missing Add Namespace heading")
	}
}

func TestNamespaceCreate(t *testing.T) {
	srv := newTestServer(t)

	form := strings.NewReader("name=production&description=Prod+servers&deny_by_default=false")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/namespaces", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 303, got %d: %s", resp.StatusCode, body)
	}

	if _, ok := srv.cfg.GetNamespace("production"); !ok {
		t.Error("production namespace not found in config")
	}
}

func TestNamespaceDelete(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/namespaces/default/delete", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", resp.StatusCode)
	}

	if _, ok := srv.cfg.GetNamespace("default"); ok {
		t.Error("default namespace should be deleted")
	}
}

func TestAPIListServers(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "test-stdio") {
		t.Error("API response should contain test-stdio")
	}
}

func TestAPIGetServer(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers/test-stdio", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "test-stdio") {
		t.Error("API response should contain test-stdio")
	}
}

func TestAPIGetServer_NotFound(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers/nonexistent", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAPICreateServer(t *testing.T) {
	srv := newTestServer(t)

	body := `{"name":"api-server","config":{"command":"echo","args":["hello"]}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/servers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, respBody)
	}

	if _, ok := srv.cfg.GetServer("api-server"); !ok {
		t.Error("api-server not found in config")
	}
}

func TestAPIDeleteServer(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/servers/test-http", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	if _, ok := srv.cfg.GetServer("test-http"); ok {
		t.Error("test-http should be deleted")
	}
}

func TestAPIUpdateServer(t *testing.T) {
	srv := newTestServer(t)

	body := `{"enabled":false}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/servers/test-stdio", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	s, _ := srv.cfg.GetServer("test-stdio")
	if s.IsEnabled() {
		t.Error("server should be disabled after update")
	}
}

func TestAPIListNamespaces(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/namespaces", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "default") {
		t.Error("API response should contain default namespace")
	}
}

func TestAPIExportConfig(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/config/export", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	disp := resp.Header.Get("Content-Disposition")
	if !strings.Contains(disp, "mcpmu-config.json") {
		t.Errorf("expected config filename in Content-Disposition, got %q", disp)
	}
}

// --- Checkbox parsing tests ---

func TestServerCreate_CheckboxEnabled(t *testing.T) {
	srv := newTestServer(t)

	// Simulate browser form: hidden "false" + checked "true" for enabled
	form := url.Values{
		"name":            {"checkbox-test"},
		"kind":            {"stdio"},
		"command":         {"echo"},
		"enabled":         {"false", "true"}, // hidden + checkbox when checked
		"autostart":       {"false", "true"}, // hidden + checkbox when checked
		"startup_timeout": {"10"},
		"tool_timeout":    {"60"},
		"auth_mode":       {"none"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 303, got %d: %s", resp.StatusCode, body)
	}

	created, ok := srv.cfg.GetServer("checkbox-test")
	if !ok {
		t.Fatal("server not found")
	}
	if !created.IsEnabled() {
		t.Error("server should be enabled (checkbox was checked)")
	}
	if !created.Autostart {
		t.Error("autostart should be true (checkbox was checked)")
	}
}

func TestServerCreate_CheckboxUnchecked(t *testing.T) {
	srv := newTestServer(t)

	// Simulate browser form: hidden "false" only (checkbox unchecked)
	form := url.Values{
		"name":            {"unchecked-test"},
		"kind":            {"stdio"},
		"command":         {"echo"},
		"enabled":         {"false"}, // hidden only, checkbox unchecked
		"autostart":       {"false"}, // hidden only, checkbox unchecked
		"startup_timeout": {"10"},
		"tool_timeout":    {"60"},
		"auth_mode":       {"none"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/servers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 303, got %d: %s", resp.StatusCode, body)
	}

	created, ok := srv.cfg.GetServer("unchecked-test")
	if !ok {
		t.Fatal("server not found")
	}
	if created.IsEnabled() {
		t.Error("server should be disabled (checkbox was unchecked)")
	}
	if created.Autostart {
		t.Error("autostart should be false (checkbox was unchecked)")
	}
}

// --- Namespace API mutation tests ---

func TestAPICreateNamespace(t *testing.T) {
	srv := newTestServer(t)

	body := `{"name":"staging","config":{"description":"Staging env","serverIds":[]}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/namespaces", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, respBody)
	}

	ns, ok := srv.cfg.GetNamespace("staging")
	if !ok {
		t.Fatal("staging namespace not found")
	}
	if ns.Description != "Staging env" {
		t.Errorf("expected description 'Staging env', got %q", ns.Description)
	}
}

func TestAPIUpdateNamespace(t *testing.T) {
	srv := newTestServer(t)

	body := `{"description":"Updated desc","denyByDefault":true}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/namespaces/default", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	ns, _ := srv.cfg.GetNamespace("default")
	if ns.Description != "Updated desc" {
		t.Errorf("expected description 'Updated desc', got %q", ns.Description)
	}
	if !ns.DenyByDefault {
		t.Error("denyByDefault should be true")
	}
}

func TestAPIDeleteNamespace(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/namespaces/default", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	if _, ok := srv.cfg.GetNamespace("default"); ok {
		t.Error("default namespace should be deleted")
	}
}

// --- Config import tests ---

func TestAPIImportPreview(t *testing.T) {
	srv := newTestServer(t)

	// Import a config with one new server, one changed server, and the http server removed
	incoming := config.NewConfig()
	_ = incoming.AddServer("test-stdio", config.ServerConfig{Command: "cat"})
	_ = incoming.AddServer("new-server", config.ServerConfig{Command: "echo"})

	body, _ := json.Marshal(incoming)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/config/import", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	var preview importPreview
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}

	if len(preview.Servers.Added) != 1 || preview.Servers.Added[0] != "new-server" {
		t.Errorf("expected 1 added server 'new-server', got %v", preview.Servers.Added)
	}
	if len(preview.Servers.Removed) != 1 || preview.Servers.Removed[0] != "test-http" {
		t.Errorf("expected 1 removed server 'test-http', got %v", preview.Servers.Removed)
	}
	if len(preview.Servers.Changed) != 1 || preview.Servers.Changed[0] != "test-stdio" {
		t.Errorf("expected 1 changed server 'test-stdio', got %v", preview.Servers.Changed)
	}
}

func TestAPIImportApply_PreservesRootFields(t *testing.T) {
	srv := newTestServer(t)

	// Set a root field that should survive import
	srv.cfg.MCPOAuthCredentialStore = "keyring"
	if err := config.SaveTo(srv.cfg, srv.configPath); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Import a config that also has root fields set
	incoming := config.NewConfig()
	incoming.MCPOAuthCredentialStore = "file"
	callbackPort := 9999
	incoming.MCPOAuthCallbackPort = &callbackPort
	_ = incoming.AddServer("imported-server", config.ServerConfig{Command: "echo"})

	body, _ := json.Marshal(incoming)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/config/import/apply", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	// The imported config's root fields should be present
	if srv.cfg.MCPOAuthCredentialStore != "file" {
		t.Errorf("expected MCPOAuthCredentialStore 'file', got %q", srv.cfg.MCPOAuthCredentialStore)
	}
	if srv.cfg.MCPOAuthCallbackPort == nil || *srv.cfg.MCPOAuthCallbackPort != 9999 {
		t.Error("MCPOAuthCallbackPort should be 9999 from imported config")
	}

	// Servers should be replaced
	if _, ok := srv.cfg.GetServer("imported-server"); !ok {
		t.Error("imported-server should exist")
	}
	if _, ok := srv.cfg.GetServer("test-stdio"); ok {
		t.Error("test-stdio should be gone after import")
	}
}

// --- Partial update API tests ---

func TestAPIUpdateServer_PartialDoesNotDropFields(t *testing.T) {
	srv := newTestServer(t)

	// First, verify test-stdio has autostart=false and is enabled
	original, _ := srv.cfg.GetServer("test-stdio")
	if !original.IsEnabled() {
		t.Fatal("precondition: server should be enabled")
	}

	// Disable via partial update — should not affect other fields
	body := `{"enabled":false}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/servers/test-stdio", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	updated, _ := srv.cfg.GetServer("test-stdio")
	if updated.IsEnabled() {
		t.Error("server should be disabled")
	}
	if updated.Command != "echo" {
		t.Errorf("command should still be 'echo', got %q", updated.Command)
	}
	if len(updated.Args) != 1 || updated.Args[0] != "hello" {
		t.Errorf("args should still be ['hello'], got %v", updated.Args)
	}
}

func TestAPIUpdateServer_CombinedPartialUpdate(t *testing.T) {
	srv := newTestServer(t)

	// Set both enabled and deniedTools in one request
	body := `{"enabled":false,"deniedTools":["tool-a","tool-b"]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/servers/test-stdio", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, respBody)
	}

	updated, _ := srv.cfg.GetServer("test-stdio")
	if updated.IsEnabled() {
		t.Error("server should be disabled")
	}
	if len(updated.DeniedTools) != 2 {
		t.Errorf("expected 2 denied tools, got %v", updated.DeniedTools)
	}
	if updated.Command != "echo" {
		t.Errorf("command should be preserved, got %q", updated.Command)
	}
}

// --- Config import page tests ---

func TestConfigImportPage(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/config/import", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "Import Configuration") {
		t.Error("missing Import Configuration heading")
	}
	if !strings.Contains(html, "config-file") {
		t.Error("missing file input")
	}
}

func TestLayoutHasExportImportLinks(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "/api/config/export") {
		t.Error("missing export link in layout")
	}
	if !strings.Contains(html, "/config/import") {
		t.Error("missing import link in layout")
	}
}

// --- Phase 4: Registry browser tests ---

// registryFixture is a minimal registry API response for tests.
const registryFixture = `{
  "servers": [
    {
      "server": {
        "name": "io.github.example/test-mcp-server",
        "title": "Test MCP Server",
        "description": "A test MCP server for unit tests.",
        "version": "1.0.0",
        "repository": { "url": "https://github.com/example/test-mcp-server", "source": "github" },
        "packages": [
          {
            "registryType": "npm",
            "identifier": "@example/test-mcp-server",
            "version": "1.0.0",
            "runtimeHint": "npx",
            "transport": { "type": "stdio" },
            "environmentVariables": [
              { "name": "TEST_API_KEY", "description": "Test API key", "isRequired": true, "isSecret": true }
            ]
          }
        ]
      },
      "_meta": {}
    }
  ],
  "metadata": { "count": 1 }
}`

// newTestServerWithRegistry creates a test web server with a mock registry backend.
func newTestServerWithRegistry(t *testing.T, fixture string) *Server {
	t.Helper()

	mockRegistry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(mockRegistry.Close)

	srv := newTestServer(t)
	srv.registry = registry.NewClientWithBase(mockRegistry.URL)
	return srv
}

func TestRegistryPage_EmptySearch(t *testing.T) {
	srv := newTestServerWithRegistry(t, registryFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/registry", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "MCP Registry") {
		t.Error("missing Registry heading")
	}
	if !strings.Contains(html, "Search the MCP Registry") {
		t.Error("missing empty state message")
	}
	// Should not have results without a query
	if strings.Contains(html, "reg-item") {
		t.Error("should not show results without a query")
	}
}

func TestRegistryPage_WithQuery(t *testing.T) {
	srv := newTestServerWithRegistry(t, registryFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/registry?q=test", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "io.github.example/test-mcp-server") {
		t.Error("missing server name in results")
	}
	if !strings.Contains(html, "A test MCP server") {
		t.Error("missing server description")
	}
	if !strings.Contains(html, "npm") {
		t.Error("missing registry type badge")
	}
	if !strings.Contains(html, "stdio") {
		t.Error("missing transport badge")
	}
	if !strings.Contains(html, "TEST_API_KEY") {
		t.Error("missing env var in results")
	}
	if !strings.Contains(html, "/servers/add?") {
		t.Error("missing install URL")
	}
}

func TestRegistryPage_NoResults(t *testing.T) {
	emptyFixture := `{"servers": [], "metadata": {"count": 0}}`
	srv := newTestServerWithRegistry(t, emptyFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/registry?q=nonexistent", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "No results found") {
		t.Error("missing no-results message")
	}
}

func TestFragmentRegistryResults(t *testing.T) {
	srv := newTestServerWithRegistry(t, registryFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fragments/registry/results?q=test", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Fragment should NOT contain full layout
	if strings.Contains(html, "<html") {
		t.Error("fragment should not contain full HTML layout")
	}
	if !strings.Contains(html, "io.github.example/test-mcp-server") {
		t.Error("fragment should contain server name")
	}
	if !strings.Contains(html, "Install") {
		t.Error("fragment should contain Install button")
	}
}

func TestFragmentRegistryResults_EmptyQuery(t *testing.T) {
	srv := newTestServerWithRegistry(t, registryFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fragments/registry/results", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, "Search the MCP Registry") {
		t.Error("should show empty state for no query")
	}
}

func TestAPIRegistrySearch(t *testing.T) {
	srv := newTestServerWithRegistry(t, registryFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/registry/search?q=test", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %q", ct)
	}

	var results []json.RawMessage
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &results); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}

	// Verify the result contains an install spec
	if !strings.Contains(string(body), "installSpec") {
		t.Error("result should contain installSpec")
	}
	if !strings.Contains(string(body), "npx") {
		t.Error("install spec should reference npx command")
	}
}

func TestAPIRegistrySearch_NoQuery(t *testing.T) {
	srv := newTestServerWithRegistry(t, registryFixture)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/registry/search", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestServerAddPage_RegistryPrepopulate(t *testing.T) {
	srv := newTestServer(t)

	params := url.Values{
		"from":    {"registry"},
		"name":    {"brave-search"},
		"kind":    {"stdio"},
		"command": {"npx"},
		"args":    {"-y @brave/brave-search-mcp-server"},
		"env":     {"BRAVE_API_KEY=<your-BRAVE_API_KEY>"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/add?"+params.Encode(), nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	// Verify form is pre-populated
	if !strings.Contains(html, `value="brave-search"`) {
		t.Error("name field not pre-populated")
	}
	if !strings.Contains(html, `value="npx"`) {
		t.Error("command field not pre-populated")
	}
	if !strings.Contains(html, `value="-y @brave/brave-search-mcp-server"`) {
		t.Error("args field not pre-populated")
	}
	if !strings.Contains(html, "BRAVE_API_KEY") {
		t.Error("env var key not pre-populated")
	}
}

func TestServerAddPage_RegistryPrepopulate_HTTP(t *testing.T) {
	srv := newTestServer(t)

	params := url.Values{
		"from":       {"registry"},
		"name":       {"my-remote"},
		"kind":       {"http"},
		"url":        {"https://example.com/mcp"},
		"bearer_env": {"MY_TOKEN"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers/add?"+params.Encode(), nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, `value="my-remote"`) {
		t.Error("name field not pre-populated")
	}
	if !strings.Contains(html, `value="https://example.com/mcp"`) {
		t.Error("url field not pre-populated")
	}
	if !strings.Contains(html, `value="MY_TOKEN"`) {
		t.Error("bearer_env field not pre-populated")
	}
	// HTTP mode should set kind to http
	if !strings.Contains(html, `value="http"`) {
		t.Error("kind field should be http")
	}
}

func TestRegistryNavLink(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	if !strings.Contains(html, `href="/registry"`) {
		t.Error("missing Registry nav link")
	}
}
