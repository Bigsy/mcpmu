package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
)

// frameSink is a session writer that hands each written frame to the test.
type frameSink struct {
	frames chan []byte
	fail   error
}

func newFrameSink() *frameSink { return &frameSink{frames: make(chan []byte, 64)} }

func (f *frameSink) Write(p []byte) (int, error) {
	if f.fail != nil {
		return 0, f.fail
	}
	f.frames <- append([]byte(nil), p...)
	return len(p), nil
}

// next returns the next written frame decoded as a generic JSON-RPC message.
func (f *frameSink) next(t *testing.T) rpcFrame {
	t.Helper()
	select {
	case raw := <-f.frames:
		var frame rpcFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("unmarshal frame %s: %v", raw, err)
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("no frame written")
		return rpcFrame{}
	}
}

// none fails if a frame is written within d.
func (f *frameSink) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case raw := <-f.frames:
		t.Fatalf("unexpected frame: %s", raw)
	case <-time.After(d):
	}
}

type rpcFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// newBareSession builds a session over an empty config whose writes land in
// the returned sink. Nothing is started.
func newBareSession(t *testing.T) (*Session, *frameSink) {
	t.Helper()
	sink := newFrameSink()
	cfg := config.NewConfig()
	s, err := New(Options{
		Config:        cfg,
		PIDTrackerDir: t.TempDir(),
		Stdin:         strings.NewReader(""),
		Stdout:        sink,
		Stderr:        io.Discard,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.shutdown)
	return s, sink
}

type requestOutcome struct {
	result json.RawMessage
	rpcErr *RPCError
	err    error
}

func goRequest(ctx context.Context, s *Session, related json.RawMessage) <-chan requestOutcome {
	done := make(chan requestOutcome, 1)
	go func() {
		result, rpcErr, err := s.Request(ctx, "elicitation/create", map[string]string{"message": "Confirm?"}, related)
		done <- requestOutcome{result, rpcErr, err}
	}()
	return done
}

func respond(t *testing.T, s *Session, line string) {
	t.Helper()
	if err := s.handleMessage(context.Background(), []byte(line)); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
}

// TestSessionRequest_RoundTrip: the request goes out with an mcpmu-<n> id and
// the client's response completes it.
func TestSessionRequest_RoundTrip(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	done := goRequest(context.Background(), s, nil)

	req := sink.next(t)
	if req.Method != "elicitation/create" || !strings.HasPrefix(string(req.ID), `"mcpmu-`) {
		t.Fatalf("request frame = %+v", req)
	}
	respond(t, s, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"accept","content":{"ok":true}}}`)

	out := <-done
	if out.err != nil || out.rpcErr != nil || string(out.result) != `{"action":"accept","content":{"ok":true}}` {
		t.Fatalf("outcome = %+v", out)
	}
	if n := s.outbound.len(); n != 0 {
		t.Errorf("%d outbound entries left", n)
	}
}

// TestSessionRequest_ClientError: a JSON-RPC error from the client comes back
// as the request's error, not a transport failure.
func TestSessionRequest_ClientError(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	done := goRequest(context.Background(), s, nil)
	req := sink.next(t)
	respond(t, s, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"error":{"code":-32601,"message":"no elicitation here"}}`)
	out := <-done
	if out.err != nil || out.rpcErr == nil || out.rpcErr.Code != -32601 {
		t.Fatalf("outcome = %+v", out)
	}
}

