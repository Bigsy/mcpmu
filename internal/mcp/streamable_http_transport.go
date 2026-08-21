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

	// SSEReconnectMinUptime is how long a standalone SSE stream must stay up to
	// count as working when it delivered no events. Below this, a server that
	// accepts the GET and immediately closes it is treated as a failure so the
	// backoff keeps growing instead of reconnecting every base delay forever.
	SSEReconnectMinUptime = 5 * time.Second
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
	sessionID string
	// expiredSessionID latches the ID of a session the server 404'd until a
	// replacement session is established. While latched, Send fails every
	// non-initialize frame locally with SessionExpiredError instead of
	// sending it: sessionID is already cleared, and a request without the
	// header would come back 400 ("missing Mcp-Session-Id"), which nothing
	// recognises as expiry — the client would be stuck failing forever. The
	// local error routes every caller into the Client's reinitialize path.
	expiredSessionID  string
	endpointURL       string // POST endpoint URL (may include session ID query param)
	lastEventID       string
	negotiatedVersion string // Protocol version negotiated with server

	// Standalone server→client SSE stream (the GET stream). Opened after the
	// first successful POST has settled the protocol version and session ID.
	// sseActive guards against double-starts; unlike a sync.Once it is
	// cleared on session expiry so the fresh session reopens the stream.
	sseCancel context.CancelFunc // cancels just the stream, derived from baseCtx
	sseConn   io.ReadCloser      // active stream body, so Close can unblock the read
	sseActive bool

	// Message queue for received messages from SSE
	msgQueue chan []byte

	// Ready signal - closed when session ID is received (for legacy HTTP+SSE)
	readyChan chan struct{}
	readyOnce sync.Once

	// Shutdown coordination. baseCtx is cancelled by Close and is the parent of
	// every background stream, so no reader can outlive the transport.
	baseCtx    context.Context
	baseCancel context.CancelFunc
	done       chan struct{}
	wg         sync.WaitGroup
	mu         sync.Mutex
	closed     bool
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

	baseCtx, baseCancel := context.WithCancel(context.Background())

	return &StreamableHTTPTransport{
		config:     config,
		sseClient:  sseClient,
		rpcClient:  rpcClient,
		msgQueue:   make(chan []byte, 100),
		readyChan:  make(chan struct{}),
		baseCtx:    baseCtx,
		baseCancel: baseCancel,
		done:       make(chan struct{}),
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
	expiredSessionID := t.expiredSessionID
	endpointURL := t.endpointURL
	negotiatedVersion := t.negotiatedVersion
	t.mu.Unlock()

	if sessionID == "" && expiredSessionID != "" {
		// Expired and not yet replaced. Only initialize may go to the wire —
		// it is how the replacement session is minted (and the spec forbids
		// it carrying a session header, so the cleared state is correct for
		// it). Everything else fails locally as expired so the Client
		// reinitializes, however many callers race into this window.
		var m struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(msg, &m); err != nil || m.Method != "initialize" {
			return &SessionExpiredError{}
		}
	}

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
			return &UpstreamHTTPError{Code: resp.StatusCode, Status: resp.Status, Body: bodyStr}
		}

		// A server may rotate the session ID even on an error response (for
		// example, alongside a 401). An explicit replacement settles the expiry
		// question immediately; an error with no replacement must leave any
		// existing expiry latch intact.
		responseSessionID := resp.Header.Get("Mcp-Session-Id")
		if responseSessionID != "" {
			t.mu.Lock()
			t.sessionID = responseSessionID
			t.expiredSessionID = ""
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
			if resp.StatusCode == http.StatusNotFound && sessionID != "" {
				// The server no longer knows our session. Per the Streamable
				// HTTP spec the client MUST start over: clear the session
				// state so the caller (Client) can reinitialize, and stop the
				// stale standalone stream so the fresh session reopens it.
				// (Callers that race in after the clearing never reach the
				// wire — the expiredSessionID latch fails them locally.)
				t.handleSessionExpired(sessionID)
				return &SessionExpiredError{}
			}
			return &UpstreamHTTPError{Code: resp.StatusCode, Status: resp.Status, Body: string(body)}
		}

		// A successful POST with no session header means this server issues no
		// sessions, so it also settles the expiry question. Keep this after the
		// status check: a stale 404 without a replacement ID must not clear a
		// latch set concurrently by the GET stream.
		if responseSessionID == "" {
			t.mu.Lock()
			t.expiredSessionID = ""
			t.mu.Unlock()
		}

		// Success! Store the negotiated version
		if negotiatedVersion == "" || negotiatedVersion != version {
			t.mu.Lock()
			t.negotiatedVersion = version
			t.mu.Unlock()
			log.Printf("HTTP negotiated protocol version: %s", version)
		}

		// The version and session ID are settled, so the server→client stream can
		// be opened now. Without it there is nowhere for notifications/
		// resources/updated or notifications/tools/list_changed to arrive.
		t.startStandaloneSSE()

		// Handle response based on content type
		contentType := resp.Header.Get("Content-Type")
		if strings.HasPrefix(contentType, "text/event-stream") {
			// Drain in the background. The spec allows a server to keep this
			// stream open after the response event to push further messages, and
			// reading it here would block Send — which holds Client.sendMu, so
			// every other RPC on this transport would queue behind it.
			t.drainResponseStream(ctx, resp.Body)
			return nil
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

// pumpSSE reads an SSE stream and queues each message event for Receive. It
// returns the number of messages queued, which the standalone stream uses to
// decide whether a connection was worth resetting its backoff for.
//
// Every event ID is recorded so a reconnect can resume with Last-Event-ID.
func (t *StreamableHTTPTransport) pumpSSE(ctx context.Context, body io.Reader) (int, error) {
	scanner := newSSEScanner(body, MaxSSEEventSize)
	delivered := 0
	for {
		event, err := scanner.Next()
		if err != nil {
			if err == io.EOF {
				return delivered, nil
			}
			return delivered, fmt.Errorf("read SSE stream: %w", err)
		}
		if event.ID != "" {
			t.mu.Lock()
			t.lastEventID = event.ID
			t.mu.Unlock()
		}

		// Legacy HTTP+SSE servers announce the POST endpoint on the stream
		// rather than accepting posts to the base URL. Send resolves this
		// against the base URL when it is set.
		if event.Event == "endpoint" {
			if endpoint := strings.TrimSpace(string(event.Data)); endpoint != "" {
				t.mu.Lock()
				t.endpointURL = endpoint
				t.mu.Unlock()
				log.Printf("HTTP transport learned POST endpoint from SSE stream: %s", endpoint)
			}
			continue
		}

		if len(event.Data) > 0 && (event.Event == "" || event.Event == "message") {
			select {
			case <-t.done:
				return delivered, errors.New("transport closed")
			case t.msgQueue <- event.Data:
				delivered++
			case <-ctx.Done():
				return delivered, ctx.Err()
			}
		}
	}
}

// drainResponseStream consumes an SSE POST response without blocking Send.
//
// The stream is tied to the caller's context, so it ends when that request's
// context does. That is the right lifetime for a response stream: the reply has
// already been queued, and the standalone GET stream is the channel for
// server-initiated messages.
func (t *StreamableHTTPTransport) drainResponseStream(ctx context.Context, body io.ReadCloser) {
	if !t.trackGoroutine() {
		_ = body.Close()
		return
	}

	go func() {
		defer t.wg.Done()
		// Closing the body is what unblocks a scanner parked on a stream the
		// server is holding open, so Close cannot be made to wait on it.
		stop := context.AfterFunc(t.baseCtx, func() { _ = body.Close() })
		defer stop()
		defer func() { _ = body.Close() }()

		if _, err := t.pumpSSE(ctx, body); err != nil && DebugLogging {
			log.Printf("HTTP POST response stream ended: %v", err)
		}
	}()
}

// trackGoroutine registers a background reader with the shutdown WaitGroup,
// reporting false if the transport is already closing. Because Close sets
// closed under mu before it waits, an Add that observed closed==false is
// guaranteed to be visible to that Wait.
func (t *StreamableHTTPTransport) trackGoroutine() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.wg.Add(1)
	return true
}

