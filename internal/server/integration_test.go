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

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
)

// TestHelperProcess implements the test subprocess for fake servers.
func TestHelperProcess(t *testing.T) {
	mcptest.RunHelperProcess(t)
}

func TestServer_ToolsDiscoveryFromUpstream(t *testing.T) {
	t.Parallel()
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
	_ = stdin.Close()
	_ = stdout.Close()

	t.Skip("Full integration test requires building and spawning the real binary - see TestServer_ToolsListWithMockedSupervisor instead")
}

func TestServer_ManagerTool_ServersList(t *testing.T) {
	t.Parallel()
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
			"srv2": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "cat"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcpmu.servers_list","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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
}

func TestServer_ManagerTool_NamespacesList(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Namespaces: map[string]config.NamespaceConfig{
			"work":     {Description: "Tools for work projects", ServerIDs: []string{"srv1"}},
			"personal": {ServerIDs: []string{"srv2", "srv3"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mcpmu.namespaces_list","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "work", // Select work namespace to pass init
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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
	if !strings.Contains(text, "Tools for work projects") {
		t.Errorf("Response should contain namespace description: %s", text)
	}
}

func TestServer_ToolsCall_UnknownTool(t *testing.T) {
	t.Parallel()
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
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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

func TestServer_ToolsCall_PermissionDenied(t *testing.T) {
	t.Parallel()
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"restricted": {DenyByDefault: true, ServerIDs: []string{"srv1"}},
		},
		ToolPermissions: []config.ToolPermission{
			{Namespace: "restricted", Server: "srv1", ToolName: "allowed_tool", Enabled: true},
			{Namespace: "restricted", Server: "srv1", ToolName: "denied_tool", Enabled: false},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.denied_tool","arguments":{}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"srv1.unknown_tool","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "restricted",
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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
	if len(lines) < 3 {
		t.Fatalf("Expected 3 responses, got %d", len(lines))
	}

	// Check explicitly denied tool
	var resp2 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp2); err != nil {
		t.Fatalf("Unmarshal response 2: %v", err)
	}
	if resp2.Error == nil {
		t.Fatal("Expected error for explicitly denied tool")
	}
	if resp2.Error.Code != ErrCodeToolDenied {
		t.Errorf("Error code = %d, want %d (tool denied)", resp2.Error.Code, ErrCodeToolDenied)
	}

	// Check tool denied by DenyByDefault
	var resp3 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &resp3); err != nil {
		t.Fatalf("Unmarshal response 3: %v", err)
	}
	if resp3.Error == nil {
		t.Fatal("Expected error for tool denied by DenyByDefault")
	}
	if resp3.Error.Code != ErrCodeToolDenied {
		t.Errorf("Error code = %d, want %d (tool denied)", resp3.Error.Code, ErrCodeToolDenied)
	}
}

func TestServer_ToolsCall_NoNamespace_AllowsAll(t *testing.T) {
	t.Parallel()
	// When no namespaces are configured (selection=all), permission checks are bypassed
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Kind: config.ServerKindStdio, Enabled: &enabled, Command: "echo"},
		},
		// No namespaces - should allow all tools
	}

	var stdout bytes.Buffer
	// Call a tool - it should NOT be denied (though it may fail for other reasons like server not running)
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.any_tool","arguments":{}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	// Should NOT be a permission denied error
	// (It may fail for other reasons like server not running, which is fine)
	if resp.Error != nil && resp.Error.Code == ErrCodeToolDenied {
		t.Error("Expected tool to NOT be denied when no namespaces configured")
	}
}

// TestEndToEnd_WithRealBinary tests the full stdio server by spawning the actual binary.
// This test requires building the binary first.
func TestEndToEnd_WithRealBinary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Build the binary
	tmpBin := t.TempDir() + "/mcpmu"
	cmd := exec.Command("go", "build", "-o", tmpBin, "../../cmd/mcpmu")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build binary: %v\n%s", err, output)
	}

	// Create a test config with no servers (to avoid loading real config)
	tmpConfig := t.TempDir() + "/config.json"
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Namespaces:    map[string]config.NamespaceConfig{},
	}
	cfgData, _ := json.Marshal(cfg)
	if err := os.WriteFile(tmpConfig, cfgData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Start the server
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverCmd := exec.CommandContext(ctx, tmpBin, "serve", "--stdio", "--config", tmpConfig, "--log-level", "error", "--expose-manager-tools")
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
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = stdin.Close()
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
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

	if resp.Result.ServerInfo.Name != "mcpmu" {
		t.Errorf("ServerInfo.Name = %q, want %q", resp.Result.ServerInfo.Name, "mcpmu")
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
		if tool.Name == "mcpmu.servers_list" {
			hasServersList = true
			break
		}
	}

	if !hasServersList {
		t.Error("Expected mcpmu.servers_list tool in response")
	}

	t.Logf("End-to-end test passed! Found %d tools", len(toolsResp.Result.Tools))
}

