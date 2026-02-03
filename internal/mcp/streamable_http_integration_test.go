package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// MockMCPServer simulates an MCP server with SSE streaming
type MockMCPServer struct {
	t             *testing.T
	server        *httptest.Server
	sessionID     string
	lastEventID   string
	mu            sync.Mutex
	initResponse  json.RawMessage
	toolsResponse json.RawMessage
}

func NewMockMCPServer(t *testing.T) *MockMCPServer {
	m := &MockMCPServer{
		t:         t,
		sessionID: "test-session-123",
		initResponse: json.RawMessage(`{
			"jsonrpc": "2.0",
			"id": 1,
			"result": {
				"protocolVersion": "2024-11-05",
				"capabilities": {},
				"serverInfo": {"name": "mock-server", "version": "1.0.0"}
			}
		}`),
		toolsResponse: json.RawMessage(`{
			"jsonrpc": "2.0",
			"id": 2,
			"result": {
				"tools": [
					{"name": "test_tool", "description": "A test tool"}
				]
			}
		}`),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", m.handleMCP)

	m.server = httptest.NewServer(mux)
	return m
}

func (m *MockMCPServer) URL() string {
	return m.server.URL + "/mcp"
}

func (m *MockMCPServer) Close() {
	m.server.Close()
}

func (m *MockMCPServer) handleMCP(w http.ResponseWriter, r *http.Request) {
	// Set session ID header
	w.Header().Set("Mcp-Session-Id", m.sessionID)

	switch r.Method {
	case "GET":
		// SSE stream
		m.handleSSE(w, r)
	case "POST":
		// JSON-RPC request
		m.handlePost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *MockMCPServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Check for session resumption
	clientSessionID := r.Header.Get("Mcp-Session-Id")
	clientLastEventID := r.Header.Get("Last-Event-ID")

	m.mu.Lock()
	if clientSessionID != "" && clientSessionID == m.sessionID {
		// Session resumption
		m.t.Logf("Session resumed: %s, last event: %s", clientSessionID, clientLastEventID)
	}
	m.lastEventID = clientLastEventID
	m.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send a keep-alive comment
	_, _ = fmt.Fprint(w, ": keep-alive\n\n")
	flusher.Flush()

	// Keep connection open until client disconnects
	<-r.Context().Done()
}

func (m *MockMCPServer) handlePost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "initialize":
		// Update ID in response
		resp := make(map[string]interface{})
		_ = json.Unmarshal(m.initResponse, &resp)
		resp["id"] = req.ID
		_ = json.NewEncoder(w).Encode(resp)

	case "tools/list":
		resp := make(map[string]interface{})
		_ = json.Unmarshal(m.toolsResponse, &resp)
		resp["id"] = req.ID
		_ = json.NewEncoder(w).Encode(resp)

	default:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
			},
		})
	}
}

func TestStreamableHTTPTransport_Connect(t *testing.T) {
	mock := NewMockMCPServer(t)
	defer mock.Close()

	config := StreamableHTTPConfig{
		URL: mock.URL(),
	}
	transport := NewStreamableHTTPTransport(config)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := transport.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer func() { _ = transport.Close() }()

	// Per MCP spec 2025-03-26, session ID comes from Initialize response header,
	// not from SSE endpoint event. Just verify Connect() succeeds.
}

func TestStreamableHTTPTransport_SendReceive(t *testing.T) {
	mock := NewMockMCPServer(t)
	defer mock.Close()

	config := StreamableHTTPConfig{
		URL: mock.URL(),
	}
	transport := NewStreamableHTTPTransport(config)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer func() { _ = transport.Close() }()

	// Send initialize request
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	if err := transport.Send(ctx, []byte(initReq)); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Receive response
	resp, err := transport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	// Verify response contains expected data
	if !strings.Contains(string(resp), "mock-server") {
		t.Errorf("response should contain 'mock-server': %s", string(resp))
	}
}

func TestStreamableHTTPTransport_BearerAuth(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	config := StreamableHTTPConfig{
		URL:         server.URL,
		BearerToken: "test-token-123",
	}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if receivedAuth != "Bearer test-token-123" {
		t.Errorf("expected 'Bearer test-token-123', got %q", receivedAuth)
	}
}

func TestStreamableHTTPTransport_CustomHeaders(t *testing.T) {
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	config := StreamableHTTPConfig{
		URL: server.URL,
		HTTPHeaders: map[string]string{
			"X-Custom-Header": "custom-value",
			"X-Another":       "another-value",
		},
	}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("expected 'custom-value', got %q", receivedHeaders.Get("X-Custom-Header"))
	}
	if receivedHeaders.Get("X-Another") != "another-value" {
		t.Errorf("expected 'another-value', got %q", receivedHeaders.Get("X-Another"))
	}
}

