package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/mcptest"
)

// TestHelperProcess implements the test subprocess for fake servers.
func TestHelperProcess(t *testing.T) {
	mcptest.RunHelperProcess(t)
}

func TestServer_ToolsDiscoveryFromUpstream(t *testing.T) {
	// Start a fake upstream MCP server
	cfg := mcptest.FakeServerConfig{
		Tools: []mcptest.Tool{
			{Name: "read_file", Description: "Read a file from disk"},
			{Name: "write_file", Description: "Write content to a file"},
		},
		EchoToolCalls: true,
	}

	stdin, stdout, stop := mcptest.StartFakeServer(t, cfg)
	_ = stop // cleanup is registered via t.Cleanup

	// We need to find what command was used to start the fake server
	// For the integration test, we'll use a simpler approach: test the server
	// directly with a config that points to the fake server binary

	// Close these since we're not using them directly in this test
	stdin.Close()
	stdout.Close()

	t.Skip("Full integration test requires building and spawning the real binary - see TestServer_ToolsListWithMockedSupervisor instead")
}

func TestServer_ManagerTool_ServersList(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Test Server 1", Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
			"srv2": {ID: "srv2", Name: "Test Server 2", Kind: config.ServerKindStdio, Enabled: &enabled, Command: "cat"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcp-studio.servers_list","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcp-studio-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}

	if len(resp.Result.Content) == 0 {
		t.Fatal("Expected content in response")
	}

	// The response should contain JSON with our servers
	text := resp.Result.Content[0].Text
	if !strings.Contains(text, "srv1") {
		t.Errorf("Response should contain srv1: %s", text)
	}
	if !strings.Contains(text, "srv2") {
		t.Errorf("Response should contain srv2: %s", text)
	}
	if !strings.Contains(text, "Test Server 1") {
		t.Errorf("Response should contain server names: %s", text)
	}
}

func TestServer_ManagerTool_NamespacesList(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Namespaces: []config.NamespaceConfig{
			{ID: "work", Name: "Work Tools", Description: "Tools for work projects", ServerIDs: []string{"srv1"}},
			{ID: "personal", Name: "Personal Tools", ServerIDs: []string{"srv2", "srv3"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcp-studio.namespaces_list","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		Namespace:       "work", // Select work namespace to pass init
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcp-studio-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}

	text := resp.Result.Content[0].Text
	if !strings.Contains(text, "work") {
		t.Errorf("Response should contain work namespace: %s", text)
	}
	if !strings.Contains(text, "personal") {
		t.Errorf("Response should contain personal namespace: %s", text)
	}
	if !strings.Contains(text, "Work Tools") {
		t.Errorf("Response should contain namespace names: %s", text)
	}
}

func TestServer_ToolsCall_UnknownTool(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"unknown.tool","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcp-studio-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var resp struct {
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if resp.Error == nil {
		t.Fatal("Expected error for unknown tool")
	}

	if resp.Error.Code != ErrCodeServerNotFound {
		t.Errorf("Error code = %d, want %d (server not found)", resp.Error.Code, ErrCodeServerNotFound)
	}
}

// TestEndToEnd_WithRealBinary tests the full stdio server by spawning the actual binary.
// This test requires building the binary first.
func TestEndToEnd_WithRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Build the binary
	tmpBin := t.TempDir() + "/mcp-studio"
	cmd := exec.Command("go", "build", "-o", tmpBin, "../../cmd/mcp-studio")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build binary: %v\n%s", err, output)
	}

	// Create a test config with no servers (to avoid loading real config)
	tmpConfig := t.TempDir() + "/config.json"
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Namespaces:    []config.NamespaceConfig{},
	}
	cfgData, _ := json.Marshal(cfg)
	if err := os.WriteFile(tmpConfig, cfgData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Start the server
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverCmd := exec.CommandContext(ctx, tmpBin, "serve", "--stdio", "--config", tmpConfig, "--log-level", "error")
	stdin, err := serverCmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := serverCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := serverCmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	// Drain stderr
	go io.Copy(io.Discard, stderr)

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		stdin.Close()
		serverCmd.Process.Kill()
		serverCmd.Wait()
	})

	// Use a buffered reader for proper NDJSON handling
	reader := bufio.NewReader(stdout)

	// Send initialize request
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test-client","version":"1.0.0"}}}` + "\n"
	if _, err := stdin.Write([]byte(initReq)); err != nil {
		t.Fatalf("Write initialize: %v", err)
	}

	// Read response line
	respLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("Read response: %v", err)
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal(respLine, &resp); err != nil {
		t.Fatalf("Unmarshal response: %v\nRaw: %s", err, string(respLine))
	}

	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}

	if resp.Result.ServerInfo.Name != "mcp-studio" {
		t.Errorf("ServerInfo.Name = %q, want %q", resp.Result.ServerInfo.Name, "mcp-studio")
	}

	// Send tools/list request
	toolsReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	if _, err := stdin.Write([]byte(toolsReq)); err != nil {
		t.Fatalf("Write tools/list: %v", err)
	}

	// Read tools/list response line
	toolsLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("Read tools/list response: %v", err)
	}

	var toolsResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal(toolsLine, &toolsResp); err != nil {
		t.Fatalf("Unmarshal tools/list response: %v\nRaw: %s", err, string(toolsLine))
	}

	if toolsResp.Error != nil {
		t.Fatalf("Unexpected error: %v", toolsResp.Error)
	}

	// Should have manager tools
	hasServersList := false
	for _, tool := range toolsResp.Result.Tools {
		if tool.Name == "mcp-studio.servers_list" {
			hasServersList = true
			break
		}
	}

	if !hasServersList {
		t.Error("Expected mcp-studio.servers_list tool in response")
	}

	t.Logf("End-to-end test passed! Found %d tools", len(toolsResp.Result.Tools))
}