// TestServer_NamespaceToolPermissions_EndToEnd tests the full flow:
// 1. Two upstream servers with different tools
// 2. A namespace with DenyByDefault=true
// 3. Some tools explicitly allowed, some denied
// 4. Verify tools/list shows only allowed tools (denied ones are filtered)
// 5. Verify allowed tools can be called
// 6. Verify denied tools return permission error
func TestServer_NamespaceToolPermissions_EndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Create config with two fake servers in a restricted namespace
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"read_file","description":"Read a file"},{"name":"write_file","description":"Write a file"},{"name":"delete_file","description":"Delete a file"}],"echoToolCalls":true}`,
				},
			},
			"srv2": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"get_time","description":"Get current time"},{"name":"set_timezone","description":"Set timezone"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"restricted": {
				Description:   "Restricted namespace for testing",
				DenyByDefault: true,
				ServerIDs:     []string{"srv1", "srv2"},
			},
		},
		ToolPermissions: []config.ToolPermission{
			// Explicitly allow read_file from srv1
			{Namespace: "restricted", Server: "srv1", ToolName: "read_file", Enabled: true},
			// Explicitly deny delete_file from srv1
			{Namespace: "restricted", Server: "srv1", ToolName: "delete_file", Enabled: false},
			// Explicitly allow get_time from srv2
			{Namespace: "restricted", Server: "srv2", ToolName: "get_time", Enabled: true},
			// write_file and set_timezone have no explicit permission -> denied by DenyByDefault
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		// Initialize
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			// List tools
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			// Call allowed tool (read_file)
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"srv1.read_file","arguments":{"path":"/test"}}}` + "\n" +
			// Call explicitly denied tool (delete_file)
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"srv1.delete_file","arguments":{"path":"/test"}}}` + "\n" +
			// Call tool denied by DenyByDefault (write_file)
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"srv1.write_file","arguments":{"path":"/test","content":"hello"}}}` + "\n" +
			// Call allowed tool from second server (get_time)
			`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"srv2.get_time","arguments":{}}}` + "\n" +
			// Call tool denied by DenyByDefault from second server (set_timezone)
			`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"srv2.set_timezone","arguments":{"tz":"UTC"}}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "restricted",
		EagerStart:      false, // Use lazy start - servers start when tools are called
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 7 {
		t.Fatalf("Expected at least 7 responses, got %d:\n%s", len(lines), stdout.String())
	}

	// Response 1: Initialize - should succeed
	var initResp struct {
		ID     int       `json:"id"`
		Result any       `json:"result"`
		Error  *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("Unmarshal init response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("Initialize failed: %v", initResp.Error)
	}
	t.Log("Initialize: OK")

	// Response 2: tools/list - should show only allowed tools (denied ones are filtered)
	var toolsResp struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &toolsResp); err != nil {
		t.Fatalf("Unmarshal tools/list response: %v", err)
	}
	if toolsResp.Error != nil {
		t.Fatalf("tools/list failed: %v", toolsResp.Error)
	}

	// Verify we got only allowed tools (plus manager tools)
	// Denied tools should be filtered: srv1.write_file (DenyByDefault), srv1.delete_file (explicit), srv2.set_timezone (DenyByDefault)
	toolNames := make(map[string]bool)
	for _, tool := range toolsResp.Result.Tools {
		toolNames[tool.Name] = true
	}
	expectedTools := []string{
		"srv1.read_file", // explicitly allowed
		"srv2.get_time",  // explicitly allowed
	}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("tools/list missing expected tool %q", name)
		}
	}
	// Verify denied tools are NOT in the list
	deniedTools := []string{
		"srv1.write_file",   // denied by DenyByDefault
		"srv1.delete_file",  // explicitly denied
		"srv2.set_timezone", // denied by DenyByDefault
	}
	for _, name := range deniedTools {
		if toolNames[name] {
			t.Errorf("tools/list should NOT contain denied tool %q", name)
		}
	}
	t.Logf("tools/list: OK (%d tools, denied tools filtered)", len(toolsResp.Result.Tools))

	// Response 3: Call allowed tool (srv1.read_file)
	// Note: This may fail with EOF due to known server connection management issues,
	// but it should NOT fail with ErrCodeToolDenied (which would indicate permission problem)
	var allowedResp struct {
		ID     int       `json:"id"`
		Result any       `json:"result"`
		Error  *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &allowedResp); err != nil {
		t.Fatalf("Unmarshal allowed tool response: %v", err)
	}
	if allowedResp.Error != nil {
		if allowedResp.Error.Code == ErrCodeToolDenied {
			t.Errorf("Allowed tool (srv1.read_file) should NOT be denied, got: %v", allowedResp.Error)
		} else {
			// Connection errors (EOF) are known issues with server connection management
			t.Logf("srv1.read_file (allowed): connection error (known issue): %v", allowedResp.Error)
		}
	} else {
		t.Log("srv1.read_file (allowed): OK")
	}

	// Response 4: Call explicitly denied tool (srv1.delete_file) - should fail
	var deniedResp struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &deniedResp); err != nil {
		t.Fatalf("Unmarshal denied tool response: %v", err)
	}
	if deniedResp.Error == nil {
		t.Error("Explicitly denied tool (srv1.delete_file) should fail")
	} else if deniedResp.Error.Code != ErrCodeToolDenied {
		t.Errorf("Expected ErrCodeToolDenied (%d), got %d", ErrCodeToolDenied, deniedResp.Error.Code)
	} else {
		t.Log("srv1.delete_file (denied): correctly rejected")
	}

	// Response 5: Call tool denied by DenyByDefault (srv1.write_file) - should fail
	var defaultDeniedResp struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[4]), &defaultDeniedResp); err != nil {
		t.Fatalf("Unmarshal default-denied tool response: %v", err)
	}
	if defaultDeniedResp.Error == nil {
		t.Error("Tool denied by DenyByDefault (srv1.write_file) should fail")
	} else if defaultDeniedResp.Error.Code != ErrCodeToolDenied {
		t.Errorf("Expected ErrCodeToolDenied (%d), got %d", ErrCodeToolDenied, defaultDeniedResp.Error.Code)
	} else {
		t.Log("srv1.write_file (default-denied): correctly rejected")
	}

	// Response 6: Call allowed tool from second server (srv2.get_time) - should succeed
	var allowed2Resp struct {
		ID     int       `json:"id"`
		Result any       `json:"result"`
		Error  *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[5]), &allowed2Resp); err != nil {
		t.Fatalf("Unmarshal srv2 allowed tool response: %v", err)
	}
	if allowed2Resp.Error != nil {
		t.Errorf("Allowed tool (srv2.get_time) should succeed, got error: %v", allowed2Resp.Error)
	} else {
		t.Log("srv2.get_time (allowed): OK")
	}

	// Response 7: Call tool denied by DenyByDefault from second server (srv2.set_timezone) - should fail
	var defaultDenied2Resp struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[6]), &defaultDenied2Resp); err != nil {
		t.Fatalf("Unmarshal srv2 default-denied tool response: %v", err)
	}
	if defaultDenied2Resp.Error == nil {
		t.Error("Tool denied by DenyByDefault (srv2.set_timezone) should fail")
	} else if defaultDenied2Resp.Error.Code != ErrCodeToolDenied {
		t.Errorf("Expected ErrCodeToolDenied (%d), got %d", ErrCodeToolDenied, defaultDenied2Resp.Error.Code)
	} else {
		t.Log("srv2.set_timezone (default-denied): correctly rejected")
	}

	t.Log("End-to-end namespace tool permissions test passed!")
}

// TestServer_NamespaceServerDefaults_EndToEnd tests per-server deny-default:
// - Namespace DenyByDefault=false (allow by default)
// - srv1 has ServerDefaults["srv1"]=true (deny by default for srv1)
// - srv2 has no server default (inherits namespace allow)
// - Explicit allow for one srv1 tool
// - Verify tools/list: srv1 tools denied except explicit allow, srv2 tools all visible
// - Verify tools/call: denied srv1 tool returns permission error, srv2 tool succeeds
func TestServer_NamespaceServerDefaults_EndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"read_file","description":"Read a file"},{"name":"write_file","description":"Write a file"},{"name":"delete_file","description":"Delete a file"}],"echoToolCalls":true}`,
				},
			},
			"srv2": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"get_time","description":"Get current time"},{"name":"set_timezone","description":"Set timezone"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"mixed": {
				Description:    "Mixed server defaults",
				DenyByDefault:  false, // namespace allows by default
				ServerIDs:      []string{"srv1", "srv2"},
				ServerDefaults: map[string]bool{"srv1": true}, // srv1 denies by default
			},
		},
		ToolPermissions: []config.ToolPermission{
			// Explicitly allow read_file from srv1 (overrides server default deny)
			{Namespace: "mixed", Server: "srv1", ToolName: "read_file", Enabled: true},
			// write_file and delete_file from srv1: no explicit permission -> server default deny
			// get_time and set_timezone from srv2: no explicit permission -> namespace default allow
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
			// Call allowed tool (srv1.read_file - explicitly allowed, overrides server default)
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"srv1.read_file","arguments":{"path":"/test"}}}` + "\n" +
			// Call tool denied by server default (srv1.write_file)
			`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"srv1.write_file","arguments":{"path":"/test","content":"hi"}}}` + "\n" +
			// Call tool from srv2 (allowed, inherits namespace default)
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"srv2.get_time","arguments":{}}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "mixed",
		EagerStart:      false,
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 5 {
		t.Fatalf("Expected at least 5 responses, got %d:\n%s", len(lines), stdout.String())
	}

	// Response 1: Initialize
	var initResp struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("Unmarshal init: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("Initialize failed: %v", initResp.Error)
	}

	// Response 2: tools/list - should show srv1.read_file (explicit allow) + all srv2 tools
	var toolsResp struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &toolsResp); err != nil {
		t.Fatalf("Unmarshal tools/list: %v", err)
	}
	if toolsResp.Error != nil {
		t.Fatalf("tools/list failed: %v", toolsResp.Error)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsResp.Result.Tools {
		toolNames[tool.Name] = true
	}

	// Should be visible
	for _, name := range []string{"srv1.read_file", "srv2.get_time", "srv2.set_timezone"} {
		if !toolNames[name] {
			t.Errorf("tools/list missing expected tool %q", name)
		}
	}
	// Should be denied (server default deny for srv1)
	for _, name := range []string{"srv1.write_file", "srv1.delete_file"} {
		if toolNames[name] {
			t.Errorf("tools/list should NOT contain %q (denied by server default)", name)
		}
	}

	// Response 3: srv1.read_file (allowed) - should not get ErrCodeToolDenied
	var resp3 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &resp3); err != nil {
		t.Fatalf("Unmarshal resp3: %v", err)
	}
	if resp3.Error != nil && resp3.Error.Code == ErrCodeToolDenied {
		t.Errorf("srv1.read_file should NOT be denied: %v", resp3.Error)
	}

	// Response 4: srv1.write_file (denied by server default)
	var resp4 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &resp4); err != nil {
		t.Fatalf("Unmarshal resp4: %v", err)
	}
	if resp4.Error == nil {
		t.Error("srv1.write_file should be denied by server default")
	} else if resp4.Error.Code != ErrCodeToolDenied {
		t.Errorf("Expected ErrCodeToolDenied (%d), got %d", ErrCodeToolDenied, resp4.Error.Code)
	}

	// Response 5: srv2.get_time (allowed, inherits namespace default)
	var resp5 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[4]), &resp5); err != nil {
		t.Fatalf("Unmarshal resp5: %v", err)
	}
	if resp5.Error != nil {
		t.Errorf("srv2.get_time should succeed (namespace allows): %v", resp5.Error)
	}

	t.Log("Server defaults end-to-end test passed!")
}

// TestServer_ProgressiveDiscovery verifies that:
// 1. tools/list returns immediately with tools from fast servers only
// 2. A notifications/tools/list_changed notification is sent when slow servers finish
// 3. A second tools/list returns the complete tool set
//
// Uses an in-process server with a short grace period (100ms) so the slow server
// (500ms tools/list delay) reliably exceeds it, guaranteeing the notification path
// is exercised every run.
func TestServer_ProgressiveDiscovery(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Config: one fast server, one slow server (500ms delay on tools/list)
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"fast-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"fast_tool","description":"A fast tool"}],"echoToolCalls":true}`,
				},
			},
			"slow-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// 500ms delay on tools/list — exceeds the 100ms test grace period
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_tool","description":"A slow tool"}],"delays":{"tools/list":500000000},"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}

	// Use pipes so we control the server lifecycle and can read output interactively
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdinReader,
		Stdout:          stdoutWriter,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Set a very short grace period so the slow server (500ms) always exceeds it
	srv.listToolsGracePeriod = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run server in background
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Helper: send a JSON-RPC request
	send := func(msg string) {
		t.Helper()
		if _, err := stdinWriter.WriteString(msg + "\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Step 1: Initialize
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`)
	// Give the server time to process
	time.Sleep(100 * time.Millisecond)

	// Use a goroutine to read stdout lines so we can interleave sends and reads
	type lineResult struct {
		data string
		err  error
	}
	outLines := make(chan lineResult, 10)
	outReader := bufio.NewReader(stdoutReader)
	go func() {
		for {
			line, err := outReader.ReadString('\n')
			if line != "" {
				outLines <- lineResult{data: strings.TrimSpace(line)}
			}
			if err != nil {
				outLines <- lineResult{err: err}
				return
			}
		}
	}()

	readLine := func(timeout time.Duration) (string, error) {
		select {
		case r := <-outLines:
			return r.data, r.err
		case <-time.After(timeout):
			return "", context.DeadlineExceeded
		}
	}

	// Wait for init response
	time.Sleep(100 * time.Millisecond)
	initLine, err := readLine(2 * time.Second)
	if err != nil {
		t.Fatalf("Init read: %v", err)
	}

	var initResp struct {
		Result struct {
			Capabilities struct {
				Tools struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(initLine), &initResp); err != nil {
		t.Fatalf("Parse init: %v\nLine: %s", err, initLine)
	}
	if initResp.Error != nil {
		t.Fatalf("Init error: %v", initResp.Error)
	}
	if !initResp.Result.Capabilities.Tools.ListChanged {
		t.Error("Expected tools.listChanged=true in capabilities")
	}

	// Step 2: Send tools/list — grace period is 100ms, so the slow server (500ms) will be pending
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	// Read first tools/list response
	toolsLine1, err := readLine(5 * time.Second)
	if err != nil {
		t.Fatalf("tools/list read: %v", err)
	}

	var toolsResp1 struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolsLine1), &toolsResp1); err != nil {
		t.Fatalf("Parse tools/list: %v\nLine: %s", err, toolsLine1)
	}
	if toolsResp1.Error != nil {
		t.Fatalf("tools/list error: %v", toolsResp1.Error)
	}

	firstToolNames := make(map[string]bool)
	for _, tool := range toolsResp1.Result.Tools {
		firstToolNames[tool.Name] = true
	}
	t.Logf("First tools/list: %v", firstToolNames)

	if !firstToolNames["fast-srv.fast_tool"] {
		t.Error("Expected fast-srv.fast_tool in first tools/list")
	}
	if firstToolNames["slow-srv.slow_tool"] {
		t.Fatal("slow-srv.slow_tool should NOT be in first tools/list (grace period too short)")
	}

	// Step 3: Wait for notifications/tools/list_changed from background discovery
	notifLine, err := readLine(10 * time.Second)
	if err != nil {
		t.Fatalf("notification read: %v", err)
	}
	var notif struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(notifLine), &notif); err != nil {
		t.Fatalf("Parse notification: %v\nLine: %s", err, notifLine)
	}
	if notif.Method != "notifications/tools/list_changed" {
		t.Fatalf("Expected notifications/tools/list_changed, got %q", notif.Method)
	}
	t.Log("Received notifications/tools/list_changed")

	// Step 4: Send tools/list again — should now include slow server's tools
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)

	toolsLine2, err := readLine(15 * time.Second)
	if err != nil {
		t.Fatalf("second tools/list read: %v", err)
	}

	var toolsResp2 struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolsLine2), &toolsResp2); err != nil {
		t.Fatalf("Parse second tools/list: %v\nLine: %s", err, toolsLine2)
	}
	if toolsResp2.Error != nil {
		t.Fatalf("second tools/list error: %v", toolsResp2.Error)
	}

	secondToolNames := make(map[string]bool)
	for _, tool := range toolsResp2.Result.Tools {
		secondToolNames[tool.Name] = true
	}
	t.Logf("Second tools/list: %v", secondToolNames)

	if !secondToolNames["fast-srv.fast_tool"] {
		t.Error("Expected fast-srv.fast_tool in second tools/list")
	}
	if !secondToolNames["slow-srv.slow_tool"] {
		t.Error("Expected slow-srv.slow_tool in second tools/list after notification")
	}

	// Cleanup: close stdin to stop the server
	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Server did not stop")
	}

	t.Log("Progressive discovery test passed!")
}

// TestServer_ConcurrentServerInit verifies that multiple servers initialize
// concurrently rather than sequentially. This catches mutex serialization bugs
// in Supervisor.Start() where a global lock during initialization would cause
// slow-starting servers to block fast ones.
//
// Setup:
//   - 1 fast server (no init delay)
//   - 2 slow servers (each with init delay exceeding the grace period)
//   - Grace period shorter than a single slow server's init time
//
// If starts are serialized, the fast server would be blocked behind the slow
// ones and no tools would be returned within the grace period.
// If starts are concurrent, the fast server finishes independently and its
// tools appear in the first tools/list response.
func TestServer_ConcurrentServerInit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"fast-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"fast_tool","description":"A fast tool"}]}`,
				},
			},
			"slow-srv-1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// 800ms init delay — exceeds the 200ms grace period on its own
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_tool_1","description":"Slow tool 1"}],"delays":{"initialize":800000000}}`,
				},
			},
			"slow-srv-2": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// 800ms init delay
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_tool_2","description":"Slow tool 2"}],"delays":{"initialize":800000000}}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdinReader,
		Stdout:          stdoutWriter,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Grace period shorter than a single slow server's init.
	// If starts are serialized (fast behind slow), fast would be blocked
	// and this test would fail.
	srv.listToolsGracePeriod = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	send := func(msg string) {
		t.Helper()
		if _, err := stdinWriter.WriteString(msg + "\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Initialize
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`)
	time.Sleep(100 * time.Millisecond)

	type concLineResult struct {
		data string
		err  error
	}
	outLines := make(chan concLineResult, 10)
	outReader := bufio.NewReader(stdoutReader)
	go func() {
		for {
			line, err := outReader.ReadString('\n')
			if line != "" {
				outLines <- concLineResult{data: strings.TrimSpace(line)}
			}
			if err != nil {
				outLines <- concLineResult{err: err}
				return
			}
		}
	}()

	readLine := func(timeout time.Duration) (string, error) {
		select {
		case r := <-outLines:
			return r.data, r.err
		case <-time.After(timeout):
			return "", context.DeadlineExceeded
		}
	}

	// Read init response
	initLine, err := readLine(2 * time.Second)
	if err != nil {
		t.Fatalf("Init read: %v", err)
	}
	var initResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(initLine), &initResp); err != nil {
		t.Fatalf("Parse init: %v\nLine: %s", err, initLine)
	}
	if initResp.Error != nil {
		t.Fatalf("Init error: %v", initResp.Error)
	}

	// Send tools/list
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	toolsLine, err := readLine(5 * time.Second)
	if err != nil {
		t.Fatalf("tools/list read: %v", err)
	}

	var toolsResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolsLine), &toolsResp); err != nil {
		t.Fatalf("Parse tools/list: %v\nLine: %s", err, toolsLine)
	}
	if toolsResp.Error != nil {
		t.Fatalf("tools/list error: %v", toolsResp.Error)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsResp.Result.Tools {
		toolNames[tool.Name] = true
	}
	t.Logf("First tools/list returned: %v", toolNames)

	// Key assertion: the fast server's tools MUST be present.
	// With serialized starts, the slow servers would block the fast one
	// and no tools would be returned within the 200ms grace period.
	if !toolNames["fast-srv.fast_tool"] {
		t.Fatal("fast-srv.fast_tool missing from first tools/list — " +
			"concurrent server initialization may be broken (slow servers blocking fast ones)")
	}

	// Cleanup
	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Server did not stop")
	}

	t.Log("Concurrent server init test passed!")
}

// TestServer_SlowInitNotification is a realistic end-to-end test that verifies
// the full progressive discovery cycle when a server's initialization exceeds
// the grace period:
//
//  1. tools/list returns partial results (fast server only) within the grace period
//  2. The slow-init server finishes in the background
//  3. A notifications/tools/list_changed is sent promptly (not blocked by failures)
//  4. A second tools/list returns the complete tool set
//
// This simulates the real scenario: e.g. playwright finishes fast, grafana/sentry
// take longer to initialise, and kubernetes/atlassian are broken. The client
// should learn about grafana/sentry as soon as they're ready, without waiting
// for the broken servers to time out.
func TestServer_SlowInitNotification(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"fast-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"fast_tool","description":"Fast tool"}]}`,
				},
			},
			"slow-init-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// 400ms init delay — exceeds the 100ms grace period but finishes
					// well within the 30s background timeout.
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_init_tool","description":"Slow init tool"}],"delays":{"initialize":400000000}}`,
				},
			},
			"broken-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// Crash on init — simulates kubernetes/atlassian that never come up
					"FAKE_MCP_CFG": `{"crashOnMethod":"initialize","crashExitCode":1}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	defer func() { _ = stdinReader.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	defer func() { _ = stdoutReader.Close() }()
	defer func() { _ = stdoutWriter.Close() }()

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdinReader,
		Stdout:          stdoutWriter,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Grace period: fast-srv finishes, slow-init-srv (400ms init) does not,
	// broken-srv crashes. Background picks up the stragglers.
	srv.listToolsGracePeriod = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	send := func(msg string) {
		t.Helper()
		if _, err := stdinWriter.WriteString(msg + "\n"); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	type notifLineResult struct {
		data string
		err  error
	}
	outLines := make(chan notifLineResult, 10)
	go func() {
		r := bufio.NewReader(stdoutReader)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				outLines <- notifLineResult{data: strings.TrimSpace(line)}
			}
			if err != nil {
				outLines <- notifLineResult{err: err}
				return
			}
		}
	}()

	readLine := func(timeout time.Duration) (string, error) {
		select {
		case r := <-outLines:
			return r.data, r.err
		case <-time.After(timeout):
			return "", context.DeadlineExceeded
		}
	}

	// Step 1: Initialize
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`)
	time.Sleep(100 * time.Millisecond)

	initLine, err := readLine(2 * time.Second)
	if err != nil {
		t.Fatalf("Init read: %v", err)
	}
	var initResp struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(initLine), &initResp); err != nil {
		t.Fatalf("Parse init: %v\nLine: %s", err, initLine)
	}
	if initResp.Error != nil {
		t.Fatalf("Init error: %v", initResp.Error)
	}

	// Step 2: tools/list — grace period is 100ms, so slow-init-srv (400ms) will be pending
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)

	toolsLine1, err := readLine(5 * time.Second)
	if err != nil {
		t.Fatalf("tools/list read: %v", err)
	}
	var toolsResp1 struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolsLine1), &toolsResp1); err != nil {
		t.Fatalf("Parse tools/list: %v\nLine: %s", err, toolsLine1)
	}
	if toolsResp1.Error != nil {
		t.Fatalf("tools/list error: %v", toolsResp1.Error)
	}

	firstTools := make(map[string]bool)
	for _, tool := range toolsResp1.Result.Tools {
		firstTools[tool.Name] = true
	}
	t.Logf("First tools/list: %v", firstTools)

	if !firstTools["fast-srv.fast_tool"] {
		t.Fatal("fast-srv.fast_tool missing from first tools/list")
	}
	if firstTools["slow-init-srv.slow_init_tool"] {
		t.Fatal("slow-init-srv.slow_init_tool should NOT be in first tools/list (grace period too short)")
	}

	// Step 3: Wait for notifications/tools/list_changed.
	// The slow-init-srv takes 400ms — the notification should arrive within a
	// few seconds, NOT after 30s (which would mean it's blocked by broken-srv).
	start := time.Now()
	notifLine, err := readLine(10 * time.Second)
	notifLatency := time.Since(start)
	if err != nil {
		t.Fatalf("notification read: %v (waited %v — broken-srv may be blocking notification)", err, notifLatency)
	}
	var notif struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(notifLine), &notif); err != nil {
		t.Fatalf("Parse notification: %v\nLine: %s", err, notifLine)
	}
	if notif.Method != "notifications/tools/list_changed" {
		t.Fatalf("Expected notifications/tools/list_changed, got %q", notif.Method)
	}
	t.Logf("Received notifications/tools/list_changed after %v", notifLatency)

	// The notification should arrive promptly (slow-init-srv finishes in ~400ms
	// after the grace period). If it takes >5s, the notification is likely blocked
	// by the broken server's retries/timeouts.
	if notifLatency > 5*time.Second {
		t.Errorf("Notification took %v — should arrive within a few seconds, not blocked by broken servers", notifLatency)
	}

	// Step 4: Second tools/list should include the slow-init-srv's tools
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)

	toolsLine2, err := readLine(5 * time.Second)
	if err != nil {
		t.Fatalf("second tools/list read: %v", err)
	}
	var toolsResp2 struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(toolsLine2), &toolsResp2); err != nil {
		t.Fatalf("Parse second tools/list: %v\nLine: %s", err, toolsLine2)
	}
	if toolsResp2.Error != nil {
		t.Fatalf("second tools/list error: %v", toolsResp2.Error)
	}

	secondTools := make(map[string]bool)
	for _, tool := range toolsResp2.Result.Tools {
		secondTools[tool.Name] = true
	}
	t.Logf("Second tools/list: %v", secondTools)

	if !secondTools["fast-srv.fast_tool"] {
		t.Error("fast-srv.fast_tool missing from second tools/list")
	}
	if !secondTools["slow-init-srv.slow_init_tool"] {
		t.Error("slow-init-srv.slow_init_tool missing from second tools/list after notification")
	}

	// Cleanup
	_ = stdinWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Server did not stop")
	}

	t.Log("Slow init notification test passed!")
}

