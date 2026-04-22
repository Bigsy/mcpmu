package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

// testPipe creates a pair of connected pipes that can be used for testing.
// Returns (serverStdin, serverStdout, clientStdin, clientStdout).
func testPipe() (serverIn io.ReadCloser, serverOut io.WriteCloser, clientIn io.WriteCloser, clientOut io.ReadCloser) {
	// Client writes to serverIn, server reads from serverIn
	serverReader, clientWriter := io.Pipe()
	// Server writes to clientOut, client reads from clientOut
	clientReader, serverWriter := io.Pipe()

	return serverReader, serverWriter, clientWriter, clientReader
}

// runFakeServer starts a fake server in a goroutine, reading from serverIn and writing to serverOut.
func runFakeServer(ctx context.Context, serverIn io.Reader, serverOut io.Writer, cfg fakeserver.Config) chan error {
	done := make(chan error, 1)
	go func() {
		done <- fakeserver.Serve(ctx, serverIn, serverOut, cfg)
	}()
	return done
}

func TestClient_HappyPath(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Tools: []fakeserver.Tool{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	// Initialize
	err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	name, version := client.ServerInfo()
	if name != "fake-server" {
		t.Errorf("expected server name 'fake-server', got %q", name)
	}
	if version != "1.0.0" {
		t.Errorf("expected server version '1.0.0', got %q", version)
	}

	// List tools
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Errorf("expected first tool 'read_file', got %q", tools[0].Name)
	}

	// Close client - this closes the pipes and server should exit
	_ = client.Close()

	// Wait for server to exit
	select {
	case err := <-serverDone:
		if err != nil && err != io.EOF && err != io.ErrClosedPipe {
			t.Errorf("server error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("server didn't exit in time")
	}
}

func TestClient_NotificationBeforeResponse(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Tools:                          []fakeserver.Tool{{Name: "test_tool"}},
		SendNotificationBeforeResponse: true,
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	// Client should skip notifications and get real response
	err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}

	_ = client.Close()
	<-serverDone
}

func TestClient_MismatchedIDFirst(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Tools:                 []fakeserver.Tool{{Name: "test_tool"}},
		SendMismatchedIDFirst: true,
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	// Client should skip mismatched ID and wait for correct one
	err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	_ = client.Close()
	<-serverDone
}

func TestClient_JSONRPCError(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Errors: map[string]fakeserver.JSONRPCError{
			"initialize": {Code: -32600, Message: "Invalid Request"},
		},
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	err := client.Initialize(ctx)
	if err == nil {
		t.Fatal("expected JSON-RPC error, got nil")
	}
	t.Logf("Got expected error: %v", err)

	_ = client.Close()
	<-serverDone
}

func TestClient_EmptyToolList(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Tools: []fakeserver.Tool{},
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}

	_ = client.Close()
	<-serverDone
}

