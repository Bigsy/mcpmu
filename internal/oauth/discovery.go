package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DiscoveryTimeout is the timeout for OAuth discovery requests.
	DiscoveryTimeout = 5 * time.Second

	// MCPProtocolVersion is the MCP protocol version header value.
	MCPProtocolVersion = "2024-11-05"
)

// AuthorizationServerMetadata holds OAuth server metadata from RFC 8414.
type AuthorizationServerMetadata struct {
	// Required fields
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`

	// Optional but important
	RegistrationEndpoint string   `json:"registration_endpoint,omitempty"`
	RevocationEndpoint   string   `json:"revocation_endpoint,omitempty"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`

	// PKCE support
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`

	// Grant types
	GrantTypesSupported      []string `json:"grant_types_supported,omitempty"`
	ResponseTypesSupported   []string `json:"response_types_supported,omitempty"`
	TokenEndpointAuthMethods []string `json:"token_endpoint_auth_methods_supported,omitempty"`
}

// SupportsS256 returns true if the server supports S256 PKCE.
func (m *AuthorizationServerMetadata) SupportsS256() bool {
	for _, method := range m.CodeChallengeMethodsSupported {
		if method == "S256" {
			return true
		}
	}
	return false
}

// DiscoverResult holds the result of OAuth discovery.
type DiscoverResult struct {
	// Metadata is the discovered authorization server metadata.
	Metadata *AuthorizationServerMetadata

	// URL is the URL where the metadata was found.
	URL string
}

// Discover performs OAuth 2.0 authorization server discovery as per RFC 8414.
// It tries multiple well-known paths as specified in the MCP authorization spec.
func Discover(ctx context.Context, serverURL string) (*DiscoverResult, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}

	// Build discovery URLs to try (per MCP spec):
	// 1. /.well-known/oauth-authorization-server/<path>
	// 2. /<path>/.well-known/oauth-authorization-server
	// 3. /.well-known/oauth-authorization-server
	paths := buildDiscoveryPaths(parsed)

	client := &http.Client{
		Timeout: DiscoveryTimeout,
	}

	var lastErr error
	for _, discoveryURL := range paths {
		result, err := tryDiscovery(ctx, client, discoveryURL)
		if err != nil {
			lastErr = err
			continue
		}
		return result, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("oauth discovery failed: %w", lastErr)
	}
	return nil, errors.New("oauth discovery: no valid metadata found")
}

// buildDiscoveryPaths builds the list of URLs to try for discovery.
func buildDiscoveryPaths(serverURL *url.URL) []string {
	base := serverURL.Scheme + "://" + serverURL.Host
	path := strings.TrimSuffix(serverURL.Path, "/")

	paths := []string{}

	// Path-based discovery (if there's a path)
	if path != "" && path != "/" {
		// /.well-known/oauth-authorization-server/<path>
		// Strip leading slash from path for this variant
		pathPart := strings.TrimPrefix(path, "/")
		paths = append(paths, base+"/.well-known/oauth-authorization-server/"+pathPart)

		// /<path>/.well-known/oauth-authorization-server
		paths = append(paths, base+path+"/.well-known/oauth-authorization-server")
	}

	// Root discovery (always try)
	paths = append(paths, base+"/.well-known/oauth-authorization-server")

	return paths
}

// tryDiscovery attempts to fetch and parse OAuth metadata from a URL.
func tryDiscovery(ctx context.Context, client *http.Client, discoveryURL string) (*DiscoverResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var metadata AuthorizationServerMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	// Validate required fields
	if metadata.AuthorizationEndpoint == "" {
		return nil, errors.New("missing authorization_endpoint")
	}
	if metadata.TokenEndpoint == "" {
		return nil, errors.New("missing token_endpoint")
	}

	return &DiscoverResult{
		Metadata: &metadata,
		URL:      discoveryURL,
	}, nil
}

// SupportsOAuth checks if a server URL supports OAuth by attempting discovery.
// Returns nil if OAuth is not supported.
func SupportsOAuth(ctx context.Context, serverURL string) (*AuthorizationServerMetadata, error) {
	result, err := Discover(ctx, serverURL)
	if err != nil {
		// Not an error - just means OAuth isn't supported
		return nil, nil
	}
	return result.Metadata, nil
}

// ResourceMetadata holds OAuth Protected Resource Metadata per RFC 9728.
// This is returned by the resource_metadata URL from WWW-Authenticate header.
type ResourceMetadata struct {
	// Resource is the protected resource identifier (URL).
	Resource string `json:"resource"`

	// AuthorizationServers are the OAuth authorization server URLs.
	AuthorizationServers []string `json:"authorization_servers"`

	// ScopesSupported are the scopes available for this resource.
	ScopesSupported []string `json:"scopes_supported,omitempty"`

	// BearerMethodsSupported indicates how bearer tokens can be sent.
	BearerMethodsSupported []string `json:"bearer_methods_supported,omitempty"`

	// ResourceDocumentation is a URL for human-readable documentation.
	ResourceDocumentation string `json:"resource_documentation,omitempty"`
}

// AuthChallenge is an alias for BearerChallenge for backward compatibility.
// Deprecated: Use BearerChallenge directly.
type AuthChallenge = BearerChallenge

// DiscoverFromChallenge discovers OAuth metadata from a 401 WWW-Authenticate challenge.
// This implements RFC 9728 OAuth Protected Resource Metadata flow:
// 1. Fetch the resource_metadata URL from the challenge
// 2. Parse authorization_servers from the response
// 3. Do standard discovery on the authorization server URL
func DiscoverFromChallenge(ctx context.Context, challenge *BearerChallenge) (*DiscoverResult, error) {
	if challenge == nil || challenge.ResourceMetadata == "" {
		return nil, errors.New("no resource_metadata in challenge")
	}

	// Step 1: Fetch resource metadata
	resourceMeta, err := fetchResourceMetadata(ctx, challenge.ResourceMetadata)
	if err != nil {
		return nil, fmt.Errorf("fetch resource metadata: %w", err)
	}

	if len(resourceMeta.AuthorizationServers) == 0 {
		return nil, errors.New("resource metadata has no authorization_servers")
	}

	// Step 2: Do standard discovery on the authorization server URL
	// Try each authorization server in order
	var lastErr error
	for _, authServerURL := range resourceMeta.AuthorizationServers {
		result, err := Discover(ctx, authServerURL)
		if err != nil {
			lastErr = err
			continue
		}
		return result, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("discovery on auth servers failed: %w", lastErr)
	}
	return nil, errors.New("no valid authorization server found")
}

// fetchResourceMetadata fetches and parses OAuth Protected Resource Metadata (RFC 9728).
func fetchResourceMetadata(ctx context.Context, metadataURL string) (*ResourceMetadata, error) {
	client := &http.Client{Timeout: DiscoveryTimeout}

	req, err := http.NewRequestWithContext(ctx, "GET", metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var metadata ResourceMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	return &metadata, nil
}