// TestSessionRequest_WithdrawnOnContextEnd: when the waiter gives up, the
// client is told with notifications/cancelled for the mcpmu id, and a late
// answer is dropped rather than resolving anything.
func TestSessionRequest_WithdrawnOnContextEnd(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	done := goRequest(ctx, s, nil)
	req := sink.next(t)

	stop := errors.New("upstream withdrew it")
	cancel(stop)
	out := <-done
	if out.err != stop {
		t.Fatalf("err = %v, want the context cause", out.err)
	}
	withdrawal := sink.next(t)
	if withdrawal.Method != "notifications/cancelled" {
		t.Fatalf("expected notifications/cancelled, got %+v", withdrawal)
	}
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	_ = json.Unmarshal(withdrawal.Params, &params)
	if string(params.RequestID) != string(req.ID) || params.Reason == "" {
		t.Errorf("withdrawal params = %s", withdrawal.Params)
	}

	// The late answer resolves nothing and gets no reply.
	if s.HandleClientResponse(RPCMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"action":"accept"}`)}) {
		t.Error("a late answer to a withdrawn request was accepted")
	}
	sink.none(t, 100*time.Millisecond)
}

// TestSessionRequest_DeliveryFailureFailsAtOnce: a request that cannot be
// written fails immediately instead of waiting for an answer.
func TestSessionRequest_DeliveryFailureFailsAtOnce(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	sink.fail = io.ErrClosedPipe
	start := time.Now()
	_, _, err := s.Request(context.Background(), "elicitation/create", nil, nil)
	if !errors.Is(err, errNotDelivered) {
		t.Fatalf("err = %v, want errNotDelivered", err)
	}
	if time.Since(start) > time.Second {
		t.Error("delivery failure did not fail promptly")
	}
	if n := s.outbound.len(); n != 0 {
		t.Errorf("%d outbound entries left after a failed delivery", n)
	}
}

// TestSessionRequest_SessionCloseFailsPending: closing the session fails
// every request still waiting, and later requests fail at once.
func TestSessionRequest_SessionCloseFailsPending(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	done := goRequest(context.Background(), s, nil)
	sink.next(t)
	s.Close()
	if out := <-done; !errors.Is(out.err, errSessionClosed) {
		t.Fatalf("err = %v, want errSessionClosed", out.err)
	}
	if _, _, err := s.Request(context.Background(), "elicitation/create", nil, nil); !errors.Is(err, errSessionClosed) {
		t.Fatalf("request after close: err = %v, want errSessionClosed", err)
	}
}

// TestSessionRequest_ClientCancellationDoesNotTouchOutbound: a client cannot
// cancel an mcpmu-<n> request with notifications/cancelled (the spec only
// lets a sender cancel requests it issued). The notification is looked up
// among the client's own requests, finds nothing, and the relayed request is
// still waiting.
func TestSessionRequest_ClientCancellationDoesNotTouchOutbound(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	done := goRequest(context.Background(), s, nil)
	req := sink.next(t)

	respond(t, s, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":`+string(req.ID)+`}}`)
	select {
	case out := <-done:
		t.Fatalf("client cancellation completed an outbound request: %+v", out)
	case <-time.After(100 * time.Millisecond):
	}
	respond(t, s, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"decline"}}`)
	if out := <-done; out.err != nil || string(out.result) != `{"action":"decline"}` {
		t.Fatalf("outcome = %+v", out)
	}
}

// TestSessionRequest_ClientReusingMcpmuIDStaysIndependent: a client that
// picks "mcpmu-1" for its own request id cannot confuse the two directions —
// its cancellation reaches only its own request, and its response only
// mcpmu's.
func TestSessionRequest_ClientReusingMcpmuIDStaysIndependent(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	done := goRequest(context.Background(), s, nil)
	req := sink.next(t)

	// The client's own in-flight request with the very same id.
	inbound, release := s.TrackRequest(context.Background(), req.ID)
	defer release()

	respond(t, s, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"accept"}}`)
	if out := <-done; out.err != nil || string(out.result) != `{"action":"accept"}` {
		t.Fatalf("outcome = %+v", out)
	}
	if inbound.Err() != nil {
		t.Fatal("a response to mcpmu's request cancelled the client's own request")
	}

	respond(t, s, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":`+string(req.ID)+`}}`)
	select {
	case <-inbound.Done():
	case <-time.After(time.Second):
		t.Fatal("the client's cancellation did not reach its own request")
	}
}

// TestInteract_EndsWithOwningCall: when the call that asked for it ends, the
// interaction fails, the client's request is withdrawn, and the budget is no
// longer paused.
func TestInteract_EndsWithOwningCall(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	parent, cancelCall := context.WithCancel(withDownstreamRequestID(context.Background(), json.RawMessage(`7`)))
	_, call, end := s.beginUpstreamCall(parent, "srv", time.Minute)
	defer end()

	done := make(chan error, 1)
	go func() {
		_, _, err := interactionTarget{session: s, exact: call, calls: []*upstreamCall{call}}.
			interact(context.Background(), "elicitation/create", nil, time.Minute)
		done <- err
	}()
	sink.next(t)
	cancelCall()
	if err := <-done; err != errOwningCallEnded {
		t.Fatalf("err = %v, want errOwningCallEnded", err)
	}
	if sink.next(t).Method != "notifications/cancelled" {
		t.Error("the client's request was not withdrawn")
	}
}

