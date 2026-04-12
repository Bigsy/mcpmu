package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
)

const testToken = "test-secret-token"

func newTestServerWithAuth(t *testing.T) *Server {
	t.Helper()

	cfg := config.NewConfig()
	enabled := true
	_ = cfg.AddServer("test-stdio", config.ServerConfig{
		Command: "echo",
		Args:    []string{"hello"},
		Enabled: &enabled,
	})

	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save test config: %v", err)
	}

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	srv, err := New(Options{
		Addr:       "127.0.0.1:0",
		Config:     cfg,
		ConfigPath: configPath,
		Supervisor: process.NewSupervisor(bus),
		Bus:        bus,
		Token:      testToken,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// login performs a login and returns the session cookie.
func login(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	form := url.Values{"token": {testToken}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == authCookieName {
			return c
		}
	}
	t.Fatal("no session cookie set after login")
	return nil
}

func TestAuthDisabled(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without auth, got %d", rec.Code)
	}
}

func TestUnauthenticatedRedirectsToLogin(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Fatalf("expected redirect to /login, got %q", loc)
	}
}

func TestUnauthenticatedAPI401(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestUnauthenticatedHtmxRedirect(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/fragments/servers/table", nil)
	req.Header.Set("HX-Request", "true")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with HX-Redirect header, got %d", rec.Code)
	}
	if loc := rec.Header().Get("HX-Redirect"); loc != "/login" {
		t.Fatalf("expected HX-Redirect /login, got %q", loc)
	}
}

func TestLoginPageAccessible(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for login page, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html, got %q", ct)
	}
}

func TestStaticAccessibleWithoutAuth(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/static/styles.css", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for static file, got %d", rec.Code)
	}
}

func TestLoginWrongToken(t *testing.T) {
	srv := newTestServerWithAuth(t)

	form := url.Values{"token": {"wrong-token"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=1") {
		t.Fatalf("expected redirect with error=1, got %q", loc)
	}
}

func TestLoginAndAccess(t *testing.T) {
	srv := newTestServerWithAuth(t)
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	req.AddCookie(cookie)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", rec.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with bearer token, got %d", rec.Code)
	}
}

func TestBearerAuthWrongToken(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuthRequiresPrefix(t *testing.T) {
	srv := newTestServerWithAuth(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/servers", nil)
	req.Header.Set("Authorization", testToken)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without Bearer prefix, got %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	srv := newTestServerWithAuth(t)
	cookie := login(t, srv)

	// Logout
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(cookie)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: expected 303, got %d", rec.Code)
	}

	// Extract cleared cookie
	var clearedCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == authCookieName {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil || clearedCookie.MaxAge >= 0 {
		t.Fatal("expected session cookie to be cleared (MaxAge < 0)")
	}

	// Access with cleared cookie should redirect
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/servers", nil)
	req.AddCookie(clearedCookie)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 after logout, got %d", rec.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	srv := newTestServerWithAuth(t)

	// Forge a cookie with a timestamp from 2001 — well past the 7-day window
	payload := "1000000000"
	mac := hmac.New(sha256.New, srv.auth.signingKey)
	_, _ = mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	expiredCookie := &http.Cookie{
		Name:  authCookieName,
		Value: payload + "." + sig,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/servers", nil)
	req.AddCookie(expiredCookie)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 for expired session, got %d", rec.Code)
	}
}

func TestLoginRedirectsWhenAuthenticated(t *testing.T) {
	srv := newTestServerWithAuth(t)
	cookie := login(t, srv)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/login", nil)
	req.AddCookie(cookie)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect to /, got %q", loc)
	}
}
