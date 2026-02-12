package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/oauth"
)

const (
	// MaxSSEEventSize is the maximum size of a single SSE event (1MB).
	MaxSSEEventSize = 1024 * 1024

	// DefaultConnectTimeout is the timeout for initial HTTP connections.
	DefaultConnectTimeout = 30 * time.Second

	// SSEReconnectBaseDelay is the base delay for SSE reconnection.
	SSEReconnectBaseDelay = 500 * time.Millisecond

	// SSEReconnectMaxDelay is the maximum delay for SSE reconnection.
	SSEReconnectMaxDelay = 30 * time.Second
)

// SupportedProtocolVersions lists the MCP protocol versions we support,
// in order of preference (newest first). During connection, we try each
// version until one is accepted by the server.
var SupportedProtocolVersions = []string{
	"2025-11-25", // current
	"2025-06-18",
	"2025-03-26",
	"2024-11-05", // legacy fallback
}

// StreamableHTTPConfig holds configuration for the HTTP transport.
type StreamableHTTPConfig struct {
	// URL is the base URL of the MCP server (e.g., "https://mcp.figma.com/mcp").
	URL string

	// BearerToken is the bearer token for authentication (optional).
	BearerToken string

	// BearerTokenProvider resolves a bearer token for each request (optional).
	// When set, it takes precedence over BearerToken.
	BearerTokenProvider func(context.Context) (string, error)

	// HTTPHeaders are static headers to include in all requests.
	HTTPHeaders map[string]string

	// Client is the HTTP client to use. If nil, http.DefaultClient is used.
	Client *http.Client
}

