package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// startCall issues client.call on a goroutine and returns the upstream id it
// was sent with, plus a channel carrying the call's result.
func startCall(t *testing.T, ctx context.Context, client *Client, tp *syntheticTransport, method string) (int64, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		var result json.RawMessage
		done <- client.call(ctx, method, nil, &result)
	}()
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(tp.nextSent(t, 2*time.Second), &req); err != nil {
		t.Fatalf("unmarshal call frame: %v", err)
	}
	return req.ID, done
}

// TestClient_ServerRequestHandlerAnswers verifies an installed handler sees
// the request verbatim and its result is sent back under the server's id.
func TestClient_ServerRequestHandlerAnswers(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	seen := make(chan ServerRequest, 1)
	client.SetServerRequestHandler(func(_ context.Context, req ServerRequest) (json.RawMessage, *RPCError) {
		seen <- req
		return json.RawMessage(`{"action":"accept","content":{"ok":true}}`), nil
	})

	tp.inject([]byte(`{"jsonrpc":"2.0","id":"e-1","method":"elicitation/create","params":{"message":"Confirm?"}}`))

	req := <-seen
	if req.Method != "elicitation/create" || string(req.ID) != `"e-1"` || string(req.Params) != `{"message":"Confirm?"}` {
		t.Errorf("handler saw %+v", req)
	}
	if req.OriginRequestID != 0 || req.Origin != nil {
		t.Errorf("stdio-style request must carry no origin, got %d / %v", req.OriginRequestID, req.Origin)
	}
	id, result, rpcErr := decodeReply(t, tp.nextSent(t, 2*time.Second))
	if string(id) != `"e-1"` || rpcErr != nil || string(result) != `{"action":"accept","content":{"ok":true}}` {
		t.Errorf("reply = id %s result %s err %v", id, result, rpcErr)
	}
}

// TestClient_ServerCancelledRequestGetsNoReply verifies the server's own
// notifications/cancelled for a request it sent cancels the handler, and that
// no reply of any kind goes back, because the spec says a cancelled request
// SHOULD NOT be answered.
func TestClient_ServerCancelledRequestGetsNoReply(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	cancelled := make(chan error, 1)
	client.SetServerRequestHandler(func(ctx context.Context, req ServerRequest) (json.RawMessage, *RPCError) {
		<-ctx.Done()
		cancelled <- context.Cause(ctx)
		return nil, &RPCError{Code: -1, Message: "must not be sent"}
	})

	tp.inject([]byte(`{"jsonrpc":"2.0","id":7,"method":"elicitation/create","params":{}}`))
	tp.inject([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user gave up"}}`))

	select {
	case cause := <-cancelled:
		if cause != errServerCancelledRequest {
			t.Errorf("handler cause = %v, want %v", cause, errServerCancelledRequest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled")
	}
	select {
	case frame := <-tp.out:
		t.Fatalf("a reply went upstream for a cancelled request: %s", frame)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestClient_ServerCancellationDoesNotTouchOwnCalls verifies a server's
// notifications/cancelled is matched only against requests the server issued:
// naming one of mcpmu's own outstanding ids does nothing to that call.
func TestClient_ServerCancellationDoesNotTouchOwnCalls(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, done := startCall(t, ctx, client, tp, "tools/call")
	tp.inject([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + strconv.FormatInt(id, 10) + `}}`))
	tp.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(id, 10) + `,"result":{}}`))
	if err := <-done; err != nil {
		t.Fatalf("call failed after an unrelated cancellation: %v", err)
	}
}

// TestClient_HandlerRunsOffReader verifies a handler blocked on a human does
// not stop the reader: a response behind it is still delivered.
func TestClient_HandlerRunsOffReader(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	release := make(chan struct{})
	client.SetServerRequestHandler(func(ctx context.Context, req ServerRequest) (json.RawMessage, *RPCError) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, done := startCall(t, ctx, client, tp, "tools/call")
	tp.inject([]byte(`{"jsonrpc":"2.0","id":"blocked","method":"elicitation/create","params":{}}`))
	tp.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(id, 10) + `,"result":{}}`))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("call: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response stuck behind a blocked server request handler")
	}
}

// TestClient_HandlerPanicAnswersInternalError verifies a panicking handler
// still answers the server, with -32603, instead of leaving it waiting.
func TestClient_HandlerPanicAnswersInternalError(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()
	client.SetServerRequestHandler(func(context.Context, ServerRequest) (json.RawMessage, *RPCError) {
		panic("boom")
	})
	tp.inject([]byte(`{"jsonrpc":"2.0","id":3,"method":"roots/list"}`))
	_, _, rpcErr := decodeReply(t, tp.nextSent(t, 2*time.Second))
	if rpcErr == nil || rpcErr.Code != rpcCodeInternalError {
		t.Fatalf("reply error = %v, want internal error", rpcErr)
	}
}

// TestClient_CallOwnerRegistration verifies an owner attached to the call's
// context is visible while the request is in flight and gone once it ends.
func TestClient_CallOwnerRegistration(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	owner := &CallOwner{Session: "session-1", RequestID: json.RawMessage(`5`)}
	ctx, cancel := context.WithTimeout(WithCallOwner(context.Background(), owner), 5*time.Second)
	defer cancel()
	id, done := startCall(t, ctx, client, tp, "tools/call")

	if got := client.OwnerOf(id); got != owner {
		t.Errorf("OwnerOf(%d) = %v, want the call's owner", id, got)
	}
	if owners := client.InFlightOwners(); len(owners) != 1 || owners[0] != owner {
		t.Errorf("InFlightOwners() = %v, want [owner]", owners)
	}

	tp.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(id, 10) + `,"result":{}}`))
	if err := <-done; err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := client.OwnerOf(id); got != nil {
		t.Errorf("owner still registered after the call returned: %v", got)
	}
	if owners := client.InFlightOwners(); len(owners) != 0 {
		t.Errorf("InFlightOwners() after return = %v, want none", owners)
	}
}

