package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sseStreamFixture is an MCP-over-HTTP server that answers POSTs with JSON and
// offers a standalone GET SSE stream tests can push events onto.
type sseStreamFixture struct {
	server *httptest.Server

	gets  atomic.Int32
	posts atomic.Int32

	mu            sync.Mutex
	streamHeaders []http.Header
	events        chan string
	connected     chan struct{}
	connectOnce   sync.Once

	// declineStream makes GET answer 405, as a server with no stream would.
	declineStream atomic.Bool
	// dropAfterFirst closes the stream once it has sent one event, to exercise
	// reconnection.
	dropAfterFirst atomic.Bool
}

func newSSEStreamFixture(t *testing.T) *sseStreamFixture {
	t.Helper()
	f := &sseStreamFixture{
		events:    make(chan string, 16),
		connected: make(chan struct{}),
	}

	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			f.gets.Add(1)
			f.mu.Lock()
			f.streamHeaders = append(f.streamHeaders, r.Header.Clone())
			f.mu.Unlock()

			if f.declineStream.Load() {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			f.connectOnce.Do(func() { close(f.connected) })

			for {
				select {
				case ev := <-f.events:
					_, _ = fmt.Fprint(w, ev)
					if flusher != nil {
						flusher.Flush()
					}
					if f.dropAfterFirst.Load() {
						return
					}
				case <-r.Context().Done():
					return
				}
			}
		}

		f.posts.Add(1)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Mcp-Session-Id", "session-abc")
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18",`+
				`"capabilities":{"resources":{"subscribe":true},"tools":{"listChanged":true}},`+
				`"serverInfo":{"name":"sse-fixture","version":"1"}}}`, req.ID)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *sseStreamFixture) pushEvent(id, payload string) {
	if id != "" {
		f.events <- fmt.Sprintf("id: %s\ndata: %s\n\n", id, payload)
		return
	}
	f.events <- fmt.Sprintf("data: %s\n\n", payload)
}

func (f *sseStreamFixture) lastStreamHeader() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.streamHeaders) == 0 {
		return nil
	}
	return f.streamHeaders[len(f.streamHeaders)-1]
}

