package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenManager_RefreshTokenFailure_ReturnsError(t *testing.T) {
	var tokenEndpointURL string

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 "https://issuer.example",
			"authorization_endpoint": "https://auth.example/authorize",
			"token_endpoint":         tokenEndpointURL,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid_grant"))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	tokenEndpointURL = server.URL + "/token"
	serverURL := server.URL + "/mcp"

	tmpDir := t.TempDir()
	store := NewFileStoreAt(filepath.Join(tmpDir, "creds.json"))

	cred := &Credential{
		ServerName:   "test",
		ServerURL:    serverURL,
		ClientID:     "client-123",
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(),
	}
	if err := store.Put(cred); err != nil {
		t.Fatalf("failed to store credential: %v", err)
	}

	manager := NewTokenManager(store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := manager.GetAccessToken(ctx, serverURL)
	if err == nil {
		t.Fatal("expected error from refresh failure")
	}
	if !strings.Contains(err.Error(), "refresh token") {
		t.Errorf("expected error to contain %q, got: %v", "refresh token", err)
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("expected error to contain %q, got: %v", "HTTP 400", err)
	}

	// Ensure a failed refresh doesn't mutate the stored credential.
	stored, err := store.Get(serverURL)
	if err != nil {
		t.Fatalf("failed to re-read credential: %v", err)
	}
	if stored.AccessToken != "expired-token" {
		t.Fatalf("AccessToken mutated on refresh failure: got %q, want %q", stored.AccessToken, "expired-token")
	}
}
