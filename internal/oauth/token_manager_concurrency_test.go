package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// refreshTestServer stands in for an authorization server that rotates refresh
// tokens: each refresh_token is valid exactly once, and replaying a consumed
// one fails the way a real provider would.
type refreshTestServer struct {
	*httptest.Server

	tokenRequests atomic.Int64
	discoveries   atomic.Int64
	replays       atomic.Int64

	mu       sync.Mutex
	consumed map[string]bool
	issued   int
}

func newRefreshTestServer(t *testing.T, tokenDelay time.Duration) *refreshTestServer {
	t.Helper()
	rts := &refreshTestServer{consumed: map[string]bool{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server/mcp", func(w http.ResponseWriter, r *http.Request) {
		rts.discoveries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 "https://issuer.example",
			"authorization_endpoint": "https://auth.example/authorize",
			"token_endpoint":         rts.URL + "/token",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		rts.tokenRequests.Add(1)
		if tokenDelay > 0 {
			time.Sleep(tokenDelay)
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		presented := r.FormValue("refresh_token")

		rts.mu.Lock()
		if rts.consumed[presented] {
			rts.mu.Unlock()
			rts.replays.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		rts.consumed[presented] = true
		rts.issued++
		next := fmt.Sprintf("refresh-%d", rts.issued)
		access := fmt.Sprintf("access-%d", rts.issued)
		rts.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": access, "token_type": "Bearer",
			"expires_in": 3600, "refresh_token": next,
		})
	})

	rts.Server = httptest.NewServer(mux)
	t.Cleanup(rts.Close)
	return rts
}

func storeExpiredCredential(t *testing.T, store CredentialStore, serverURL string) {
	t.Helper()
	cred := &Credential{
		ServerName:   "test",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(),
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("store credential: %v", err)
	}
}

// TestTokenManager_ConcurrentGetAccessTokenIsRaceFree exercises the path that
// used to crash the process with "fatal error: concurrent map writes": one
// shared TokenManager, several servers, many goroutines all refreshing at once.
// Run under -race this also covers the metadata cache and warning handler.
func TestTokenManager_ConcurrentGetAccessTokenIsRaceFree(t *testing.T) {
	const servers = 4
	const callersPerServer = 16

	urls := make([]string, 0, servers)
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	for i := range servers {
		rts := newRefreshTestServer(t, 20*time.Millisecond)
		serverURL := rts.URL + "/mcp"
		urls = append(urls, serverURL)
		storeExpiredCredential(t, store, serverURL)
		_ = i
	}

	manager := NewTokenManager(store)
	manager.SetWarningHandler(func(string, error) {})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, servers*callersPerServer)
	for _, serverURL := range urls {
		for range callersPerServer {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				token, err := manager.GetAccessToken(ctx, url)
				if err != nil {
					errs <- fmt.Errorf("%s: %w", url, err)
					return
				}
				if token == "" || token == "expired-token" {
					errs <- fmt.Errorf("%s: got stale token %q", url, token)
				}
			}(serverURL)
		}
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent GetAccessToken: %v", err)
	}
}

// TestTokenManager_RefreshIsSingleFlighted asserts that concurrent callers for
// one server produce exactly one token-endpoint request. Without single-flight
// every caller replays the same refresh token, which a rotating provider treats
// as a replay attack and answers by revoking the grant.
func TestTokenManager_RefreshIsSingleFlighted(t *testing.T) {
	rts := newRefreshTestServer(t, 100*time.Millisecond)
	serverURL := rts.URL + "/mcp"

	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))
	storeExpiredCredential(t, store, serverURL)

	manager := NewTokenManager(store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const callers = 20
	tokens := make([]string, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			token, err := manager.GetAccessToken(ctx, serverURL)
			if err != nil {
				t.Errorf("caller %d: %v", idx, err)
				return
			}
			tokens[idx] = token
		}(i)
	}
	wg.Wait()

	if got := rts.tokenRequests.Load(); got != 1 {
		t.Errorf("token endpoint called %d times for %d concurrent callers, want 1", got, callers)
	}
	if got := rts.replays.Load(); got != 0 {
		t.Errorf("replayed a consumed refresh token %d times; a rotating provider would revoke the grant", got)
	}
	for i, token := range tokens {
		if token != "access-1" {
			t.Errorf("caller %d got token %q, want every caller to share the single refresh result", i, token)
		}
	}
}

