package process_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// TestHandleFieldsRaceDuringOAuthDiscovery guards the HTTP handle fields that
// startHTTP mutates after the handle is already reachable.
//
// startHTTP publishes the handle into s.handles and only then runs the
// handshake. On the OAuth-401 path it goes on to overwrite authStatus,
// oauthMeta, authChallenge, client and httpTransport. Meanwhile any caller
// holding the handle can read them: internal/web/pages.go reads AuthStatus() on
// a request goroutine, and internal/server/core.go and aggregator.go read
// Client(). Both are live while a different request is starting this server.
//
// Before the fix, those fields were plain struct fields written and read with no
// synchronization; this test fails under -race with a DATA RACE between
// startHTTP and Handle.AuthStatus/Client.
func TestHandleFieldsRaceDuringOAuthDiscovery(t *testing.T) {
	// The fixture answers 401 on the MCP endpoint and serves valid
	// authorization-server metadata on GET, so startHTTP reaches the OAuth
	// branch that does the mutating. The sleep widens the publish→mutate window
	// so the reader reliably lands inside it.
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
			}); err != nil {
				t.Errorf("encode metadata: %v", err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "login required", http.StatusUnauthorized)
	}))
	defer server.Close()

	supervisor := newNeedsLoginSupervisor(t)

	const serverName = "oauth-race"
	stop := make(chan struct{})
	var readers sync.WaitGroup

	// Readers poll the supervisor for the handle the way a concurrent web or
	// serve-mode request would, then hammer the accessors.
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if h := supervisor.Get(serverName); h != nil {
					_ = h.Client()
					_ = h.AuthStatus()
					_ = h.Capabilities()
				}
			}
		})
	}

	_, err := supervisor.Start(context.Background(), serverName, config.ServerConfig{
		URL: server.URL + "/mcp",
	})
	close(stop)
	readers.Wait()

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
}
