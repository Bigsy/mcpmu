package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const defaultBaseURL = "https://registry.modelcontextprotocol.io"

// Client is an HTTP client for the MCP registry API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a client using the official MCP registry.
func NewClient() *Client {
	return NewClientWithBase(defaultBaseURL)
}

// NewClientWithBase creates a client with a custom base URL (useful for testing).
func NewClientWithBase(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Search queries the registry for servers matching the given query.
// Returns an empty slice (not an error) for zero results.
func (c *Client) Search(ctx context.Context, query string) ([]Server, error) {
	u := fmt.Sprintf("%s/v0.1/servers?search=%s&version=latest", c.baseURL, url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build registry request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("registry search timed out")
		}
		return nil, fmt.Errorf("registry search failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned status %d", resp.StatusCode)
	}

	var searchResp SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("invalid registry response: %w", err)
	}

	servers := make([]Server, len(searchResp.Servers))
	for i, entry := range searchResp.Servers {
		servers[i] = entry.Server
	}
	return servers, nil
}
