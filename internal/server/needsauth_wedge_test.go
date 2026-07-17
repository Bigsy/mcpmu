package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/oauth"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

func TestNeedsAuthToolResourceAndPromptPathsDoNotWedge(t *testing.T) {
	testutil.SetupTestHome(t)

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 upstream.URL,
				"authorization_endpoint": upstream.URL + "/authorize",
				"token_endpoint":         upstream.URL + "/token",
			})
			return
		}

		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "login required", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		SchemaVersion:           1,
		MCPOAuthCredentialStore: string(oauth.StoreModeFile),
		Servers: map[string]config.ServerConfig{
			"oauth": {URL: upstream.URL + "/mcp"},
		},
	}
	s, err := New(Options{
		Config:        cfg,
		PIDTrackerDir: t.TempDir(),
		Stdin:         strings.NewReader(""),
		Stdout:        &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.supervisor.StopAll()

	s.initialized = true
	s.activeServerNames = []string{"oauth"}

	assertPrompt := func(label string, call func(context.Context) *RPCError) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		started := time.Now()
		rpcErr := call(ctx)
		if rpcErr == nil {
			t.Fatalf("%s unexpectedly succeeded", label)
		}
		if !strings.Contains(rpcErr.Message, "oauth login required") {
			t.Fatalf("%s error = %q, want oauth login required", label, rpcErr.Message)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("%s took %v; needs-auth path wedged", label, elapsed)
		}
	}

	assertPrompt("tools/call", func(ctx context.Context) *RPCError {
		_, rpcErr := s.router.CallTool(ctx, "oauth.echo", json.RawMessage(`{}`))
		return rpcErr
	})

	assertListCompletes := func(label string, call func(context.Context) (any, *RPCError)) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		started := time.Now()
		_, rpcErr := call(ctx)
		if rpcErr != nil {
			t.Fatalf("%s returned RPC error: %v", label, rpcErr)
		}
		if elapsed := time.Since(started); elapsed >= 2*time.Second {
			t.Fatalf("%s took %v; needs-auth path wedged", label, elapsed)
		}
	}
	assertListCompletes("resources/list", s.handleResourcesList)
	assertListCompletes("prompts/list", s.handlePromptsList)
}
