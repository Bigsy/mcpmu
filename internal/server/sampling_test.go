package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

var (
	plainSampling = fakeserver.ServerRequestScript{
		Method: "sampling/createMessage",
		Params: json.RawMessage(`{"messages":[{"role":"user","content":{"type":"text","text":"Summarize"}}],"maxTokens":50,"_meta":{"trace":"t1"}}`),
	}
	toolSampling = fakeserver.ServerRequestScript{
		Method: "sampling/createMessage",
		Params: json.RawMessage(`{"messages":[{"role":"user","content":{"type":"text","text":"Look it up"}}],"maxTokens":50,"tools":[{"name":"search","inputSchema":{"type":"object"}}]}`),
	}
)

const sampleAnswer = `{"role":"assistant","content":{"type":"text","text":"Done."},"model":"m","stopReason":"endTurn"}`

func samplingServer(t *testing.T, shared bool, features config.ClientFeatures, capsLog string) config.ServerConfig {
	t.Helper()
	srv := elicitServer(t, shared, false, fakeserver.Config{
		Tools:                     []fakeserver.Tool{{Name: "summarize"}, {Name: "research"}},
		ToolServerRequests:        map[string]fakeserver.ServerRequestScript{"summarize": plainSampling, "research": toolSampling},
		ClientCapabilitiesLogPath: capsLog,
	})
	srv.ClientFeatures = &features
	return srv
}

