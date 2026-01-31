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

	// MCPProtocolVersion is the MCP protocol version to advertise.
	MCPProtocolVersion = "2024-11-05"
)

// StreamableHTTPConfig holds configuration for the HTTP transport.
type StreamableHTTPConfig struct {
	// URL is the base URL of the MCP server (e.g., "https://mcp.figma.com/mcp").
	URL string

	// BearerToken is the bearer token for authentication (optional).
	BearerToken string

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
	sessionID   string
	endpointURL string // POST endpoint URL (may include session ID query param)
	lastEventID string

	// SSE stream
	sseCancel  context.CancelFunc
	sseConn    io.ReadCloser
	sseScanner *sseScanner

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

// connectSSE establishes the SSE stream (must hold mu lock).
func (t *StreamableHTTPTransport) connectSSE(ctx context.Context) error {
	// Use a timeout for the initial SSE connection attempt.
	// If the server doesn't support SSE or hangs, we'll fall back to POST-only mode.
	connectCtx, connectCancel := context.WithTimeout(ctx, DefaultConnectTimeout)
	defer connectCancel()

	sseCtx, cancel := context.WithCancel(ctx)
	t.sseCancel = cancel

	req, err := http.NewRequestWithContext(connectCtx, "GET", t.config.URL, nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}

	t.setCommonHeaders(req)
	req.Header.Set("Accept", "text/event-stream")

	// Session resumption
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	if t.lastEventID != "" {
		req.Header.Set("Last-Event-ID", t.lastEventID)
	}

	resp, err := t.sseClient.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}

	// Check response
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("unauthorized - authentication required")
		}
		return fmt.Errorf("SSE connect failed: %s", resp.Status)
	}

	// Capture session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionID = sid
	}

	t.sseConn = resp.Body
	t.sseScanner = newSSEScanner(resp.Body, MaxSSEEventSize)

	// Start SSE reader goroutine
	t.wg.Add(1)
	go t.readSSE(sseCtx)

	return nil
}

// readSSE reads events from the SSE stream and queues them.
func (t *StreamableHTTPTransport) readSSE(ctx context.Context) {
	defer t.wg.Done()
	defer func() {
		t.mu.Lock()
		if t.sseConn != nil {
			t.sseConn.Close()
			t.sseConn = nil
		}
		t.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.done:
			return
		default:
		}

		event, err := t.sseScanner.Next()
		if err != nil {
			if ctx.Err() != nil {
				return // Context cancelled, normal shutdown
			}
			// Check if transport is closing
			select {
			case <-t.done:
				return
			default:
			}
			// Send error (non-blocking, check done first)
			select {
			case <-t.done:
				return
			case t.errChan <- fmt.Errorf("SSE read error: %w", err):
			default:
			}
			return
		}

		// Track last event ID for resume
		if event.ID != "" {
			t.mu.Lock()
			t.lastEventID = event.ID
			t.mu.Unlock()
		}

		// Handle endpoint event (legacy HTTP+SSE protocol)
		// The endpoint event contains a URL path with session ID as query param:
		// event: endpoint
		// data: /v1/sse?sessionId=xxxx
		if event.Event == "endpoint" && len(event.Data) > 0 {
			endpointPath := strings.TrimSpace(string(event.Data))
			log.Printf("SSE endpoint event: %s", endpointPath)

			// Parse session ID from the endpoint URL
			if u, err := url.Parse(endpointPath); err == nil {
				if sid := u.Query().Get("sessionId"); sid != "" {
					t.mu.Lock()
					t.sessionID = sid
					t.endpointURL = endpointPath
					t.mu.Unlock()
					log.Printf("Session ID from endpoint: %s", sid)
					// Signal that we're ready (only once)
					t.readyOnce.Do(func() {
						close(t.readyChan)
					})
				}
			}
			continue
		}

		// Skip other non-message events
		if event.Event != "" && event.Event != "message" {
			continue
		}

		// Empty data is a keep-alive
		if len(event.Data) == 0 {
			continue
		}

		log.Printf("SSE Recv: %s", string(event.Data))

		// Send to queue (check done first to avoid send on closed channel)
		select {
		case <-t.done:
			return
		case t.msgQueue <- event.Data:
		case <-ctx.Done():
			return
		}
	}
}

// Send sends a JSON-RPC message via HTTP POST.
func (t *StreamableHTTPTransport) Send(ctx context.Context, msg []byte) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("transport closed")
	}
	sessionID := t.sessionID
	endpointURL := t.endpointURL
	t.mu.Unlock()

	log.Printf("HTTP Send: %s", string(msg))

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

	req, err := http.NewRequestWithContext(ctx, "POST", postURL, bytes.NewReader(msg))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	t.setCommonHeaders(req)
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
	defer resp.Body.Close()

	// Capture session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}

	// Check response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("unauthorized - authentication required")
		}
		return fmt.Errorf("request failed: %s - %s", resp.Status, string(body))
	}

	// Handle response based on content type
	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		// Response is streamed via SSE - read any inline events
		scanner := newSSEScanner(resp.Body, MaxSSEEventSize)
		for {
			event, err := scanner.Next()
			if err != nil {
				if err == io.EOF {
					break
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
	} else if strings.HasPrefix(contentType, "application/json") {
		// Direct JSON response - queue it
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		if len(body) > 0 {
			log.Printf("HTTP Recv: %s", string(body))
			select {
			case <-t.done:
				return errors.New("transport closed")
			case t.msgQueue <- body:
			case <-ctx.Done():
				return ctx.Err()
			}
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
		t.sseConn.Close()
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
func (t *StreamableHTTPTransport) setCommonHeaders(req *http.Request) {
	req.Header.Set("MCP-Protocol-Version", MCPProtocolVersion)

	// Bearer token auth
	if t.config.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.config.BearerToken)
	}

	// Custom headers
	for k, v := range t.config.HTTPHeaders {
		req.Header.Set(k, v)
	}
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
			if !(isLetter || b == '_') {
				return false
			}
			continue
		}
		if !(isLetter || isDigit || b == '_') {
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
