package server

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
	"github.com/Bigsy/mcpmu/internal/process"
)

// startCoreSessions attaches one stdio session per entry of sessionOpts to a
// single Core — the daemon's shape — and initializes each with elicitation.
func startCoreSessions(t *testing.T, cfg *config.Config, sessionOpts ...SessionOptions) []*relayHarness {
	t.Helper()
	return startCoreSessionsWith(t, cfg, `{"elicitation":{}}`, sessionOpts...)
}

// startCoreSessionsWithCaps is startCoreSessions for one session whose
// client declares clientCaps.
func startCoreSessionsWithCaps(t *testing.T, cfg *config.Config, clientCaps string) []*relayHarness {
	t.Helper()
	return startCoreSessionsWith(t, cfg, clientCaps, SessionOptions{})
}

func startCoreSessionsWith(t *testing.T, cfg *config.Config, clientCaps string, sessionOpts ...SessionOptions) []*relayHarness {
	t.Helper()
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	harnesses := make([]*relayHarness, 0, len(sessionOpts))
	for _, so := range sessionOpts {
		pr, pw := io.Pipe()
		stdout := &lockedBuffer{}
		sess, err := NewSession(core, Options{
			SessionOptions: so, Config: cfg, Stdin: pr, Stdout: stdout,
			ServerName: "mcpmu-test", ServerVersion: "1.0.0",
		})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan struct{})
		go func() {
			defer close(runDone)
			_ = sess.Run(ctx)
		}()
		h := &relayHarness{
			subscribeTestServer: &subscribeTestServer{srv: sess, pw: pw, pr: pr, stdout: stdout, runDone: runDone, cancel: cancel},
			t:                   t,
			consumed:            map[int]bool{},
		}
		t.Cleanup(func() {
			h.close(t)
			cancel()
			_ = pr.Close()
		})
		h.write(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":`+clientCaps+`,"clientInfo":{"name":"test","version":"1.0"}}}`,
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		)
		h.response("1")
		harnesses = append(harnesses, h)
	}
	return harnesses
}

// sharedElicitConfig is one shared server opted in to elicitation, with an
// eliciting tool, a delayed-request tool and a long-running tool.
func sharedElicitConfig(t *testing.T, heuristic *config.SingleCallerHeuristic, logPath string) *config.Config {
	t.Helper()
	script := formElicitation("confirm")["confirm"]
	delayed := script
	delayed.AfterResponse = true
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, true, true, fakeserver.Config{
			Tools:              []fakeserver.Tool{{Name: "confirm"}, {Name: "later"}, {Name: "slow"}},
			ToolServerRequests: map[string]fakeserver.ServerRequestScript{"confirm": script, "later": delayed},
			ToolHoldMs:         map[string]int{"slow": 1500},
			RequestLogPath:     logPath,
		}),
	}}
	cfg.ElicitationSingleCallerHeuristic = heuristic
	return cfg
}

func noElicitation(f rpcFrame) bool { return f.Method == "elicitation/create" }

// TestSharedElicitation_HeuristicOffFallsBack: with one caller on a shared
// stdio instance but the heuristic off (the default), the request is not
// relayed at all; the upstream gets "cancel".
func TestSharedElicitation_HeuristicOffFallsBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	hs := startCoreSessions(t, sharedElicitConfig(t, nil, ""), SessionOptions{}, SessionOptions{})
	hs[0].write(callTool(2, "srv.confirm"))
	if outcome := hs[0].outcome(hs[0].response("2")); string(outcome.Result) != `{"action":"cancel"}` {
		t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
	}
	hs[0].noFrame("elicitation", 0, noElicitation)
	hs[1].noFrame("elicitation", 0, noElicitation)
}