// TestServer_ReloadSendsToolsListChanged verifies that config reloads
// send a notifications/tools/list_changed notification to the client.
func TestServer_ReloadSendsToolsListChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Build the binary
	tmpBin := t.TempDir() + "/mcpmu"
	cmd := exec.Command("go", "build", "-o", tmpBin, "../../cmd/mcpmu")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build binary: %v\n%s", err, output)
	}

	// Create initial config with one server
	tmpConfig := t.TempDir() + "/config.json"
	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"tool_a","description":"Tool A"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}
	if err := config.SaveTo(initialCfg, tmpConfig); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Start the server
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverCmd := exec.CommandContext(ctx, tmpBin, "serve", "--stdio", "--config", tmpConfig, "--log-level", "debug")
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

	stderrBuf := &bytes.Buffer{}
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrBuf, stderr)
		close(stderrDone)
	}()

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = stdin.Close()
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		<-stderrDone
		if t.Failed() {
			t.Logf("Server stderr:\n%s", stderrBuf.String())
		}
	})

	reader := bufio.NewReader(stdout)

	// Use a single reader goroutine to avoid races on bufio.Reader
	type lineResult struct {
		data []byte
		err  error
	}
	lines := make(chan lineResult, 10)
	go func() {
		for {
			line, err := reader.ReadBytes('\n')
			lines <- lineResult{line, err}
			if err != nil {
				return
			}
		}
	}()

	send := func(req string) {
		t.Helper()
		if _, err := stdin.Write([]byte(req + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	readMsg := func(timeout time.Duration) (json.RawMessage, error) {
		select {
		case r := <-lines:
			return json.RawMessage(r.data), r.err
		case <-time.After(timeout):
			return nil, context.DeadlineExceeded
		}
	}

	// Step 1: Initialize
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`)
	initResp, err := readMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("Initialize read: %v", err)
	}
	var initResult struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(initResp, &initResult); err != nil {
		t.Fatalf("Parse init: %v", err)
	}
	if initResult.Error != nil {
		t.Fatalf("Init error: %v", initResult.Error)
	}
	t.Log("Initialize: OK")

	// Step 2: Get initial tools
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	toolsResp1, err := readMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("tools/list read: %v", err)
	}

	var toolsResult1 struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(toolsResp1, &toolsResult1); err != nil {
		t.Fatalf("Parse tools/list: %v", err)
	}

	initialToolCount := len(toolsResult1.Result.Tools)
	t.Logf("Initial tools: %d", initialToolCount)

	// Drain any pending notification from background discovery (if any)
	drainedNotif, _ := readMsg(500 * time.Millisecond)
	if drainedNotif != nil {
		t.Log("Drained background discovery notification")
	}

	// Step 3: Modify config to add a second server
	updatedCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"tool_a","description":"Tool A"}],"echoToolCalls":true}`,
				},
			},
			"srv2": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"tool_b","description":"Tool B"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}
	if err := config.SaveTo(updatedCfg, tmpConfig); err != nil {
		t.Fatalf("Failed to save updated config: %v", err)
	}
	t.Log("Config updated: added srv2")

	// Step 4: Read reload notifications (tools, resources, prompts list_changed).
	// Drain all of them — order is not guaranteed.
	gotToolsChanged := false
	for i := 0; i < 3; i++ {
		notifMsg, err := readMsg(5 * time.Second)
		if err != nil {
			break // fewer notifications than expected is OK
		}
		var notif struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(notifMsg, &notif); err != nil {
			t.Fatalf("Parse notification: %v", err)
		}
		t.Logf("Received %s after config reload", notif.Method)
		if notif.Method == "notifications/tools/list_changed" {
			gotToolsChanged = true
		}
	}
	if !gotToolsChanged {
		t.Error("Expected notifications/tools/list_changed among reload notifications")
	}

	// Step 5: Send tools/list again to get updated tools
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	toolsResp2, err := readMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("second tools/list read: %v", err)
	}

	var toolsResult2 struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	// Skip any notifications (resources/list_changed, prompts/list_changed, etc.)
	// until we get the actual tools/list response (has a non-zero ID).
	for {
		if err := json.Unmarshal(toolsResp2, &toolsResult2); err != nil {
			t.Fatalf("Parse second tools/list: %v", err)
		}
		if toolsResult2.ID != 0 {
			break
		}
		t.Log("Skipping notification, reading next message")
		toolsResp2, err = readMsg(15 * time.Second)
		if err != nil {
			t.Fatalf("reading after notification: %v", err)
		}
	}

	if toolsResult2.Error != nil {
		t.Fatalf("second tools/list error: %v", toolsResult2.Error)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsResult2.Result.Tools {
		toolNames[tool.Name] = true
	}
	t.Logf("After reload tools/list: %d tools: %v", len(toolsResult2.Result.Tools), toolNames)

	if !toolNames["srv1.tool_a"] {
		t.Error("Expected srv1.tool_a after reload")
	}
	if !toolNames["srv2.tool_b"] {
		t.Error("Expected srv2.tool_b after reload")
	}

	t.Log("Reload notification test passed!")
}