// TestSampling_PrivateInstanceRelays: a private instance's sampling request
// reaches its client with the requester named in _meta (the messages and
// system prompt untouched), and the client's result reaches the tool.
func TestSampling_PrivateInstanceRelays(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	capsLog := filepath.Join(t.TempDir(), "caps.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": samplingServer(t, false, config.ClientFeatures{Sampling: true}, capsLog),
	}}
	h := startRelayHarness(t, cfg, `{"sampling":{}}`, Options{})
	h.write(callTool(2, "srv.summarize"))

	req := h.request("sampling/createMessage")
	var params struct {
		Messages json.RawMessage   `json:"messages"`
		Meta     map[string]string `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params, &params)
	if params.Meta["mcpmu/server"] != "srv" || params.Meta["trace"] != "t1" {
		t.Errorf("_meta = %v, want the requester added and the rest kept", params.Meta)
	}
	if string(params.Messages) != `[{"role":"user","content":{"type":"text","text":"Summarize"}}]` {
		t.Errorf("messages changed in transit: %s", params.Messages)
	}
	h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + sampleAnswer + `}`)
	if outcome := h.outcome(h.response("2")); string(outcome.Result) != sampleAnswer {
		t.Errorf("upstream saw %+v", outcome)
	}
	if caps := readLines(t, capsLog)[0]; !strings.Contains(caps, `"sampling":{}`) {
		t.Errorf("declared %s, want sampling without tools", caps)
	}
}

// TestSampling_NotDeclaredWithoutOptIn: sampling stays off unless opted in.
func TestSampling_NotDeclaredWithoutOptIn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	capsLog := filepath.Join(t.TempDir(), "caps.log")
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"srv": samplingServer(t, false, config.ClientFeatures{Elicitation: true}, capsLog),
	}}
	h := startRelayHarness(t, cfg, `{"sampling":{}}`, Options{})
	h.write(callTool(2, "srv.summarize"))
	if outcome := h.outcome(h.response("2")); outcome.Error == nil || outcome.Error.Code != -32601 {
		t.Errorf("upstream saw %+v, want method not found", outcome)
	}
	if caps := readLines(t, capsLog)[0]; strings.Contains(caps, "sampling") {
		t.Errorf("declared %s without opt-in", caps)
	}
}

// TestSampling_CapabilityChecks: a client that did not declare sampling is
// never sent one, and a request offering tools needs both the server's
// sampling-tools opt-in and the client's sampling.tools.
func TestSampling_CapabilityChecks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	for _, tc := range []struct {
		name       string
		features   config.ClientFeatures
		clientCaps string
		tool       string
		relayed    bool
	}{
		{"client without sampling", config.ClientFeatures{Sampling: true}, `{}`, "summarize", false},
		{"tools, server not opted in", config.ClientFeatures{Sampling: true}, `{"sampling":{"tools":{}}}`, "research", false},
		{"tools, client without sampling.tools", config.ClientFeatures{Sampling: true, SamplingTools: true}, `{"sampling":{}}`, "research", false},
		{"tools, both opted in", config.ClientFeatures{Sampling: true, SamplingTools: true}, `{"sampling":{"tools":{}}}`, "research", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
				"srv": samplingServer(t, false, tc.features, ""),
			}}
			h := startRelayHarness(t, cfg, tc.clientCaps, Options{})
			h.write(callTool(2, "srv."+tc.tool))
			if tc.relayed {
				req := h.request("sampling/createMessage")
				h.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + sampleAnswer + `}`)
			}
			outcome := h.outcome(h.response("2"))
			if tc.relayed != (outcome.Error == nil) {
				t.Errorf("upstream saw %+v, relayed want %v", outcome, tc.relayed)
			}
			if !tc.relayed {
				h.noFrame("sampling request", 0, func(f rpcFrame) bool { return f.Method == "sampling/createMessage" })
			}
		})
	}
}

// TestSampling_NeverUsesHeuristic: even with the single-caller heuristic on,
// a shared stdio instance's sampling request is refused.
func TestSampling_NeverUsesHeuristic(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("subprocess test")
	}
	cfg := &config.Config{SchemaVersion: 1,
		ElicitationSingleCallerHeuristic: &config.SingleCallerHeuristic{Stdio: true},
		Servers: map[string]config.ServerConfig{
			"srv": samplingServer(t, true, config.ClientFeatures{Sampling: true}, ""),
		}}
	h := startRelayHarness(t, cfg, `{"sampling":{}}`, Options{SessionOptions: SessionOptions{SingleCallerHeuristic: config.ToggleOn}})
	h.write(callTool(2, "srv.summarize"))
	outcome := h.outcome(h.response("2"))
	if outcome.Error == nil || !strings.Contains(outcome.Error.Message, "not relayed") {
		t.Errorf("upstream saw %+v, want a not-relayed error", outcome)
	}
	h.noFrame("sampling request", 0, func(f rpcFrame) bool { return f.Method == "sampling/createMessage" })
}

// TestSampling_PostOriginOnSharedInstance: an HTTP upstream's sampling request
// on the call's own response stream is strong enough evidence even for a
// shared instance.
func TestSampling_PostOriginOnSharedInstance(t *testing.T) {
	t.Parallel()
	fake := mcptest.StartHTTPFake(t, mcptest.HTTPFakeConfig{
		Tools:        []string{"summarize"},
		OnPOSTStream: map[string]fakeserver.ServerRequestScript{"summarize": plainSampling},
	})
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"web": {URL: fake.URL, ClientFeatures: &config.ClientFeatures{Sampling: true}},
	}}
	// A session whose client did not declare sampling is refused with an
	// error (the HTTP fake reports an error reply inside its result)...
	h := startCoreSessions(t, cfg, SessionOptions{})[0]
	h.write(callTool(2, "web.summarize"))
	if outcome := h.outcome(h.response("2")); !strings.Contains(string(outcome.Result), "not relayed") {
		t.Fatalf("a client without sampling was not refused: %+v", outcome)
	}
	h.noFrame("sampling request", 0, func(f rpcFrame) bool { return f.Method == "sampling/createMessage" })
	// ...while one that declared sampling gets the request.
	sessions := startCoreSessionsWithCaps(t, cfg, `{"sampling":{}}`)
	s := sessions[0]
	s.write(callTool(2, "web.summarize"))
	req := s.request("sampling/createMessage")
	s.write(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + sampleAnswer + `}`)
	if outcome := s.outcome(s.response("2")); string(outcome.Result) != sampleAnswer {
		t.Errorf("upstream saw %+v", outcome)
	}
}
