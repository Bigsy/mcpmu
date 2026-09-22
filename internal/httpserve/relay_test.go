package httpserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/mcptest"
)

const testRequestFrame = `{"jsonrpc":"2.0","id":"mcpmu-1","method":"elicitation/create","params":{"message":"Confirm?"}}`

// TestHubDeliverFailureModes: a request is refused when nobody can see it —
// the hub is closed, or no GET stream is attached — instead of queueing into
// silence.
func TestHubDeliverFailureModes(t *testing.T) {
	hub := newSSEHub()
	if err := hub.Deliver([]byte(testRequestFrame + "\n")); !errors.Is(err, errNoStandaloneStream) {
		t.Fatalf("Deliver with no stream = %v, want errNoStandaloneStream", err)
	}
	own, _, _ := hub.attach()
	if err := hub.Deliver([]byte(testRequestFrame + "\n")); err != nil {
		t.Fatalf("Deliver with a stream attached: %v", err)
	}
	if frames := hub.takeAll(own); len(frames) != 1 || !frames[0].request {
		t.Fatalf("queued frames = %+v, want the one request", frames)
	}
	hub.detach(own)
	if err := hub.Deliver([]byte(testRequestFrame + "\n")); !errors.Is(err, errNoStandaloneStream) {
		t.Fatalf("Deliver after detach = %v, want errNoStandaloneStream", err)
	}
	hub.attach()
	hub.close()
	if err := hub.Deliver([]byte(testRequestFrame + "\n")); !errors.Is(err, errHubClosed) {
		t.Fatalf("Deliver after close = %v, want errHubClosed", err)
	}
}

// TestHubBacklogNeverEvictsRequests: overflow evicts notifications to make
// room, never a request; a backlog of nothing but requests refuses the next
// request and drops the next notification.
func TestHubBacklogNeverEvictsRequests(t *testing.T) {
	hub := newSSEHub()
	own, _, _ := hub.attach()
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"n":0}}` + "\n"))
	for i := range hubBacklogCap - 1 {
		frame := fmt.Sprintf(`{"jsonrpc":"2.0","id":"mcpmu-%d","method":"elicitation/create"}`, i)
		if err := hub.Deliver([]byte(frame)); err != nil {
			t.Fatalf("Deliver %d: %v", i, err)
		}
	}
	// Full: one notification plus requests. A request evicts the notification.
	if err := hub.Deliver([]byte(`{"jsonrpc":"2.0","id":"mcpmu-last","method":"elicitation/create"}`)); err != nil {
		t.Fatalf("Deliver into a backlog holding a notification: %v", err)
	}
	// Now all requests: the next request is refused, a notification dropped.
	if err := hub.Deliver([]byte(`{"jsonrpc":"2.0","id":"mcpmu-over","method":"elicitation/create"}`)); !errors.Is(err, errBacklogFull) {
		t.Fatalf("Deliver into a backlog of requests = %v, want errBacklogFull", err)
	}
	_, _ = hub.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"n":1}}` + "\n"))

	frames := hub.takeAll(own)
	if len(frames) != hubBacklogCap {
		t.Fatalf("backlog holds %d frames, want %d", len(frames), hubBacklogCap)
	}
	for _, frame := range frames {
		if !frame.request {
			t.Fatalf("a notification survived while requests were evicted: %s", frame.data)
		}
	}
}