// TestStandaloneSSEStreamDeliversServerNotifications is the core of the fix:
// before it, the transport only ever issued POSTs, so a server-initiated
// notification had no channel to arrive on at all.
//
// This is what made resource subscriptions a silent dead end over HTTP.
// handleInitialize advertises subscribe:true, resources/subscribe checks the
// upstream capability and succeeds, and Core tracks and replays the intent
// across process generations — but notifications/resources/updated could never
// be delivered, because a POST response body closes as soon as the reply is
// read. A probe counted 0 GET requests after Initialize + SubscribeResource.
func TestStandaloneSSEStreamDeliversServerNotifications(t *testing.T) {
	fixture := newSSEStreamFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: fixture.server.URL})
	client := NewClient(transport)
	defer func() { _ = client.Close() }()

	notifications := make(chan string, 8)
	client.SetNotificationHandler(func(method string, _ json.RawMessage) {
		notifications <- method
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := client.SubscribeResource(ctx, "file:///watched.txt"); err != nil {
		t.Fatalf("SubscribeResource: %v", err)
	}

	// The stream must actually be opened.
	select {
	case <-fixture.connected:
	case <-time.After(5 * time.Second):
		t.Fatalf("transport never opened the standalone SSE stream (GET count = %d)", fixture.gets.Load())
	}

	// The GET must carry the negotiated version and the session ID, or a real
	// server will reject it.
	header := fixture.lastStreamHeader()
	if got := header.Get("Accept"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("stream Accept = %q, want text/event-stream", got)
	}
	if got := header.Get("Mcp-Session-Id"); got != "session-abc" {
		t.Errorf("stream Mcp-Session-Id = %q, want %q", got, "session-abc")
	}
	if got := header.Get("MCP-Protocol-Version"); got != "2025-11-25" {
		t.Errorf("stream MCP-Protocol-Version = %q, want the negotiated version", got)
	}

	fixture.pushEvent("ev-1",
		`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"file:///watched.txt"}}`)
	fixture.pushEvent("ev-2",
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{}}`)

	want := map[string]bool{
		"notifications/resources/updated":  false,
		"notifications/tools/list_changed": false,
	}
	deadline := time.After(5 * time.Second)
	for range len(want) {
		select {
		case method := <-notifications:
			if _, expected := want[method]; !expected {
				t.Errorf("unexpected notification %q", method)
				continue
			}
			want[method] = true
		case <-deadline:
			t.Fatalf("timed out waiting for notifications; got %v", want)
		}
	}
	for method, seen := range want {
		if !seen {
			t.Errorf("notification %q never arrived", method)
		}
	}
}

// TestSSEResponseHeldOpenDoesNotStallSend guards the second bug found next to
// the missing stream.
//
// handleSSEResponse used to run inline in Send, looping until the server closed
// the stream. The MCP spec lets a server keep a POST's SSE response open to push
// further messages, so Send blocked for as long as the server chose — and
// because Client.call holds sendMu across Send, every other RPC on the transport
// queued behind it. Measured before the fix: Initialize took exactly 2s against
// a server holding the stream open for 2s.
func TestSSEResponseHeldOpenDoesNotStallSend(t *testing.T) {
	const hold = 3 * time.Second
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprintf(w, "data: "+
			`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},`+
			`"serverInfo":{"name":"holder","version":"1"}}}`+"\n\n", req.ID)
		if flusher != nil {
			flusher.Flush()
		}
		// Hold the stream open the way a server intending to push more would.
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(hold):
		}
	}))
	defer server.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: server.URL})
	client := NewClient(transport)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	started := time.Now()
	err := client.Initialize(ctx)
	elapsed := time.Since(started)
	close(release)

	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// Generous margin: the point is that it does not wait out the full hold.
	if elapsed >= hold {
		t.Fatalf("Initialize took %v, i.e. it waited for the server to close the "+
			"SSE response (held %v); Send is stalling and sendMu blocks every other RPC",
			elapsed.Round(10*time.Millisecond), hold)
	}
}

// TestStandaloneSSEStreamNotRetriedWhenDeclined checks that a server entitled to
// refuse the stream is not hammered. 405 is the spec's way of saying "no stream
// here", and retrying it on a backoff would be a pointless request loop for the
// entire life of the transport.
func TestStandaloneSSEStreamNotRetriedWhenDeclined(t *testing.T) {
	fixture := newSSEStreamFixture(t)
	fixture.declineStream.Store(true)

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: fixture.server.URL})
	client := NewClient(transport)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Well past several base reconnect delays.
	time.Sleep(4 * SSEReconnectBaseDelay)

	if got := fixture.gets.Load(); got != 1 {
		t.Errorf("GET attempts = %d, want exactly 1: a 405 means the server has no "+
			"stream and must not be retried", got)
	}
}

// TestStandaloneSSEStreamReconnectsWithLastEventID checks that a dropped stream
// comes back and resumes from where it left off. Without Last-Event-ID a
// reconnect silently loses every event the server emitted while disconnected.
func TestStandaloneSSEStreamReconnectsWithLastEventID(t *testing.T) {
	fixture := newSSEStreamFixture(t)
	fixture.dropAfterFirst.Store(true)

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: fixture.server.URL})
	client := NewClient(transport)
	defer func() { _ = client.Close() }()

	notifications := make(chan string, 8)
	client.SetNotificationHandler(func(method string, _ json.RawMessage) {
		notifications <- method
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	select {
	case <-fixture.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never connected")
	}

	// One event, after which the fixture closes the stream.
	fixture.pushEvent("event-42",
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{}}`)
	select {
	case <-notifications:
	case <-time.After(5 * time.Second):
		t.Fatal("first notification never arrived")
	}

	// The transport should come back on its own and tell the server where it
	// stopped.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fixture.gets.Load() >= 2 {
			if got := fixture.lastStreamHeader().Get("Last-Event-ID"); got == "event-42" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("stream did not reconnect with Last-Event-ID=event-42 (GET attempts=%d, last header=%v)",
		fixture.gets.Load(), fixture.lastStreamHeader())
}

// TestCloseTearsDownStandaloneStream guards the teardown path. The stream
// goroutine is tracked by the transport's WaitGroup and Close waits on it, so a
// stream the server is holding open must be interrupted rather than pinning
// Close forever.
func TestCloseTearsDownStandaloneStream(t *testing.T) {
	fixture := newSSEStreamFixture(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: fixture.server.URL})
	client := NewClient(transport)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	select {
	case <-fixture.connected:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never connected")
	}

	closed := make(chan error, 1)
	go func() { closed <- client.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on the open SSE stream")
	}
}
