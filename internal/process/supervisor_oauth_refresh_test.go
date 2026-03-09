package process_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
		fakeBaseURL        = "https://oauth-http.test"
	)

	var refreshCalls atomic.Int32

	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server/mcp":
			return jsonResponse(r, http.StatusOK, map[string]any{
				"issuer":                 fakeBaseURL,
				"authorization_endpoint": fakeBaseURL + "/authorize",
				"token_endpoint":         fakeBaseURL + "/token",
			})

		case "/token":
			if err := r.ParseForm(); err != nil {
				return textResponse(r, http.StatusBadRequest, "bad form"), nil
			}
			if got := r.Form.Get("grant_type"); got != "refresh_token" {
				return textResponse(r, http.StatusBadRequest, "unexpected grant_type"), nil
			}
			if got := r.Form.Get("refresh_token"); got != refreshToken {
				return textResponse(r, http.StatusBadRequest, "unexpected refresh_token"), nil
			}
			refreshCalls.Add(1)
			return jsonResponse(r, http.StatusOK, map[string]any{
				"access_token":  refreshedToken,
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": refreshToken,
			})

		case "/mcp":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return textResponse(r, http.StatusInternalServerError, "read body failed"), nil
			}
			defer func() { _ = r.Body.Close() }()

			var req struct {
				ID     any    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				return textResponse(r, http.StatusBadRequest, "invalid json"), nil
			}

			auth := r.Header.Get("Authorization")
			switch req.Method {
			case "tools/call":
				if auth != "Bearer "+refreshedToken {
					return textResponse(r, http.StatusUnauthorized, "unauthorized"), nil
				}
			case "initialize", "tools/list", "notifications/initialized":
				if auth != "Bearer "+initialAccessToken && auth != "Bearer "+refreshedToken {
					return textResponse(r, http.StatusUnauthorized, "unauthorized"), nil
				}
			default:
				return textResponse(r, http.StatusBadRequest, "unexpected method"), nil
			}

			switch req.Method {
			case "notifications/initialized":
				return textResponse(r, http.StatusAccepted, ""), nil
			case "initialize":
				return jsonResponse(r, http.StatusOK, map[string]any{
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
			case "tools/list":
				return jsonResponse(r, http.StatusOK, map[string]any{
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
			case "tools/call":
				return jsonResponse(r, http.StatusOK, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"content": []map[string]any{
							{"type": "text", "text": "ok"},
						},
						"isError": false,
					},
				})
			}
		}

		return textResponse(r, http.StatusNotFound, "not found"), nil
	})

	origDefaultTransport := http.DefaultTransport
	origDefaultClientTransport := http.DefaultClient.Transport
	http.DefaultTransport = transport
	http.DefaultClient.Transport = transport
	t.Cleanup(func() {
		http.DefaultTransport = origDefaultTransport
		http.DefaultClient.Transport = origDefaultClientTransport
	})

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

	serverURL := fakeBaseURL + "/mcp"
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(req *http.Request, status int, v any) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:    io.NopCloser(bytes.NewReader(body)),
		Request: req,
	}, nil
}

func textResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
