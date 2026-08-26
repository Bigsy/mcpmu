package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/httpclient"
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

	// ClientSecret is a pre-registered OAuth client secret.
	// When set (along with ClientID), dynamic registration is skipped.
	ClientSecret string
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

// DefaultTokenLifetime is assumed when the token endpoint omits expires_in
// (it is optional per RFC 6749 §5.1). Without a default the token would be
// treated as already expired and every request would trigger a refresh.
const DefaultTokenLifetime = time.Hour

// RefreshTimeout bounds one shared refresh: discovery (possibly via challenge)
// plus the token request. The refresh runs detached from the caller that
// happened to start it, so this is the only thing that ends a hung refresh.
const RefreshTimeout = 2*DiscoveryTimeout + TokenTimeout

// expiresAfter returns the expiry time for a token response, applying
// DefaultTokenLifetime when expires_in is absent or non-positive.
func expiresAfter(tokens *TokenResponse) time.Time {
	if tokens.ExpiresIn <= 0 {
		return time.Now().Add(DefaultTokenLifetime)
	}
	return time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
}

// TokenError is a non-2xx response from the token endpoint. Code and
// Description are filled from an RFC 6749 §5.2 JSON error body when present.
type TokenError struct {
	StatusCode  int
	Code        string
	Description string
	Body        string
}

func (e *TokenError) Error() string {
	return fmt.Sprintf("token endpoint returned HTTP %d: %s", e.StatusCode, e.Body)
}

// IsInvalidGrant reports whether the server rejected the grant itself
// (revoked, expired or replayed refresh token) — retrying cannot help.
func (e *TokenError) IsInvalidGrant() bool { return e.Code == "invalid_grant" }

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
		result, err = discoverViaChallenge(ctx, f.config.ServerURL)
		if err != nil {
			return fmt.Errorf("oauth discovery failed (tried standard and challenge-based): %w", err)
		}
	}
	f.metadata = result.Metadata

	// Use discovered scopes as fallback when none configured
	if len(f.config.Scopes) == 0 && len(f.metadata.ScopesSupported) > 0 {
		f.config.Scopes = f.metadata.ScopesSupported
		log.Printf("Using discovered scopes: %v", f.config.Scopes)
	}

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
		f.clientSecret = f.config.ClientSecret
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

	cred, err := NewCredential(
		f.config.ServerName,
		f.config.ServerURL,
		f.clientID,
		f.clientSecret,
		tokens.AccessToken,
		tokens.RefreshToken,
		expiresAfter(tokens),
		scopes,
	)
	if err != nil {
		return fmt.Errorf("create credential: %w", err)
	}
	cred.TokenEndpoint = f.metadata.TokenEndpoint

	if err := f.config.Store.Put(cred); err != nil {
		return fmt.Errorf("store credentials: %w", err)
	}

	return nil
}