func TestStreamableHTTPTransport_MCPProtocolVersion(t *testing.T) {
	var receivedVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedVersion = r.Header.Get("MCP-Protocol-Version")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	_ = transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))

	// Server accepts all versions, so we should get the first (newest) version
	expectedVersion := SupportedProtocolVersions[0]
	if receivedVersion != expectedVersion {
		t.Errorf("expected MCP-Protocol-Version %q, got %q", expectedVersion, receivedVersion)
	}

	// Verify version was negotiated
	if transport.NegotiatedVersion() != expectedVersion {
		t.Errorf("expected negotiated version %q, got %q", expectedVersion, transport.NegotiatedVersion())
	}
}

func TestStreamableHTTPTransport_VersionNegotiation_Fallback(t *testing.T) {
	// Simulate a server that only accepts the legacy version (2024-11-05)
	// This tests the automatic fallback behavior
	var attemptedVersions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("MCP-Protocol-Version")
		attemptedVersions = append(attemptedVersions, version)

		// Reject all versions except the legacy one
		if version != "2024-11-05" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":"Unsupported MCP-Protocol-Version: %s"}`, version)
			return
		}

		// Accept legacy version
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Should have tried all versions until finding 2024-11-05
	if len(attemptedVersions) != len(SupportedProtocolVersions) {
		t.Errorf("expected %d version attempts, got %d: %v",
			len(SupportedProtocolVersions), len(attemptedVersions), attemptedVersions)
	}

	// Should have negotiated the legacy version
	if transport.NegotiatedVersion() != "2024-11-05" {
		t.Errorf("expected negotiated version '2024-11-05', got %q", transport.NegotiatedVersion())
	}

	// Subsequent requests should use the negotiated version directly
	attemptedVersions = nil
	err = transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"test"}`))
	if err != nil {
		t.Fatalf("Second Send failed: %v", err)
	}

	if len(attemptedVersions) != 1 {
		t.Errorf("expected 1 version attempt on subsequent request, got %d", len(attemptedVersions))
	}
	if attemptedVersions[0] != "2024-11-05" {
		t.Errorf("expected version '2024-11-05' on subsequent request, got %q", attemptedVersions[0])
	}
}

func TestStreamableHTTPTransport_VersionNegotiation_AllRejected(t *testing.T) {
	// Simulate a server that rejects all versions
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("MCP-Protocol-Version")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":"Unsupported protocol version: %s"}`, version)
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))

	if err == nil {
		t.Fatal("expected error when all versions are rejected")
	}
	if !strings.Contains(err.Error(), "all protocol versions rejected") {
		t.Errorf("expected 'all protocol versions rejected' error, got: %v", err)
	}
}

func TestStreamableHTTPTransport_VersionNegotiation_LenientThenStrict(t *testing.T) {
	// Simulate a server like Sentry that is lenient on first request but strict on subsequent
	// First request accepts any version, second request only accepts specific versions
	var requestCount int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("MCP-Protocol-Version")
		mu.Lock()
		requestCount++
		count := requestCount
		mu.Unlock()

		if count == 1 {
			// First request: accept any version (lenient)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`)
			return
		}

		// Subsequent requests: only accept 2025-06-18 or 2025-03-26
		if version != "2025-06-18" && version != "2025-03-26" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unsupported MCP-Protocol-Version: %s. Supported versions: 2025-03-26, 2025-06-18"}}`, version)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, count)
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()

	// First request succeeds (server is lenient)
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}

	// Read the response
	_, err = transport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive first response failed: %v", err)
	}

	// Second request: server is now strict, should re-negotiate
	err = transport.Send(ctx, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}

	// Check that we negotiated to a supported version
	negotiated := transport.NegotiatedVersion()
	if negotiated != "2025-06-18" && negotiated != "2025-03-26" {
		t.Errorf("expected negotiated version to be 2025-06-18 or 2025-03-26, got %q", negotiated)
	}
}

func TestStreamableHTTPTransport_UnauthorizedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "Unauthorized")
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))

	if err == nil {
		t.Fatal("expected error for unauthorized request")
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("expected 'unauthorized' error, got: %v", err)
	}
}

func TestStreamableHTTPTransport_ForbiddenError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, "Forbidden")
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx := context.Background()
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))

	if err == nil {
		t.Fatal("expected error for forbidden request")
	}
	if strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		t.Errorf("did not expect unauthorized error for 403, got: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to contain status code 403, got: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		t.Errorf("expected error to contain 'forbidden', got: %v", err)
	}
}