// TestSharedElicitation_HeuristicRoutesSingleCaller: enabled for stdio
// sessions, the single-caller heuristic sends the request to the caller's
// session and nowhere else.
func TestSharedElicitation_HeuristicRoutesSingleCaller(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	hs := startCoreSessions(t, sharedElicitConfig(t, &config.SingleCallerHeuristic{Stdio: true}, ""),
		SessionOptions{}, SessionOptions{})
	hs[1].write(callTool(2, "srv.confirm"))
	req := hs[1].request("elicitation/create")
	hs[1].write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"accept","content":{"ok":true}}}`)
	if outcome := hs[1].outcome(hs[1].response("2")); string(outcome.Result) != `{"action":"accept","content":{"ok":true}}` {
		t.Errorf("upstream saw %+v", outcome)
	}
	hs[0].noFrame("elicitation on the session that did not call", 0, noElicitation)
}

// TestSharedElicitation_SessionOverride: a session's --elicitation-heuristic
// wins over the config in both directions.
func TestSharedElicitation_SessionOverride(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	t.Run("on over config off", func(t *testing.T) {
		hs := startCoreSessions(t, sharedElicitConfig(t, nil, ""), SessionOptions{SingleCallerHeuristic: config.ToggleOn})
		hs[0].write(callTool(2, "srv.confirm"))
		req := hs[0].request("elicitation/create")
		hs[0].write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"decline"}}`)
		if outcome := hs[0].outcome(hs[0].response("2")); string(outcome.Result) != `{"action":"decline"}` {
			t.Errorf("upstream saw %+v", outcome)
		}
	})
	t.Run("off over config on", func(t *testing.T) {
		hs := startCoreSessions(t, sharedElicitConfig(t, &config.SingleCallerHeuristic{Stdio: true}, ""),
			SessionOptions{SingleCallerHeuristic: config.ToggleOff})
		hs[0].write(callTool(2, "srv.confirm"))
		if outcome := hs[0].outcome(hs[0].response("2")); string(outcome.Result) != `{"action":"cancel"}` {
			t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
		}
	})
}

// TestSharedElicitation_TwoCallersFallBack: with calls from two sessions in
// flight on a shared stdio instance, nothing ties the request to either, so
// even with the heuristic on it falls back.
func TestSharedElicitation_TwoCallersFallBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	hs := startCoreSessions(t, sharedElicitConfig(t, &config.SingleCallerHeuristic{Stdio: true}, ""),
		SessionOptions{}, SessionOptions{})
	hs[1].write(callTool(2, "srv.slow"))
	time.Sleep(200 * time.Millisecond) // the slow call is in flight
	hs[0].write(callTool(2, "srv.confirm"))
	if outcome := hs[0].outcome(hs[0].response("2")); string(outcome.Result) != `{"action":"cancel"}` {
		t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
	}
	hs[1].response("2")
	hs[0].noFrame("elicitation", 0, noElicitation)
	hs[1].noFrame("elicitation", 0, noElicitation)
}

