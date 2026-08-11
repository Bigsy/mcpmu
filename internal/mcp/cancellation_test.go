package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// silentTransport accepts sends and never answers, so a call can only end by
// its context expiring.
type silentTransport struct {
	mu       sync.Mutex
	sent     [][]byte
	received chan []byte
	closed   chan struct{}
	once     sync.Once
}

func newSilentTransport() *silentTransport {
	return &silentTransport{
		received: make(chan []byte, 8),
		closed:   make(chan struct{}),
	}
}

func (s *silentTransport) Send(_ context.Context, msg []byte) error {
	s.mu.Lock()
	s.sent = append(s.sent, append([]byte(nil), msg...))
	s.mu.Unlock()
	select {
	case s.received <- append([]byte(nil), msg...):
	default:
	}
	return nil
}

func (s *silentTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case <-s.closed:
		return nil, errors.New("transport closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *silentTransport) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

// awaitFrame waits for a frame whose method matches, returning its params.
func (s *silentTransport) awaitFrame(t *testing.T, method string) json.RawMessage {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-s.received:
			var frame struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				continue
			}
			if frame.Method == method {
				return frame.Params
			}
		case <-deadline:
			s.mu.Lock()
			sent := s.sent
			s.mu.Unlock()
			var seen []string
			for _, frame := range sent {
				seen = append(seen, string(frame))
			}
			t.Fatalf("no %s frame was sent upstream; frames seen:\n%s", method, strings.Join(seen, "\n"))
			return nil
		}
	}
}

// A cancelled call must be withdrawn upstream. Cancelling only the local
// context leaves the server working on a result nobody will read, still
// holding whatever the call reserved.
func TestClient_CancelledCallNotifiesUpstream(t *testing.T) {
	t.Parallel()
	transport := newSilentTransport()
	client := NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithCancelCause(context.Background())
	callDone := make(chan error, 1)
	go func() {
		_, err := client.CallTool(ctx, "slow_tool", json.RawMessage(`{}`))
		callDone <- err
	}()

	// Wait for the request to go out before cancelling it.
	transport.awaitFrame(t, "tools/call")
	cancel(errors.New("user pressed escape"))

	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallTool error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CallTool did not return after cancellation")
	}

	params := transport.awaitFrame(t, "notifications/cancelled")
	var cancelled struct {
		RequestID int64  `json:"requestId"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(params, &cancelled); err != nil {
		t.Fatalf("unmarshal cancellation params: %v (raw: %s)", err, params)
	}
	if cancelled.RequestID == 0 {
		t.Errorf("cancellation names no request: %s", params)
	}
	if !strings.Contains(cancelled.Reason, "user pressed escape") {
		t.Errorf("cancellation reason = %q, want the caller's cause", cancelled.Reason)
	}
}

// The cancellation spec asks for timed-out requests to be withdrawn too — a
// deadline that fires locally is still a request the server should stop.
func TestClient_TimedOutCallNotifiesUpstream(t *testing.T) {
	t.Parallel()
	transport := newSilentTransport()
	client := NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := client.CallTool(ctx, "slow_tool", json.RawMessage(`{}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallTool error = %v, want context.DeadlineExceeded", err)
	}
	transport.awaitFrame(t, "notifications/cancelled")
}

// A call that completes normally must not be withdrawn — the deferred cleanup
// on the caller's context must not look like a cancellation.
func TestClient_CompletedCallDoesNotNotifyUpstream(t *testing.T) {
	t.Parallel()
	transport := newRespondingTransport()
	client := NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := client.CallTool(ctx, "quick_tool", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	cancel()

	// Give any stray notification time to appear.
	time.Sleep(100 * time.Millisecond)
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, frame := range transport.sent {
		if strings.Contains(string(frame), "notifications/cancelled") {
			t.Fatalf("a completed call was withdrawn upstream: %s", frame)
		}
	}
}

// respondingTransport answers every request with an empty result.
type respondingTransport struct {
	mu       sync.Mutex
	sent     [][]byte
	incoming chan []byte
	closed   chan struct{}
	once     sync.Once
}

func newRespondingTransport() *respondingTransport {
	return &respondingTransport{
		incoming: make(chan []byte, 8),
		closed:   make(chan struct{}),
	}
}

func (r *respondingTransport) Send(_ context.Context, msg []byte) error {
	r.mu.Lock()
	r.sent = append(r.sent, append([]byte(nil), msg...))
	r.mu.Unlock()

	var req struct {
		ID *json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(msg, &req); err != nil || req.ID == nil {
		return nil
	}
	response := []byte(`{"jsonrpc":"2.0","id":` + string(*req.ID) + `,"result":{"content":[]}}`)
	select {
	case r.incoming <- response:
	case <-r.closed:
	}
	return nil
}

func (r *respondingTransport) Receive(ctx context.Context) ([]byte, error) {
	select {
	case msg := <-r.incoming:
		return msg, nil
	case <-r.closed:
		return nil, errors.New("transport closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *respondingTransport) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}