func TestStreamableHTTPTransport_SessionIDUpdatedOnError_AllowsRetry(t *testing.T) {
	const (
		session1 = "sid-1"
		session2 = "sid-2"
	)

	var (
		mu      sync.Mutex
		rotated bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First request succeeds and establishes a session.
		gotSID := r.Header.Get("Mcp-Session-Id")
		if gotSID == "" {
			w.Header().Set("Mcp-Session-Id", session1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
			return
		}

		mu.Lock()
		defer mu.Unlock()

		// Second request sees old session; simulate expiration and rotate to a new session ID.
		if gotSID == session1 && !rotated {
			rotated = true
			w.Header().Set("Mcp-Session-Id", session2)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, "session expired")
			return
		}

		// Third request retries with the new session ID and succeeds.
		if gotSID == session2 && rotated {
			w.Header().Set("Mcp-Session-Id", session2)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":3,"result":{}}`)
			return
		}

		http.Error(w, "unexpected session id: "+gotSID, http.StatusBadRequest)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: server.URL})
	defer func() { _ = transport.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Establish initial session.
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`)); err != nil {
		t.Fatalf("Send (initial) failed: %v", err)
	}
	if transport.SessionID() != session1 {
		t.Fatalf("SessionID (after initial): got %q, want %q", transport.SessionID(), session1)
	}
	if _, err := transport.Receive(ctx); err != nil {
		t.Fatalf("Receive (initial) failed: %v", err)
	}

	// Session expires mid-conversation. Send returns an auth error but should still capture the rotated session ID.
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"test"}`)); err == nil {
		t.Fatal("expected error for expired session")
	}
	if transport.SessionID() != session2 {
		t.Fatalf("SessionID (after expiry): got %q, want %q", transport.SessionID(), session2)
	}

	// Caller can retry and succeed with updated session ID.
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"test"}`)); err != nil {
		t.Fatalf("Send (retry) failed: %v", err)
	}
	if _, err := transport.Receive(ctx); err != nil {
		t.Fatalf("Receive (retry) failed: %v", err)
	}
}

func TestStreamableHTTPTransport_SSEInlineResponse(t *testing.T) {
	// Server that returns SSE-formatted response to POST
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "id: 1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"test\":true}}\n\n")
	}))
	defer server.Close()

	config := StreamableHTTPConfig{URL: server.URL}
	transport := NewStreamableHTTPTransport(config)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send request - response will be queued via SSE
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Receive the SSE response
	resp, err := transport.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	if !strings.Contains(string(resp), `"test":true`) {
		t.Errorf("expected response to contain '\"test\":true': %s", string(resp))
	}
}

func TestStreamableHTTPTransport_DoesNotInheritClientTimeout(t *testing.T) {
	config := StreamableHTTPConfig{
		URL: "http://example.invalid",
		Client: &http.Client{
			Timeout: 123 * time.Second,
		},
	}
	transport := NewStreamableHTTPTransport(config)

	if transport.sseClient.Timeout != 0 {
		t.Errorf("sseClient.Timeout: got %v, want 0", transport.sseClient.Timeout)
	}
	if transport.rpcClient.Timeout != 0 {
		t.Errorf("rpcClient.Timeout: got %v, want 0", transport.rpcClient.Timeout)
	}
}

func TestStreamableHTTPTransport_CloseWhileSendInFlight_NoPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: server.URL})

	result := make(chan interface{}, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				result <- r
			}
		}()
		result <- transport.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	}()

	time.Sleep(25 * time.Millisecond)
	_ = transport.Close()

	select {
	case v := <-result:
		if v == nil {
			return // successful completion is fine; test is "no panic"
		}
		if _, ok := v.(error); ok {
			return // error is fine; test is "no panic"
		}
		t.Fatalf("Send panicked after Close: %v", v)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Send to finish")
	}
}

func TestValidateBearerTokenEnvVar(t *testing.T) {
	t.Setenv("MCP_STUDIO_TEST_BEARER", "token123")

	got, err := ValidateBearerTokenEnvVar("MCP_STUDIO_TEST_BEARER")
	if err != nil {
		t.Fatalf("ValidateBearerTokenEnvVar returned error: %v", err)
	}
	if got != "token123" {
		t.Errorf("token: got %q, want %q", got, "token123")
	}

	if _, err := ValidateBearerTokenEnvVar("MCP_STUDIO_NOT_SET"); err == nil {
		t.Fatal("expected error for unset env var")
	}
	if _, err := ValidateBearerTokenEnvVar("1INVALID"); err == nil {
		t.Fatal("expected error for invalid env var name")
	}
}

// LegacySSEMockServer simulates a legacy HTTP+SSE MCP server (like Atlassian)
// that sends session ID via endpoint event and requires it as a query parameter.
type LegacySSEMockServer struct {
	t             *testing.T
	server        *httptest.Server
	sessionID     string
	mu            sync.Mutex
	toolCallCount int
}

func NewLegacySSEMockServer(t *testing.T) *LegacySSEMockServer {
	m := &LegacySSEMockServer{
		t:         t,
		sessionID: "legacy-session-456",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/sse", m.handleSSE)

	m.server = httptest.NewServer(mux)
	return m
}

func (m *LegacySSEMockServer) URL() string {
	return m.server.URL + "/v1/sse"
}

func (m *LegacySSEMockServer) Close() {
	m.server.Close()
}

func (m *LegacySSEMockServer) ToolCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.toolCallCount
}

func (m *LegacySSEMockServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		m.handleSSEStream(w, r)
	case "POST":
		m.handlePost(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *LegacySSEMockServer) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send endpoint event with session ID as query parameter (like Atlassian)
	_, _ = fmt.Fprintf(w, "event: endpoint\n")
	_, _ = fmt.Fprintf(w, "data: /v1/sse?sessionId=%s\n\n", m.sessionID)
	flusher.Flush()

	// Send keep-alive
	_, _ = fmt.Fprint(w, ": keep-alive\n\n")
	flusher.Flush()

	// Keep connection open
	<-r.Context().Done()
}

func (m *LegacySSEMockServer) handlePost(w http.ResponseWriter, r *http.Request) {
	// Legacy protocol requires sessionId as query parameter
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "Missing sessionId parameter", http.StatusBadRequest)
		return
	}
	if sessionID != m.sessionID {
		http.Error(w, "Invalid sessionId", http.StatusBadRequest)
		return
	}

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "initialize":
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"serverInfo": map[string]interface{}{
					"name":    "legacy-mock-server",
					"version": "1.0.0",
				},
			},
		})

	case "notifications/initialized":
		// No response for notification
		w.WriteHeader(http.StatusAccepted)

	case "tools/list":
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "legacyTool", "description": "A legacy test tool"},
				},
			},
		})

	case "tools/call":
		m.mu.Lock()
		m.toolCallCount++
		m.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": "tool called successfully"},
				},
				"isError": false,
			},
		})

	default:
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]interface{}{
				"code":    -32601,
				"message": "Method not found",
			},
		})
	}
}

func TestLegacySSE_EndpointEvent(t *testing.T) {
	t.Skip("Legacy SSE not supported - using Streamable HTTP POST-only per MCP spec 2025-03-26")
	mock := NewLegacySSEMockServer(t)
	defer mock.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: mock.URL()})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer func() { _ = transport.Close() }()

	// Verify session ID was captured from endpoint event
	if transport.SessionID() != "legacy-session-456" {
		t.Errorf("expected session ID 'legacy-session-456', got %q", transport.SessionID())
	}
}

func TestLegacySSE_Initialize(t *testing.T) {
	t.Skip("Legacy SSE not supported - using Streamable HTTP POST-only per MCP spec 2025-03-26")
	mock := NewLegacySSEMockServer(t)
	defer mock.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: mock.URL()})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer func() { _ = transport.Close() }()

	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	name, version := client.ServerInfo()
	if name != "legacy-mock-server" {
		t.Errorf("expected server name 'legacy-mock-server', got %q", name)
	}
	if version != "1.0.0" {
		t.Errorf("expected server version '1.0.0', got %q", version)
	}
}

func TestLegacySSE_ListTools(t *testing.T) {
	t.Skip("Legacy SSE not supported - using Streamable HTTP POST-only per MCP spec 2025-03-26")
	mock := NewLegacySSEMockServer(t)
	defer mock.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: mock.URL()})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer func() { _ = transport.Close() }()

	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "legacyTool" {
		t.Errorf("expected tool name 'legacyTool', got %q", tools[0].Name)
	}
}

func TestLegacySSE_CallTool(t *testing.T) {
	t.Skip("Legacy SSE not supported - using Streamable HTTP POST-only per MCP spec 2025-03-26")
	mock := NewLegacySSEMockServer(t)
	defer mock.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: mock.URL()})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer func() { _ = transport.Close() }()

	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	result, err := client.CallTool(ctx, "legacyTool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if result.IsError {
		t.Error("expected isError=false")
	}
	if mock.ToolCallCount() != 1 {
		t.Errorf("expected 1 tool call, got %d", mock.ToolCallCount())
	}
}

func TestLegacySSE_SessionIDRequired(t *testing.T) {
	t.Skip("Legacy SSE not supported - using Streamable HTTP POST-only per MCP spec 2025-03-26")
	// Test that the mock server rejects requests without sessionId
	mock := NewLegacySSEMockServer(t)
	defer mock.Close()

	// Make a direct POST without session ID - should fail
	resp, err := http.Post(mock.URL(), "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", resp.StatusCode)
	}
}