// TestInteract_TimeoutResumesBudget: an interaction that outlives its own
// timeout fails, and the call's execution budget resumes.
func TestInteract_TimeoutResumesBudget(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	ctx, call, end := s.beginUpstreamCall(context.Background(), "srv", 200*time.Millisecond)
	defer end()

	_, _, err := interactionTarget{session: s, exact: call, calls: []*upstreamCall{call}}.
		interact(context.Background(), "elicitation/create", nil, 300*time.Millisecond)
	if err != errInteractionTimeout {
		t.Fatalf("err = %v, want errInteractionTimeout", err)
	}
	sink.next(t) // the request
	if ctx.Err() != nil {
		t.Fatal("call expired while paused")
	}
	if cause := waitDone(t, ctx, time.Second); cause != errExecutionBudgetExpired {
		t.Fatalf("cause = %v, want the resumed budget to expire", cause)
	}
}

// TestInteract_BudgetSpentFailsAtOnce: a call whose cumulative interaction
// budget is spent cannot start another interaction.
func TestInteract_BudgetSpentFailsAtOnce(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	_, call, end := s.beginUpstreamCall(context.Background(), "srv", time.Minute)
	defer end()
	call.budget.mu.Lock()
	call.budget.interactionLeft = 0
	call.budget.mu.Unlock()

	_, _, err := interactionTarget{session: s, exact: call, calls: []*upstreamCall{call}}.
		interact(context.Background(), "elicitation/create", nil, time.Minute)
	if err != errInteractionBudgetSpent {
		t.Fatalf("err = %v, want errInteractionBudgetSpent", err)
	}
	sink.none(t, 50*time.Millisecond)
}

// TestInteract_PausesEverySiblingCall: with the exact call unknown (a private
// instance with several calls in flight), every candidate call is paused and
// each is charged against its own budget.
func TestInteract_PausesEverySiblingCall(t *testing.T) {
	t.Parallel()
	s, sink := newBareSession(t)
	ctxA, callA, endA := s.beginUpstreamCall(context.Background(), "srv", 100*time.Millisecond)
	defer endA()
	ctxB, callB, endB := s.beginUpstreamCall(context.Background(), "srv", 100*time.Millisecond)
	defer endB()

	done := make(chan error, 1)
	go func() {
		_, _, err := interactionTarget{session: s, calls: []*upstreamCall{callA, callB}}.
			interact(context.Background(), "elicitation/create", nil, time.Minute)
		done <- err
	}()
	req := sink.next(t)
	assertLive(t, ctxA, 250*time.Millisecond)
	if ctxB.Err() != nil {
		t.Fatal("sibling call expired while paused")
	}
	respond(t, s, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"cancel"}}`)
	if err := <-done; err != nil {
		t.Fatalf("interact: %v", err)
	}
	for name, call := range map[string]*upstreamCall{"A": callA, "B": callB} {
		call.budget.mu.Lock()
		waited := call.budget.waited
		call.budget.mu.Unlock()
		if waited < 200*time.Millisecond {
			t.Errorf("call %s charged %v, want the whole pause", name, waited)
		}
	}
}

// TestUpstreamCall_CarriesOwner: a call's context carries its owner, which
// names the session and the client's request id and leads back to the call.
func TestUpstreamCall_CarriesOwner(t *testing.T) {
	t.Parallel()
	s, _ := newBareSession(t)
	ctx, call, end := s.beginUpstreamCall(withDownstreamRequestID(context.Background(), json.RawMessage(`"req-1"`)), "srv", time.Minute)

	if string(call.requestID) != `"req-1"` || call.owner.Session != s.id || string(call.owner.RequestID) != `"req-1"` {
		t.Errorf("call attribution = %s / %+v", call.requestID, call.owner)
	}
	if callFromOwner(call.owner) != call || callFromOwner(nil) != nil {
		t.Error("callFromOwner does not lead back to the call")
	}
	if owner := mcp.CallOwnerFromContext(ctx); owner != call.owner {
		t.Error("the call context does not carry the call's owner")
	}
	end()
	if ctx.Err() == nil {
		t.Error("the call context outlived end")
	}
}
