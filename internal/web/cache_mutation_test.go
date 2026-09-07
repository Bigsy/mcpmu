package web

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func seedWebToolCache(t *testing.T, s *Server, names ...string) {
	t.Helper()
	tc, err := config.NewToolCache(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	s.toolCache = tc
	for _, name := range names {
		if err := tc.Update(name, []config.CachedToolInput{{Name: "cached_tool"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertWebCache(t *testing.T, s *Server, expected map[string]bool) {
	t.Helper()
	disk, err := config.NewToolCache(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []*config.ToolCache{s.toolCache, disk} {
		for name, want := range expected {
			tools, ok := tc.Get(name)
			if ok != want || (ok && (len(tools) != 1 || tools[0].Name != "cached_tool")) {
				t.Errorf("cache %s = %v, %v; want present=%v", name, tools, ok, want)
			}
		}
	}
}

func TestAPIUpdateCacheMaintenance(t *testing.T) {
	for _, tt := range []struct {
		name, server, body, final string
		cached                    bool
		status                    int
	}{
		{"command", "test-stdio", `{"command":"other"}`, "test-stdio", false, 200},
		{"args", "test-stdio", `{"args":["other"]}`, "test-stdio", false, 200},
		{"url", "test-http", `{"url":"https://other.example/mcp"}`, "test-http", false, 200},
		{"enabled", "test-stdio", `{"enabled":false}`, "test-stdio", true, 200},
		{"autostart", "test-stdio", `{"autostart":true}`, "test-stdio", true, 200},
		{"rename", "test-stdio", `{"name":"renamed"}`, "renamed", true, 200},
		{"rename-command", "test-stdio", `{"name":"renamed","command":"other"}`, "renamed", false, 200},
		{"invalid", "test-stdio", `{"command":""}`, "test-stdio", true, 422},
		{"invalid-rename", "test-stdio", `{"command":"other","name":"test-http"}`, "test-stdio", true, 409},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t)
			seedWebToolCache(t, s, tt.server)
			before, err := os.ReadFile(s.configPath)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := s.configSnapshot()
			rec := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(rec, newRequest("PUT", "/api/servers/"+tt.server, strings.NewReader(tt.body)))
			if rec.Code != tt.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			assertWebCache(t, s, map[string]bool{tt.final: tt.cached})
			if tt.final != tt.server {
				assertWebCache(t, s, map[string]bool{tt.server: false})
			}
			if tt.status != 200 {
				after, err := os.ReadFile(s.configPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) || snapshot != s.configSnapshot() {
					t.Error("failed update changed config")
				}
			}
		})
	}
}

func TestAPIImportCacheMaintenance(t *testing.T) {
	for _, mode := range []string{"replace", "empty", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestServer(t)
			if err := s.mutateConfig(func(c *config.Config) error { return c.AddServer("removed", config.ServerConfig{Command: "echo"}) }); err != nil {
				t.Fatal(err)
			}
			seedWebToolCache(t, s, "test-stdio", "test-http", "removed")
			before, err := os.ReadFile(s.configPath)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := s.configSnapshot()
			var incoming config.Config
			if err := json.Unmarshal(before, &incoming); err != nil {
				t.Fatal(err)
			}
			delete(incoming.Servers, "removed")
			srv := incoming.Servers["test-stdio"]
			srv.Command = "other"
			incoming.Servers["test-stdio"] = srv
			incoming.Namespaces["normalized"] = config.NamespaceConfig{Compression: " MEDIUM "}
			incoming.Namespaces["unknown"] = config.NamespaceConfig{Compression: "future"}
			expected := map[string]bool{"test-stdio": false, "test-http": true, "removed": false}
			status := 200
			if mode == "empty" {
				incoming = config.Config{}
				expected["test-http"] = false
			}
			if mode == "invalid" {
				srv.Command = ""
				incoming.Servers["test-stdio"] = srv
				status = 422
				expected["test-stdio"] = true
				expected["removed"] = true
			}
			body, err := json.Marshal(incoming)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(rec, newRequest("POST", "/api/config/import/apply", bytes.NewReader(body)))
			if rec.Code != status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			assertWebCache(t, s, expected)
			cfg := s.configSnapshot()
			if mode == "invalid" {
				after, err := os.ReadFile(s.configPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) || snapshot != cfg {
					t.Error("invalid import changed config")
				}
			} else if mode == "empty" {
				if cfg.Servers == nil || cfg.Namespaces == nil {
					t.Error("nil imported maps")
				}
				if err := s.mutateConfig(func(c *config.Config) error { return c.AddServer("after", config.ServerConfig{Command: "echo"}) }); err != nil {
					t.Fatal(err)
				}
			} else if cfg.Namespaces["normalized"].Compression != "medium" || cfg.Namespaces["unknown"].Compression != "future" {
				t.Error("compression normalization changed")
			}
		})
	}
}