// startStandaloneSSE opens the server→client SSE stream, once per session.
//
// This is the only channel for messages the server originates rather than sends
// in reply: notifications/resources/updated for a subscribed resource, and
// notifications/tools/list_changed. Without it, resources/subscribe succeeds
// against an HTTP upstream and then never delivers anything.
//
// The guard is restartable: handleSessionExpired clears sseActive along with
// the session state, so the first successful POST of the replacement session
// opens a fresh stream. A stream that ends on its own (the server declined it
// permanently) leaves sseActive set — reconnecting would just repeat.
func (t *StreamableHTTPTransport) startStandaloneSSE() {
	t.mu.Lock()
	if t.closed || t.sseActive {
		t.mu.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(t.baseCtx)
	t.sseCancel = cancel
	t.sseActive = true
	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.wg.Done()
		t.runStandaloneSSE(streamCtx)
	}()
}

// handleSessionExpired resets per-session state after the server answered 404
// for a session it once issued. No-op if a concurrent recovery already
// replaced the session.
func (t *StreamableHTTPTransport) handleSessionExpired(stale string) {
	t.mu.Lock()
	if t.sessionID != stale {
		t.mu.Unlock()
		return
	}
	log.Printf("HTTP session %s expired at %s; clearing session state", stale, t.config.URL)
	t.sessionID = ""
	t.expiredSessionID = stale
	t.negotiatedVersion = ""
	t.lastEventID = ""
	sseCancel := t.sseCancel
	sseConn := t.sseConn
	t.sseCancel = nil
	t.sseConn = nil
	t.sseActive = false
	t.mu.Unlock()

	if sseCancel != nil {
		sseCancel()
	}
	if sseConn != nil {
		_ = sseConn.Close()
	}
}

