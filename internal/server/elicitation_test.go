package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
	"github.com/Bigsy/mcpmu/internal/process"
)

// relayHarness drives one stdio session interactively and reads its output
// as frames, telling requests (method + id) from responses (id only).
type relayHarness struct {
	*subscribeTestServer
	t        *testing.T
	consumed map[int]bool // frame indexes already matched by a wait
}

func (h *relayHarness) frames() []rpcFrame {
	var frames []rpcFrame
	scanner := bufio.NewScanner(strings.NewReader(h.stdout.String()))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		var frame rpcFrame
		if json.Unmarshal(scanner.Bytes(), &frame) == nil {
			frames = append(frames, frame)
		}
	}
	return frames
}

// waitFrame waits for the first not-yet-matched frame that matches and
// marks it matched. Frames are matched in any order: a relayed request and
// the response of the call that caused it are written concurrently.
func (h *relayHarness) waitFrame(what string, match func(rpcFrame) bool) rpcFrame {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		frames := h.frames()
		for i, frame := range frames {
			if !h.consumed[i] && match(frame) {
				h.consumed[i] = true
				return frame
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; output:\n%s", what, h.stdout.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// noFrame fails if an unmatched frame matching appears within d.
func (h *relayHarness) noFrame(what string, d time.Duration, match func(rpcFrame) bool) {
	h.t.Helper()
	time.Sleep(d)
	for i, frame := range h.frames() {
		if !h.consumed[i] && match(frame) {
			h.t.Fatalf("unexpected %s: %+v", what, frame)
		}
	}
}

func (h *relayHarness) request(method string) rpcFrame {
	h.t.Helper()
	return h.waitFrame(method+" request", func(f rpcFrame) bool { return f.Method == method && f.ID != nil })
}

func (h *relayHarness) response(id string) rpcFrame {
	h.t.Helper()
	return h.waitFrame("response "+id, func(f rpcFrame) bool { return f.Method == "" && string(f.ID) == id })
}

func (h *relayHarness) notification(method string) rpcFrame {
	h.t.Helper()
	return h.waitFrame(method, func(f rpcFrame) bool { return f.Method == method && f.ID == nil })
}

// outcome decodes the fake's ServerRequestOutcome from a tools/call response.
func (h *relayHarness) outcome(resp rpcFrame) fakeserver.ServerRequestOutcome {
	h.t.Helper()
	if resp.Error != nil {
		h.t.Fatalf("tools/call failed: %d %s", resp.Error.Code, resp.Error.Message)
	}
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil || len(result.Content) == 0 {
		h.t.Fatalf("tools/call result = %s", resp.Result)
	}
	var outcome fakeserver.ServerRequestOutcome
	if err := json.Unmarshal([]byte(result.Content[0].Text), &outcome); err != nil {
		h.t.Fatalf("tool text is not an outcome: %s", result.Content[0].Text)
	}
	return outcome
}

// startRelayHarness serves cfg over stdio and initializes with the given
// client capabilities (JSON object).
func startRelayHarness(t *testing.T, cfg *config.Config, clientCaps string, opts Options) *relayHarness {
	t.Helper()
	opts.Config = cfg
	h := &relayHarness{subscribeTestServer: startSubscribeTestServer(t, opts), t: t, consumed: map[int]bool{}}
	t.Cleanup(func() { h.close(t) })
	h.write(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":`+clientCaps+`,"clientInfo":{"name":"test","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	h.response("1")
	return h
}

// elicitServer builds a server config whose tools make scripted
// server-to-client requests.
func elicitServer(t *testing.T, shared, optIn bool, fake fakeserver.Config) config.ServerConfig {
	t.Helper()
	encoded, err := json.Marshal(fake)
	if err != nil {
		t.Fatalf("marshal fake config: %v", err)
	}
	srv := fakeUpstream(string(encoded))
	srv.Shared = &shared
	if optIn {
		srv.ClientFeatures = &config.ClientFeatures{Elicitation: true}
	}
	return srv
}

func formElicitation(tool string) map[string]fakeserver.ServerRequestScript {
	return map[string]fakeserver.ServerRequestScript{tool: {
		Method: "elicitation/create",
		Params: json.RawMessage(`{"mode":"form","message":"Proceed?","requestedSchema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}`),
	}}
}

func callTool(id int, name string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, id, name)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// TestElicitation_PrivateInstanceRelaysForm: the certain case end to end over
// stdio. The capability is declared upstream, the request reaches the client
// with the requester named, and the client's answer reaches the tool.
func TestElicitation_PrivateInstanceRelaysForm(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	capsLog := filepath.Join(t.TempDir(), "caps.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools:                     []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests:        formElicitation("confirm"),
			ClientCapabilitiesLogPath: capsLog,
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{}}`, Options{})
	h.write(callTool(2, "srv.confirm"))

	req := h.request("elicitation/create")
	if !strings.HasPrefix(string(req.ID), `"mcpmu-`) {
		t.Errorf("relayed request id = %s, want an mcpmu-<n> id", req.ID)
	}
	var params struct {
		Message         string          `json:"message"`
		Mode            string          `json:"mode"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	_ = json.Unmarshal(req.Params, &params)
	if params.Message != "[srv] Proceed?" || params.Mode != "form" || len(params.RequestedSchema) == 0 {
		t.Errorf("relayed params = %s", req.Params)
	}
	h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"accept","content":{"ok":true}}}`)

	outcome := h.outcome(h.response("2"))
	if !outcome.Answered || string(outcome.Result) != `{"action":"accept","content":{"ok":true}}` {
		t.Errorf("upstream saw %+v", outcome)
	}
	if lines := readLines(t, capsLog); !strings.Contains(lines[0], `"elicitation"`) || !strings.Contains(lines[0], `"url"`) {
		t.Errorf("declared capabilities = %v, want elicitation with form and url", lines)
	}
}

// TestElicitation_NotDeclaredWithoutOptIn: a private server that did not opt
// in declares nothing, and an elicitation it sends anyway is refused as
// before.
func TestElicitation_NotDeclaredWithoutOptIn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	capsLog := filepath.Join(t.TempDir(), "caps.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, false, fakeserver.Config{
			Tools:                     []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests:        formElicitation("confirm"),
			ClientCapabilitiesLogPath: capsLog,
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{}}`, Options{})
	h.write(callTool(2, "srv.confirm"))
	outcome := h.outcome(h.response("2"))
	if outcome.Error == nil || outcome.Error.Code != -32601 {
		t.Errorf("upstream saw %+v, want method not found", outcome)
	}
	if lines := readLines(t, capsLog); lines[0] != "{}" {
		t.Errorf("declared capabilities = %v, want {}", lines)
	}
}

// TestElicitation_ClientWithoutCapabilityGetsCancel: a client that did not
// declare elicitation is never sent one; the upstream gets "cancel".
func TestElicitation_ClientWithoutCapabilityGetsCancel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools:              []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests: formElicitation("confirm"),
		}),
	}}
	h := startRelayHarness(t, cfg, `{}`, Options{})
	h.write(callTool(2, "srv.confirm"))
	outcome := h.outcome(h.response("2"))
	if !outcome.Answered || string(outcome.Result) != `{"action":"cancel"}` {
		t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
	}
	h.noFrame("elicitation for a client that never declared the capability", 0, func(f rpcFrame) bool {
		return f.Method == "elicitation/create"
	})
}

// TestElicitation_URLModeNeedsURLCapability: a URL-mode elicitation for a
// client that declared only form gets "cancel"; for a client that declared
// url it is relayed with its id rewritten and its URL untouched.
func TestElicitation_URLModeNeedsURLCapability(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	const url = "https://auth.example.test/connect?elicitationId=up-1"
	fake := fakeserver.Config{
		Tools: []fakeserver.Tool{{Name: "connect"}},
		ToolServerRequests: map[string]fakeserver.ServerRequestScript{"connect": {
			Method: "elicitation/create",
			Params: json.RawMessage(`{"mode":"url","elicitationId":"up-1","url":"` + url + `","message":"Sign in"}`),
		}},
	}
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fake),
	}}

	t.Run("form only", func(t *testing.T) {
		h := startRelayHarness(t, cfg, `{"elicitation":{"form":{}}}`, Options{})
		h.write(callTool(2, "srv.connect"))
		if outcome := h.outcome(h.response("2")); string(outcome.Result) != `{"action":"cancel"}` {
			t.Errorf("upstream saw %+v, want the cancel fallback", outcome)
		}
	})
	t.Run("url", func(t *testing.T) {
		h := startRelayHarness(t, cfg, `{"elicitation":{"url":{}}}`, Options{})
		h.write(callTool(2, "srv.connect"))
		req := h.request("elicitation/create")
		var params struct {
			ElicitationID string `json:"elicitationId"`
			URL           string `json:"url"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.URL != url {
			t.Errorf("url = %q, want it untouched", params.URL)
		}
		if params.ElicitationID == "up-1" || !strings.HasPrefix(params.ElicitationID, "mcpmu/") {
			t.Errorf("elicitationId = %q, want a minted mcpmu/ id", params.ElicitationID)
		}
		h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"accept"}}`)
		if outcome := h.outcome(h.response("2")); string(outcome.Result) != `{"action":"accept"}` {
			t.Errorf("upstream saw %+v", outcome)
		}
	})
}

// TestElicitation_URLRequiredErrorAndCompletion: a -32042 passes through with
// its elicitation ids rewritten, and the upstream's later
// notifications/elicitation/complete reaches the client under the rewritten
// id.
func TestElicitation_URLRequiredErrorAndCompletion(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools: []fakeserver.Tool{{Name: "files"}},
			ToolURLElicitations: map[string]fakeserver.URLElicitationScript{"files": {
				ElicitationID: "up-42", URL: "https://auth.example.test/x", Message: "Authorize", CompleteAfterMs: 200,
			}},
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{"url":{}}}`, Options{})
	h.write(callTool(2, "srv.files"))

	resp := h.response("2")
	if resp.Error == nil || resp.Error.Code != ErrCodeURLElicitationRequired {
		t.Fatalf("response = %+v, want -32042", resp)
	}
	var data struct {
		Elicitations []struct {
			ElicitationID string `json:"elicitationId"`
			URL           string `json:"url"`
			Mode          string `json:"mode"`
		} `json:"elicitations"`
	}
	if err := json.Unmarshal(resp.Error.Data, &data); err != nil || len(data.Elicitations) != 1 {
		t.Fatalf("error data = %s", resp.Error.Data)
	}
	minted := data.Elicitations[0].ElicitationID
	if minted == "up-42" || !strings.HasPrefix(minted, "mcpmu/") || data.Elicitations[0].URL != "https://auth.example.test/x" {
		t.Errorf("elicitation = %+v, want the id rewritten and the url untouched", data.Elicitations[0])
	}

	done := h.notification("notifications/elicitation/complete")
	var params struct {
		ElicitationID string `json:"elicitationId"`
	}
	_ = json.Unmarshal(done.Params, &params)
	if params.ElicitationID != minted {
		t.Errorf("completion id = %q, want %q", params.ElicitationID, minted)
	}
}

