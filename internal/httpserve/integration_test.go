//go:build integration

package httpserve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestOwnClientAgainstOwnServer points mcpmu's own Streamable HTTP client at
// mcpmu's own Streamable HTTP server — the pairing we actually ship
// (mcpmu-behind-mcpmu). Full loop: initialize → tools/list → tools/call →
// upstream emits tools/list_changed → the client sees it on the standalone
// GET stream → DELETE ends the session server-side → the client's
// session-expiry recovery reinitializes transparently.
func TestOwnClientAgainstOwnServer(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.EmitToolsListChangedAfterFirstList = true
	fake.EchoToolCalls = true
	_, base := startServer(t, singleServerConfig(t, fake), func(o *Options) {
		o.Token = "integration-token"
	})

	transport := mcp.NewStreamableHTTPTransport(mcp.StreamableHTTPConfig{
		URL:         base + "/mcp",
		BearerToken: "integration-token",
	})
	client := mcp.NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })

	notifications := make(chan string, 16)
	client.SetNotificationHandler(func(method string, params json.RawMessage) {
		select {
		case notifications <- method:
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := transport.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if name, _ := client.ServerInfo(); name != "mcpmu" {
		t.Fatalf("server name = %q, want mcpmu", name)
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var toolName string
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "fake.") {
			toolName = tool.Name
			break
		}
	}
	if toolName == "" {
		t.Fatalf("no fake.* tool in tools/list: %+v", tools)
	}

	result, err := client.CallTool(ctx, toolName, json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("tools/call returned no content")
	}

	// The upstream emitted tools/list_changed after its first tools/list;
	// it must arrive over the standalone GET stream.
	waitForNotification(t, notifications, "notifications/tools/list_changed", 10*time.Second)

	// Terminate the session server-side, then keep using the client: the
	// next POST gets 404, and the client must reinitialize and retry.
	firstSession := transport.SessionID()
	if firstSession == "" {
		t.Fatal("transport captured no session ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/mcp", nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	req.Header.Set("Authorization", "Bearer integration-token")
	req.Header.Set("Mcp-Session-Id", firstSession)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d, want 204", resp.StatusCode)
	}

	if _, err := client.ListTools(ctx); err != nil {
		t.Fatalf("tools/list after server-side session termination should recover: %v", err)
	}
	if second := transport.SessionID(); second == "" || second == firstSession {
		t.Fatalf("expected a fresh session after recovery, got %q (was %q)", second, firstSession)
	}
}

func waitForNotification(t *testing.T, notifications <-chan string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case method := <-notifications:
			if method == want {
				return
			}
		case <-deadline:
			t.Fatalf("never received %s", want)
		}
	}
}

// TestGoSDKInterop drives the server with the official MCP Go SDK's client —
// a strict independent implementation is the only thing that catches spec
// misreadings shared by our own client and server (both halves share an
// author). Test-only dependency; the SDK is not used in shipped code.
func TestGoSDKInterop(t *testing.T) {
	fake := mcptest.DefaultConfig()
	fake.EmitToolsListChangedAfterFirstList = true
	fake.EchoToolCalls = true
	_, base := startServer(t, singleServerConfig(t, fake), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	listChanged := make(chan struct{}, 4)
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "go-sdk-interop", Version: "0"},
		&sdkmcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *sdkmcp.ToolListChangedRequest) {
				select {
				case listChanged <- struct{}{}:
				default:
				}
			},
		},
	)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: base + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("go-sdk connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	if got := session.InitializeResult().ServerInfo.Name; got != "mcpmu" {
		t.Fatalf("serverInfo.name = %q, want mcpmu", got)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("go-sdk tools/list: %v", err)
	}
	var toolName string
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "fake.") {
			toolName = tool.Name
			break
		}
	}
	if toolName == "" {
		t.Fatalf("no fake.* tool via go-sdk: %+v", tools.Tools)
	}

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      toolName,
		Arguments: map[string]any{"path": "x"},
	})
	if err != nil {
		t.Fatalf("go-sdk tools/call: %v", err)
	}
	if result.IsError {
		t.Fatalf("go-sdk tools/call returned isError: %+v", result.Content)
	}

	// The upstream's list_changed must reach the SDK client over the
	// standalone GET stream it opened after initialize.
	select {
	case <-listChanged:
	case <-time.After(10 * time.Second):
		t.Fatal("go-sdk client never received tools/list_changed on the GET stream")
	}
}
