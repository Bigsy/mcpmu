package httpserve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

var elicitCaps = map[string]any{"elicitation": map[string]any{}}

// privateElicitConfig is one shared:false server opted in to elicitation.
func privateElicitConfig(t *testing.T, fake mcptest.FakeServerConfig) *config.Config {
	t.Helper()
	srv := fakeUpstream(t, fake)
	shared := false
	srv.Shared = &shared
	srv.ClientFeatures = &config.ClientFeatures{Elicitation: true}
	return &config.Config{Servers: map[string]config.ServerConfig{"fake": srv}}
}

func elicitingTool(name string, script fakeserver.ServerRequestScript) mcptest.FakeServerConfig {
	fake := mcptest.DefaultConfig()
	fake.Tools = append(fake.Tools, fakeserver.Tool{Name: name})
	if script.Method == "" {
		script.Method = "elicitation/create"
		script.Params = json.RawMessage(`{"message":"Proceed?","requestedSchema":{"type":"object","properties":{}}}`)
	}
	fake.ToolServerRequests = map[string]fakeserver.ServerRequestScript{name: script}
	return fake
}

func postToolCall(t *testing.T, probe *mcptest.HTTPProbe, id int, tool string) <-chan *http.Response {
	t.Helper()
	ch := make(chan *http.Response, 1)
	go func() {
		ch <- probe.Post(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, id, tool))
	}()
	return ch
}

type sseMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

func decodeEvent(t *testing.T, ev mcptest.SSEEvent) sseMessage {
	t.Helper()
	var msg sseMessage
	if err := json.Unmarshal([]byte(ev.Data), &msg); err != nil {
		t.Fatalf("SSE event is not JSON-RPC: %s", ev.Data)
	}
	return msg
}

func toolOutcome(t *testing.T, result json.RawMessage) fakeserver.ServerRequestOutcome {
	t.Helper()
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	var outcome fakeserver.ServerRequestOutcome
	if err := json.Unmarshal(result, &r); err != nil || len(r.Content) == 0 ||
		json.Unmarshal([]byte(r.Content[0].Text), &outcome) != nil {
		t.Fatalf("tool result is not an outcome: %s", result)
	}
	return outcome
}

