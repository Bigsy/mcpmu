//go:build integration && !windows

package daemon

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
	"github.com/Bigsy/mcpmu/internal/server"
)

// TestHelperProcess implements the fake MCP upstream subprocess.
func TestHelperProcess(t *testing.T) {
	mcptest.RunHelperProcess(t)
}

// readUntil reads session lines until one matches, failing on timeout.
func readUntil(t *testing.T, conn *net.UnixConn, reader *bufio.Reader, what string, match func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		var msg map[string]json.RawMessage
		if json.Unmarshal(line, &msg) == nil && match(msg) {
			return msg
		}
	}
}

// TestDaemonSessionRelaysElicitation: a session attached through the daemon —
// the path every shim takes — gets a private instance's elicitation, and its
// answer, written back over the same socket, reaches the upstream. The shim
// is a byte pipe after the handshake, so nothing in it changes.
func TestDaemonSessionRelaysElicitation(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.Tools = append(fake.Tools, fakeserver.Tool{Name: "confirm"})
	fake.ToolServerRequests = map[string]fakeserver.ServerRequestScript{"confirm": {
		Method: "elicitation/create",
		Params: json.RawMessage(`{"message":"Proceed?","requestedSchema":{"type":"object","properties":{}}}`),
	}}
	shared := false
	cfg := config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{"fake": {
		Command:        os.Args[0],
		Args:           []string{"-test.run=TestHelperProcess", "--"},
		Env:            mcptest.FakeServerEnv(t, fake),
		Shared:         &shared,
		ClientFeatures: &config.ClientFeatures{Elicitation: true},
	}}}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d := startTestDaemonWithConfig(t, string(encoded), nil)

	conn, reader, response := dialHandshake(t, d, Handshake{
		Type: "session", Protocol: SessionProtocol, Build: d.build,
		SessionOptions: server.SessionOptions{},
	})
	defer func() { _ = conn.Close() }()
	if !response.OK {
		t.Fatalf("handshake rejected: %+v", response)
	}

	write := func(line string) {
		t.Helper()
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"elicitation":{}},"clientInfo":{"name":"test","version":"1"}}}`)
	readUntil(t, conn, reader, "initialize response", func(m map[string]json.RawMessage) bool { return string(m["id"]) == "1" })
	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	write(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fake.confirm","arguments":{}}}`)

	req := readUntil(t, conn, reader, "elicitation/create", func(m map[string]json.RawMessage) bool {
		return string(m["method"]) == `"elicitation/create"`
	})
	if !strings.Contains(string(req["params"]), "[fake] Proceed?") {
		t.Errorf("relayed params = %s", req["params"])
	}
	write(`{"jsonrpc":"2.0","id":` + string(req["id"]) + `,"result":{"action":"accept","content":{}}}`)

	resp := readUntil(t, conn, reader, "tools/call response", func(m map[string]json.RawMessage) bool { return string(m["id"]) == "2" })
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp["result"], &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("tools/call response = %s", resp["result"])
	}
	var outcome fakeserver.ServerRequestOutcome
	if err := json.Unmarshal([]byte(result.Content[0].Text), &outcome); err != nil {
		t.Fatalf("tool text: %s", result.Content[0].Text)
	}
	if !outcome.Answered || string(outcome.Result) != `{"action":"accept","content":{}}` {
		t.Errorf("upstream saw %+v", outcome)
	}
}