func TestClient_LargeToolList(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Generate 100 tools
	tools := make([]fakeserver.Tool, 100)
	for i := range 100 {
		tools[i] = fakeserver.Tool{
			Name:        "tool_" + string(rune('a'+i%26)),
			Description: "Test tool",
		}
	}

	cfg := fakeserver.Config{
		Tools: tools,
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	resultTools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(resultTools) != 100 {
		t.Errorf("expected 100 tools, got %d", len(resultTools))
	}

	_ = client.Close()
	<-serverDone
}

func TestClient_ListResources(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Resources: []fakeserver.Resource{
			{URI: "file:///readme.md", Name: "readme", Description: "The readme", MimeType: "text/markdown"},
		},
		ResourceContents: map[string]json.RawMessage{
			"file:///readme.md": json.RawMessage(`[{"uri":"file:///readme.md","text":"# Hello"}]`),
		},
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	resources, err := client.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources failed: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if resources[0].URI != "file:///readme.md" {
		t.Errorf("expected URI 'file:///readme.md', got %q", resources[0].URI)
	}
	if resources[0].Name != "readme" {
		t.Errorf("expected name 'readme', got %q", resources[0].Name)
	}

	contents, err := client.ReadResource(ctx, "file:///readme.md")
	if err != nil {
		t.Fatalf("ReadResource failed: %v", err)
	}
	if contents == nil {
		t.Fatal("expected non-nil contents")
	}

	_ = client.Close()
	<-serverDone
}

func TestClient_ListPrompts(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{
		Prompts: []fakeserver.Prompt{
			{
				Name:        "summarize",
				Description: "Summarize text",
				Arguments: []fakeserver.PromptArgument{
					{Name: "text", Description: "Text to summarize", Required: true},
				},
			},
		},
		PromptMessages: map[string]json.RawMessage{
			"summarize": json.RawMessage(`[{"role":"user","content":{"type":"text","text":"Summarize: hello"}}]`),
		},
	}

	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	prompts, err := client.ListPrompts(ctx)
	if err != nil {
		t.Fatalf("ListPrompts failed: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompts))
	}
	if prompts[0].Name != "summarize" {
		t.Errorf("expected name 'summarize', got %q", prompts[0].Name)
	}
	if len(prompts[0].Arguments) != 1 {
		t.Fatalf("expected 1 argument, got %d", len(prompts[0].Arguments))
	}

	messages, err := client.GetPrompt(ctx, "summarize", map[string]string{"text": "hello"})
	if err != nil {
		t.Fatalf("GetPrompt failed: %v", err)
	}
	if messages == nil {
		t.Fatal("expected non-nil messages")
	}

	_ = client.Close()
	<-serverDone
}

func TestClient_Timeout(t *testing.T) {
	// Test that StdioTransport.Receive() respects context cancellation.
	_, _, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	transport := NewStdioTransport(clientIn, clientOut)

	// Create a context that will be cancelled quickly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Try to receive - no data will be sent, so this should block until context is cancelled
	_, err := transport.Receive(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if err != context.DeadlineExceeded {
		t.Logf("received error: %v (context cancellation triggered pipe close)", err)
	}
}

// syntheticTransport is a minimal Transport used to drive specific reader-loop
// scenarios (malformed frames, server→client requests, etc.) by feeding
// hand-crafted JSON frames from a channel.
type syntheticTransport struct {
	in     chan []byte
	out    chan []byte
	done   chan struct{}
	mu     sync.Mutex
	closed bool
}

func newSyntheticTransport() *syntheticTransport {
	return &syntheticTransport{
		in:   make(chan []byte, 16),
		out:  make(chan []byte, 16),
		done: make(chan struct{}),
	}
}

func (s *syntheticTransport) Send(ctx context.Context, msg []byte) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return io.ErrClosedPipe
	}
	s.mu.Unlock()

	cp := make([]byte, len(msg))
	copy(cp, msg)
	select {
	case s.out <- cp:
		return nil
	case <-s.done:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *syntheticTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case msg, ok := <-s.in:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-s.done:
		return nil, io.ErrClosedPipe
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *syntheticTransport) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	return nil
}

// inject writes a raw frame to the client's reader as if it had been received
// from the server.
func (s *syntheticTransport) inject(frame []byte) {
	select {
	case s.in <- frame:
	case <-s.done:
	}
}

// nextSent returns the next frame the client attempted to send, or fails the
// test if none arrives within timeout.
func (s *syntheticTransport) nextSent(t *testing.T, timeout time.Duration) []byte {
	t.Helper()
	select {
	case msg := <-s.out:
		return msg
	case <-time.After(timeout):
		t.Fatal("no outgoing frame within timeout")
		return nil
	}
}

// TestClient_NotificationHandler_Idle verifies that a notification received
// while no call is in flight is delivered to the installed handler.
func TestClient_NotificationHandler_Idle(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	got := make(chan string, 1)
	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		got <- method
	})

	tp.inject([]byte(`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"file:///x"}}`))

	select {
	case method := <-got:
		if method != "notifications/resources/updated" {
			t.Errorf("unexpected method: %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked")
	}
}

// TestClient_NotificationHandler_ConcurrentWithCall verifies that a
// notification arriving while a call is outstanding is delivered to the
// handler AND the call still completes successfully.
func TestClient_NotificationHandler_ConcurrentWithCall(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	gotNotif := make(chan struct{}, 1)
	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		if method == "test/noise" {
			gotNotif <- struct{}{}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(ctx)
		callDone <- err
	}()

	// Wait for the request frame so we can learn its ID.
	reqFrame := tp.nextSent(t, 2*time.Second)
	var parsed struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(reqFrame, &parsed); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	// Inject a notification, then the response.
	tp.inject([]byte(`{"jsonrpc":"2.0","method":"test/noise"}`))
	respFrame := fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, parsed.ID)
	tp.inject(respFrame)

	select {
	case <-gotNotif:
	case <-time.After(2 * time.Second):
		t.Fatal("notification handler not invoked during pending call")
	}

	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("ListTools failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not complete")
	}
}

