package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// expiringServer is a minimal Streamable HTTP server whose session can be
// invalidated on demand, for exercising the client's expiry recovery.
type expiringServer struct {
	t  *testing.T
	mu sync.Mutex

	currentSession string
	initCount      int
	getCount       int
	getOpened      chan struct{} // one token per accepted GET stream
	dropGET        chan struct{} // closed to hang up every open GET stream
}

func newExpiringServer(t *testing.T) (*expiringServer, *httptest.Server) {
	es := &expiringServer{t: t, getOpened: make(chan struct{}, 8), dropGET: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(es.handle))
	t.Cleanup(srv.Close)
	return es, srv
}

func (es *expiringServer) expireSession() {
	es.mu.Lock()
	es.currentSession = ""
	es.mu.Unlock()
}

// dropStreams hangs up every open GET stream, as a restart or a reverse proxy
// would, forcing the client to reconnect.
func (es *expiringServer) dropStreams() {
	es.mu.Lock()
	close(es.dropGET)
	es.dropGET = make(chan struct{})
	es.mu.Unlock()
}

func (es *expiringServer) counts() (inits, gets int) {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.initCount, es.getCount
}

func (es *expiringServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Status shapes mirror httpserve: 400 for a missing header, 404 for
		// a session the server no longer knows.
		if r.Header.Get("Mcp-Session-Id") == "" {
			http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
			return
		}
		es.mu.Lock()
		valid := r.Header.Get("Mcp-Session-Id") == es.currentSession && es.currentSession != ""
		if valid {
			es.getCount++
		}
		drop := es.dropGET
		es.mu.Unlock()
		if !valid {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case es.getOpened <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-drop:
		}
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if msg.Method == "initialize" {
			es.mu.Lock()
			es.initCount++
			es.currentSession = fmt.Sprintf("session-%d", es.initCount)
			sid := es.currentSession
			es.mu.Unlock()
			w.Header().Set("Mcp-Session-Id", sid)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"expiring","version":"0"}}}`, msg.ID)
			return
		}
		if r.Header.Get("Mcp-Session-Id") == "" {
			http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
			return
		}
		es.mu.Lock()
		valid := r.Header.Get("Mcp-Session-Id") == es.currentSession && es.currentSession != ""
		es.mu.Unlock()
		if !valid {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, msg.ID)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// TestClientRecoversFromExpiredSession drives the spec-mandated recovery:
// a POST carrying a session ID that comes back 404 clears session state,
// reinitializes, retries once — and the fresh session reopens the standalone
// GET stream.
func TestClientRecoversFromExpiredSession(t *testing.T) {
	es, srv := newExpiringServer(t)

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	client := NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := client.ListTools(ctx); err != nil {
		t.Fatalf("tools/list before expiry: %v", err)
	}
	select {
	case <-es.getOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone GET stream never opened for the first session")
	}

	es.expireSession()

	if _, err := client.ListTools(ctx); err != nil {
		t.Fatalf("tools/list after expiry should recover, got: %v", err)
	}

	inits, _ := es.counts()
	if inits != 2 {
		t.Fatalf("initialize count = %d, want 2 (one per session)", inits)
	}
	select {
	case <-es.getOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone GET stream was not reopened for the fresh session")
	}
	if got := transport.SessionID(); got != "session-2" {
		t.Fatalf("transport session ID = %q, want session-2", got)
	}
}

// TestSessionExpiredErrorSurfacesForInitialize makes sure the recovery never
// recurses: an expired-session 404 on initialize itself is returned, not
// retried.
func TestSessionExpiredErrorClearsState(t *testing.T) {
	es, srv := newExpiringServer(t)
	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	t.Cleanup(func() { _ = transport.Close() })

	ctx := context.Background()
	// Establish a session directly at transport level.
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)); err != nil {
		t.Fatalf("initialize send: %v", err)
	}
	if transport.SessionID() == "" {
		t.Fatal("no session ID captured")
	}
	es.expireSession()

	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if _, ok := err.(*SessionExpiredError); !ok {
		t.Fatalf("Send after expiry = %v, want *SessionExpiredError", err)
	}
	if transport.SessionID() != "" {
		t.Fatalf("session ID not cleared after expiry: %q", transport.SessionID())
	}
	if transport.NegotiatedVersion() != "" {
		t.Fatalf("negotiated version not cleared after expiry: %q", transport.NegotiatedVersion())
	}
}

// TestStalePOSTDoesNotClearGETExpiryLatch forces the ordering where the GET
// stream detects expiry while a POST carrying the same stale session is still
// in flight. The later POST 404 must not erase the GET path's expiry latch;
// otherwise the next call goes out with no session header and gets a plain 400
// instead of entering client recovery.
func TestStalePOSTDoesNotClearGETExpiryLatch(t *testing.T) {
	postStarted := make(chan struct{})
	releasePost := make(chan struct{})
	var requestsMu sync.Mutex
	nonInitRequests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if msg.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-1")
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, msg.ID)
			return
		}

		requestsMu.Lock()
		nonInitRequests++
		requestNumber := nonInitRequests
		requestsMu.Unlock()
		if r.Header.Get("Mcp-Session-Id") == "" {
			http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
			return
		}
		if requestNumber == 1 {
			close(postStarted)
			<-releasePost
		}
		http.Error(w, "unknown session", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	t.Cleanup(func() { _ = transport.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)); err != nil {
		t.Fatalf("initialize send: %v", err)
	}

	postErr := make(chan error, 1)
	go func() {
		postErr <- transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	}()
	select {
	case <-postStarted:
	case <-ctx.Done():
		t.Fatal("stale POST did not reach server")
	}

	// Simulate the standalone GET stream winning the expiry race.
	transport.handleSessionExpired("session-1")
	close(releasePost)
	var expired *SessionExpiredError
	if err := <-postErr; !errors.As(err, &expired) {
		t.Fatalf("stale POST error = %v, want *SessionExpiredError", err)
	}

	// The expiry latch must still reject locally; reaching the server would
	// produce its missing-header 400 and increment nonInitRequests.
	err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	if !errors.As(err, &expired) {
		t.Fatalf("Send after competing expiry signals = %v, want *SessionExpiredError", err)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	if nonInitRequests != 1 {
		t.Fatalf("non-initialize requests reaching server = %d, want 1", nonInitRequests)
	}
}

// TestExpiredSessionOnGETStreamClearsSession covers the channel an idle client
// has open: only the standalone GET stream. When the session dies and the
// stream reconnects, its 404 means "reinitialize", not "this server has no
// stream to give" — mistaking the two leaves the transport believing it still
// has a session while nothing is listening, and no reconnect ever follows.
func TestExpiredSessionOnGETStreamClearsSession(t *testing.T) {
	es, srv := newExpiringServer(t)
	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	t.Cleanup(func() { _ = transport.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)); err != nil {
		t.Fatalf("initialize send: %v", err)
	}
	select {
	case <-es.getOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone GET stream never opened")
	}

	// The session dies (idle reap, DELETE, restart) and the stream drops, so
	// the reconnect presents a session the server no longer knows.
	es.expireSession()
	es.dropStreams()

	deadline := time.Now().Add(10 * time.Second)
	for transport.SessionID() != "" {
		if time.Now().After(deadline) {
			t.Fatalf("session ID still %q after the GET stream was 404'd", transport.SessionID())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// sseActive must have been re-armed too, or the replacement session would
	// run without a stream: notifications/tools/list_changed would never land.
	if err := transport.Send(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{}}`)); err != nil {
		t.Fatalf("reinitialize send: %v", err)
	}
	select {
	case <-es.getOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone GET stream was not reopened for the replacement session")
	}
}