// StreamableHTTPTransport implements Transport over HTTP with SSE streaming.
// It uses POST for sending JSON-RPC requests and GET for the SSE event stream.
type StreamableHTTPTransport struct {
	config    StreamableHTTPConfig
	sseClient *http.Client // Client for SSE (no timeout - long-lived)
	rpcClient *http.Client // Client for POST requests (with timeout)

	// Session state
	sessionID         string
	endpointURL       string // POST endpoint URL (may include session ID query param)
	lastEventID       string
	negotiatedVersion string // Protocol version negotiated with server

	// SSE stream
	sseCancel context.CancelFunc
	sseConn   io.ReadCloser

	// Message queue for received messages from SSE
	msgQueue chan []byte
	errChan  chan error

	// Ready signal - closed when session ID is received (for legacy HTTP+SSE)
	readyChan chan struct{}
	readyOnce sync.Once

	// Shutdown coordination
	done   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// NewStreamableHTTPTransport creates a new HTTP transport for MCP.
func NewStreamableHTTPTransport(config StreamableHTTPConfig) *StreamableHTTPTransport {
	baseClient := config.Client
	if baseClient == nil {
		baseClient = http.DefaultClient
	}

	// Ensure we don't use http.Client.Timeout for SSE or potentially streamed responses.
	// Client timeouts are managed by context cancellation and transport-level timeouts.
	sseClient := cloneHTTPClient(baseClient)
	rpcClient := cloneHTTPClient(baseClient)

	return &StreamableHTTPTransport{
		config:    config,
		sseClient: sseClient,
		rpcClient: rpcClient,
		msgQueue:  make(chan []byte, 100),
		errChan:   make(chan error, 1),
		readyChan: make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Connect prepares the transport for use.
// Per MCP spec (2025-03-26), Streamable HTTP uses POST for requests.
// SSE is optional and only used if the server indicates support via response headers.
// Legacy SSE-only servers (pre-2025) should be detected by POST failing with 4xx.
func (t *StreamableHTTPTransport) Connect(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("transport closed")
	}
	t.mu.Unlock()

	// Per MCP spec: try POST first (Streamable HTTP).
	// SSE GET is only for backwards compatibility with legacy servers.
	// Signal ready for POST-based communication immediately.
	t.readyOnce.Do(func() {
		close(t.readyChan)
	})
	return nil
}

// Send sends a JSON-RPC message via HTTP POST.
// On version rejection (400 with "Unsupported MCP-Protocol-Version"), it automatically
// retries with the next supported version until one is accepted.
func (t *StreamableHTTPTransport) Send(ctx context.Context, msg []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("transport closed")
	}
	sessionID := t.sessionID
	endpointURL := t.endpointURL
	negotiatedVersion := t.negotiatedVersion
	t.mu.Unlock()

	if DebugLogging {
		log.Printf("HTTP Send: %s", string(msg))
	}

	// Build the POST URL - use endpoint URL if we got one from SSE (legacy HTTP+SSE protocol),
	// otherwise use the base URL (newer Streamable HTTP protocol)
	postURL := t.config.URL
	if endpointURL != "" {
		// Legacy HTTP+SSE: endpoint URL is a path like "/v1/sse?sessionId=xxx"
		// We need to resolve it against the base URL
		if baseURL, err := url.Parse(t.config.URL); err == nil {
			if epURL, err := url.Parse(endpointURL); err == nil {
				postURL = baseURL.ResolveReference(epURL).String()
			}
		}
	} else if sessionID != "" {
		// No endpoint URL but we have a session ID - might need to add as query param
		// for servers that expect it that way (fallback)
		if u, err := url.Parse(postURL); err == nil {
			q := u.Query()
			q.Set("sessionId", sessionID)
			u.RawQuery = q.Encode()
			postURL = u.String()
		}
	}

	// Determine which versions to try
	versionsToTry := SupportedProtocolVersions
	startIdx := 0
	if negotiatedVersion != "" {
		// Already negotiated - start from that version but allow fallback if rejected
		for i, v := range SupportedProtocolVersions {
			if v == negotiatedVersion {
				startIdx = i
				break
			}
		}
		versionsToTry = SupportedProtocolVersions[startIdx:]
	}

	var lastErr error
	for i, version := range versionsToTry {
		req, err := http.NewRequestWithContext(ctx, "POST", postURL, bytes.NewReader(msg))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		if err := t.setCommonHeaders(ctx, req, version); err != nil {
			return fmt.Errorf("set headers: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")

		// Also set session ID header for servers that expect it there (Streamable HTTP protocol)
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}

		resp, err := t.rpcClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}

		// Check for version rejection (400 Bad Request with version error)
		// Allow re-negotiation even if we thought we had a version, since some servers
		// are lenient on first request but strict on subsequent requests
		if resp.StatusCode == http.StatusBadRequest {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			bodyStr := string(body)

			// Check if this is a version rejection
			if isVersionRejection(bodyStr) {
				log.Printf("HTTP version %s rejected by server, trying next version", version)
				lastErr = fmt.Errorf("version %s rejected: %s", version, bodyStr)

				// Clear the negotiated version since it was wrong
				if negotiatedVersion != "" {
					t.mu.Lock()
					t.negotiatedVersion = ""
					t.mu.Unlock()
					negotiatedVersion = ""
				}

				// Try next version
				if i < len(versionsToTry)-1 {
					continue
				}
				return fmt.Errorf("all protocol versions rejected by server: %w", lastErr)
			}

			// Not a version rejection - return the error
			return fmt.Errorf("request failed: %s - %s", resp.Status, bodyStr)
		}

		// Capture session ID from response
		if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
			t.mu.Lock()
			t.sessionID = sid
			t.mu.Unlock()
		}

		// Check response status
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				// Parse WWW-Authenticate headers for OAuth discovery (RFC 9728)
				// Uses all header values to find Bearer challenge with resource_metadata
				challenge := oauth.ParseBearerChallenge(resp.Header)
				return &UnauthorizedError{Challenge: challenge}
			}
			return fmt.Errorf("request failed: %s - %s", resp.Status, string(body))
		}

		// Success! Store the negotiated version
		if negotiatedVersion == "" || negotiatedVersion != version {
			t.mu.Lock()
			t.negotiatedVersion = version
			t.mu.Unlock()
			log.Printf("HTTP negotiated protocol version: %s", version)
		}

		// Handle response based on content type
		contentType := resp.Header.Get("Content-Type")
		if strings.HasPrefix(contentType, "text/event-stream") {
			// Response is streamed via SSE - read any inline events
			err = t.handleSSEResponse(ctx, resp.Body)
			_ = resp.Body.Close()
			return err
		} else if strings.HasPrefix(contentType, "application/json") {
			// Direct JSON response - queue it
			err = t.handleJSONResponse(ctx, resp.Body)
			_ = resp.Body.Close()
			return err
		}

		_ = resp.Body.Close()
		return nil
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("no protocol versions to try")
}