// TestClient_UnknownResponseID verifies that a response with an ID that does
// not match any in-flight call is dropped silently (no panic, no effect on
// subsequent calls).
func TestClient_UnknownResponseID(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	// Drop a bogus response before any call is made.
	tp.inject([]byte(`{"jsonrpc":"2.0","id":99999,"result":{}}`))

	// Now make a real call and make sure it still works.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(ctx)
		callDone <- err
	}()

	req := tp.nextSent(t, 2*time.Second)
	var parsed struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(req, &parsed)
	tp.inject(fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, parsed.ID))

	if err := <-callDone; err != nil {
		t.Fatalf("call after unknown-id drop failed: %v", err)
	}
}

// TestClient_ServerToClientRequest verifies that a frame with both an id and a
// method (a server-initiated request such as sampling/roots) is dropped
// without panic.
func TestClient_ServerToClientRequest(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	// Inject a server→client request. Stage 1 logs + drops.
	tp.inject([]byte(`{"jsonrpc":"2.0","id":42,"method":"sampling/createMessage","params":{}}`))

	// Verify the reader is still alive by completing a normal call afterwards.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(ctx)
		callDone <- err
	}()

	req := tp.nextSent(t, 2*time.Second)
	var parsed struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(req, &parsed)
	tp.inject(fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, parsed.ID))

	if err := <-callDone; err != nil {
		t.Fatalf("call after server→client request failed: %v", err)
	}
}

// TestClient_MalformedFrameSkipped verifies that a non-JSON frame does not
// kill the reader — subsequent valid frames still arrive.
func TestClient_MalformedFrameSkipped(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	tp.inject([]byte(`this is not json`))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(ctx)
		callDone <- err
	}()

	req := tp.nextSent(t, 2*time.Second)
	var parsed struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(req, &parsed)
	tp.inject(fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, parsed.ID))

	if err := <-callDone; err != nil {
		t.Fatalf("call after malformed frame failed: %v", err)
	}
}

// TestClient_CloseUnblocksPendingCall verifies that Close while a call is
// waiting causes the call to return a clean transport-closed error and no
// goroutines are leaked.
func TestClient_CloseUnblocksPendingCall(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	callDone := make(chan error, 1)
	go func() {
		_, err := client.ListTools(ctx)
		callDone <- err
	}()

	// Drain the outgoing request frame so send can proceed past sendMu.
	_ = tp.nextSent(t, 2*time.Second)

	// Close without ever delivering a response.
	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("expected transport-closed error, got nil")
		}
		if !strings.Contains(err.Error(), "transport closed") {
			t.Errorf("expected 'transport closed' in error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not unblock after Close")
	}
}

// TestClient_Capabilities verifies Capabilities() is the zero value before
// Initialize and populated with the server's typed capabilities after.
func TestClient_Capabilities(t *testing.T) {
	serverIn, serverOut, clientIn, clientOut := testPipe()
	defer func() { _ = clientIn.Close() }()
	defer func() { _ = clientOut.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := fakeserver.Config{Tools: []fakeserver.Tool{{Name: "t"}}}
	serverDone := runFakeServer(ctx, serverIn, serverOut, cfg)

	transport := NewStdioTransport(clientIn, clientOut)
	client := NewClient(transport)

	if got := client.Capabilities(); got.Tools != nil || got.Resources != nil || got.Prompts != nil {
		t.Errorf("Capabilities() before Initialize: want zero value, got %+v", got)
	}

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	caps := client.Capabilities()
	if caps.Tools == nil {
		t.Errorf("expected Tools capability, got nil; caps=%+v", caps)
	}

	_ = client.Close()
	<-serverDone
}

// TestClient_CloseWithoutInitialize ensures Close is safe on a client that
// was never Initialize'd — the reader goroutine started in NewClient must
// still tear down cleanly.
func TestClient_CloseWithoutInitialize(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)

	done := make(chan error, 1)
	go func() { done <- client.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return on un-Initialized client")
	}
}
