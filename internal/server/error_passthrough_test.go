package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcp"
)

// urlElicitationRequiredJSON is a URLElicitationRequiredError (-32042) as an
// upstream would send it: its data carries the URL-mode elicitations the
// client must show before retrying, so dropping or rewriting it leaves the
// client with nothing to act on.
const urlElicitationRequiredJSON = `{"code":-32042,"message":"This request requires more information.","data":{"elicitations":[{"mode":"url","elicitationId":"550e8400-e29b-41d4-a716-446655440000","url":"https://mcp.example.com/connect?elicitationId=550e8400-e29b-41d4-a716-446655440000","message":"Authorization is required to access your Example Co files."}]}}`

// TestToolsCall_UpstreamRPCErrorPassesThrough verifies that an upstream
// JSON-RPC error reaches the client with its code, message and data intact,
// instead of being rewrapped as -32603.
func TestToolsCall_UpstreamRPCErrorPassesThrough(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping subprocess test in short mode")
	}

	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": fakeUpstream(`{"tools":[{"name":"connect"}],"errors":{"tools/call":` + urlElicitationRequiredJSON + `}}`),
		},
	}
	responses := runCompressSession(t, Options{Config: cfg}, initLine+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.connect","arguments":{}}}`+"\n")

	var resp struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(responses[2], &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, responses[2])
	}
	if !jsonEqual(t, resp.Error, json.RawMessage(urlElicitationRequiredJSON)) {
		t.Errorf("upstream error changed in transit:\n  upstream:   %s\n  downstream: %s", urlElicitationRequiredJSON, resp.Error)
	}
}

// TestUpstreamRPCError_OnlyForUpstreamErrors pins the classification: an
// error the upstream answered passes through, while transport failures and
// timeouts are not upstream answers and stay internal errors.
func TestUpstreamRPCError_OnlyForUpstreamErrors(t *testing.T) {
	t.Parallel()
	upstream := &mcp.RPCError{Code: -32042, Message: "needs info", Data: json.RawMessage(`{"elicitations":[]}`)}
	got := upstreamRPCError(fmt.Errorf("tools/call: %w", upstream))
	if got == nil || got.Code != -32042 || got.Message != "needs info" || string(got.Data) != `{"elicitations":[]}` {
		t.Fatalf("wrapped upstream error = %+v, want it unchanged", got)
	}
	for _, err := range []error{
		errors.New("transport closed: EOF"),
		fmt.Errorf("send: %w", &mcp.SessionExpiredError{}),
	} {
		if got := upstreamRPCError(err); got != nil {
			t.Errorf("upstreamRPCError(%v) = %+v, want nil", err, got)
		}
	}
}
