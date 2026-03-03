package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearch_ValidResults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0.1/servers" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("search"); q != "brave" {
			t.Errorf("unexpected search query: %s", q)
		}
		if v := r.URL.Query().Get("version"); v != "latest" {
			t.Errorf("unexpected version: %s", v)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(braveFixture))
	}))
	defer ts.Close()

	client := NewClientWithBase(ts.URL)
	servers, err := client.Search(context.Background(), "brave")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}

	srv := servers[0]
	if srv.Name != "io.github.brave/brave-search-mcp-server" {
		t.Errorf("unexpected name: %s", srv.Name)
	}
	if srv.Title != "Brave Search MCP Server" {
		t.Errorf("unexpected title: %s", srv.Title)
	}
	if srv.Version != "2.0.75" {
		t.Errorf("unexpected version: %s", srv.Version)
	}
	if len(srv.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(srv.Packages))
	}

	pkg := srv.Packages[0]
	if pkg.RegistryType != "npm" {
		t.Errorf("unexpected registry type: %s", pkg.RegistryType)
	}
	if pkg.Identifier != "@brave/brave-search-mcp-server" {
		t.Errorf("unexpected identifier: %s", pkg.Identifier)
	}
	if pkg.RuntimeHint != "npx" {
		t.Errorf("unexpected runtime hint: %s", pkg.RuntimeHint)
	}
	if pkg.Transport.Type != "stdio" {
		t.Errorf("unexpected transport: %s", pkg.Transport.Type)
	}
	if len(pkg.EnvironmentVariables) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(pkg.EnvironmentVariables))
	}
	if pkg.EnvironmentVariables[0].Name != "BRAVE_API_KEY" {
		t.Errorf("unexpected env var name: %s", pkg.EnvironmentVariables[0].Name)
	}
	if !pkg.EnvironmentVariables[0].IsRequired {
		t.Error("expected env var to be required")
	}
	if !pkg.EnvironmentVariables[0].IsSecret {
		t.Error("expected env var to be secret")
	}
}

func TestSearch_EmptyResults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers": [], "metadata": {"count": 0}}`))
	}))
	defer ts.Close()

	client := NewClientWithBase(ts.URL)
	servers, err := client.Search(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

func TestSearch_Non200Status(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewClientWithBase(ts.URL)
	_, err := client.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "registry returned status 500" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestSearch_MalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer ts.Close()

	client := NewClientWithBase(ts.URL)
	_, err := client.Search(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "invalid registry response") {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestSearch_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // block until caller gives up
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewClientWithBase(ts.URL)
	// Use only context timeout (not HTTP client timeout) so the context.Err()
	// branch in client.go fires deterministically → "registry search timed out".
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Search(ctx, "test")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "registry search timed out" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestSearch_FullFixtureRoundTrip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fullFixture))
	}))
	defer ts.Close()

	client := NewClientWithBase(ts.URL)
	servers, err := client.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}

	// First server: has packages
	srv0 := servers[0]
	if srv0.Name != "io.github.brave/brave-search-mcp-server" {
		t.Errorf("srv0 name: %s", srv0.Name)
	}
	if len(srv0.Packages) != 1 {
		t.Errorf("srv0 packages: %d", len(srv0.Packages))
	}
	if len(srv0.Packages[0].PackageArguments) != 0 {
		t.Errorf("srv0 package args: %d", len(srv0.Packages[0].PackageArguments))
	}

	// Second server: has remotes and package args
	srv1 := servers[1]
	if srv1.Name != "io.github.bytedance/mcp-server-filesystem" {
		t.Errorf("srv1 name: %s", srv1.Name)
	}
	if len(srv1.Packages) != 1 {
		t.Errorf("srv1 packages: %d", len(srv1.Packages))
	}
	if len(srv1.Packages[0].PackageArguments) != 1 {
		t.Fatalf("srv1 package args: %d", len(srv1.Packages[0].PackageArguments))
	}
	arg := srv1.Packages[0].PackageArguments[0]
	if arg.Name != "allowed-directories" {
		t.Errorf("arg name: %s", arg.Name)
	}
	if arg.Type != "named" {
		t.Errorf("arg type: %s", arg.Type)
	}
	if !arg.IsRequired {
		t.Error("expected arg to be required")
	}

	if len(srv1.Remotes) != 1 {
		t.Fatalf("srv1 remotes: %d", len(srv1.Remotes))
	}
	remote := srv1.Remotes[0]
	if remote.Type != "streamable-http" {
		t.Errorf("remote type: %s", remote.Type)
	}
	if len(remote.Headers) != 1 {
		t.Errorf("remote headers: %d", len(remote.Headers))
	}
}