// TestConcurrentExpiryRecoveryInitializesOnce pins the single-flight guard:
// every in-flight call sees the same 404, and one reinitialize has to serve
// them all. One per caller would strand N-1 server-side sessions — each with
// its own private upstream instances for shared:false servers — reachable by
// nobody, since only the last session ID survives in the transport.
func TestConcurrentExpiryRecoveryInitializesOnce(t *testing.T) {
	es, srv := newExpiringServer(t)
	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	client := NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	es.expireSession()

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Go(func() {
			if _, err := client.ListTools(ctx); err != nil {
				errs <- fmt.Errorf("caller %d: %w", i, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if inits, _ := es.counts(); inits != 2 {
		t.Fatalf("initialize count = %d, want 2 (the original plus one shared recovery)", inits)
	}
}

// TestClientRecoversAfterGETStreamExpiry is the idle-client story end to end:
// the only open channel is the standalone GET stream, the session dies behind
// it, and the *next call* must recover. The trap is on the wire: by then the
// transport has cleared its session ID, and a request sent with no session
// header gets 400 ("missing Mcp-Session-Id"), not 404 — nothing downstream
// recognises 400 as expiry, so without the local expiry latch the client
// would fail every call forever.
func TestClientRecoversAfterGETStreamExpiry(t *testing.T) {
	es, srv := newExpiringServer(t)
	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	client := NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := client.ListTools(ctx); err != nil {
		t.Fatalf("tools/list before expiry: %v", err)
	}
	select {
	case <-es.getOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone GET stream never opened")
	}

	es.expireSession()
	es.dropStreams()

	// Wait for the GET reconnect's 404 to clear the session — the client is
	// idle, so this is the only expiry signal it gets.
	deadline := time.Now().Add(10 * time.Second)
	for transport.SessionID() != "" {
		if time.Now().After(deadline) {
			t.Fatal("GET-stream 404 never cleared the session")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := client.ListTools(ctx); err != nil {
		t.Fatalf("first call after GET-stream expiry should recover transparently, got: %v", err)
	}
	if inits, _ := es.counts(); inits != 2 {
		t.Fatalf("initialize count = %d, want 2", inits)
	}
	select {
	case <-es.getOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("standalone GET stream was not reopened for the replacement session")
	}
}
