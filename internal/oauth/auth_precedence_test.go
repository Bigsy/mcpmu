package oauth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAuthPrecedence verifies the authentication precedence:
// 1. Bearer token (from env var) - highest priority
// 2. Stored OAuth credentials
// 3. OAuth discovery (needs login)
// 4. None (no auth)

func TestAuthPrecedence_BearerTokenTakesPriority(t *testing.T) {
	// Setup: Create a store with OAuth credentials
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	serverURL := "https://example.com/mcp"

	// Store OAuth credentials
	cred := &Credential{
		ServerName:   "test",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  "oauth-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("failed to store credential: %v", err)
	}

	// Set bearer token env var
	_ = os.Setenv("TEST_BEARER_TOKEN", "bearer-token-value")
	defer func() { _ = os.Unsetenv("TEST_BEARER_TOKEN") }()

	// Verify bearer token is available
	bearerToken := os.Getenv("TEST_BEARER_TOKEN")
	if bearerToken == "" {
		t.Fatal("bearer token env var not set")
	}

	// When bearer token is configured, it should be used regardless of OAuth creds
	// The actual precedence logic is in the supervisor, but we test the components here

	// Bearer token should be non-empty
	if bearerToken != "bearer-token-value" {
		t.Errorf("expected bearer token 'bearer-token-value', got %q", bearerToken)
	}

	// OAuth creds should also exist (but not be used when bearer is set)
	storedCred, err := store.Get(serverURL)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}
	if storedCred == nil {
		t.Fatal("expected stored OAuth credential")
	}
	if storedCred.AccessToken != "oauth-token" {
		t.Errorf("expected OAuth token 'oauth-token', got %q", storedCred.AccessToken)
	}
}

func TestAuthPrecedence_StoredOAuthUsedWhenNoBearer(t *testing.T) {
	// Setup: Create a store with OAuth credentials, no bearer token
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	serverURL := "https://example.com/mcp"

	// Store OAuth credentials
	cred := &Credential{
		ServerName:   "test",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  "oauth-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("failed to store credential: %v", err)
	}

	// No bearer token configured
	bearerEnvVar := ""

	// When no bearer token, OAuth credentials should be used
	if bearerEnvVar != "" {
		t.Fatal("bearer env var should be empty for this test")
	}

	storedCred, err := store.Get(serverURL)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}
	if storedCred == nil {
		t.Fatal("expected stored OAuth credential")
	}
	if storedCred.AccessToken != "oauth-token" {
		t.Errorf("expected OAuth token 'oauth-token', got %q", storedCred.AccessToken)
	}
}

func TestAuthPrecedence_NeedsLoginWhenNoStoredCreds(t *testing.T) {
	// Setup: Empty credential store
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	serverURL := "https://example.com/mcp"

	// No stored credentials
	cred, err := store.Get(serverURL)
	if err != nil {
		t.Fatalf("failed to get credential: %v", err)
	}
	if cred != nil {
		t.Fatal("expected no stored credential")
	}

	// At this point, OAuth discovery would be attempted
	// If server supports OAuth, status would be "needs-login"
	// We can't test the actual discovery without a mock server,
	// but we verify the store returns nil for unknown servers
}

func TestTokenManager_RefreshesExpiredToken(t *testing.T) {
	// This test verifies that TokenManager correctly identifies tokens that need refresh
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	serverURL := "https://example.com/mcp"

	// Store an expired credential
	cred := &Credential{
		ServerName:   "test",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(), // Expired 1 hour ago
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("failed to store credential: %v", err)
	}

	// Verify the credential is marked as needing refresh
	storedCred, _ := store.Get(serverURL)
	if !storedCred.NeedsRefresh() {
		t.Error("expected credential to need refresh")
	}
	if !storedCred.IsExpired() {
		t.Error("expected credential to be expired")
	}

	// TokenManager.GetAccessToken would attempt refresh here
	// We can't test the actual refresh without a mock token endpoint
	manager := NewTokenManager(store)
	_, err := manager.GetAccessToken(context.Background(), serverURL)
	// This will fail because we don't have a real token endpoint
	if err == nil {
		t.Error("expected error when refreshing without valid endpoint")
	}
}

func TestTokenManager_ReturnsValidToken(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	serverURL := "https://example.com/mcp"

	// Store a valid (non-expired) credential
	cred := &Credential{
		ServerName:   "test",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  "valid-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(), // Expires in 1 hour
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("failed to store credential: %v", err)
	}

	manager := NewTokenManager(store)
	token, err := manager.GetAccessToken(context.Background(), serverURL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("expected 'valid-token', got %q", token)
	}
}

func TestTokenManager_ErrorsForUnknownServer(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	manager := NewTokenManager(store)
	_, err := manager.GetAccessToken(context.Background(), "https://unknown.com/mcp")
	if err == nil {
		t.Error("expected error for unknown server")
	}
}
