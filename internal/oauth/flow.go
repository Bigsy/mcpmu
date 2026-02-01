package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// FlowConfig holds configuration for an OAuth flow.
type FlowConfig struct {
	// ServerURL is the MCP server URL.
	ServerURL string

	// ServerName is the user-facing name of the server.
	ServerName string

	// Scopes are the OAuth scopes to request.
	Scopes []string

	// CallbackPort is the port for the callback server (nil = random).
	CallbackPort *int

	// Store is the credential store for saving tokens.
	Store CredentialStore
}

// Flow orchestrates an OAuth 2.1 authorization flow.
type Flow struct {
	config   FlowConfig
	metadata *AuthorizationServerMetadata
	clientID string
	pkce     *PKCE
	state    string
	callback *CallbackServer
}

// TokenResponse is the response from the token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// NewFlow creates a new OAuth flow.
func NewFlow(config FlowConfig) *Flow {
	return &Flow{config: config}
}

// Run executes the full OAuth flow:
// 1. Discover OAuth metadata
// 2. Start callback server
// 3. Register client (if registration endpoint available)
// 4. Open browser for authorization
// 5. Wait for callback
// 6. Exchange code for tokens
// 7. Store credentials
func (f *Flow) Run(ctx context.Context) error {
	// Step 1: Discover OAuth metadata
	result, err := Discover(ctx, f.config.ServerURL)
	if err != nil {
		return fmt.Errorf("oauth discovery: %w", err)
	}
	f.metadata = result.Metadata

	// Step 2: Start callback server
	f.callback, err = NewCallbackServer(f.config.CallbackPort)
	if err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}
	if err := f.callback.Start(); err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}
	defer func() { _ = f.callback.Stop() }()

	redirectURI := f.callback.RedirectURI()

	// Step 3: Register client if endpoint available
	if f.metadata.RegistrationEndpoint != "" {
		reg, err := RegisterClient(ctx, f.metadata.RegistrationEndpoint, redirectURI, f.config.Scopes)
		if err != nil {
			return fmt.Errorf("client registration: %w", err)
		}
		f.clientID = reg.ClientID
	} else {
		// Use a default client ID for servers that don't support registration
		f.clientID = "mcpmu"
	}

	// Step 4: Generate PKCE and state
	f.pkce, err = NewPKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}

	f.state, err = GenerateState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	// Step 5: Build and open authorization URL
	authURL := f.buildAuthorizationURL(redirectURI)
	if err := openBrowser(authURL); err != nil {
		return fmt.Errorf("open browser: %w (URL: %s)", err, authURL)
	}

	// Step 6: Wait for callback
	callbackCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	callbackResult, err := f.callback.Wait(callbackCtx)
	if err != nil {
		return fmt.Errorf("waiting for callback: %w", err)
	}

	if callbackResult.Error != "" {
		return fmt.Errorf("authorization error: %s - %s", callbackResult.Error, callbackResult.ErrorDescription)
	}

	if callbackResult.State != f.state {
		return fmt.Errorf("state mismatch: possible CSRF attack")
	}

	if callbackResult.Code == "" {
		return fmt.Errorf("no authorization code received")
	}

	// Step 7: Exchange code for tokens
	tokens, err := f.exchangeCode(ctx, callbackResult.Code, redirectURI)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}

	// Step 8: Store credentials
	scopes := f.config.Scopes
	if tokens.Scope != "" {
		scopes = strings.Split(tokens.Scope, " ")
	}

	cred := &Credential{
		ServerName:   f.config.ServerName,
		ServerURL:    f.config.ServerURL,
		ClientID:     f.clientID,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli(),
		Scopes:       scopes,
	}

	if err := f.config.Store.Put(cred); err != nil {
		return fmt.Errorf("store credentials: %w", err)
	}

	return nil
}

// buildAuthorizationURL constructs the OAuth authorization URL.
func (f *Flow) buildAuthorizationURL(redirectURI string) string {
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {f.state},
		"code_challenge":        {f.pkce.Challenge},
		"code_challenge_method": {f.pkce.Method},
	}

	if len(f.config.Scopes) > 0 {
		params.Set("scope", joinScopes(f.config.Scopes))
	}

	return f.metadata.AuthorizationEndpoint + "?" + params.Encode()
}

// exchangeCode exchanges the authorization code for tokens.
func (f *Flow) exchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {f.clientID},
		"code_verifier": {f.pkce.Verifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.metadata.TokenEndpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("response missing access_token")
	}

	return &tokens, nil
}

// RefreshToken refreshes an access token using a refresh token.
func RefreshToken(ctx context.Context, tokenEndpoint, clientID, refreshToken string) (*TokenResponse, error) {
	params := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("response missing access_token")
	}

	return &tokens, nil
}

// openBrowser opens the default browser to a URL.
func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}

// TokenManager handles automatic token refresh.
type TokenManager struct {
	store    CredentialStore
	metadata map[string]*AuthorizationServerMetadata // cached by server URL
}

// NewTokenManager creates a new token manager.
func NewTokenManager(store CredentialStore) *TokenManager {
	return &TokenManager{
		store:    store,
		metadata: make(map[string]*AuthorizationServerMetadata),
	}
}

// GetAccessToken returns a valid access token for a server, refreshing if needed.
func (m *TokenManager) GetAccessToken(ctx context.Context, serverURL string) (string, error) {
	cred, err := m.store.Get(serverURL)
	if err != nil {
		return "", fmt.Errorf("get credential: %w", err)
	}
	if cred == nil {
		return "", fmt.Errorf("no credentials for %s", serverURL)
	}

	// Check if token needs refresh
	if !cred.NeedsRefresh() {
		return cred.AccessToken, nil
	}

	// No refresh token - can't refresh
	if cred.RefreshToken == "" {
		return "", fmt.Errorf("token expired and no refresh token available")
	}

	// Get or discover metadata for token endpoint
	metadata, ok := m.metadata[serverURL]
	if !ok {
		result, err := Discover(ctx, serverURL)
		if err != nil {
			return "", fmt.Errorf("discover metadata: %w", err)
		}
		metadata = result.Metadata
		m.metadata[serverURL] = metadata
	}

	// Refresh the token
	tokens, err := RefreshToken(ctx, metadata.TokenEndpoint, cred.ClientID, cred.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh token: %w", err)
	}

	// Update stored credential
	cred.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		cred.RefreshToken = tokens.RefreshToken
	}
	cred.ExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
	if tokens.Scope != "" {
		cred.Scopes = strings.Split(tokens.Scope, " ")
	}

	if err := m.store.Put(cred); err != nil {
		// Log but don't fail - we have the token
		log.Printf("Warning: failed to store refreshed token: %v", err)
	}

	return cred.AccessToken, nil
}

// Logout removes credentials for a server.
func Logout(ctx context.Context, store CredentialStore, serverURL string) error {
	return store.Delete(serverURL)
}

// RequestBody helper for creating JSON request bodies.
func RequestBody(v interface{}) (io.Reader, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}
