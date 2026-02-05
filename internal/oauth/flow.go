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

	// ClientID is a pre-registered OAuth client ID (for servers without dynamic registration).
	// If empty, dynamic registration will be attempted, falling back to "mcpmu".
	ClientID string
}

// Flow orchestrates an OAuth 2.1 authorization flow.
type Flow struct {
	config       FlowConfig
	metadata     *AuthorizationServerMetadata
	clientID     string
	clientSecret string // From dynamic registration, may be empty for public clients
	pkce         *PKCE
	state        string
	callback     *CallbackServer
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
// 1. Discover OAuth metadata (via standard discovery or RFC 9728 challenge)
// 2. Start callback server
// 3. Register client (if registration endpoint available)
// 4. Open browser for authorization
// 5. Wait for callback
// 6. Exchange code for tokens
// 7. Store credentials
func (f *Flow) Run(ctx context.Context) error {
	// Step 1: Discover OAuth metadata
	// First try standard discovery on the server URL
	result, err := Discover(ctx, f.config.ServerURL)
	if err != nil {
		// Standard discovery failed - try RFC 9728 Protected Resource Metadata flow
		// This involves triggering a 401 to get WWW-Authenticate header
		log.Printf("Standard OAuth discovery failed, trying challenge-based discovery: %v", err)
		result, err = f.discoverViaChallenge(ctx)
		if err != nil {
			return fmt.Errorf("oauth discovery failed (tried standard and challenge-based): %w", err)
		}
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

	// Step 3: Register client or use configured client ID
	// Priority: 1) Configured client ID, 2) Dynamic registration, 3) Default "mcpmu"
	if f.config.ClientID != "" {
		// Use pre-configured client ID (for servers without dynamic registration)
		f.clientID = f.config.ClientID
		log.Printf("Using configured OAuth client ID: %s", f.clientID)
	} else if f.metadata.RegistrationEndpoint != "" {
		// Try dynamic registration
		// Some servers advertise registration but don't support it (return 403/401),
		// so we treat registration failure as non-fatal and fall back to default client ID.
		reg, err := RegisterClient(ctx, f.metadata.RegistrationEndpoint, redirectURI, f.config.Scopes)
		if err != nil {
			log.Printf("Client registration failed (falling back to default client ID): %v", err)
			f.clientID = "mcpmu"
		} else {
			f.clientID = reg.ClientID
			f.clientSecret = reg.ClientSecret // May be empty for public clients
		}
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

	expiresAt := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	cred, err := NewCredential(
		f.config.ServerName,
		f.config.ServerURL,
		f.clientID,
		f.clientSecret,
		tokens.AccessToken,
		tokens.RefreshToken,
		expiresAt,
		scopes,
	)
	if err != nil {
		return fmt.Errorf("create credential: %w", err)
	}

	if err := f.config.Store.Put(cred); err != nil {
		return fmt.Errorf("store credentials: %w", err)
	}

	return nil
}

// discoverViaChallenge triggers a 401 response from the MCP server to get
// the WWW-Authenticate header, then uses RFC 9728 Protected Resource Metadata
// to discover the OAuth server.
func (f *Flow) discoverViaChallenge(ctx context.Context) (*DiscoverResult, error) {
	// Send a request to trigger a 401
	client := &http.Client{Timeout: DiscoveryTimeout}

	// Send a proper MCP initialize request shape to ensure servers return the expected 401
	req, err := http.NewRequestWithContext(ctx, "POST", f.config.ServerURL, strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"mcpmu","version":"1.0.0"},"capabilities":{}}}`))
	if err != nil {
		return nil, fmt.Errorf("create challenge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("challenge request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// We expect a 401 response
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, fmt.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// Parse WWW-Authenticate headers using centralized parser
	// This handles multiple header values and multiple challenges per value
	challenge := ParseBearerChallenge(resp.Header)
	if challenge == nil {
		return nil, fmt.Errorf("no Bearer challenge in WWW-Authenticate header")
	}

	if challenge.ResourceMetadata == "" {
		return nil, fmt.Errorf("no resource_metadata in WWW-Authenticate Bearer challenge")
	}

	return DiscoverFromChallenge(ctx, challenge)
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

// TokenAuthMethod specifies how to authenticate to the token endpoint.
type TokenAuthMethod string

const (
	// TokenAuthNone is for public clients (no authentication).
	TokenAuthNone TokenAuthMethod = "none"
	// TokenAuthSecretPost sends client_id and client_secret in POST body.
	TokenAuthSecretPost TokenAuthMethod = "client_secret_post"
	// TokenAuthSecretBasic uses HTTP Basic authentication.
	TokenAuthSecretBasic TokenAuthMethod = "client_secret_basic"
)

// TokenRequestConfig holds configuration for token endpoint requests.
type TokenRequestConfig struct {
	Endpoint     string
	Params       url.Values
	ClientID     string
	ClientSecret string
	AuthMethod   TokenAuthMethod
}

// doTokenRequest performs a token endpoint request with the given config.
// This is the common HTTP request/response handling shared by exchangeCode and RefreshToken.
func doTokenRequest(ctx context.Context, cfg TokenRequestConfig) (*TokenResponse, error) {
	params := cfg.Params

	// Apply client authentication based on method
	switch cfg.AuthMethod {
	case TokenAuthSecretPost:
		// Add client_id and client_secret to POST body
		params.Set("client_id", cfg.ClientID)
		if cfg.ClientSecret != "" {
			params.Set("client_secret", cfg.ClientSecret)
		}
	case TokenAuthSecretBasic:
		// Will set Authorization header below
		// Still need client_id in body for some servers
		params.Set("client_id", cfg.ClientID)
	default:
		// Public client - just client_id
		params.Set("client_id", cfg.ClientID)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	// Set Basic auth if using client_secret_basic
	if cfg.AuthMethod == TokenAuthSecretBasic && cfg.ClientSecret != "" {
		req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	}

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

// determineAuthMethod picks the best auth method based on server metadata and client credentials.
func determineAuthMethod(metadata *AuthorizationServerMetadata, clientSecret string) TokenAuthMethod {
	if clientSecret == "" {
		return TokenAuthNone
	}

	// Check server's supported methods
	supportedMethods := metadata.TokenEndpointAuthMethods
	if len(supportedMethods) == 0 {
		// Default per RFC: client_secret_basic
		return TokenAuthSecretBasic
	}

	// Prefer client_secret_post (simpler), fall back to client_secret_basic
	for _, method := range supportedMethods {
		if method == "client_secret_post" {
			return TokenAuthSecretPost
		}
	}
	for _, method := range supportedMethods {
		if method == "client_secret_basic" {
			return TokenAuthSecretBasic
		}
	}

	// Server doesn't support our methods - try post anyway
	return TokenAuthSecretPost
}

// exchangeCode exchanges the authorization code for tokens.
func (f *Flow) exchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {f.pkce.Verifier},
	}

	authMethod := determineAuthMethod(f.metadata, f.clientSecret)
	return doTokenRequest(ctx, TokenRequestConfig{
		Endpoint:     f.metadata.TokenEndpoint,
		Params:       params,
		ClientID:     f.clientID,
		ClientSecret: f.clientSecret,
		AuthMethod:   authMethod,
	})
}

// RefreshToken refreshes an access token using a refresh token.
// Pass empty clientSecret for public clients.
func RefreshToken(ctx context.Context, tokenEndpoint, clientID, clientSecret, refreshToken string, metadata *AuthorizationServerMetadata) (*TokenResponse, error) {
	params := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}

	authMethod := TokenAuthNone
	if metadata != nil {
		authMethod = determineAuthMethod(metadata, clientSecret)
	} else if clientSecret != "" {
		// Default to client_secret_post if no metadata
		authMethod = TokenAuthSecretPost
	}

	return doTokenRequest(ctx, TokenRequestConfig{
		Endpoint:     tokenEndpoint,
		Params:       params,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthMethod:   authMethod,
	})
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

// WarningHandler is called when a non-fatal error occurs that should be surfaced to the user.
type WarningHandler func(serverURL string, warning error)

// TokenManager handles automatic token refresh.
type TokenManager struct {
	store     CredentialStore
	metadata  map[string]*AuthorizationServerMetadata // cached by server URL
	onWarning WarningHandler
}

// NewTokenManager creates a new token manager.
func NewTokenManager(store CredentialStore) *TokenManager {
	return &TokenManager{
		store:    store,
		metadata: make(map[string]*AuthorizationServerMetadata),
	}
}

// SetWarningHandler sets a callback for non-fatal warnings (e.g., token storage failures).
// This allows callers to surface warnings to users without failing the operation.
func (m *TokenManager) SetWarningHandler(handler WarningHandler) {
	m.onWarning = handler
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
	tokens, err := RefreshToken(ctx, metadata.TokenEndpoint, cred.ClientID, cred.ClientSecret, cred.RefreshToken, metadata)
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
		// Log but don't fail - we have the token in memory
		log.Printf("Warning: failed to store refreshed token: %v", err)
		// Surface to user if handler is set - they need to know re-auth will be required on restart
		if m.onWarning != nil {
			m.onWarning(serverURL, fmt.Errorf("failed to save refreshed token (re-login required on restart): %w", err))
		}
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