// runStandaloneSSE keeps the stream up, backing off between attempts. It stops
// for good once openStandaloneSSE reports the server has no stream to give.
func (t *StreamableHTTPTransport) runStandaloneSSE(ctx context.Context) {
	delay := SSEReconnectBaseDelay
	for {
		if ctx.Err() != nil {
			return
		}

		productive, retry := t.openStandaloneSSE(ctx)
		if !retry {
			return
		}
		if productive {
			// The stream worked; treat the next failure as a fresh one rather
			// than continuing to escalate from a long-past problem.
			delay = SSEReconnectBaseDelay
		}

		select {
		case <-ctx.Done():
			return
		case <-t.done:
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, SSEReconnectMaxDelay)
	}
}

// openStandaloneSSE runs one GET attempt. productive reports whether the stream
// did any useful work; retry reports whether reconnecting could ever help.
func (t *StreamableHTTPTransport) openStandaloneSSE(ctx context.Context) (productive, retry bool) {
	t.mu.Lock()
	sessionID := t.sessionID
	version := t.negotiatedVersion
	lastEventID := t.lastEventID
	t.mu.Unlock()
	if version == "" {
		version = SupportedProtocolVersions[0]
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.config.URL, nil)
	if err != nil {
		log.Printf("SSE stream request for %s could not be built: %v", t.config.URL, err)
		return false, false
	}
	if err := t.setCommonHeaders(ctx, req, version); err != nil {
		// Usually a token that could not be resolved; it may resolve later.
		log.Printf("SSE stream headers for %s: %v", t.config.URL, err)
		return false, true
	}
	req.Header.Set("Accept", "text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}

	resp, err := t.sseClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, false
		}
		if DebugLogging {
			log.Printf("SSE stream connect to %s failed: %v", t.config.URL, err)
		}
		return false, true
	}

	switch resp.StatusCode {
	case http.StatusOK:
		// Fall through to streaming below.
	case http.StatusNotFound:
		_ = resp.Body.Close()
		if sessionID != "" {
			// Same meaning as a 404 on the POST path: the server no longer
			// knows this session (idle reap, DELETE, restart). Clearing the
			// session state stops this stream and re-arms sseActive, so the
			// reinitialize triggered by the next Send opens a fresh stream.
			// Without this the transport mistakes an expired session for a
			// server that has no stream to give and never reconnects.
			t.handleSessionExpired(sessionID)
			return false, false
		}
		// No session to expire: a server that simply does not route GET here.
		log.Printf("Server %s offers no SSE stream (HTTP 404); server-initiated "+
			"notifications such as resources/updated will not arrive", t.config.URL)
		return false, false
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		// A server entitled to decline the stream. Retrying would just repeat.
		_ = resp.Body.Close()
		log.Printf("Server %s offers no SSE stream (HTTP %d); server-initiated "+
			"notifications such as resources/updated will not arrive",
			t.config.URL, resp.StatusCode)
		return false, false
	case http.StatusUnauthorized, http.StatusForbidden:
		// The POST path owns authentication and its own 401 handling; retrying
		// here would hammer the endpoint with the same rejected credentials.
		_ = resp.Body.Close()
		log.Printf("SSE stream for %s rejected with HTTP %d; not retrying",
			t.config.URL, resp.StatusCode)
		return false, false
	default:
		_ = resp.Body.Close()
		if DebugLogging {
			log.Printf("SSE stream for %s returned HTTP %d", t.config.URL, resp.StatusCode)
		}
		return false, true
	}

	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		_ = resp.Body.Close()
		log.Printf("SSE stream for %s answered with Content-Type %q, not text/event-stream; not retrying",
			t.config.URL, contentType)
		return false, false
	}

	t.mu.Lock()
	t.sseConn = resp.Body
	t.mu.Unlock()

	startedAt := time.Now()
	delivered, pumpErr := t.pumpSSE(ctx, resp.Body)

	t.mu.Lock()
	if t.sseConn == resp.Body {
		t.sseConn = nil
	}
	t.mu.Unlock()
	_ = resp.Body.Close()

	if ctx.Err() != nil {
		return false, false
	}
	if pumpErr != nil && DebugLogging {
		log.Printf("SSE stream for %s ended: %v", t.config.URL, pumpErr)
	}

	// A stream that delivered something, or simply stayed up, was working; one
	// that connected and immediately ended was not, and should back off.
	productive = delivered > 0 || time.Since(startedAt) >= SSEReconnectMinUptime
	return productive, true
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

	// Cancel every background stream. baseCancel also aborts in-flight GETs and,
	// via the AfterFunc in drainResponseStream, closes POST response bodies the
	// server is holding open.
	t.baseCancel()

	t.mu.Lock()
	sseCancel := t.sseCancel
	sseConn := t.sseConn
	t.sseConn = nil
	t.mu.Unlock()

	if sseCancel != nil {
		sseCancel()
	}
	// Close the active stream body too: cancelling the context unblocks the read
	// on its own, but this makes the teardown independent of that.
	if sseConn != nil {
		_ = sseConn.Close()
	}

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
		before, after, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			field = line
			value = nil
		} else {
			field = before
			value = after
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

// SessionExpiredError is returned by Send when a POST carrying a session ID
// came back 404 — the server has expired or forgotten the session. The
// transport has already cleared its session state; the client is expected to
// reinitialize and retry.
type SessionExpiredError struct{}

func (e *SessionExpiredError) Error() string {
	return "session expired - server no longer recognizes the session ID"
}

// UnauthorizedError is returned on HTTP 401 responses.
// It preserves the WWW-Authenticate challenge info so callers can use
// errors.As() to extract challenge info for OAuth discovery.
type UnauthorizedError struct {
	Challenge *oauth.BearerChallenge
}

func (e *UnauthorizedError) Error() string {
	return "unauthorized - authentication required"
}

// UpstreamHTTPError is returned when a request to an HTTP upstream fails with
// a status that has no dedicated handling (success is 200/202; 400 carries
// version-rejection parsing; 401 becomes UnauthorizedError; a 404 for a
// session the server once issued becomes SessionExpiredError). Code carries
// the numeric status so callers classify structurally via errors.As instead
// of substring-matching error strings — a string match on "4" once treated
// every 4xx, 403 and 429 included, as a restartable failure.
type UpstreamHTTPError struct {
	Code   int    // numeric status, e.g. 403
	Status string // status line, e.g. "403 Forbidden"
	Body   string // up to 1 KiB of the response body, for diagnostics
}

func (e *UpstreamHTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("request failed: %s - %s", e.Status, e.Body)
	}
	return "request failed: " + e.Status
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
