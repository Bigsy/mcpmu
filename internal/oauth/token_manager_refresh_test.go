package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// tokenOnlyServer is an authorization server that serves /token and nothing
// else: no RFC 8414 metadata anywhere. It records the last refresh request's
// form so tests can assert on what was sent.
type tokenOnlyServer struct {
	*httptest.Server
	tokenRequests atomic.Int64
	lastForm      atomic.Pointer[map[string][]string]
	respond       func(w http.ResponseWriter)
	delay         time.Duration
}

func newTokenOnlyServer(t *testing.T) *tokenOnlyServer {
	t.Helper()
	tos := &tokenOnlyServer{}
	tos.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "next",
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tos.tokenRequests.Add(1)
		if tos.delay > 0 {
			time.Sleep(tos.delay)
		}
		_ = r.ParseForm()
		form := map[string][]string(r.PostForm)
		tos.lastForm.Store(&form)
		tos.respond(w)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	tos.Server = httptest.NewServer(mux)
	t.Cleanup(tos.Close)
	return tos
}

func (s *tokenOnlyServer) formValue(key string) string {
	form := s.lastForm.Load()
	if form == nil || len((*form)[key]) == 0 {
		return ""
	}
	return (*form)[key][0]
}

func newTestStore(t *testing.T) CredentialStore {
	t.Helper()
	return NewFileStoreAt(filepath.Join(t.TempDir(), "creds.json"))
}

func mustGet(t *testing.T, store CredentialStore, serverURL string) *Credential {
	t.Helper()
	cred, err := store.Get(serverURL)
	if err != nil || cred == nil {
		t.Fatalf("get credential: cred=%v err=%v", cred, err)
	}
	return cred
}

// Bullet: refresh runs detached from the leader's ctx. Cancelling the caller
// that started the refresh must not fail the other waiters, and the refreshed
// token must still be stored.
func TestTokenManager_RefreshSurvivesLeaderCancellation(t *testing.T) {
	rts := newRefreshTestServer(t, 300*time.Millisecond)
	serverURL := rts.URL + "/mcp"
	store := newTestStore(t)
	storeExpiredCredential(t, store, serverURL)
	manager := NewTokenManager(store)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := manager.GetAccessToken(leaderCtx, serverURL)
		leaderErr <- err
	}()
	time.Sleep(100 * time.Millisecond) // leader is inside the token request

	waiterCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waiterResult := make(chan error, 1)
	waiterToken := make(chan string, 1)
	go func() {
		tok, err := manager.GetAccessToken(waiterCtx, serverURL)
		waiterToken <- tok
		waiterResult <- err
	}()
	time.Sleep(50 * time.Millisecond) // waiter has joined the in-flight call

	cancelLeader()

	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Errorf("leader error = %v, want context.Canceled", err)
	}
	if err := <-waiterResult; err != nil {
		t.Fatalf("waiter failed even though only the leader was cancelled: %v", err)
	}
	if tok := <-waiterToken; tok != "access-1" {
		t.Errorf("waiter token = %q, want access-1", tok)
	}
	if got := rts.tokenRequests.Load(); got != 1 {
		t.Errorf("token requests = %d, want exactly 1 (single flight)", got)
	}
	if stored := mustGet(t, store, serverURL); stored.AccessToken != "access-1" {
		t.Errorf("stored token = %q, want the refreshed one", stored.AccessToken)
	}
}

// Bullet: challenge-only (RFC 9728) servers refresh. The credential carries the
// token endpoint from login, so no metadata discovery is needed at all.
func TestTokenManager_RefreshUsesStoredTokenEndpoint(t *testing.T) {
	tos := newTokenOnlyServer(t)
	serverURL := tos.URL + "/mcp" // no .well-known anywhere on this host
	store := newTestStore(t)
	cred := &Credential{
		ServerName: "test", ServerURL: serverURL, ClientID: "client-123",
		AccessToken: "expired", RefreshToken: "refresh-token",
		ExpiresAt:     time.Now().Add(-time.Hour).UnixMilli(),
		TokenEndpoint: tos.URL + "/token",
	}
	if err := store.Put(cred); err != nil {
		t.Fatal(err)
	}
	manager := NewTokenManager(store)

	tok, err := manager.GetAccessToken(context.Background(), serverURL)
	if err != nil {
		t.Fatalf("refresh with stored endpoint failed: %v", err)
	}
	if tok != "fresh" {
		t.Errorf("token = %q, want fresh", tok)
	}
}