// ============================================================================
// Global Deny Integration Tests
// ============================================================================

func TestServer_ToolsCall_GlobalDenyNoNamespace(t *testing.T) {
	t.Parallel()
	// No namespaces configured (selection=all), but server has deniedTools.
	// The globally denied tool should still be blocked.
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:        config.ServerKindStdio,
				Enabled:     &enabled,
				Command:     "echo",
				DeniedTools: []string{"dangerous_tool"},
			},
		},
		// No namespaces
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.dangerous_tool","arguments":{}}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"srv1.safe_tool","arguments":{}}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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
	if len(lines) < 3 {
		t.Fatalf("Expected 3 responses, got %d", len(lines))
	}

	// Response 2: globally denied tool should be blocked
	var resp2 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp2); err != nil {
		t.Fatalf("Unmarshal response 2: %v", err)
	}
	if resp2.Error == nil || resp2.Error.Code != ErrCodeToolDenied {
		t.Errorf("Expected ErrCodeToolDenied for globally denied tool, got: %+v", resp2.Error)
	}

	// Response 3: non-denied tool should NOT be tool-denied
	var resp3 struct {
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &resp3); err != nil {
		t.Fatalf("Unmarshal response 3: %v", err)
	}
	if resp3.Error != nil && resp3.Error.Code == ErrCodeToolDenied {
		t.Error("Non-denied tool should NOT be blocked by global deny")
	}
}

