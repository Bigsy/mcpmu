package oauth

import "testing"

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