// TestSharedElicitation_DelayedRequestDoesNotMisroute: a request sent after
// its own call finished, while another session's call is in flight, looks
// exactly like that other call's request. With the heuristic off it must fall
// back rather than reach the wrong session.
func TestSharedElicitation_DelayedRequestDoesNotMisroute(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	hs := startCoreSessions(t, sharedElicitConfig(t, nil, logPath), SessionOptions{}, SessionOptions{})
	hs[1].write(callTool(2, "srv.slow"))
	time.Sleep(200 * time.Millisecond)
	hs[0].write(callTool(2, "srv.later"))
	hs[0].response("2")

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(readLines(t, logPath), "\n"), `outcome elicitation/create {"answered":true,"result":{"action":"cancel"}}`) {
		if time.Now().After(deadline) {
			t.Fatalf("upstream never got the fallback:\n%s", strings.Join(readLines(t, logPath), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	hs[1].response("2")
	hs[1].noFrame("misrouted elicitation", 0, noElicitation)
}

// TestSharedElicitation_PostOriginRoutesEachCaller: an HTTP upstream sends
// each elicitation on the response stream of the POST that caused it, so with
// two sessions' calls in flight on one shared instance — and the heuristic
// off — each request still reaches its own session.
func TestSharedElicitation_PostOriginRoutesEachCaller(t *testing.T) {
	t.Parallel()
	fake := mcptest.StartHTTPFake(t, mcptest.HTTPFakeConfig{
		Tools: []string{"confirm"},
		OnPOSTStream: map[string]fakeserver.ServerRequestScript{"confirm": {
			Method: "elicitation/create",
			Params: json.RawMessage(`{"message":"Proceed?","requestedSchema":{"type":"object","properties":{}}}`),
		}},
	})
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"web": {URL: fake.URL, ClientFeatures: &config.ClientFeatures{Elicitation: true}},
	}}
	hs := startCoreSessions(t, cfg, SessionOptions{}, SessionOptions{})

	hs[0].write(callTool(2, "web.confirm"))
	req0 := hs[0].request("elicitation/create")
	// The first call is still in flight (unanswered) when the second starts.
	hs[1].write(callTool(2, "web.confirm"))
	req1 := hs[1].request("elicitation/create")

	hs[1].write(`{"jsonrpc":"2.0","id":` + string(req1.ID) + `,"result":{"action":"accept","content":{"who":"second"}}}`)
	hs[0].write(`{"jsonrpc":"2.0","id":` + string(req0.ID) + `,"result":{"action":"accept","content":{"who":"first"}}}`)
	if outcome := hs[0].outcome(hs[0].response("2")); !strings.Contains(string(outcome.Result), `"first"`) {
		t.Errorf("first session's upstream call saw %s", outcome.Result)
	}
	if outcome := hs[1].outcome(hs[1].response("2")); !strings.Contains(string(outcome.Result), `"second"`) {
		t.Errorf("second session's upstream call saw %s", outcome.Result)
	}
	if caps := string(fake.DeclaredCapabilities()); !strings.Contains(caps, `"elicitation"`) {
		t.Errorf("shared opted-in server declared %s, want elicitation", caps)
	}
}

// TestSharedElicitation_GetStreamRequestNeedsHeuristic: a request on an HTTP
// upstream's standalone GET stream carries no origin, so on a shared instance
// it is routed only by the heuristic.
func TestSharedElicitation_GetStreamRequestNeedsHeuristic(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		heuristic *config.SingleCallerHeuristic
		want      string
	}{
		{"heuristic off", nil, `{"action":"cancel"}`},
		{"heuristic on", &config.SingleCallerHeuristic{Stdio: true}, `{"action":"accept"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := mcptest.StartHTTPFake(t, mcptest.HTTPFakeConfig{
				Tools: []string{"confirm"},
				OnGETStream: map[string]fakeserver.ServerRequestScript{"confirm": {
					Method: "elicitation/create",
					Params: json.RawMessage(`{"message":"Proceed?","requestedSchema":{"type":"object","properties":{}}}`),
				}},
			})
			cfg := &config.Config{SchemaVersion: 1, ElicitationSingleCallerHeuristic: tc.heuristic, Servers: map[string]config.ServerConfig{
				"web": {URL: fake.URL, ClientFeatures: &config.ClientFeatures{Elicitation: true}},
			}}
			hs := startCoreSessions(t, cfg, SessionOptions{})
			hs[0].write(callTool(2, "web.confirm"))
			if tc.heuristic != nil {
				req := hs[0].request("elicitation/create")
				hs[0].write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"accept"}}`)
			}
			if outcome := hs[0].outcome(hs[0].response("2")); string(outcome.Result) != tc.want {
				t.Errorf("upstream saw %+v, want %s", outcome, tc.want)
			}
		})
	}
}