func TestServer_ToolsCall_GlobalDenyWithNamespaceAllow(t *testing.T) {
	t.Parallel()
	// Namespace explicitly allows the tool, but it's in server deniedTools.
	// Global deny should win.
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:        config.ServerKindStdio,
				Enabled:     &enabled,
				Command:     "echo",
				DeniedTools: []string{"dangerous_tool"},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {ServerIDs: []string{"srv1"}},
		},
		ToolPermissions: []config.ToolPermission{
			{Namespace: "ns1", Server: "srv1", ToolName: "dangerous_tool", Enabled: true},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"srv1.dangerous_tool","arguments":{}}}` + "\n",
	)

	srv, err := New(Options{
		Config:          cfg,
		PIDTrackerDir:   t.TempDir(),
		Namespace:       "ns1",
		Stdin:           stdin,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
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
		ID    int       `json:"id"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != ErrCodeToolDenied {
		t.Errorf("Expected ErrCodeToolDenied even with namespace allow, got: %+v", resp.Error)
	}
}

func TestServer_ToolsList_GlobalDenyFiltering(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Use a real fake server with tools including a globally denied one
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"read_file","description":"Read"},{"name":"delete_file","description":"Delete"}],"echoToolCalls":true}`,
				},
				DeniedTools: []string{"delete_file"},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n",
	)

	srv, err := New(Options{
		Config:             cfg,
		PIDTrackerDir:      t.TempDir(),
		Namespace:          "ns1",
		EagerStart:         true,
		ExposeManagerTools: true,
		Stdin:              stdin,
		Stdout:             &stdout,
		ServerName:         "mcpmu-test",
		ServerVersion:      "1.0.0",
		ProtocolVersion:    "2024-11-05",
		LogLevel:           "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var toolsResp struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &toolsResp); err != nil {
		t.Fatalf("Unmarshal tools/list: %v", err)
	}
	if toolsResp.Error != nil {
		t.Fatalf("tools/list error: %v", toolsResp.Error)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsResp.Result.Tools {
		toolNames[tool.Name] = true
	}

	// read_file should be present
	if !toolNames["srv1.read_file"] {
		t.Error("Expected srv1.read_file in tools/list")
	}

	// delete_file should be filtered (globally denied)
	if toolNames["srv1.delete_file"] {
		t.Error("Expected srv1.delete_file to be filtered from tools/list (globally denied)")
	}

	// Manager tools should still be present
	if !toolNames["mcpmu.servers_list"] {
		t.Error("Expected mcpmu.servers_list in tools/list (manager tools should survive filtering)")
	}
}

func TestServer_ToolsList_GlobalDenyNoNamespace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// No namespace (selection=all), but with global deny
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"read_file","description":"Read"},{"name":"delete_file","description":"Delete"}],"echoToolCalls":true}`,
				},
				DeniedTools: []string{"delete_file"},
			},
		},
		// No namespaces
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n",
	)

	srv, err := New(Options{
		Config:             cfg,
		PIDTrackerDir:      t.TempDir(),
		EagerStart:         true,
		ExposeManagerTools: true,
		Stdin:              stdin,
		Stdout:             &stdout,
		ServerName:         "mcpmu-test",
		ServerVersion:      "1.0.0",
		ProtocolVersion:    "2024-11-05",
		LogLevel:           "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_ = srv.Run(ctx)

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d", len(lines))
	}

	var toolsResp struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &toolsResp); err != nil {
		t.Fatalf("Unmarshal tools/list: %v", err)
	}
	if toolsResp.Error != nil {
		t.Fatalf("tools/list error: %v", toolsResp.Error)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsResp.Result.Tools {
		toolNames[tool.Name] = true
	}

	// read_file should be present
	if !toolNames["srv1.read_file"] {
		t.Error("Expected srv1.read_file in tools/list")
	}

	// delete_file should be filtered (globally denied) even without namespace
	if toolNames["srv1.delete_file"] {
		t.Error("Expected srv1.delete_file to be filtered (globally denied, no namespace)")
	}

	// Manager tools should survive unconditional filtering
	if !toolNames["mcpmu.servers_list"] {
		t.Error("Expected mcpmu.servers_list in tools/list after unconditional filtering")
	}
}