// TestElicitationRidesPostStream: with no GET stream, an elicitation from a
// private instance with one call in flight arrives on that call's POST
// response stream, and the final response follows on the same stream.
func TestElicitationRidesPostStream(t *testing.T) {
	_, base := startServer(t, privateElicitConfig(t, elicitingTool("confirm", fakeserver.ServerRequestScript{})), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.InitializeWith(t, "2025-11-25", elicitCaps)

	resp := <-postToolCall(t, probe, 2, "fake.confirm")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("POST was not upgraded (Content-Type %q): %s", ct, mcptest.ReadBody(t, resp))
	}
	stream := mcptest.ResponseStream(t, resp)
	req := decodeEvent(t, stream.NextMessage(t, 5*time.Second))
	if req.Method != "elicitation/create" || !strings.Contains(string(req.Params), "[fake] Proceed?") {
		t.Fatalf("first event = %+v, want the relayed elicitation", req)
	}

	answer := probe.Post(t, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"accept","content":{}}}`)
	if answer.StatusCode != http.StatusAccepted {
		t.Fatalf("answer POST status %d", answer.StatusCode)
	}
	_ = answer.Body.Close()

	final := decodeEvent(t, stream.NextMessage(t, 5*time.Second))
	if string(final.ID) != "2" {
		t.Fatalf("final event = %+v, want the tools/call response", final)
	}
	if outcome := toolOutcome(t, final.Result); string(outcome.Result) != `{"action":"accept","content":{}}` {
		t.Errorf("upstream saw %+v", outcome)
	}
}

// TestElicitationAmbiguousCallsWithoutStreamFallsBack: two concurrent calls
// on a private instance, no origin hint (stdio upstream) and no GET stream —
// the second elicitation cannot be tied to either POST, so rather than guess
// it fails delivery and the upstream gets "cancel".
func TestElicitationAmbiguousCallsWithoutStreamFallsBack(t *testing.T) {
	fake := elicitingTool("first", fakeserver.ServerRequestScript{})
	fake.Tools = append(fake.Tools, fakeserver.Tool{Name: "second"})
	fake.ToolServerRequests["second"] = fake.ToolServerRequests["first"]
	_, base := startServer(t, privateElicitConfig(t, fake), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.InitializeWith(t, "2025-11-25", elicitCaps)

	// The first call's elicitation is tied to it and rides its POST; leave
	// it unanswered so the call stays in flight.
	first := <-postToolCall(t, probe, 2, "fake.first")
	firstStream := mcptest.ResponseStream(t, first)
	firstReq := decodeEvent(t, firstStream.NextMessage(t, 5*time.Second))

	second := <-postToolCall(t, probe, 3, "fake.second")
	if ct := second.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("an ambiguous elicitation was guessed onto a POST stream (Content-Type %q)", ct)
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(mcptest.ReadBody(t, second)), &resp); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if outcome := toolOutcome(t, resp.Result); string(outcome.Result) != `{"action":"cancel"}` {
		t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
	}

	answer := probe.Post(t, `{"jsonrpc":"2.0","id":`+string(firstReq.ID)+`,"result":{"action":"decline"}}`)
	_ = answer.Body.Close()
	final := decodeEvent(t, firstStream.NextMessage(t, 5*time.Second))
	if outcome := toolOutcome(t, final.Result); string(outcome.Result) != `{"action":"decline"}` {
		t.Errorf("first call's upstream saw %+v", outcome)
	}
}

// TestElicitationDelayedRequestUsesGetStream: a request that arrives after
// its call returned has no POST to ride; with a GET stream attached it is
// delivered there and the client's answer reaches the upstream.
func TestElicitationDelayedRequestUsesGetStream(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "requests.log")
	fake := elicitingTool("confirm", fakeserver.ServerRequestScript{})
	script := fake.ToolServerRequests["confirm"]
	script.AfterResponse = true
	fake.ToolServerRequests["confirm"] = script
	fake.RequestLogPath = logPath
	_, base := startServer(t, privateElicitConfig(t, fake), nil)
	probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
	probe.InitializeWith(t, "2025-11-25", elicitCaps)
	stream := probe.OpenStream(t)
	if stream.Status != http.StatusOK {
		t.Fatalf("GET status %d", stream.Status)
	}

	resp := <-postToolCall(t, probe, 2, "fake.confirm")
	_ = mcptest.ReadBody(t, resp)

	var req sseMessage
	for req.Method != "elicitation/create" {
		req = decodeEvent(t, stream.NextMessage(t, 5*time.Second))
	}
	answer := probe.Post(t, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"accept","content":{}}}`)
	_ = answer.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(logPath)
		if strings.Contains(string(data), `outcome elicitation/create {"answered":true,"result":{"action":"accept","content":{}}}`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("upstream never got the answer:\n%s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestElicitationHeuristicUsesHTTPSwitch: serve --http sessions read the
// heuristic's http switch, not the stdio one — HTTP sessions may belong to
// different people, so enabling it for local agents must not enable it here.
func TestElicitationHeuristicUsesHTTPSwitch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		heuristic config.SingleCallerHeuristic
		relayed   bool
	}{
		{"stdio switch only", config.SingleCallerHeuristic{Stdio: true}, false},
		{"http switch", config.SingleCallerHeuristic{HTTP: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := privateElicitConfig(t, elicitingTool("confirm", fakeserver.ServerRequestScript{}))
			srv := cfg.Servers["fake"]
			srv.Shared = nil
			cfg.Servers["fake"] = srv
			heuristic := tc.heuristic
			cfg.ElicitationSingleCallerHeuristic = &heuristic
			_, base := startServer(t, cfg, nil)
			probe := &mcptest.HTTPProbe{BaseURL: base + "/mcp"}
			probe.InitializeWith(t, "2025-11-25", elicitCaps)

			resp := <-postToolCall(t, probe, 2, "fake.confirm")
			if !tc.relayed {
				var body struct {
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal([]byte(mcptest.ReadBody(t, resp)), &body); err != nil {
					t.Fatal(err)
				}
				if outcome := toolOutcome(t, body.Result); string(outcome.Result) != `{"action":"cancel"}` {
					t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
				}
				return
			}
			stream := mcptest.ResponseStream(t, resp)
			req := decodeEvent(t, stream.NextMessage(t, 5*time.Second))
			if req.Method != "elicitation/create" {
				t.Fatalf("first event = %+v", req)
			}
			answer := probe.Post(t, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":{"action":"accept","content":{}}}`)
			_ = answer.Body.Close()
			final := decodeEvent(t, stream.NextMessage(t, 5*time.Second))
			if outcome := toolOutcome(t, final.Result); string(outcome.Result) != `{"action":"accept","content":{}}` {
				t.Errorf("upstream saw %+v", outcome)
			}
		})
	}
}
