package mcptest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// HTTPProbe drives a Streamable HTTP MCP endpoint with raw requests. It
// exists because much of what the httpserve tests assert is deliberately
// non-compliant traffic (batch arrays, wrong Content-Type, stale session
// IDs, second GET streams, exact status/body/framing checks) that no real
// MCP client can be made to send. The mirror of the fakeserver pattern, one
// layer up.
type HTTPProbe struct {
	BaseURL string // endpoint URL, e.g. srv.URL + "/mcp" or + "/mcp/work"
	Token   string // bearer token; "" sends no Authorization header
	// SessionID is attached to every request when set. Post captures the
	// Mcp-Session-Id response header into it automatically.
	SessionID string
	Client    *http.Client
}

func (p *HTTPProbe) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// Request performs one HTTP request against the endpoint with the probe's
// auth and session headers plus any extra header pairs ("Name", "Value",
// ...). Pass an empty value to suppress a default header.
func (p *HTTPProbe) Request(t *testing.T, method, body string, extraHeaders ...string) *http.Response {
	t.Helper()
	if len(extraHeaders)%2 != 0 {
		t.Fatalf("extraHeaders must be name/value pairs")
	}
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, p.BaseURL, reader)
	if err != nil {
		t.Fatalf("build %s request: %v", method, err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	if p.SessionID != "" {
		req.Header.Set("Mcp-Session-Id", p.SessionID)
	}
	for i := 0; i < len(extraHeaders); i += 2 {
		if extraHeaders[i+1] == "" {
			req.Header.Del(extraHeaders[i])
		} else {
			req.Header.Set(extraHeaders[i], extraHeaders[i+1])
		}
	}
	resp, err := p.client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, p.BaseURL, err)
	}
	return resp
}

// Post sends one JSON body and records any Mcp-Session-Id response header.
func (p *HTTPProbe) Post(t *testing.T, body string, extraHeaders ...string) *http.Response {
	t.Helper()
	resp := p.Request(t, http.MethodPost, body, extraHeaders...)
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		p.SessionID = sid
	}
	return resp
}

// RPCResult is a decoded JSON-RPC response body.
type RPCResult struct {
	Status int
	ID     json.RawMessage
	Result json.RawMessage
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}

// Call sends a JSON-RPC request and decodes the response body. It fails the
// test if the body is not a JSON-RPC response; use Post for status-only
// assertions.
func (p *HTTPProbe) Call(t *testing.T, id int, method string, params any) RPCResult {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	resp := p.Post(t, string(body))
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}
	var decoded struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("%s: response is not JSON-RPC (status %d): %s", method, resp.StatusCode, data)
	}
	return RPCResult{Status: resp.StatusCode, ID: decoded.ID, Result: decoded.Result, Error: decoded.Error}
}

// Initialize performs the initialize round-trip and returns the negotiated
// protocol version. The session ID lands in p.SessionID.
func (p *HTTPProbe) Initialize(t *testing.T) string {
	t.Helper()
	res := p.Call(t, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "httpprobe", "version": "0"},
	})
	if res.Error != nil {
		t.Fatalf("initialize failed: %d %s", res.Error.Code, res.Error.Message)
	}
	if p.SessionID == "" {
		t.Fatalf("initialize returned no Mcp-Session-Id")
	}
	notify := p.Post(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	_ = notify.Body.Close()
	if notify.StatusCode != http.StatusAccepted {
		t.Fatalf("notifications/initialized: status %d, want 202", notify.StatusCode)
	}
	var out struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(res.Result, &out); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	return out.ProtocolVersion
}

// SSEEvent is one parsed server-sent event, comments included.
type SSEEvent struct {
	Event   string
	Data    string
	Comment string // ": ping" keepalives arrive here with Data empty
}

// SSEStream is an attached standalone GET stream.
type SSEStream struct {
	Events <-chan SSEEvent
	// Status is the GET's response status; Events is nil unless it was 200.
	Status int
	cancel context.CancelFunc
	body   io.Closer
}

// Close tears the stream down.
func (s *SSEStream) Close() {
	s.cancel()
	if s.body != nil {
		_ = s.body.Close()
	}
}

// Next waits for the next event, failing the test on timeout.
func (s *SSEStream) Next(t *testing.T, timeout time.Duration) SSEEvent {
	t.Helper()
	select {
	case ev, ok := <-s.Events:
		if !ok {
			t.Fatalf("SSE stream closed while waiting for an event")
		}
		return ev
	case <-time.After(timeout):
		t.Fatalf("no SSE event within %v", timeout)
	}
	return SSEEvent{}
}

// NextMessage waits for the next non-comment event.
func (s *SSEStream) NextMessage(t *testing.T, timeout time.Duration) SSEEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("no SSE message within %v", timeout)
		}
		ev := s.Next(t, remaining)
		if ev.Comment == "" {
			return ev
		}
	}
}

// OpenStream attaches the standalone GET SSE stream and parses events into a
// channel. Always returns a stream; check Status before reading Events.
func (p *HTTPProbe) OpenStream(t *testing.T, extraHeaders ...string) *SSEStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("build GET request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	if p.SessionID != "" {
		req.Header.Set("Mcp-Session-Id", p.SessionID)
	}
	for i := 0; i+1 < len(extraHeaders); i += 2 {
		if extraHeaders[i+1] == "" {
			req.Header.Del(extraHeaders[i])
		} else {
			req.Header.Set(extraHeaders[i], extraHeaders[i+1])
		}
	}
	resp, err := p.client().Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", p.BaseURL, err)
	}
	stream := &SSEStream{Status: resp.StatusCode, cancel: cancel, body: resp.Body}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return stream
	}
	events := make(chan SSEEvent, 64)
	stream.Events = events
	go func() {
		defer close(events)
		parseSSE(resp.Body, events)
	}()
	t.Cleanup(stream.Close)
	return stream
}

// parseSSE reads SSE frames until the body ends. Minimal by design — this is
// a test probe, not a client.
func parseSSE(body io.Reader, out chan<- SSEEvent) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var current SSEEvent
	var dataLines []string
	flush := func() {
		if current.Event != "" || len(dataLines) > 0 || current.Comment != "" {
			current.Data = strings.Join(dataLines, "\n")
			out <- current
		}
		current = SSEEvent{}
		dataLines = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			current.Comment = line
			flush()
		case strings.HasPrefix(line, "event:"):
			current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
}

// DecodeNotification decodes an SSE message event as a JSON-RPC notification.
func (e SSEEvent) DecodeNotification(t *testing.T) (method string, params json.RawMessage) {
	t.Helper()
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(e.Data), &msg); err != nil {
		t.Fatalf("SSE event is not a JSON-RPC message: %v (data: %s)", err, e.Data)
	}
	return msg.Method, msg.Params
}

// ReadBody drains and closes a response body, returning it as a string.
func ReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(data)
}

// FakeServerEnv marshals a fakeserver config into the env map that makes the
// re-exec'd test binary (os.Args[0] -test.run=TestHelperProcess --) serve it.
func FakeServerEnv(t *testing.T, cfg FakeServerConfig) map[string]string {
	t.Helper()
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal fake server config: %v", err)
	}
	return map[string]string{
		"GO_WANT_HELPER_PROCESS": "1",
		"FAKE_MCP_CFG":           string(cfgJSON),
	}
}