// TestClient_DeclaresConfiguredCapabilities verifies initialize carries the
// capabilities set with SetClientCapabilities, and an empty object otherwise.
func TestClient_DeclaresConfiguredCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps map[string]any
		want string
	}{
		{name: "none", want: `{}`},
		{name: "elicitation", caps: map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}, want: `{"elicitation":{"form":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tp := newSyntheticTransport()
			client := NewClient(tp)
			defer func() { _ = client.Close() }()
			client.SetClientCapabilities(tc.caps)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			go func() { _ = client.Initialize(ctx) }()
			var req struct {
				Params struct {
					Capabilities json.RawMessage `json:"capabilities"`
				} `json:"params"`
			}
			if err := json.Unmarshal(tp.nextSent(t, 2*time.Second), &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(req.Params.Capabilities) != tc.want {
				t.Errorf("declared capabilities = %s, want %s", req.Params.Capabilities, tc.want)
			}
		})
	}
}

// TestStreamableHTTP_ServerRequestOnPOSTStreamCarriesOrigin verifies that a
// server request arriving on the response stream of one of mcpmu's POSTs is
// stamped with that request's id and resolved to its owner, while the reply
// the handler produces reaches the server and unblocks the call.
func TestStreamableHTTP_ServerRequestOnPOSTStreamCarriesOrigin(t *testing.T) {
	answered := make(chan json.RawMessage, 1)
	var answerOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(body, &msg)
		switch {
		case msg.Method == "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n",
				`{"jsonrpc":"2.0","id":"srv-1","method":"elicitation/create","params":{"message":"Confirm?"}}`)
			w.(http.Flusher).Flush()
			select {
			case result := <-answered:
				_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[],\"answer\":%s}}\n\n", msg.ID, result)
			case <-r.Context().Done():
			}
		case msg.Method == "" && string(msg.ID) == `"srv-1"`:
			answerOnce.Do(func() { answered <- msg.Result })
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	transport := NewStreamableHTTPTransport(StreamableHTTPConfig{URL: srv.URL})
	client := NewClient(transport)
	defer func() { _ = client.Close() }()

	owner := &CallOwner{Session: "session-1", RequestID: json.RawMessage(`"call-a"`)}
	seen := make(chan ServerRequest, 1)
	client.SetServerRequestHandler(func(_ context.Context, req ServerRequest) (json.RawMessage, *RPCError) {
		seen <- req
		return json.RawMessage(`{"action":"accept"}`), nil
	})

	ctx, cancel := context.WithTimeout(WithCallOwner(context.Background(), owner), 5*time.Second)
	defer cancel()
	var result struct {
		Answer json.RawMessage `json:"answer"`
	}
	if err := client.call(ctx, "tools/call", map[string]any{"name": "x"}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}

	req := <-seen
	if req.OriginRequestID == 0 {
		t.Error("request on a POST response stream carried no origin id")
	}
	if req.Origin != owner {
		t.Errorf("Origin = %v, want the call's owner", req.Origin)
	}
	if string(result.Answer) != `{"action":"accept"}` {
		t.Errorf("server saw answer %s", result.Answer)
	}
}

// expiringTransport fails the first tools/call send with SessionExpiredError
// and answers everything else, recording the owner registered for the call's
// id at the moment of each send.
type expiringTransport struct {
	*syntheticTransport
	client  *Client
	expired bool
	session string
	owners  chan *CallOwner
}

func (e *expiringTransport) SessionID() string { return e.session }

func (e *expiringTransport) Send(ctx context.Context, msg []byte) error {
	var req struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	_ = json.Unmarshal(msg, &req)
	switch req.Method {
	case "tools/call":
		e.owners <- e.client.OwnerOf(req.ID)
		if !e.expired {
			e.expired = true
			e.session = ""
			return &SessionExpiredError{}
		}
		e.inject([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, req.ID)))
	case "initialize":
		e.session = "fresh"
		e.inject([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"s","version":"1"}}}`, req.ID)))
	}
	return nil
}

