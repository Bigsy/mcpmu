package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestNewPKCE(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE failed: %v", err)
	}

	// Verifier should be base64url encoded (no padding)
	if pkce.Verifier == "" {
		t.Error("Verifier is empty")
	}

	// Challenge should be S256 hash of verifier
	if pkce.Challenge == "" {
		t.Error("Challenge is empty")
	}

	// Method should be S256
	if pkce.Method != "S256" {
		t.Errorf("Method: got %q, want %q", pkce.Method, "S256")
	}

	// Verify the challenge is correct
	hash := sha256.Sum256([]byte(pkce.Verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(hash[:])
	if pkce.Challenge != expectedChallenge {
		t.Errorf("Challenge mismatch:\n  got:  %q\n  want: %q", pkce.Challenge, expectedChallenge)
	}
}

func TestNewPKCE_Uniqueness(t *testing.T) {
	// Generate multiple PKCE pairs and verify they're unique
	seen := make(map[string]bool)
	for range 100 {
		pkce, err := NewPKCE()
		if err != nil {
			t.Fatalf("NewPKCE failed: %v", err)
		}
		if seen[pkce.Verifier] {
			t.Error("Generated duplicate verifier")
		}
		seen[pkce.Verifier] = true
	}
}

func TestGenerateState(t *testing.T) {
	state, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState failed: %v", err)
	}

	if state == "" {
		t.Error("State is empty")
	}

	// Verify uniqueness
	seen := make(map[string]bool)
	for range 100 {
		s, err := GenerateState()
		if err != nil {
			t.Fatalf("GenerateState failed: %v", err)
		}
		if seen[s] {
			t.Error("Generated duplicate state")
		}
		seen[s] = true
	}
}