// TestTokenManager_RefreshAfterRotationDoesNotReplay covers the sequential
// hand-off: a caller that read the credential before an earlier refresh
// committed must not go on to present the rotated-away refresh token.
func TestTokenManager_RefreshAfterRotationDoesNotReplay(t *testing.T) {
	rts := newRefreshTestServer(t, 0)
	serverURL := rts.URL + "/mcp"

	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))
	storeExpiredCredential(t, store, serverURL)

	manager := NewTokenManager(store)
	ctx := context.Background()

	// First refresh rotates refresh-token -> refresh-1 and stores access-1
	// with a one-hour lifetime.
	if _, err := manager.GetAccessToken(ctx, serverURL); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// A caller arriving now sees a healthy credential and must not refresh again.
	token, err := manager.GetAccessToken(ctx, serverURL)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if token != "access-1" {
		t.Errorf("token = %q, want the freshly stored access-1", token)
	}
	if got := rts.tokenRequests.Load(); got != 1 {
		t.Errorf("token endpoint called %d times, want 1 (second call should hit the fast path)", got)
	}
	if got := rts.replays.Load(); got != 0 {
		t.Errorf("replayed a consumed refresh token %d times", got)
	}
}

// TestTokenManager_MetadataCachedAcrossRefreshes pins the caching behaviour that
// moved behind a mutex, so a future change can't silently rediscover per call.
func TestTokenManager_MetadataCachedAcrossRefreshes(t *testing.T) {
	rts := newRefreshTestServer(t, 0)
	serverURL := rts.URL + "/mcp"

	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))
	storeExpiredCredential(t, store, serverURL)

	manager := NewTokenManager(store)
	ctx := context.Background()

	for i := range 3 {
		// Force each iteration back onto the refresh path.
		cred, err := store.Get(serverURL)
		if err != nil {
			t.Fatalf("get credential: %v", err)
		}
		cred.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
		if err := store.Put(cred); err != nil {
			t.Fatalf("expire credential: %v", err)
		}
		if _, err := manager.GetAccessToken(ctx, serverURL); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}

	if got := rts.tokenRequests.Load(); got != 3 {
		t.Errorf("token endpoint called %d times, want 3", got)
	}
	if got := rts.discoveries.Load(); got != 1 {
		t.Errorf("discovery ran %d times, want 1 (metadata should stay cached)", got)
	}
}

// TestTokenManager_WaiterHonoursOwnContext ensures a caller blocked on someone
// else's slow refresh still returns when its own deadline expires, rather than
// being pinned for the length of the leader's request.
func TestTokenManager_WaiterHonoursOwnContext(t *testing.T) {
	rts := newRefreshTestServer(t, 2*time.Second)
	serverURL := rts.URL + "/mcp"

	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))
	storeExpiredCredential(t, store, serverURL)

	manager := NewTokenManager(store)

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = manager.GetAccessToken(context.Background(), serverURL)
	}()

	// Let the leader claim the slot and get into its token request.
	time.Sleep(200 * time.Millisecond)

	waiterCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := manager.GetAccessToken(waiterCtx, serverURL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the waiter to fail on its own deadline")
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("waiter error = %v, want a deadline-exceeded error", err)
	}
	if elapsed > time.Second {
		t.Errorf("waiter blocked for %v; it should give up on its own deadline, not the leader's", elapsed)
	}

	<-leaderDone
}
