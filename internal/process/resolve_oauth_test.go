package process

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/oauth"
)

func TestResolveOAuthFlowConfig_CallbackPortPriority(t *testing.T) {
	global := new(9999)

	t.Run("per-server wins over global", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			&config.OAuthConfig{CallbackPort: new(3118)},
			global, nil, nil,
		)
		if fc.CallbackPort == nil || *fc.CallbackPort != 3118 {
			t.Errorf("expected per-server port 3118, got %v", fc.CallbackPort)
		}
	})

	t.Run("global used when per-server nil", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			&config.OAuthConfig{ClientID: "id"}, // no CallbackPort
			global, nil, nil,
		)
		if fc.CallbackPort == nil || *fc.CallbackPort != 9999 {
			t.Errorf("expected global port 9999, got %v", fc.CallbackPort)
		}
	})

	t.Run("global used when no oauth config", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			nil, // no per-server oauth config
			global, nil, nil,
		)
		if fc.CallbackPort == nil || *fc.CallbackPort != 9999 {
			t.Errorf("expected global port 9999, got %v", fc.CallbackPort)
		}
	})

	t.Run("nil when both unset", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			nil, nil, nil, nil,
		)
		if fc.CallbackPort != nil {
			t.Errorf("expected nil port, got %v", *fc.CallbackPort)
		}
	})
}

func TestResolveOAuthFlowConfig_ScopeFallback(t *testing.T) {
	challenge := &oauth.BearerChallenge{Scope: "channels:read channels:write"}
	meta := &oauth.AuthorizationServerMetadata{ScopesSupported: []string{"meta-scope"}}

	t.Run("config scopes win over challenge and metadata", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			&config.OAuthConfig{Scopes: []string{"config-scope"}},
			nil, challenge, meta,
		)
		if len(fc.Scopes) != 1 || fc.Scopes[0] != "config-scope" {
			t.Errorf("expected config scopes, got %v", fc.Scopes)
		}
	})

	t.Run("challenge scope used when config empty", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			nil, // no config scopes
			nil, challenge, meta,
		)
		if len(fc.Scopes) != 2 || fc.Scopes[0] != "channels:read" || fc.Scopes[1] != "channels:write" {
			t.Errorf("expected challenge scopes [channels:read channels:write], got %v", fc.Scopes)
		}
	})

	t.Run("metadata scopes used when config and challenge empty", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			nil, nil,
			nil, // no challenge
			meta,
		)
		if len(fc.Scopes) != 1 || fc.Scopes[0] != "meta-scope" {
			t.Errorf("expected metadata scopes, got %v", fc.Scopes)
		}
	})

	t.Run("challenge with empty scope string falls through to metadata", func(t *testing.T) {
		emptyChallenge := &oauth.BearerChallenge{Scope: ""}
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			nil, nil, emptyChallenge, meta,
		)
		if len(fc.Scopes) != 1 || fc.Scopes[0] != "meta-scope" {
			t.Errorf("expected metadata scopes when challenge scope empty, got %v", fc.Scopes)
		}
	})

	t.Run("nil scopes when all sources empty", func(t *testing.T) {
		fc := resolveOAuthFlowConfig(
			"https://example.com", "test", nil,
			nil, nil, nil, nil,
		)
		if len(fc.Scopes) != 0 {
			t.Errorf("expected empty scopes, got %v", fc.Scopes)
		}
	})
}

func TestResolveOAuthFlowConfig_ClientCredentials(t *testing.T) {
	fc := resolveOAuthFlowConfig(
		"https://mcp.slack.com/mcp", "slack", nil,
		&config.OAuthConfig{
			ClientID:     "12345.67890",
			ClientSecret: "secret-value",
		},
		nil, nil, nil,
	)

	if fc.ClientID != "12345.67890" {
		t.Errorf("expected ClientID '12345.67890', got %q", fc.ClientID)
	}
	if fc.ClientSecret != "secret-value" {
		t.Errorf("expected ClientSecret 'secret-value', got %q", fc.ClientSecret)
	}
	if fc.ServerURL != "https://mcp.slack.com/mcp" {
		t.Errorf("expected ServerURL, got %q", fc.ServerURL)
	}
	if fc.ServerName != "slack" {
		t.Errorf("expected ServerName 'slack', got %q", fc.ServerName)
	}
}