// TestPostStreamCarriesTiedRequest: a request tied to a POST still being
// handled upgrades that POST's response to SSE — the request first, then the
// final response on the same stream — with no GET stream open.
func TestPostStreamCarriesTiedRequest(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.Delays = map[string]time.Duration{"tools/call": 600 * time.Millisecond}
	srv, base := startServer(t, singleServerConfig(t, fake), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)
	hs := srv.lookup(probe.SessionID, "")
	if hs == nil {
		t.Fatal("session not registered")
	}
	toolName := firstToolName(t, probe)

	respCh := make(chan *http.Response, 1)
	go func() {
		respCh <- probe.Post(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, toolName))
	}()
	waitForPost(t, hs, "3")
	if err := hs.DeliverRequest([]byte(testRequestFrame), json.RawMessage(`3`)); err != nil {
		t.Fatalf("DeliverRequest on the call's POST: %v", err)
	}

	resp := <-respCh
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("POST response Content-Type = %q, want text/event-stream", ct)
	}
	stream := mcptest.ResponseStream(t, resp)
	if first := stream.NextMessage(t, 2*time.Second); first.Data != testRequestFrame {
		t.Fatalf("first event = %s, want the relayed request", first.Data)
	}
	final := stream.NextMessage(t, 3*time.Second)
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(final.Data), &msg); err != nil || string(msg.ID) != "3" || msg.Result == nil {
		t.Fatalf("final event = %s, want the tools/call response", final.Data)
	}

	// Once answered, the POST can no longer carry anything, and with no GET
	// stream attached a request tied to it has nowhere to go.
	if err := hs.DeliverRequest([]byte(testRequestFrame), json.RawMessage(`3`)); !errors.Is(err, errNoStandaloneStream) {
		t.Fatalf("DeliverRequest after the POST was answered = %v, want errNoStandaloneStream", err)
	}
}

// TestUntiedRequestUsesGetStreamOnly: a request tied to no POST goes on the
// GET stream when one is attached and fails when none is — even with a POST
// in flight that it could have been guessed onto.
func TestUntiedRequestUsesGetStreamOnly(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.Delays = map[string]time.Duration{"tools/call": 400 * time.Millisecond}
	srv, base := startServer(t, singleServerConfig(t, fake), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)
	hs := srv.lookup(probe.SessionID, "")
	toolName := firstToolName(t, probe)

	respCh := make(chan *http.Response, 1)
	go func() {
		respCh <- probe.Post(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, toolName))
	}()
	waitForPost(t, hs, "4")
	if err := hs.DeliverRequest([]byte(testRequestFrame), nil); !errors.Is(err, errNoStandaloneStream) {
		t.Fatalf("untied DeliverRequest with no GET stream = %v, want errNoStandaloneStream", err)
	}
	resp := <-respCh
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("an untied request upgraded an unrelated POST (Content-Type %q)", ct)
	}
	_ = resp.Body.Close()

	stream := probe.OpenStream(t)
	if stream.Status != http.StatusOK {
		t.Fatalf("GET status %d", stream.Status)
	}
	// The attach is asynchronous with respect to the handler; retry briefly.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := hs.DeliverRequest([]byte(testRequestFrame), nil)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("DeliverRequest with a GET stream: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ev := stream.NextMessage(t, 2*time.Second); ev.Data != testRequestFrame {
		t.Fatalf("GET event = %s, want the request", ev.Data)
	}
}

// TestPostedClientResponseIs202: a client's JSON-RPC response is accepted
// with 202 and no body, whether or not anything was waiting for it.
func TestPostedClientResponseIs202(t *testing.T) {
	_, base := startServer(t, singleServerConfig(t, mcptest.DefaultConfig()), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.Initialize(t)
	resp := probe.Post(t, `{"jsonrpc":"2.0","id":"mcpmu-99","result":{"action":"accept"}}`)
	if body := mcptest.ReadBody(t, resp); resp.StatusCode != http.StatusAccepted || body != "" {
		t.Fatalf("status %d body %q, want 202 and no body", resp.StatusCode, body)
	}
}

func firstToolName(t *testing.T, probe *mcptest.HTTPProbe) string {
	t.Helper()
	list := probe.Call(t, 2, "tools/list", nil)
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list.Result, &tools); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools/list: %s", list.Result)
	}
	return tools.Tools[0].Name
}

// waitForPost waits until the POST carrying request id key is registered.
func waitForPost(t *testing.T, hs *httpSession, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for hs.posts.get(key) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("POST %s never registered", key)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