// TestElicitation_UpstreamCancellationWithdrawsDownstream: when the upstream
// withdraws its elicitation, the client's copy is withdrawn with
// notifications/cancelled, nothing goes back upstream, and a late answer from
// the client is dropped.
func TestElicitation_UpstreamCancellationWithdrawsDownstream(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools: []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests: map[string]fakeserver.ServerRequestScript{"confirm": {
				Method:            "elicitation/create",
				Params:            json.RawMessage(`{"message":"Proceed?","requestedSchema":{"type":"object","properties":{}}}`),
				CancelAfterMs:     300,
				WaitAfterCancelMs: 500,
			}},
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{}}`, Options{})
	h.write(callTool(2, "srv.confirm"))

	req := h.request("elicitation/create")
	withdrawal := h.notification("notifications/cancelled")
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	_ = json.Unmarshal(withdrawal.Params, &params)
	if string(params.RequestID) != string(req.ID) {
		t.Errorf("withdrawal names %s, want %s", params.RequestID, req.ID)
	}
	// The late answer must not reach the upstream.
	h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"accept"}}`)

	outcome := h.outcome(h.response("2"))
	if !outcome.Cancelled || outcome.Answered {
		t.Errorf("upstream saw %+v, want its withdrawn request left unanswered", outcome)
	}
}

