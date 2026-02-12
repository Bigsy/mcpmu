package process_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/oauth"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

func TestSupervisor_HTTPOAuthRefreshesTokenDuringRuntime(t *testing.T) {
	testutil.SetupTestHome(t)

	const (
		initialAccessToken = "token-v1"
		refreshedToken     = "token-v2"
		refreshToken       = "refresh-v1"
	)

	var refreshCalls atomic.Int32

	var fakeHTTP *httptest.Server
	fakeHTTP = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server/mcp":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 fakeHTTP.URL,
				"authorization_endpoint": fakeHTTP.URL + "/authorize",
				"token_endpoint":         fakeHTTP.URL + "/token",
			})
			return

		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if got := r.Form.Get("grant_type"); got != "refresh_token" {
				http.Error(w, "unexpected grant_type", http.StatusBadRequest)
				return
			}
			if got := r.Form.Get("refresh_token"); got != refreshToken {
				http.Error(w, "unexpected refresh_token", http.StatusBadRequest)
				return
			}
			refreshCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"refresh_token":%q}`,
				refreshedToken, refreshToken)
			return

		case "/mcp":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body failed", http.StatusInternalServerError)
				return
			}
			defer func() { _ = r.Body.Close() }()

			var req struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}

			auth := r.Header.Get("Authorization")
			switch req.Method {
			case "tools/call":
				if auth != "Bearer "+refreshedToken {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			case "initialize", "tools/list", "notifications/initialized":
				if auth != "Bearer "+initialAccessToken && auth != "Bearer "+refreshedToken {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			default:
				http.Error(w, "unexpected method", http.StatusBadRequest)
				return
			}

			switch req.Method {
			case "notifications/initialized":
				w.WriteHeader(http.StatusAccepted)
				return
			case "initialize":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"protocolVersion": "2025-11-25",
						"capabilities":    map[string]any{},
						"serverInfo": map[string]any{
							"name":    "fake",
							"version": "1.0.0",
						},
					},
				})
				return
			case "tools/list":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"tools": []map[string]any{
							{
								"name":        "echo",
								"description": "Echo",
								"inputSchema": map[string]any{"type": "object"},
							},
						},
					},
				})
				return
			case "tools/call":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"content": []map[string]any{
							{"type": "text", "text": "ok"},
						},
						"isError": false,
					},
				})
				return
			}
		}

		http.NotFound(w, r)
	}))
	defer fakeHTTP.Close()

	bus := events.NewBus()
	defer bus.Close()

	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: string(oauth.StoreModeFile),
		PIDTrackerDir:       t.TempDir(),
	})
	defer supervisor.StopAll()

	store := supervisor.CredentialStore()
	if store == nil {
		t.Fatal("expected credential store")
	}

	serverURL := fakeHTTP.URL + "/mcp"
	cred := &oauth.Credential{
		ServerName:   "oauth-http",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  initialAccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    time.Now().Add(5 * time.Minute).UnixMilli(),
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	handle, err := supervisor.Start(context.Background(), "oauth-http", config.ServerConfig{URL: serverURL})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if err := handle.WaitForTools(waitCtx); err != nil {
		t.Fatalf("WaitForTools: %v", err)
	}

	stored, err := store.Get(serverURL)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored == nil {
		t.Fatal("expected stored credential")
	}
	stored.ExpiresAt = time.Now().Add(-1 * time.Minute).UnixMilli()
	if err := store.Put(stored); err != nil {
		t.Fatalf("store.Put(expired): %v", err)
	}

	client := handle.Client()
	if client == nil {
		t.Fatal("expected initialized client")
	}

	callCtx, cancelCall := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCall()
	result, err := client.CallTool(callCtx, "echo", json.RawMessage(`{"msg":"hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected successful tool response")
	}

	if refreshCalls.Load() == 0 {
		t.Fatal("expected token endpoint to be called for refresh")
	}

	updated, err := store.Get(serverURL)
	if err != nil {
		t.Fatalf("store.Get(updated): %v", err)
	}
	if updated == nil {
		t.Fatal("expected refreshed credential in store")
	}
	if updated.AccessToken != refreshedToken {
		t.Fatalf("access token = %q, want %q", updated.AccessToken, refreshedToken)
	}
}