// TestClient_OwnerSurvivesSessionRecovery: the resend after HTTP session
// recovery reuses the request's id, and its owner is still registered for it.
func TestClient_OwnerSurvivesSessionRecovery(t *testing.T) {
	tp := &expiringTransport{syntheticTransport: newSyntheticTransport(), session: "stale", owners: make(chan *CallOwner, 4)}
	client := NewClient(tp)
	tp.client = client
	defer func() { _ = client.Close() }()

	owner := &CallOwner{Session: "session-1", RequestID: json.RawMessage(`9`)}
	ctx, cancel := context.WithTimeout(WithCallOwner(context.Background(), owner), 5*time.Second)
	defer cancel()
	if _, err := client.CallTool(ctx, "x", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	for i, name := range []string{"first send", "resend after recovery"} {
		select {
		case got := <-tp.owners:
			if got != owner {
				t.Errorf("%s: owner = %v, want the call's owner", name, got)
			}
		default:
			t.Fatalf("send %d (%s) never happened", i, name)
		}
	}
}

// TestClient_InFlightSnapshotFollowsMessageOrder: a server request read
// before a call's response lists that call as in flight; one read after the
// response does not, however long the caller takes to return.
func TestClient_InFlightSnapshotFollowsMessageOrder(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	seen := make(chan ServerRequest, 2)
	client.SetServerRequestHandler(func(_ context.Context, req ServerRequest) (json.RawMessage, *RPCError) {
		seen <- req
		return json.RawMessage(`{}`), nil
	})

	owner := &CallOwner{Session: "session-1"}
	ctx, cancel := context.WithTimeout(WithCallOwner(context.Background(), owner), 5*time.Second)
	defer cancel()
	id, done := startCall(t, ctx, client, tp, "tools/call")

	tp.inject([]byte(`{"jsonrpc":"2.0","id":"before","method":"elicitation/create"}`))
	tp.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(id, 10) + `,"result":{}}`))
	tp.inject([]byte(`{"jsonrpc":"2.0","id":"after","method":"elicitation/create"}`))

	for range 2 {
		req := <-seen
		switch string(req.ID) {
		case `"before"`:
			if len(req.InFlight) != 1 || req.InFlight[0] != owner {
				t.Errorf("request read before the response: InFlight = %v, want [owner]", req.InFlight)
			}
		case `"after"`:
			if len(req.InFlight) != 0 {
				t.Errorf("request read after the response: InFlight = %v, want none", req.InFlight)
			}
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("call: %v", err)
	}
}