// Bullet: challenge-only servers refresh — for a credential stored before the
// endpoint was recorded, the manager falls back to challenge discovery and
// backfills TokenEndpoint on success.
func TestTokenManager_RefreshDiscoversViaChallengeAndBackfillsEndpoint(t *testing.T) {
	tos := newTokenOnlyServer(t)

	// The MCP server: no RFC 8414 metadata, 401 + resource_metadata challenge on POST.
	var mcp *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource": mcp.URL + "/mcp", "authorization_servers": []string{mcp.URL + "/as"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/as", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": mcp.URL + "/as", "authorization_endpoint": mcp.URL + "/as/authorize",
			"token_endpoint": tos.URL + "/token",
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+mcp.URL+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mcp = httptest.NewServer(mux)
	defer mcp.Close()
	serverURL := mcp.URL + "/mcp"

	store := newTestStore(t)
	storeExpiredCredential(t, store, serverURL) // no TokenEndpoint
	manager := NewTokenManager(store)

	tok, err := manager.GetAccessToken(context.Background(), serverURL)
	if err != nil {
		t.Fatalf("challenge-based refresh failed: %v", err)
	}
	if tok != "fresh" {
		t.Errorf("token = %q, want fresh", tok)
	}
	if stored := mustGet(t, store, serverURL); stored.TokenEndpoint != tos.URL+"/token" {
		t.Errorf("TokenEndpoint not backfilled: %q", stored.TokenEndpoint)
	}
}

// Bullet: missing expires_in must not produce an instantly-expired token.
func TestTokenManager_MissingExpiresInDefaultsLifetime(t *testing.T) {
	tos := newTokenOnlyServer(t)
	tos.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "token_type": "Bearer"})
	}
	serverURL := tos.URL + "/mcp"
	store := newTestStore(t)
	storeExpiredCredential(t, store, serverURL)
	cred := mustGet(t, store, serverURL)
	cred.TokenEndpoint = tos.URL + "/token"
	_ = store.Put(cred)
	manager := NewTokenManager(store)

	before := time.Now()
	if _, err := manager.GetAccessToken(context.Background(), serverURL); err != nil {
		t.Fatal(err)
	}
	stored := mustGet(t, store, serverURL)
	if stored.NeedsRefresh() {
		t.Fatalf("token with no expires_in is already due for refresh (ExpiresAt=%d)", stored.ExpiresAt)
	}
	got := time.UnixMilli(stored.ExpiresAt).Sub(before)
	if got < DefaultTokenLifetime-time.Minute || got > DefaultTokenLifetime+time.Minute {
		t.Errorf("lifetime = %v, want ≈ %v", got, DefaultTokenLifetime)
	}

	// Second call must be served from the store without another token request.
	if _, err := manager.GetAccessToken(context.Background(), serverURL); err != nil {
		t.Fatal(err)
	}
	if n := tos.tokenRequests.Load(); n != 1 {
		t.Errorf("token requests = %d, want 1 (no refresh storm)", n)
	}
}

// Bullet: refresh sends the RFC 8707 resource indicator.
func TestTokenManager_RefreshSendsResource(t *testing.T) {
	tos := newTokenOnlyServer(t)
	serverURL := tos.URL + "/mcp"
	store := newTestStore(t)
	storeExpiredCredential(t, store, serverURL)
	cred := mustGet(t, store, serverURL)
	cred.TokenEndpoint = tos.URL + "/token"
	_ = store.Put(cred)
	manager := NewTokenManager(store)

	if _, err := manager.GetAccessToken(context.Background(), serverURL); err != nil {
		t.Fatal(err)
	}
	if got := tos.formValue("resource"); got != serverURL {
		t.Errorf("resource = %q, want %q", got, serverURL)
	}
	if got := tos.formValue("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q", got)
	}
}

