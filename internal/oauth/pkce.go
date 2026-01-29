package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCE holds the PKCE challenge and verifier for OAuth 2.1.
type PKCE struct {
	// Verifier is the random string used to generate the challenge.
	Verifier string

	// Challenge is the base64url-encoded SHA256 hash of the verifier.
	Challenge string

	// Method is always "S256" for this implementation.
	Method string
}

// NewPKCE generates a new PKCE challenge/verifier pair using S256.
func NewPKCE() (*PKCE, error) {
	// Generate 32 random bytes for the verifier
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return nil, err
	}

	// Encode as base64url without padding
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Compute S256 challenge: BASE64URL(SHA256(verifier))
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return &PKCE{
		Verifier:  verifier,
		Challenge: challenge,
		Method:    "S256",
	}, nil
}

// GenerateState generates a random state parameter for OAuth.
func GenerateState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}
