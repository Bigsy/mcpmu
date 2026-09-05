package process

import (
	"context"
	"fmt"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/oauth"
)

// LoginOAuth triggers the OAuth login flow for a server that needs authentication.
// It opens a browser for the user to authenticate, then reconnects.
func (s *Supervisor) LoginOAuth(ctx context.Context, name string) error {
	s.mu.Lock()
	handle, exists := s.handles[SharedInstanceID(name)]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("server %s not found", name)
	}
	s.mu.Unlock()

	authStatus, authChallenge, oauthMeta := handle.loginState()
	if authStatus != mcp.AuthStatusOAuthNeeds {
		return fmt.Errorf("server %s doesn't need OAuth login (status: %s)", name, authStatus)
	}

	if s.credStore == nil {
		return fmt.Errorf("no credential store available")
	}

	// Build and resolve OAuth flow config
	flowConfig := resolveOAuthFlowConfig(
		handle.serverURL, name, s.credStore,
		handle.serverConfig.OAuth, s.globalOAuthCallbackPort,
		authChallenge, oauthMeta,
	)

	// Run OAuth flow
	flow := oauth.NewFlow(flowConfig)
	if err := flow.Run(ctx); err != nil {
		return fmt.Errorf("oauth login: %w", err)
	}

	// Retry connection with new tokens
	return s.retryHTTPConnection(ctx, name)
}

// resolveOAuthFlowConfig builds an OAuth FlowConfig with the correct resolution
// priority for callback port and scopes:
//   - Callback port: per-server oauth.callback_port → global → nil
//   - Scopes: per-server config → WWW-Authenticate challenge → metadata
func resolveOAuthFlowConfig(
	serverURL, serverName string,
	store oauth.CredentialStore,
	oauthCfg *config.OAuthConfig,
	globalCallbackPort *int,
	challenge *oauth.BearerChallenge,
	meta *oauth.AuthorizationServerMetadata,
) oauth.FlowConfig {
	fc := oauth.FlowConfig{
		ServerURL:  serverURL,
		ServerName: serverName,
		Store:      store,
	}

	// Apply per-server OAuth config if present
	if oauthCfg != nil {
		fc.ClientID = oauthCfg.ClientID
		fc.ClientSecret = oauthCfg.ClientSecret
		if len(oauthCfg.Scopes) > 0 {
			fc.Scopes = oauthCfg.Scopes
		}
		fc.CallbackPort = oauthCfg.CallbackPort
	}

	// Callback port fallback: per-server → global → nil
	if fc.CallbackPort == nil {
		fc.CallbackPort = globalCallbackPort
	}

	// Scope fallback: config → challenge → metadata
	if len(fc.Scopes) == 0 && challenge != nil && challenge.Scope != "" {
		fc.Scopes = strings.Split(challenge.Scope, " ")
	}
	if len(fc.Scopes) == 0 && meta != nil && len(meta.ScopesSupported) > 0 {
		fc.Scopes = meta.ScopesSupported
	}

	return fc
}

// retryHTTPConnection attempts to reconnect an HTTP server after OAuth completes.
func (s *Supervisor) retryHTTPConnection(ctx context.Context, name string) error {
	s.mu.RLock()
	handle, exists := s.handles[SharedInstanceID(name)]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("server %s not found", name)
	}

	_, err := s.Start(ctx, name, handle.serverConfig)
	return err
}
