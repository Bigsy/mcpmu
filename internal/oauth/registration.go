package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ClientRegistrationRequest is the request for dynamic client registration (RFC 7591).
type ClientRegistrationRequest struct {
	// RedirectURIs are the callback URLs for the client.
	RedirectURIs []string `json:"redirect_uris"`

	// ClientName is a human-readable name for the client.
	ClientName string `json:"client_name,omitempty"`

	// GrantTypes are the OAuth grant types the client will use.
	GrantTypes []string `json:"grant_types,omitempty"`

	// ResponseTypes are the OAuth response types the client will use.
	ResponseTypes []string `json:"response_types,omitempty"`

	// TokenEndpointAuthMethod is how the client authenticates to the token endpoint.
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`

	// Scope is a space-separated list of scopes the client may request.
	Scope string `json:"scope,omitempty"`
}

// ClientRegistrationResponse is the response from dynamic client registration.
type ClientRegistrationResponse struct {
	// ClientID is the unique client identifier.
	ClientID string `json:"client_id"`

	// ClientSecret is the client secret (may be empty for public clients).
	ClientSecret string `json:"client_secret,omitempty"`

	// ClientSecretExpiresAt is when the secret expires (0 = never).
	ClientSecretExpiresAt int64 `json:"client_secret_expires_at,omitempty"`

	// RegistrationAccessToken is used to read/update/delete the registration.
	RegistrationAccessToken string `json:"registration_access_token,omitempty"`

	// RegistrationClientURI is the URI to manage the registration.
	RegistrationClientURI string `json:"registration_client_uri,omitempty"`
}

// RegisterClient performs dynamic client registration.
func RegisterClient(ctx context.Context, registrationEndpoint string, redirectURI string, scopes []string) (*ClientRegistrationResponse, error) {
	req := ClientRegistrationRequest{
		RedirectURIs:            []string{redirectURI},
		ClientName:              "mcpmu",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none", // Public client
	}

	if len(scopes) > 0 {
		req.Scope = joinScopes(scopes)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal registration request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", registrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("registration request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registration failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result ClientRegistrationResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if result.ClientID == "" {
		return nil, fmt.Errorf("registration response missing client_id")
	}

	return &result, nil
}

// joinScopes joins scopes with space separator.
func joinScopes(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	result := scopes[0]
	for i := 1; i < len(scopes); i++ {
		result += " " + scopes[i]
	}
	return result
}