// Bullet: invalid_grant marks the credential needs-login and stops retrying.
func TestTokenManager_InvalidGrantMarksNeedsLogin(t *testing.T) {
	tos := newTokenOnlyServer(t)
	tos.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
	}
	serverURL := tos.URL + "/mcp"
	store := newTestStore(t)
	storeExpiredCredential(t, store, serverURL)
	cred := mustGet(t, store, serverURL)
	cred.TokenEndpoint = tos.URL + "/token"
	_ = store.Put(cred)
	manager := NewTokenManager(store)

	_, err := manager.GetAccessToken(context.Background(), serverURL)
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("error = %v, want ErrNeedsLogin", err)
	}
	stored := mustGet(t, store, serverURL)
	if !stored.NeedsLogin {
		t.Error("credential not marked NeedsLogin")
	}
	if stored.RefreshToken != "" {
		t.Error("dead refresh token was kept")
	}

	// Subsequent calls short-circuit: no more token requests.
	for range 3 {
		if _, err := manager.GetAccessToken(context.Background(), serverURL); !errors.Is(err, ErrNeedsLogin) {
			t.Fatalf("error = %v, want ErrNeedsLogin", err)
		}
	}
	if n := tos.tokenRequests.Load(); n != 1 {
		t.Errorf("token requests = %d, want 1 (no retry after invalid_grant)", n)
	}

	// A fresh login (Put of a new credential) clears the flag.
	fresh, _ := NewCredential("test", serverURL, "client-123", "", "new", "r2", time.Now().Add(time.Hour), nil)
	_ = store.Put(fresh)
	if tok, err := manager.GetAccessToken(context.Background(), serverURL); err != nil || tok != "new" {
		t.Errorf("after re-login: tok=%q err=%v", tok, err)
	}
}

// A transient token-endpoint failure (5xx, non-JSON body) must not mark the
// credential needs-login: the next call should retry.
func TestTokenManager_TransientFailureDoesNotMarkNeedsLogin(t *testing.T) {
	tos := newTokenOnlyServer(t)
	tos.respond = func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }
	serverURL := tos.URL + "/mcp"
	store := newTestStore(t)
	storeExpiredCredential(t, store, serverURL)
	cred := mustGet(t, store, serverURL)
	cred.TokenEndpoint = tos.URL + "/token"
	_ = store.Put(cred)
	manager := NewTokenManager(store)

	_, err := manager.GetAccessToken(context.Background(), serverURL)
	if err == nil || errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("error = %v, want a plain refresh error", err)
	}
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) || tokenErr.StatusCode != http.StatusBadGateway {
		t.Errorf("error should wrap *TokenError with status 502, got %v", err)
	}
	if stored := mustGet(t, store, serverURL); stored.NeedsLogin || stored.RefreshToken == "" {
		t.Error("transient failure mutated the credential")
	}
	_, _ = manager.GetAccessToken(context.Background(), serverURL)
	if n := tos.tokenRequests.Load(); n != 2 {
		t.Errorf("token requests = %d, want 2 (retried)", n)
	}
}

// Login stores the token endpoint and defaults the lifetime, so the two paths
// agree on what a fresh credential looks like.
func TestExpiresAfter_Default(t *testing.T) {
	got := time.Until(expiresAfter(&TokenResponse{}))
	if got < DefaultTokenLifetime-time.Minute {
		t.Errorf("expiresAfter with no expires_in = %v, want ≈ %v", got, DefaultTokenLifetime)
	}
	got = time.Until(expiresAfter(&TokenResponse{ExpiresIn: 60}))
	if got > 61*time.Second || got < 55*time.Second {
		t.Errorf("expiresAfter(60s) = %v", got)
	}
}
