package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDetermineAuthMethod_NoSecret(t *testing.T) {
	metadata := &AuthorizationServerMetadata{
		TokenEndpointAuthMethods: []string{"client_secret_post", "client_secret_basic"},
	}

	method := determineAuthMethod(metadata, "")
	if method != TokenAuthNone {
		t.Errorf("expected TokenAuthNone for empty secret, got %v", method)
	}
}

func TestDetermineAuthMethod_PrefersPost(t *testing.T) {
	metadata := &AuthorizationServerMetadata{
		TokenEndpointAuthMethods: []string{"client_secret_basic", "client_secret_post"},
	}

	method := determineAuthMethod(metadata, "secret123")
	if method != TokenAuthSecretPost {
		t.Errorf("expected TokenAuthSecretPost when post is supported, got %v", method)
	}
}

func TestDetermineAuthMethod_FallsBackToBasic(t *testing.T) {
	metadata := &AuthorizationServerMetadata{
		TokenEndpointAuthMethods: []string{"client_secret_basic"},
	}

	method := determineAuthMethod(metadata, "secret123")
	if method != TokenAuthSecretBasic {
		t.Errorf("expected TokenAuthSecretBasic when only basic is supported, got %v", method)
	}
}

func TestDetermineAuthMethod_DefaultsToBasic(t *testing.T) {
	// No supported methods specified - RFC says default is basic
	metadata := &AuthorizationServerMetadata{
		TokenEndpointAuthMethods: nil,
	}

	method := determineAuthMethod(metadata, "secret123")
	if method != TokenAuthSecretBasic {
		t.Errorf("expected TokenAuthSecretBasic as default, got %v", method)
	}
}

func TestDetermineAuthMethod_UnsupportedMethods(t *testing.T) {
	// Only unsupported methods like private_key_jwt
	metadata := &AuthorizationServerMetadata{
		TokenEndpointAuthMethods: []string{"private_key_jwt"},
	}

	method := determineAuthMethod(metadata, "secret123")
	// Should fall back to post
	if method != TokenAuthSecretPost {
		t.Errorf("expected TokenAuthSecretPost as fallback, got %v", method)
	}
}

func TestBuildAuthorizationURL_IncludesResource(t *testing.T) {
	f := &Flow{
		config: FlowConfig{
			ServerURL: "https://mcp.example.com/mcp",
			Scopes:    []string{"read"},
		},
		metadata: &AuthorizationServerMetadata{
			AuthorizationEndpoint: "https://auth.example.com/authorize",
		},
		clientID: "test-client",
		pkce:     &PKCE{Challenge: "challenge123", Method: "S256", Verifier: "verifier123"},
		state:    "state123",
	}

	authURL := f.buildAuthorizationURL("http://127.0.0.1:3118/callback")

	if !strings.Contains(authURL, "resource=") {
		t.Error("expected resource parameter in authorization URL")
	}
	if !strings.Contains(authURL, "mcp.example.com") {
		t.Error("expected resource to contain server URL")
	}
}

func TestBuildAuthorizationURL_EndpointWithExistingQuery(t *testing.T) {
	// Regression: Sentry's discovery returns an authorization_endpoint that
	// already contains a query string (e.g. "?resource=https%3A%2F%2Fmcp.sentry.dev%2Fmcp").
	// We must use '&' as the separator and preserve (not duplicate) existing params.
	f := &Flow{
		config: FlowConfig{
			ServerURL: "https://mcp.sentry.dev/mcp",
			Scopes:    []string{"org:read"},
		},
		metadata: &AuthorizationServerMetadata{
			AuthorizationEndpoint: "https://mcp.sentry.dev/oauth/authorize?resource=https%3A%2F%2Fmcp.sentry.dev%2Fmcp",
		},
		clientID: "test-client",
		pkce:     &PKCE{Challenge: "challenge123", Method: "S256", Verifier: "verifier123"},
		state:    "state123",
	}

	authURL := f.buildAuthorizationURL("http://127.0.0.1:3118/callback")

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("resulting URL is unparseable: %v\nURL: %s", err, authURL)
	}

	// Exactly one '?' between path and query.
	if strings.Count(authURL, "?") != 1 {
		t.Errorf("expected exactly one '?' separator, got %d\nURL: %s", strings.Count(authURL, "?"), authURL)
	}

	q := parsed.Query()

	// Per RFC 6749 §3.1, "Request and response parameters MUST NOT be included more than once."
	for key, vals := range q {
		if len(vals) > 1 {
			t.Errorf("param %q appears %d times; must appear once: %v", key, len(vals), vals)
		}
	}

	// client_id must be a real, parseable param — the bug swallowed it into 'resource'.
	if got := q.Get("client_id"); got != "test-client" {
		t.Errorf("client_id = %q, want %q", got, "test-client")
	}

	// The pre-existing 'resource' from the endpoint must be preserved as-is.
	if got := q.Get("resource"); got != "https://mcp.sentry.dev/mcp" {
		t.Errorf("resource = %q, want %q", got, "https://mcp.sentry.dev/mcp")
	}

	// PKCE + response_type must still be present.
	if got := q.Get("code_challenge"); got != "challenge123" {
		t.Errorf("code_challenge = %q, want %q", got, "challenge123")
	}
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want %q", got, "code")
	}
}

