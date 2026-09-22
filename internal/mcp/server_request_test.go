package mcp

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// decodeReply parses one outgoing frame as a JSON-RPC response and fails the
// test if it carries a method (i.e. is a request or notification instead).
func decodeReply(t *testing.T, frame []byte) (id json.RawMessage, result json.RawMessage, rpcErr *RPCError) {
	t.Helper()
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  *string         `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(frame, &env); err != nil {
		t.Fatalf("unmarshal outgoing frame %s: %v", frame, err)
	}
	if env.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", env.JSONRPC)
	}
	if env.Method != nil {
		t.Fatalf("expected a response frame, got method %q: %s", *env.Method, frame)
	}
	return env.ID, env.Result, env.Error
}

// TestClient_AnswersServerPing verifies that a ping request from the server is
// answered with an empty result, echoing the server's id. A server that pings
// its client as a liveness check must not see silence.
func TestClient_AnswersServerPing(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	tp.inject([]byte(`{"jsonrpc":"2.0","id":41,"method":"ping"}`))

	id, result, rpcErr := decodeReply(t, tp.nextSent(t, 2*time.Second))
	if string(id) != "41" {
		t.Errorf("id = %s, want 41", id)
	}
	if rpcErr != nil {
		t.Fatalf("unexpected error in ping reply: %v", rpcErr)
	}
	if string(result) != "{}" {
		t.Errorf("result = %s, want {}", result)
	}
}

// TestClient_RejectsUnsupportedServerRequest verifies that a server-to-client
// request mcpmu does not implement is answered with JSON-RPC "Method not
// found" rather than dropped, and that a string id is echoed verbatim.
func TestClient_RejectsUnsupportedServerRequest(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	tp.inject([]byte(`{"jsonrpc":"2.0","id":"srv-7","method":"elicitation/create","params":{"message":"Confirm?"}}`))

	id, result, rpcErr := decodeReply(t, tp.nextSent(t, 2*time.Second))
	if string(id) != `"srv-7"` {
		t.Errorf("id = %s, want \"srv-7\"", id)
	}
	if result != nil {
		t.Errorf("unexpected result in error reply: %s", result)
	}
	if rpcErr == nil {
		t.Fatal("expected a JSON-RPC error")
	}
	if rpcErr.Code != rpcCodeMethodNotFound {
		t.Errorf("code = %d, want %d", rpcErr.Code, rpcCodeMethodNotFound)
	}
	if rpcErr.Message == "" {
		t.Error("expected a non-empty error message")
	}
}

// TestClient_ServerRequestDoesNotBlockReader verifies that answering a server
// request never stalls the demultiplexer: a response to an outstanding call
// that arrives right behind the server's request must still be delivered
// even though the reply cannot be written (the transport's outbound queue is
// full and nobody drains it).
func TestClient_ServerRequestDoesNotBlockReader(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	// Fill the outbound queue so any further Send blocks until its context
	// expires. The client's own call frame goes through first.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var result json.RawMessage
		done <- client.call(ctx, "tools/list", nil, &result)
	}()
	callFrame := tp.nextSent(t, 2*time.Second)
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(callFrame, &req); err != nil {
		t.Fatalf("unmarshal call frame: %v", err)
	}
	for range cap(tp.out) {
		tp.out <- []byte("filler")
	}

	// A burst of server requests, then the response the call is waiting on.
	for i := range 8 {
		tp.inject([]byte(`{"jsonrpc":"2.0","id":"srv-` + strconv.Itoa(i) + `","method":"roots/list"}`))
	}
	tp.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(req.ID, 10) + `,"result":{"tools":[]}}`))

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call response not delivered: reader blocked behind server-request replies")
	}
}

// TestClient_Initialize_CapturesInstructions verifies that the server's
// `instructions` from initialize are retained and exposed.
func TestClient_Initialize_CapturesInstructions(t *testing.T) {
	tp := newSyntheticTransport()
	client := NewClient(tp)
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Initialize(ctx) }()

	initFrame := tp.nextSent(t, 2*time.Second)
	var req struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(initFrame, &req); err != nil {
		t.Fatalf("unmarshal initialize frame: %v", err)
	}
	if req.Method != "initialize" {
		t.Fatalf("first frame method = %q, want initialize", req.Method)
	}
	tp.inject([]byte(`{"jsonrpc":"2.0","id":` + strconv.FormatInt(req.ID, 10) + `,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"s","version":"1"},"instructions":"Always call search before fetch."}}`))
	_ = tp.nextSent(t, 2*time.Second) // notifications/initialized

	if err := <-done; err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := client.Instructions(); got != "Always call search before fetch." {
		t.Errorf("Instructions() = %q", got)
	}
}