// TestElicitation_CancelledCallWithdrawsAndFallsBack: cancelling the client's
// tools/call while its elicitation is pending withdraws the elicitation
// downstream, and the upstream — which did not cancel its own request — gets
// the fallback.
func TestElicitation_CancelledCallWithdrawsAndFallsBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools:              []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests: formElicitation("confirm"),
			RequestLogPath:     logPath,
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{}}`, Options{})
	h.write(callTool(2, "srv.confirm"))
	req := h.request("elicitation/create")

	h.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"user interrupted"}}`)
	withdrawal := h.notification("notifications/cancelled")
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	_ = json.Unmarshal(withdrawal.Params, &params)
	if string(params.RequestID) != string(req.ID) {
		t.Errorf("withdrawal names %s, want %s", params.RequestID, req.ID)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		log := strings.Join(readLines(t, logPath), "\n")
		if strings.Contains(log, `reply "srv-1"`) && strings.Contains(log, "notifications/cancelled") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("upstream never got both the fallback and the call's cancellation:\n%s", log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestElicitation_DelayedRequestOnPrivateInstance: a request that arrives
// after its call finished still belongs to the private instance's only
// session, so it is relayed (with no call to tie it to).
func TestElicitation_DelayedRequestOnPrivateInstance(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	logPath := filepath.Join(t.TempDir(), "requests.log")
	script := formElicitation("confirm")
	s := script["confirm"]
	s.AfterResponse = true
	script["confirm"] = s
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools:              []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests: script,
			RequestLogPath:     logPath,
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{}}`, Options{})
	h.write(callTool(2, "srv.confirm"))
	h.response("2")
	req := h.request("elicitation/create")
	h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"action":"decline"}}`)

	deadline := time.Now().Add(5 * time.Second)
	for {
		log := strings.Join(readLines(t, logPath), "\n")
		if strings.Contains(log, `outcome elicitation/create {"answered":true,"result":{"action":"decline"}}`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("upstream never got the client's answer:\n%s", log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestElicitationRoutes_ScopedByInstanceAndGeneration: ids from different
// servers or incarnations never collide, a completion from a restarted
// instance does not match the old incarnation's entry, and expired entries
// stop resolving.
func TestElicitationRoutes_ScopedByInstanceAndGeneration(t *testing.T) {
	t.Parallel()
	routes := newElicitationRoutes()
	now := time.Unix(1000, 0)
	routes.now = func() time.Time { return now }
	a := process.PrivateInstanceID("a", "session-1")
	b := process.PrivateInstanceID("b", "session-1")

	idA := routes.mint("session-1", a, 1, "same", time.Minute)
	idB := routes.mint("session-1", b, 1, "same", time.Minute)
	if idA == idB {
		t.Fatalf("two servers' identical ids minted the same downstream id %q", idA)
	}
	if again := routes.mint("session-1", a, 1, "same", time.Minute); again != idA {
		t.Errorf("re-seeing an elicitation minted %q, want the existing %q", again, idA)
	}

	if _, ok := routes.complete(a, 2, "same"); ok {
		t.Error("a restarted instance's completion matched the old incarnation")
	}
	if got, ok := routes.complete(a, 1, "same"); !ok || got != idA {
		t.Errorf("complete = %q, %v; want %q", got, ok, idA)
	}
	if _, ok := routes.complete(a, 1, "same"); ok {
		t.Error("a completion resolved twice")
	}

	now = now.Add(2 * time.Minute)
	if _, ok := routes.complete(b, 1, "same"); ok {
		t.Error("an expired entry still resolved")
	}
}

// TestRewriteURLElicitationError: -32042 data ids are rewritten and every
// other member survives; other errors are untouched.
func TestRewriteURLElicitationError(t *testing.T) {
	t.Parallel()
	s, _ := newBareSession(t)
	instance := process.SharedInstanceID("srv")
	in := &RPCError{Code: ErrCodeURLElicitationRequired, Message: "needs auth",
		Data: json.RawMessage(`{"elicitations":[{"mode":"url","elicitationId":"up-1","url":"https://x.test/?e=up-1","message":"m"}],"extra":1}`)}
	out := s.rewriteURLElicitationError(in, instance, 3)

	var data struct {
		Elicitations []map[string]string `json:"elicitations"`
		Extra        int                 `json:"extra"`
	}
	if err := json.Unmarshal(out.Data, &data); err != nil || len(data.Elicitations) != 1 {
		t.Fatalf("data = %s", out.Data)
	}
	got := data.Elicitations[0]
	if !strings.HasPrefix(got["elicitationId"], "mcpmu/") || got["url"] != "https://x.test/?e=up-1" || got["mode"] != "url" || data.Extra != 1 {
		t.Errorf("rewritten data = %s", out.Data)
	}
	if string(in.Data) == string(out.Data) {
		t.Error("the input error was modified or not rewritten")
	}
	if id, ok := s.elicitations.complete(instance, 3, "up-1"); !ok || id != got["elicitationId"] {
		t.Errorf("mapping not recorded: %q %v", id, ok)
	}

	other := &RPCError{Code: -32602, Message: "bad", Data: json.RawMessage(`{"elicitations":[{"elicitationId":"x"}]}`)}
	if s.rewriteURLElicitationError(other, instance, 3) != other {
		t.Error("a non -32042 error was rewritten")
	}
}

// TestElicitation_ClientDisconnectEndsPendingInteraction: a client that goes
// away with an elicitation pending must not keep serve alive — the call's
// budget is paused, so nothing else would end it before the interaction
// timeout.
func TestElicitation_ClientDisconnectEndsPendingInteraction(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": elicitServer(t, false, true, fakeserver.Config{
			Tools:              []fakeserver.Tool{{Name: "confirm"}},
			ToolServerRequests: formElicitation("confirm"),
		}),
	}}
	h := startRelayHarness(t, cfg, `{"elicitation":{}}`, Options{})
	h.write(callTool(2, "srv.confirm"))
	h.request("elicitation/create")

	start := time.Now()
	_ = h.pw.Close()
	select {
	case <-h.runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit after the client disconnected with an elicitation pending")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("serve took %v to exit", elapsed)
	}
}
