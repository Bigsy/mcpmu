package process_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/oauth"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

const needsLoginTestToken = "logged-in-token"

type needsLoginHTTPFixture struct {
	server             *httptest.Server
	unauthorizedInits  atomic.Int32
	authorizedInits    atomic.Int32
	badConfigHeader    atomic.Bool
	expectedConfigHead string
}

func newNeedsLoginHTTPFixture(t *testing.T, expectedConfigHeader string) *needsLoginHTTPFixture {
	t.Helper()

	fixture := &needsLoginHTTPFixture{expectedConfigHead: expectedConfigHeader}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeNeedsLoginJSON(t, w, map[string]any{
				"issuer":                 fixture.server.URL,
				"authorization_endpoint": fixture.server.URL + "/authorize",
				"token_endpoint":         fixture.server.URL + "/token",
			})
			return
		}

		if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		if r.Header.Get("Authorization") != "Bearer "+needsLoginTestToken {
			if req.Method == "initialize" {
				fixture.unauthorizedInits.Add(1)
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}

		if got := r.Header.Get("X-Config-Version"); got != fixture.expectedConfigHead {
			fixture.badConfigHeader.Store(true)
		}

		switch req.Method {
		case "initialize":
			fixture.authorizedInits.Add(1)
			writeNeedsLoginJSON(t, w, map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-11-25",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo": map[string]any{
						"name":    "needs-login-fixture",
						"version": "1.0.0",
					},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeNeedsLoginJSON(t, w, map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "echo",
						"description": "Echo",
						"inputSchema": map[string]any{"type": "object"},
					}},
				},
			})
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func writeNeedsLoginJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fixture response: %v", err)
	}
}

func newNeedsLoginSupervisor(t *testing.T) *process.Supervisor {
	t.Helper()
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	t.Cleanup(bus.Close)
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: string(oauth.StoreModeFile),
		PIDTrackerDir:       t.TempDir(),
	})
	t.Cleanup(supervisor.StopAll)
	return supervisor
}

func TestSupervisor_NeedsLoginCompletesReadinessPromptly(t *testing.T) {
	fixture := newNeedsLoginHTTPFixture(t, "unused")
	supervisor := newNeedsLoginSupervisor(t)

	handle, err := supervisor.Start(context.Background(), "oauth", config.ServerConfig{
		URL: fixture.server.URL + "/mcp",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	err = handle.WaitForTools(waitCtx)
	if !errors.Is(err, process.ErrNeedsLogin) {
		t.Fatalf("WaitForTools error = %v, want ErrNeedsLogin", err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("WaitForTools took %v; needs-login readiness did not fail promptly", elapsed)
	}
	if !handle.ToolsReady() {
		t.Fatal("needs-login handle readiness channel is still open")
	}
	if handle.IsRunning() {
		t.Fatal("needs-login handle must not report a running MCP connection")
	}
	if got := handle.AuthStatus(); got != mcp.AuthStatusOAuthNeeds {
		t.Fatalf("AuthStatus = %q, want %q", got, mcp.AuthStatusOAuthNeeds)
	}
	if got := fixture.unauthorizedInits.Load(); got != 1 {
		t.Fatalf("unauthorized initialize requests = %d, want 1", got)
	}
}

func TestSupervisor_ConcurrentUseAfterExternalLoginSharesRetry(t *testing.T) {
	const callers = 12

	fixture := newNeedsLoginHTTPFixture(t, "current")
	supervisor := newNeedsLoginSupervisor(t)
	serverURL := fixture.server.URL + "/mcp"

	failed, err := supervisor.Start(context.Background(), "oauth", config.ServerConfig{
		URL:         serverURL,
		HTTPHeaders: map[string]string{"X-Config-Version": "stale"},
	})
	if err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	if err := failed.WaitForTools(context.Background()); !errors.Is(err, process.ErrNeedsLogin) {
		t.Fatalf("initial WaitForTools error = %v, want ErrNeedsLogin", err)
	}

	// Use a separately constructed store to model `mcpmu mcp login` writing
	// credentials outside the long-lived supervisor process.
	externalStore, err := oauth.NewCredentialStore(oauth.StoreModeFile)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	if err := externalStore.Put(&oauth.Credential{
		ServerName:  "oauth",
		ServerURL:   serverURL,
		ClientID:    "external-cli",
		AccessToken: needsLoginTestToken,
		ExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("external credential Put: %v", err)
	}

	currentConfig := config.ServerConfig{
		URL:         serverURL,
		HTTPHeaders: map[string]string{"X-Config-Version": "current"},
	}
	start := make(chan struct{})
	handles := make(chan *process.Handle, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			handle, startErr := supervisor.Start(ctx, "oauth", currentConfig)
			if startErr == nil {
				startErr = handle.WaitForTools(ctx)
			}
			if startErr != nil {
				errs <- startErr
				return
			}
			handles <- handle
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	close(handles)

	for err := range errs {
		t.Errorf("concurrent retry: %v", err)
	}
	if t.Failed() {
		return
	}

	var winner *process.Handle
	count := 0
	for handle := range handles {
		count++
		if winner == nil {
			winner = handle
		} else if handle != winner {
			t.Errorf("concurrent retry returned multiple handles: %p and %p", winner, handle)
		}
	}
	if count != callers {
		t.Fatalf("successful callers = %d, want %d", count, callers)
	}
	if got := fixture.authorizedInits.Load(); got != 1 {
		t.Fatalf("authorized initialize requests = %d, want exactly 1", got)
	}
	if fixture.badConfigHeader.Load() {
		t.Fatal("retry used stale server configuration")
	}
	if winner == failed {
		t.Fatal("needs-login handle was not replaced after external login")
	}
	if got := len(winner.Tools()); got != 1 {
		t.Fatalf("discovered tools = %d, want 1", got)
	}

	if supervisor.Get("oauth") != winner {
		t.Fatalf("supervisor did not retain winning authenticated handle %p", winner)
	}
}