// isVersionRejection checks if an error response indicates a protocol version rejection.
func isVersionRejection(body string) bool {
	bodyLower := strings.ToLower(body)
	return strings.Contains(bodyLower, "unsupported") && strings.Contains(bodyLower, "version") ||
		strings.Contains(bodyLower, "protocol-version") ||
		strings.Contains(bodyLower, "protocolversion")
}

// handleSSEResponse processes an SSE stream response.
func (t *StreamableHTTPTransport) handleSSEResponse(ctx context.Context, body io.Reader) error {
	scanner := newSSEScanner(body, MaxSSEEventSize)
	for {
		event, err := scanner.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read SSE response: %w", err)
		}
		if event.ID != "" {
			t.mu.Lock()
			t.lastEventID = event.ID
			t.mu.Unlock()
		}
		if len(event.Data) > 0 && (event.Event == "" || event.Event == "message") {
			select {
			case <-t.done:
				return errors.New("transport closed")
			case t.msgQueue <- event.Data:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// handleJSONResponse processes a JSON response.
func (t *StreamableHTTPTransport) handleJSONResponse(ctx context.Context, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) > 0 {
		if DebugLogging {
			log.Printf("HTTP Recv: %s", string(data))
		}
		select {
		case <-t.done:
			return errors.New("transport closed")
		case t.msgQueue <- data:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Receive reads the next JSON-RPC message from the SSE stream or POST response.
func (t *StreamableHTTPTransport) Receive(ctx context.Context) ([]byte, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("transport closed")
	}
	t.mu.Unlock()

	select {
	case msg, ok := <-t.msgQueue:
		if !ok {
			return nil, errors.New("transport closed")
		}
		return msg, nil
	case err, ok := <-t.errChan:
		if !ok {
			return nil, errors.New("transport closed")
		}
		return nil, err
	case <-t.done:
		return nil, errors.New("transport closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the transport and all connections.
func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	// Signal shutdown to all goroutines
	close(t.done)

	// Cancel SSE context
	if t.sseCancel != nil {
		t.sseCancel()
	}

	// Close SSE connection to unblock reads
	t.mu.Lock()
	if t.sseConn != nil {
		_ = t.sseConn.Close()
		t.sseConn = nil
	}
	t.mu.Unlock()

	// Wait for goroutines to finish before closing channels
	t.wg.Wait()

	return nil
}

// SessionID returns the current session ID, if any.
func (t *StreamableHTTPTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// setCommonHeaders sets headers common to all requests.
func (t *StreamableHTTPTransport) setCommonHeaders(ctx context.Context, req *http.Request, version string) error {
	req.Header.Set("MCP-Protocol-Version", version)

	// Bearer token auth
	if t.config.BearerTokenProvider != nil {
		token, err := t.config.BearerTokenProvider(ctx)
		if err != nil {
			return fmt.Errorf("resolve bearer token: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	} else if t.config.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.config.BearerToken)
	}

	// Custom headers
	for k, v := range t.config.HTTPHeaders {
		req.Header.Set(k, v)
	}

	return nil
}

// NegotiatedVersion returns the protocol version negotiated with the server.
// Returns empty string if no version has been negotiated yet.
func (t *StreamableHTTPTransport) NegotiatedVersion() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.negotiatedVersion
}

// sseEvent represents a single SSE event.
type sseEvent struct {
	ID    string
	Event string
	Data  []byte
}

// sseScanner parses SSE events from a reader.
type sseScanner struct {
	reader   *bufio.Reader
	maxSize  int
	currSize int
}

func newSSEScanner(r io.Reader, maxSize int) *sseScanner {
	return &sseScanner{
		reader:  bufio.NewReader(r),
		maxSize: maxSize,
	}
}

// Next reads the next SSE event.
func (s *sseScanner) Next() (*sseEvent, error) {
	event := &sseEvent{}
	var dataLines [][]byte
	s.currSize = 0

	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF && len(dataLines) > 0 {
				// Incomplete event at EOF
				event.Data = bytes.Join(dataLines, []byte("\n"))
				return event, nil
			}
			return nil, err
		}

		// Track size to prevent unbounded buffering
		s.currSize += len(line)
		if s.currSize > s.maxSize {
			return nil, fmt.Errorf("SSE event exceeds maximum size of %d bytes", s.maxSize)
		}

		// Trim CRLF or LF
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))

		// Empty line = dispatch event
		if len(line) == 0 {
			if len(dataLines) > 0 || event.ID != "" || event.Event != "" {
				event.Data = bytes.Join(dataLines, []byte("\n"))
				return event, nil
			}
			continue // Skip empty events
		}

		// Comment line (starts with :)
		if line[0] == ':' {
			continue
		}

		// Parse field
		var field, value []byte
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx == -1 {
			field = line
			value = nil
		} else {
			field = line[:colonIdx]
			value = line[colonIdx+1:]
			// Remove leading space from value if present
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
		}

		switch string(field) {
		case "id":
			event.ID = string(value)
		case "event":
			event.Event = string(value)
		case "data":
			dataLines = append(dataLines, value)
		case "retry":
			// Ignore retry field for now
		}
	}
}

// AuthStatus represents the authentication status of a server.
type AuthStatus string

const (
	AuthStatusNone       AuthStatus = "-"
	AuthStatusBearer     AuthStatus = "bearer"
	AuthStatusOAuthOK    AuthStatus = "oauth:logged-in"
	AuthStatusOAuthNeeds AuthStatus = "oauth:needs-login"
	AuthStatusOAuthExp   AuthStatus = "oauth:expired"
)

// AuthChallenge is an alias for oauth.BearerChallenge for backward compatibility.
// Deprecated: Use oauth.BearerChallenge directly.
type AuthChallenge = oauth.BearerChallenge

// UnauthorizedError is returned on HTTP 401 responses.
// It preserves the WWW-Authenticate challenge info so callers can use
// errors.As() to extract challenge info for OAuth discovery.
type UnauthorizedError struct {
	Challenge *oauth.BearerChallenge
}

func (e *UnauthorizedError) Error() string {
	return "unauthorized - authentication required"
}

// HTTPClientConfig holds configuration for creating an HTTP transport from server config.
type HTTPClientConfig struct {
	URL         string
	BearerToken string
	Headers     map[string]string
}

// ValidateBearerTokenEnvVar checks if the bearer token environment variable is set.
// Returns an error if the env var is configured but not present.
func ValidateBearerTokenEnvVar(envVarName string) (string, error) {
	if envVarName == "" {
		return "", nil
	}
	if !isValidEnvVarName(envVarName) {
		return "", fmt.Errorf("invalid bearer token env var name %q", envVarName)
	}
	val, ok := os.LookupEnv(envVarName)
	if !ok || strings.TrimSpace(val) == "" {
		return "", fmt.Errorf("bearer token env var %s is not set", envVarName)
	}
	if strings.ContainsAny(val, "\r\n") {
		return "", fmt.Errorf("bearer token env var %s must not contain newlines", envVarName)
	}
	return val, nil
}

// MarshalJSON for AuthStatus to use string representation.
func (a AuthStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(a))
}

func cloneHTTPClient(base *http.Client) *http.Client {
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	c.Timeout = 0

	if c.Transport == nil {
		c.Transport = defaultHTTPTransport()
		return c
	}
	if t, ok := c.Transport.(*http.Transport); ok {
		tt := t.Clone()
		if tt.ResponseHeaderTimeout == 0 {
			tt.ResponseHeaderTimeout = DefaultConnectTimeout
		}
		if tt.TLSHandshakeTimeout == 0 {
			tt.TLSHandshakeTimeout = DefaultConnectTimeout
		}
		if tt.DialContext == nil {
			tt.DialContext = (&net.Dialer{
				Timeout:   DefaultConnectTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext
		}
		c.Transport = tt
	}
	return c
}

func defaultHTTPTransport() *http.Transport {
	// Start from Go's defaults and add a header timeout so requests that never
	// respond don't hang indefinitely, without imposing a hard deadline for
	// long-lived response bodies like SSE.
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t := dt.Clone()
		t.ResponseHeaderTimeout = DefaultConnectTimeout
		if t.TLSHandshakeTimeout == 0 {
			t.TLSHandshakeTimeout = DefaultConnectTimeout
		}
		return t
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   DefaultConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   DefaultConnectTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: DefaultConnectTimeout,
	}
}

func isValidEnvVarName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		isLetter := (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
		isDigit := b >= '0' && b <= '9'
		if i == 0 {
			if !isLetter && b != '_' {
				return false
			}
			continue
		}
		if !isLetter && !isDigit && b != '_' {
			return false
		}
	}
	return true
}

// UnmarshalJSON for AuthStatus.
func (a *AuthStatus) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*a = AuthStatus(s)
	return nil
}
