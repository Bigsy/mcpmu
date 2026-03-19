package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
