package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	authCookieName = "mcpmu_session"
	cookieMaxAge   = 7 * 24 * 60 * 60 // 7 days in seconds
	sessionMaxAge  = 7 * 24 * time.Hour
)

// auth holds the authentication state for the web UI.
type auth struct {
	token      string // the shared secret
	signingKey []byte // random key for HMAC-signing cookies
}

// newAuth creates a new auth instance. Returns nil if token is empty (auth disabled).
func newAuth(token string) *auth {
	if token == "" {
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return &auth{token: token, signingKey: key}
}

// middleware returns an http.Handler that gates all requests behind token auth.
// Requests to /login and /static/ are always allowed through.
func (a *auth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Always allow login page, static assets
		if path == "/login" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		// Check for valid session cookie
		if a.validSession(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for Authorization: Bearer header (for API clients)
		if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
			if authHeader[len("Bearer "):] == a.token {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Not authenticated — respond appropriately by client type
		if strings.HasPrefix(path, "/api/") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			// htmx request (fragment poll, hx-post, etc.) — tell htmx to redirect the whole page
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// validSession checks the session cookie.
func (a *auth) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(authCookieName)
	if err != nil {
		return false
	}
	return a.verifyCookie(cookie.Value)
}

// handleLoginPage renders the login form.
func (a *auth) handleLoginPage(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Already authenticated — go to home
		if a.validSession(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		data := struct {
			Error bool
		}{}
		if r.URL.Query().Get("error") == "1" {
			data.Error = true
		}

		tmpl, ok := s.templates["login.html"]
		if !ok {
			log.Printf("template login.html not found")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, data); err != nil {
			log.Printf("template login.html: %v", err)
		}
	}
}

// handleLoginSubmit validates the token and sets a session cookie.
func (a *auth) handleLoginSubmit() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		submitted := r.FormValue("token")
		if submitted != a.token {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}

		// Set signed session cookie
		http.SetCookie(w, &http.Cookie{
			Name:     authCookieName,
			Value:    a.sign(),
			Path:     "/",
			MaxAge:   cookieMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// handleLogout clears the session cookie.
func (a *auth) handleLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     authCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// sign creates an HMAC signature over a timestamp payload.
func (a *auth) sign() string {
	ts := time.Now().Unix()
	payload := fmt.Sprintf("%d", ts)
	mac := hmac.New(sha256.New, a.signingKey)
	_, _ = mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// verifyCookie checks the HMAC signature and expiry of a cookie value.
func (a *auth) verifyCookie(value string) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, sig := parts[0], parts[1]

	// Verify HMAC
	mac := hmac.New(sha256.New, a.signingKey)
	_, _ = mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return false
	}

	// Verify expiry — payload is a unix timestamp
	ts, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < sessionMaxAge
}