func TestBuildAuthorizationURL_EndpointQueryParamConflict(t *testing.T) {
	// If the endpoint's baked query sets a param we also try to set (e.g. resource)
	// with a DIFFERENT value, our value wins — our config is authoritative.
	f := &Flow{
		config: FlowConfig{
			ServerURL: "https://mcp.example.com/mcp",
		},
		metadata: &AuthorizationServerMetadata{
			AuthorizationEndpoint: "https://auth.example.com/authorize?resource=https%3A%2F%2Fstale.example.com%2Fmcp",
		},
		clientID: "test-client",
		pkce:     &PKCE{Challenge: "c", Method: "S256", Verifier: "v"},
		state:    "s",
	}

	authURL := f.buildAuthorizationURL("http://127.0.0.1:3118/callback")

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("unparseable URL: %v", err)
	}
	if got := parsed.Query().Get("resource"); got != "https://mcp.example.com/mcp" {
		t.Errorf("resource = %q, want our config value %q", got, "https://mcp.example.com/mcp")
	}
}

func TestBuildAuthorizationURL_NoResourceWhenEmpty(t *testing.T) {
	f := &Flow{
		config: FlowConfig{
			ServerURL: "",
		},
		metadata: &AuthorizationServerMetadata{
			AuthorizationEndpoint: "https://auth.example.com/authorize",
		},
		clientID: "test-client",
		pkce:     &PKCE{Challenge: "challenge123", Method: "S256", Verifier: "verifier123"},
		state:    "state123",
	}

	authURL := f.buildAuthorizationURL("http://127.0.0.1:3118/callback")

	if strings.Contains(authURL, "resource=") {
		t.Error("expected no resource parameter when ServerURL is empty")
	}
}

func TestFlowConfig_ClientSecretSkipsDCR(t *testing.T) {
	// Verify that when ClientID and ClientSecret are both configured,
	// the flow uses them directly (no dynamic registration)
	f := NewFlow(FlowConfig{
		ServerURL:    "https://mcp.example.com/mcp",
		ServerName:   "test",
		ClientID:     "pre-registered-id",
		ClientSecret: "pre-registered-secret",
	})

	if f.config.ClientID != "pre-registered-id" {
		t.Errorf("expected ClientID to be set, got %q", f.config.ClientID)
	}
	if f.config.ClientSecret != "pre-registered-secret" {
		t.Errorf("expected ClientSecret to be set, got %q", f.config.ClientSecret)
	}
}

func TestExchangeCode_IncludesResource(t *testing.T) {
	// Create a test token endpoint that captures the request
	var gotResource string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotResource = r.FormValue("resource")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "test-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		})
	}))
	defer ts.Close()

	f := &Flow{
		config: FlowConfig{
			ServerURL: "https://mcp.example.com/mcp",
		},
		metadata: &AuthorizationServerMetadata{
			TokenEndpoint: ts.URL,
		},
		clientID: "test-client",
		pkce:     &PKCE{Challenge: "c", Method: "S256", Verifier: "v"},
	}

	_, err := f.exchangeCode(context.Background(), "auth-code", "http://127.0.0.1:3118/callback")
	if err != nil {
		t.Fatalf("exchangeCode failed: %v", err)
	}

	if gotResource != "https://mcp.example.com/mcp" {
		t.Errorf("expected resource param in token exchange, got %q", gotResource)
	}
}