// discoverViaChallenge triggers a 401 response from the MCP server to get
// the WWW-Authenticate header, then uses RFC 9728 Protected Resource Metadata
// to discover the OAuth server.
func discoverViaChallenge(ctx context.Context, serverURL string) (*DiscoverResult, error) {
	// Send a request to trigger a 401
	ctx, cancel := context.WithTimeout(ctx, DiscoveryTimeout)
	defer cancel()

	// Send a proper MCP initialize request shape to ensure servers return the expected 401
	req, err := http.NewRequestWithContext(ctx, "POST", serverURL, strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1,"params":{"protocolVersion":"`+MCPProtocolVersion+`","clientInfo":{"name":"mcpmu","version":"1.0.0"},"capabilities":{}}}`))
	if err != nil {
		return nil, fmt.Errorf("create challenge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	resp, err := newHTTPClient().Do(req)
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
	// Per RFC 6749 §3.1, the authorization endpoint URI may itself include a
	// query component that MUST be retained when adding request parameters.
	// Parse the endpoint so we merge into its existing query instead of
	// blindly concatenating "?...".
	endpoint, err := url.Parse(f.metadata.AuthorizationEndpoint)
	if err != nil {
		endpoint = &url.URL{Path: f.metadata.AuthorizationEndpoint}
	}
	params := endpoint.Query()

	params.Set("response_type", "code")
	params.Set("client_id", f.clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", f.state)
	params.Set("code_challenge", f.pkce.Challenge)
	params.Set("code_challenge_method", f.pkce.Method)

	if len(f.config.Scopes) > 0 {
		params.Set("scope", joinScopes(f.config.Scopes))
	}

	// MCP spec requires the resource parameter (the MCP server URL).
	// Using Set (not Add) ensures we don't duplicate it if the endpoint
	// already has a resource= hint baked in.
	if f.config.ServerURL != "" {
		params.Set("resource", f.config.ServerURL)
	}

	endpoint.RawQuery = params.Encode()
	return endpoint.String()
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

	ctx, cancel := context.WithTimeout(ctx, TokenTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := newHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	body, err := httpclient.ReadBody(resp, MaxMetadataSize)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		tokenErr := &TokenError{StatusCode: resp.StatusCode, Body: string(body)}
		var parsed struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			tokenErr.Code, tokenErr.Description = parsed.Error, parsed.Description
		}
		return nil, tokenErr
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
	if slices.Contains(supportedMethods, "client_secret_post") {
		return TokenAuthSecretPost
	}
	if slices.Contains(supportedMethods, "client_secret_basic") {
		return TokenAuthSecretBasic
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

	// MCP spec requires the resource parameter
	if f.config.ServerURL != "" {
		params.Set("resource", f.config.ServerURL)
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
// Pass empty clientSecret for public clients. resource is the MCP server URL,
// sent as the RFC 8707 resource indicator (the MCP spec requires it on every
// token request); pass "" to omit it.
func RefreshToken(ctx context.Context, tokenEndpoint, clientID, clientSecret, refreshToken, resource string, metadata *AuthorizationServerMetadata) (*TokenResponse, error) {
	params := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if resource != "" {
		params.Set("resource", resource)
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

// TokenManager handles automatic token refresh. One instance is shared by every
// HTTP upstream in a process, and GetAccessToken runs on arbitrary request
// goroutines (once per outbound POST), so all of its state is mutex-guarded and
// refreshes are single-flighted per server URL.
type TokenManager struct {
	store CredentialStore

	// mu guards metadata, refreshing and onWarning. It is never held across
	// network I/O — discovery and the token request run outside it.
	mu         sync.Mutex
	metadata   map[string]*AuthorizationServerMetadata // cached by server URL
	refreshing map[string]*refreshCall                 // in-flight refresh per server URL
	onWarning  WarningHandler
}

// refreshCall is one in-flight refresh that later arrivals for the same server
// URL wait on instead of starting a competing refresh.
type refreshCall struct {
	done  chan struct{}
	token string
	err   error
}

// wait blocks until the refresh publishes a result, or until the caller's own
// context expires — a hung refresh must not pin unrelated request goroutines
// past their deadlines.
func (c *refreshCall) wait(ctx context.Context) (string, error) {
	select {
	case <-c.done:
		return c.token, c.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// NewTokenManager creates a new token manager.
func NewTokenManager(store CredentialStore) *TokenManager {
	return &TokenManager{
		store:      store,
		metadata:   make(map[string]*AuthorizationServerMetadata),
		refreshing: make(map[string]*refreshCall),
	}
}

// SetWarningHandler sets a callback for non-fatal warnings (e.g., token storage failures).
// This allows callers to surface warnings to users without failing the operation.
func (m *TokenManager) SetWarningHandler(handler WarningHandler) {
	m.mu.Lock()
	m.onWarning = handler
	m.mu.Unlock()
}

func (m *TokenManager) warningHandler() WarningHandler {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.onWarning
}

// GetAccessToken returns a valid access token for a server, refreshing if needed.
// It returns an error wrapping ErrNeedsLogin when the credential cannot be
// refreshed and the user has to log in again.
func (m *TokenManager) GetAccessToken(ctx context.Context, serverURL string) (string, error) {
	cred, err := m.store.Get(serverURL)
	if err != nil {
		return "", fmt.Errorf("get credential: %w", err)
	}
	if cred == nil {
		return "", fmt.Errorf("no credentials for %s", serverURL)
	}
	if cred.NeedsLogin {
		return "", fmt.Errorf("%w for %s: refresh token was rejected", ErrNeedsLogin, serverURL)
	}

	// Check if token needs refresh
	if !cred.NeedsRefresh() {
		return cred.AccessToken, nil
	}

	// No refresh token - can't refresh
	if cred.RefreshToken == "" {
		return "", fmt.Errorf("%w for %s: token expired and no refresh token available", ErrNeedsLogin, serverURL)
	}

	return m.refreshOnce(ctx, serverURL)
}

// refreshOnce elects a single refresh per server URL and lets every caller —
// including the one that started it — wait on that result. Concurrent
// refreshes must not overlap: a server that rotates refresh tokens (the common
// case) treats a replayed refresh token as a breach signal and can revoke the
// entire grant.
//
// The refresh itself runs on its own goroutine under a context detached from
// the caller's cancellation (with RefreshTimeout as its only deadline). The
// starting caller is just one waiter: if its request is cancelled mid-refresh,
// the refresh still completes and every other waiter still gets the token,
// instead of all of them failing with the leader's context error.
func (m *TokenManager) refreshOnce(ctx context.Context, serverURL string) (string, error) {
	m.mu.Lock()
	call := m.refreshing[serverURL]
	if call == nil {
		call = &refreshCall{done: make(chan struct{})}
		m.refreshing[serverURL] = call
		m.mu.Unlock()

		go m.runRefresh(context.WithoutCancel(ctx), serverURL, call)
	} else {
		m.mu.Unlock()
	}
	return call.wait(ctx)
}

// runRefresh performs the refresh and publishes its result to the waiters.
func (m *TokenManager) runRefresh(ctx context.Context, serverURL string, call *refreshCall) {
	ctx, cancel := context.WithTimeout(ctx, RefreshTimeout)
	defer cancel()

	token, err := m.refreshHoldingSlot(ctx, serverURL)

	// Publish the result and release the slot together so a caller that sees an
	// empty slot is guaranteed to be starting a genuinely new refresh.
	m.mu.Lock()
	call.token, call.err = token, err
	delete(m.refreshing, serverURL)
	close(call.done)
	m.mu.Unlock()
}

// refreshHoldingSlot performs the refresh for the elected slot holder. It
// re-reads the credential rather than trusting the caller's copy: a refresh
// that landed while this goroutine was waiting for the slot has already
// rotated the stored refresh token, and replaying the stale one is exactly
// what we must avoid.
func (m *TokenManager) refreshHoldingSlot(ctx context.Context, serverURL string) (string, error) {
	cred, err := m.store.Get(serverURL)
	if err != nil {
		return "", fmt.Errorf("get credential: %w", err)
	}
	if cred == nil {
		return "", fmt.Errorf("no credentials for %s", serverURL)
	}
	if cred.NeedsLogin {
		return "", fmt.Errorf("%w for %s: refresh token was rejected", ErrNeedsLogin, serverURL)
	}
	if !cred.NeedsRefresh() {
		// Another refresh renewed it while we waited for the slot.
		return cred.AccessToken, nil
	}
	if cred.RefreshToken == "" {
		return "", fmt.Errorf("%w for %s: token expired and no refresh token available", ErrNeedsLogin, serverURL)
	}

	metadata, err := m.serverMetadata(ctx, serverURL, cred)
	if err != nil {
		return "", err
	}

	tokens, err := RefreshToken(ctx, metadata.TokenEndpoint, cred.ClientID, cred.ClientSecret, cred.RefreshToken, serverURL, metadata)
	if err != nil {
		var tokenErr *TokenError
		if errors.As(err, &tokenErr) && tokenErr.IsInvalidGrant() {
			// The grant is gone (revoked, expired, or the refresh token was
			// rotated away under us). Record that so callers stop hammering
			// the token endpoint with a dead refresh token; only a new login
			// clears it.
			cred.NeedsLogin = true
			cred.RefreshToken = ""
			if putErr := m.store.Put(cred); putErr != nil {
				log.Printf("Warning: failed to mark credential needs-login: %v", putErr)
			}
			return "", fmt.Errorf("%w for %s: refresh token rejected (%s)", ErrNeedsLogin, serverURL, tokenErr.Code)
		}
		return "", fmt.Errorf("refresh token: %w", err)
	}

	// Update stored credential
	cred.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		cred.RefreshToken = tokens.RefreshToken
	}
	cred.ExpiresAt = expiresAfter(tokens).UnixMilli()
	if tokens.Scope != "" {
		cred.Scopes = strings.Split(tokens.Scope, " ")
	}
	// Backfill the endpoint for credentials stored before it was recorded, so
	// the next process can refresh without discovery.
	cred.TokenEndpoint = metadata.TokenEndpoint

	if err := m.store.Put(cred); err != nil {
		// Log but don't fail - we have the token in memory
		log.Printf("Warning: failed to store refreshed token: %v", err)
		// Surface to user if handler is set - they need to know re-auth will be required on restart
		if onWarning := m.warningHandler(); onWarning != nil {
			onWarning(serverURL, fmt.Errorf("failed to save refreshed token (re-login required on restart): %w", err))
		}
	}

	return cred.AccessToken, nil
}

// serverMetadata returns authorization-server metadata for serverURL, in this
// order: the per-process cache; the token endpoint recorded on the credential
// at login (no network, and the only route for servers that advertise OAuth
// solely through a WWW-Authenticate challenge); RFC 8414 discovery on the
// server URL; RFC 9728 challenge-based discovery. Network discovery runs
// outside mu so a slow endpoint cannot block refreshes for other servers.
func (m *TokenManager) serverMetadata(ctx context.Context, serverURL string, cred *Credential) (*AuthorizationServerMetadata, error) {
	m.mu.Lock()
	cached, ok := m.metadata[serverURL]
	m.mu.Unlock()
	if ok {
		return cached, nil
	}

	var metadata *AuthorizationServerMetadata
	if cred.TokenEndpoint != "" {
		// Auth methods are unknown without full metadata; determineAuthMethod
		// then applies the RFC 6749 default (client_secret_basic) for
		// confidential clients, which is what a server with no
		// token_endpoint_auth_methods_supported advertisement gets anyway.
		metadata = &AuthorizationServerMetadata{TokenEndpoint: cred.TokenEndpoint}
	} else {
		result, err := Discover(ctx, serverURL)
		if err != nil {
			challengeResult, challengeErr := discoverViaChallenge(ctx, serverURL)
			if challengeErr != nil {
				return nil, fmt.Errorf("discover metadata: %w (challenge-based discovery: %v)", err, challengeErr)
			}
			result = challengeResult
		}
		metadata = result.Metadata
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Prefer a concurrently-cached entry so every caller shares one instance.
	if existing, ok := m.metadata[serverURL]; ok {
		return existing, nil
	}
	m.metadata[serverURL] = metadata
	return metadata, nil
}

// Logout removes credentials for a server.
func Logout(ctx context.Context, store CredentialStore, serverURL string) error {
	return store.Delete(serverURL)
}