// TestSharedElicitation_ConcurrentElicitAndCancel exercises two sessions on
// one shared instance under the race detector: one answering elicitations,
// the other cancelling its calls mid-elicitation.
func TestSharedElicitation_ConcurrentElicitAndCancel(t *testing.T) {
	t.Parallel()
	fake := mcptest.StartHTTPFake(t, mcptest.HTTPFakeConfig{
		Tools: []string{"confirm"},
		OnPOSTStream: map[string]fakeserver.ServerRequestScript{"confirm": {
			Method: "elicitation/create",
			Params: json.RawMessage(`{"message":"Proceed?","requestedSchema":{"type":"object","properties":{}}}`),
		}},
	})
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"web": {URL: fake.URL, ClientFeatures: &config.ClientFeatures{Elicitation: true}},
	}}
	hs := startCoreSessions(t, cfg, SessionOptions{}, SessionOptions{})

	const rounds = 5
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range rounds {
			id := 10 + i
			hs[1].write(callTool(id, "web.confirm"))
			hs[1].request("elicitation/create")
			hs[1].write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + strconv.Itoa(id) + `}}`)
			hs[1].notification("notifications/cancelled")
		}
	}()
	for i := range rounds {
		id := 10 + i
		hs[0].write(callTool(id, "web.confirm"))
		req := hs[0].request("elicitation/create")
		hs[0].write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"accept"}}`)
		if outcome := hs[0].outcome(hs[0].response(strconv.Itoa(id))); string(outcome.Result) != `{"action":"accept"}` {
			t.Errorf("round %d: upstream saw %+v", i, outcome)
		}
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the cancelling session never finished its rounds")
	}
}

// TestRouteServerRequest_Rules pins the routing table: which rule applies to
// which evidence, and that the heuristic needs both the caller's permission
// (allowHeuristic) and the target session's switch.
func TestRouteServerRequest_Rules(t *testing.T) {
	t.Parallel()
	core, err := NewCore(Options{Config: config.NewConfig(), PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Close)
	newSession := func(heuristic config.Toggle) *Session {
		s, err := NewSession(core, Options{SessionOptions: SessionOptions{SingleCallerHeuristic: heuristic},
			Stdin: strings.NewReader(""), Stdout: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		return s
	}
	a, b := newSession(config.ToggleOn), newSession(config.ToggleUnset)
	call := func(s *Session) *upstreamCall {
		_, c, end := s.beginUpstreamCall(context.Background(), "srv", time.Minute)
		t.Cleanup(func() { end() })
		return c
	}
	a1, a2, b1 := call(a), call(a), call(b)
	shared := process.SharedInstanceID("srv")

	for _, tc := range []struct {
		name      string
		req       process.UpstreamRequest
		heuristic bool
		want      *Session
		exact     *upstreamCall
		calls     int
		rule      string
	}{
		{name: "private, one call", req: process.UpstreamRequest{Instance: process.PrivateInstanceID("srv", a.id),
			ServerRequest: mcp.ServerRequest{InFlight: []*mcp.CallOwner{a1.owner}}},
			want: a, exact: a1, calls: 1, rule: rulePrivate},
		{name: "private, two calls, no hint", req: process.UpstreamRequest{Instance: process.PrivateInstanceID("srv", a.id),
			ServerRequest: mcp.ServerRequest{InFlight: []*mcp.CallOwner{a1.owner, a2.owner}}},
			want: a, calls: 2, rule: rulePrivate},
		{name: "private, two calls, origin", req: process.UpstreamRequest{Instance: process.PrivateInstanceID("srv", a.id),
			ServerRequest: mcp.ServerRequest{Origin: a2.owner, InFlight: []*mcp.CallOwner{a1.owner, a2.owner}}},
			want: a, exact: a2, calls: 1, rule: rulePrivate},
		{name: "private, no call in flight", req: process.UpstreamRequest{Instance: process.PrivateInstanceID("srv", a.id)},
			want: a, rule: rulePrivate},
		{name: "private, unknown session", req: process.UpstreamRequest{Instance: process.PrivateInstanceID("srv", "session-x")}},
		{name: "shared, origin among several callers", req: process.UpstreamRequest{Instance: shared,
			ServerRequest: mcp.ServerRequest{Origin: b1.owner, InFlight: []*mcp.CallOwner{a1.owner, b1.owner}}},
			want: b, exact: b1, calls: 1, rule: rulePostOrigin},
		{name: "shared, one caller, heuristic enabled", heuristic: true, req: process.UpstreamRequest{Instance: shared,
			ServerRequest: mcp.ServerRequest{InFlight: []*mcp.CallOwner{a1.owner}}},
			want: a, exact: a1, calls: 1, rule: ruleSingleCaller},
		{name: "shared, one caller, session switch off", heuristic: true, req: process.UpstreamRequest{Instance: shared,
			ServerRequest: mcp.ServerRequest{InFlight: []*mcp.CallOwner{b1.owner}}}},
		{name: "shared, one caller, heuristic not allowed (sampling)", req: process.UpstreamRequest{Instance: shared,
			ServerRequest: mcp.ServerRequest{InFlight: []*mcp.CallOwner{a1.owner}}}},
		{name: "shared, two callers", heuristic: true, req: process.UpstreamRequest{Instance: shared,
			ServerRequest: mcp.ServerRequest{InFlight: []*mcp.CallOwner{a1.owner, b1.owner}}}},
		{name: "shared, no caller", heuristic: true, req: process.UpstreamRequest{Instance: shared}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, rule, ok := core.routeServerRequest(tc.req, tc.heuristic)
			if ok != (tc.want != nil) {
				t.Fatalf("ok = %v, want %v", ok, tc.want != nil)
			}
			if !ok {
				return
			}
			if target.session != tc.want || target.exact != tc.exact || len(target.calls) != tc.calls || rule != tc.rule {
				t.Errorf("target = session %s exact %v calls %d rule %s", target.session.id, target.exact, len(target.calls), rule)
			}
		})
	}
}
