package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestConfigErrorStatusWrapped(t *testing.T) {
	cause := errors.New("validation detail")
	for _, tt := range []struct {
		err    error
		status int
	}{
		{config.Errorf(config.ErrNotFound, "server missing"), 404},
		{config.Errorf(config.ErrAlreadyExists, "duplicate"), 409},
		{config.Errorf(config.ErrInvalidInput, "invalid: %w", cause), 422},
		{errors.New("file not found"), 500},
		{errors.New("directory already exists"), 500},
	} {
		wrapped := fmt.Errorf("transaction: %w", fmt.Errorf("operation: %w", tt.err))
		if got := configErrorStatus(wrapped); got != tt.status {
			t.Errorf("%v => %d, want %d", wrapped, got, tt.status)
		}
	}
	wrapped := fmt.Errorf("outer: %w", config.Errorf(config.ErrInvalidInput, "invalid: %w", cause))
	if !errors.Is(wrapped, cause) {
		t.Error("lost validation cause")
	}
}

func TestAPIMutationErrorStatuses(t *testing.T) {
	for _, tt := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"missing-server", "PUT", "/api/servers/missing", `{}`, 404},
		{"missing-namespace", "PUT", "/api/namespaces/missing", `{}`, 404},
		{"delete-missing-server", "DELETE", "/api/servers/missing", ``, 404},
		{"delete-missing-namespace", "DELETE", "/api/namespaces/missing", ``, 404},
		{"duplicate-server", "POST", "/api/servers", `{"name":"test-stdio","config":{"command":"echo"}}`, 409},
		{"duplicate-namespace", "POST", "/api/namespaces", `{"name":"default","config":{}}`, 409},
		{"rename-server-collision", "PUT", "/api/servers/test-stdio", `{"name":"test-http"}`, 409},
		{"rename-namespace-collision", "PUT", "/api/namespaces/default", `{"name":"other"}`, 409},
		{"invalid-server", "PUT", "/api/servers/test-stdio", `{"command":""}`, 422},
		{"invalid-namespace", "PUT", "/api/namespaces/default", `{"compression":"invalid"}`, 422},
		{"invalid-server-name", "POST", "/api/servers", `{"name":"a:b","config":{"command":"echo"}}`, 422},
		{"invalid-namespace-name", "POST", "/api/namespaces", `{"name":"a:b","config":{}}`, 422},
		{"missing-permission-server", "PUT", "/api/namespaces/default", `{"permissions":[{"server":"missing","toolName":"x","enabled":true}]}`, 404},
		{"empty-name", "POST", "/api/servers", `{"name":"","config":{"command":"echo"}}`, 422},
		{"malformed-json", "PUT", "/api/servers/test-stdio", `{`, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t)
			if err := s.mutateConfig(func(c *config.Config) error { return c.AddNamespace("other", config.NamespaceConfig{}) }); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(rec, newRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.status, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
				t.Fatalf("error body: %s", rec.Body.String())
			}
		})
	}
}

func TestAPIStorageFailureIs500(t *testing.T) {
	for _, tt := range []struct{ method, path, body string }{
		{"POST", "/api/servers", `{"name":"new","config":{"command":"echo"}}`},
		{"PUT", "/api/servers/test-stdio", `{"enabled":false}`},
		{"DELETE", "/api/servers/test-stdio", ``},
		{"POST", "/api/namespaces", `{"name":"new","config":{}}`},
		{"PUT", "/api/namespaces/default", `{"description":"new"}`},
		{"DELETE", "/api/namespaces/default", ``},
		{"POST", "/api/config/import/apply", `{"servers":{}}`},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			s := newTestServer(t)
			// A regular file in the parent path forces an I/O failure even as root.
			blocker := filepath.Join(t.TempDir(), "already exists not found")
			if err := os.WriteFile(blocker, []byte("blocker"), 0600); err != nil {
				t.Fatal(err)
			}
			s.configPath = filepath.Join(blocker, "config.json")
			rec := httptest.NewRecorder()
			s.httpServer.Handler.ServeHTTP(rec, newRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != 500 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