// braveFixture is a realistic single-server response from the registry.
const braveFixture = `{
  "servers": [
    {
      "server": {
        "name": "io.github.brave/brave-search-mcp-server",
        "title": "Brave Search MCP Server",
        "description": "Brave Search MCP Server: web results, images, videos, rich results, AI summaries, and more.",
        "version": "2.0.75",
        "repository": {
          "url": "https://github.com/brave/brave-search-mcp-server",
          "source": "github"
        },
        "packages": [
          {
            "registryType": "npm",
            "registryBaseUrl": "https://registry.npmjs.org",
            "identifier": "@brave/brave-search-mcp-server",
            "version": "2.0.75",
            "runtimeHint": "npx",
            "transport": { "type": "stdio" },
            "environmentVariables": [
              {
                "name": "BRAVE_API_KEY",
                "description": "Your API key for the service",
                "isRequired": true,
                "isSecret": true,
                "format": "string"
              }
            ]
          }
        ]
      },
      "_meta": {
        "io.modelcontextprotocol.registry/official": {
          "status": "active",
          "publishedAt": "2025-12-17T21:32:51.730221Z",
          "updatedAt": "2025-12-17T21:32:51.730221Z",
          "isLatest": true
        }
      }
    }
  ],
  "metadata": { "count": 1 }
}`

// fullFixture tests multi-server response with packages, remotes, and package arguments.
const fullFixture = `{
  "servers": [
    {
      "server": {
        "name": "io.github.brave/brave-search-mcp-server",
        "title": "Brave Search MCP Server",
        "description": "Web search via Brave.",
        "version": "2.0.75",
        "repository": { "url": "https://github.com/brave/brave-search-mcp-server", "source": "github" },
        "packages": [
          {
            "registryType": "npm",
            "identifier": "@brave/brave-search-mcp-server",
            "version": "2.0.75",
            "runtimeHint": "npx",
            "transport": { "type": "stdio" },
            "environmentVariables": [
              { "name": "BRAVE_API_KEY", "description": "API key", "isRequired": true, "isSecret": true }
            ]
          }
        ]
      },
      "_meta": {}
    },
    {
      "server": {
        "name": "io.github.bytedance/mcp-server-filesystem",
        "title": "Filesystem MCP Server",
        "description": "MCP server for filesystem access.",
        "version": "1.0.0",
        "repository": { "url": "https://github.com/bytedance/mcp-server-filesystem", "source": "github" },
        "packages": [
          {
            "registryType": "npm",
            "identifier": "@agent-infra/mcp-server-filesystem",
            "version": "1.0.0",
            "runtimeHint": "npx",
            "transport": { "type": "stdio" },
            "packageArguments": [
              {
                "name": "allowed-directories",
                "description": "Comma-separated list of allowed directories",
                "isRequired": true,
                "format": "string",
                "type": "named"
              }
            ]
          }
        ],
        "remotes": [
          {
            "type": "streamable-http",
            "url": "https://server.smithery.ai/filesystem",
            "headers": [
              {
                "name": "Authorization",
                "description": "Bearer token",
                "value": "Bearer {smithery_api_key}",
                "isRequired": true,
                "isSecret": true
              }
            ]
          }
        ]
      },
      "_meta": {}
    }
  ],
  "metadata": { "count": 2 }
}`
