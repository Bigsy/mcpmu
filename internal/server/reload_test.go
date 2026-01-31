package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestServer_ApplyReload_SwapsConfig(t *testing.T) {
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Old Server", Enabled: &enabled, Command: "echo"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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

	// Run initialization
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Create new config
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "New Server", Enabled: &enabled, Command: "echo"},
			"srv2": {ID: "srv2", Name: "Added Server", Enabled: &enabled, Command: "echo"},
		},
	}

	// Apply reload
	srv.applyReload(context.Background(), newCfg)

	// Verify config was swapped
	if srv.cfg != newCfg {
		t.Error("Config was not swapped after reload")
	}

	if len(srv.cfg.Servers) != 2 {
		t.Errorf("Expected 2 servers after reload, got %d", len(srv.cfg.Servers))
	}
}

func TestServer_ApplyReload_KeepsNamespaceIfStillValid(t *testing.T) {
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns1", Name: "Work", ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
		Namespace:       "ns1", // Explicit namespace selection via flag
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

	// Run initialization
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Verify initial state
	if srv.activeNamespace == nil || srv.activeNamespace.ID != "ns1" {
		t.Fatal("Expected ns1 to be active after init")
	}
	if srv.selectionMethod != SelectionFlag {
		t.Errorf("Expected SelectionFlag, got %v", srv.selectionMethod)
	}

	// Create new config with same namespace but updated servers
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1 Updated", Enabled: &enabled, Command: "echo"},
			"srv2": {ID: "srv2", Name: "Server 2", Enabled: &enabled, Command: "echo"},
		},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns1", Name: "Work", ServerIDs: []string{"srv1", "srv2"}},
		},
	}

	// Apply reload
	srv.applyReload(context.Background(), newCfg)

	// Verify namespace was kept
	if srv.activeNamespace == nil || srv.activeNamespace.ID != "ns1" {
		t.Error("Expected ns1 to still be active after reload")
	}
	if srv.selectionMethod != SelectionFlag {
		t.Errorf("Expected SelectionFlag to be preserved, got %v", srv.selectionMethod)
	}
	if len(srv.activeServerIDs) != 2 {
		t.Errorf("Expected 2 active servers after reload, got %d", len(srv.activeServerIDs))
	}
}

func TestServer_ApplyReload_ReSelectsNamespaceIfRemoved(t *testing.T) {
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns1", Name: "Work", ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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

	// Run initialization (will auto-select ns1 as only namespace)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Verify initial state
	if srv.activeNamespace == nil || srv.activeNamespace.ID != "ns1" {
		t.Fatal("Expected ns1 to be active after init")
	}

	// Create new config with different namespace (old one removed)
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns2", Name: "Personal", ServerIDs: []string{"srv1"}},
		},
	}

	// Apply reload
	srv.applyReload(context.Background(), newCfg)

	// Verify namespace was re-selected (ns2 is now the only namespace)
	if srv.activeNamespace == nil || srv.activeNamespace.ID != "ns2" {
		t.Error("Expected ns2 to be selected after reload (only available namespace)")
	}
	if srv.selectionMethod != SelectionOnly {
		t.Errorf("Expected SelectionOnly, got %v", srv.selectionMethod)
	}
}

func TestServer_ApplyReload_RebuildAggregatorAndRouter(t *testing.T) {
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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

	// Run initialization
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Capture old references
	oldAggregator := srv.aggregator
	oldRouter := srv.router

	// Create new config
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv2": {ID: "srv2", Name: "Server 2", Enabled: &enabled, Command: "echo"},
		},
	}

	// Apply reload
	srv.applyReload(context.Background(), newCfg)

	// Verify aggregator and router were rebuilt
	if srv.aggregator == oldAggregator {
		t.Error("Expected aggregator to be rebuilt")
	}
	if srv.router == oldRouter {
		t.Error("Expected router to be rebuilt")
	}
}

func TestServer_ReloadChannel_ReceivesNewConfig(t *testing.T) {
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
	}

	// Use a pipe so we can control when the server stops
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()

	var stdout bytes.Buffer
	srv, err := New(Options{
		Config:          oldCfg,
		Stdin:           pipeReader,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Start server in goroutine
	done := make(chan error)
	go func() {
		done <- srv.Run(ctx)
	}()

	// Send initialize request
	_, err = pipeWriter.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n")
	if err != nil {
		t.Fatalf("Failed to write initialize: %v", err)
	}

	// Wait a bit for initialization
	time.Sleep(100 * time.Millisecond)

	// Send new config via reload channel
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv2": {ID: "srv2", Name: "Server 2", Enabled: &enabled, Command: "echo"},
		},
	}

	select {
	case srv.reloadCh <- newCfg:
		// Config sent
	case <-time.After(time.Second):
		t.Fatal("Timeout sending to reload channel")
	}

	// Wait for reload to be processed
	time.Sleep(200 * time.Millisecond)

	// Cancel context to stop server first
	cancel()

	// Wait for server to stop
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not stop")
	}

	// Now safe to verify config was applied (server stopped, no concurrent access)
	if srv.cfg != newCfg {
		t.Error("Config was not updated via reload channel")
	}
}

func TestServer_WatchConfig_DetectsFileChange(t *testing.T) {
	// Create temp directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
	}

	if err := config.SaveTo(initialCfg, configPath); err != nil {
		t.Fatalf("Failed to save initial config: %v", err)
	}

	// Use a pipe for stdin
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()

	var stdout bytes.Buffer
	srv, err := New(Options{
		Config:          initialCfg,
		ConfigPath:      configPath, // Enable watching
		Stdin:           pipeReader,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start server in goroutine
	done := make(chan error)
	go func() {
		done <- srv.Run(ctx)
	}()

	// Send initialize request
	_, err = pipeWriter.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n")
	if err != nil {
		t.Fatalf("Failed to write initialize: %v", err)
	}

	// Wait for server to initialize and start watching
	time.Sleep(300 * time.Millisecond)

	// Modify config file
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1 Updated", Enabled: &enabled, Command: "echo"},
			"srv2": {ID: "srv2", Name: "Server 2", Enabled: &enabled, Command: "echo"},
		},
	}

	if err := config.SaveTo(newCfg, configPath); err != nil {
		t.Fatalf("Failed to save new config: %v", err)
	}

	// Wait for debounce + reload
	time.Sleep(500 * time.Millisecond)

	// Cancel context to stop server first
	cancel()

	// Wait for server to stop
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not stop")
	}

	// Now safe to verify config was reloaded (server stopped, no concurrent access)
	if len(srv.cfg.Servers) != 2 {
		t.Errorf("Expected 2 servers after file change, got %d", len(srv.cfg.Servers))
	}
}

func TestServer_WatchConfig_IgnoresParseErrors(t *testing.T) {
	// Create temp directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
		},
	}

	if err := config.SaveTo(initialCfg, configPath); err != nil {
		t.Fatalf("Failed to save initial config: %v", err)
	}

	// Use a pipe for stdin
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()

	var stdout bytes.Buffer
	srv, err := New(Options{
		Config:          initialCfg,
		ConfigPath:      configPath,
		Stdin:           pipeReader,
		Stdout:          &stdout,
		ServerName:      "mcpmu-test",
		ServerVersion:   "1.0.0",
		ProtocolVersion: "2024-11-05",
		LogLevel:        "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start server in goroutine
	done := make(chan error)
	go func() {
		done <- srv.Run(ctx)
	}()

	// Send initialize request
	_, err = pipeWriter.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n")
	if err != nil {
		t.Fatalf("Failed to write initialize: %v", err)
	}

	// Wait for server to initialize
	time.Sleep(300 * time.Millisecond)

	// Write invalid JSON to config
	if err := os.WriteFile(configPath, []byte("{ invalid json }"), 0600); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	// Wait for debounce
	time.Sleep(500 * time.Millisecond)

	// Cancel context to stop server first
	cancel()

	// Wait for server to stop
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not stop")
	}

	// Now safe to verify original config is still in place (server stopped)
	if len(srv.cfg.Servers) != 1 {
		t.Errorf("Expected 1 server (original config), got %d", len(srv.cfg.Servers))
	}
	if srv.cfg.Servers["srv1"].Name != "Server 1" {
		t.Error("Original config was modified despite parse error")
	}
}

// TestEndToEnd_HotReload_ToolsChange is an integration test that:
// 1. Builds the real binary
// 2. Starts serve mode with a config file
// 3. Sends initialize + tools/list requests
// 4. Modifies the config file (adds a server)
// 5. Waits for hot-reload
// 6. Sends tools/list again
// 7. Verifies the tools list changed
func TestEndToEnd_HotReload_ToolsChange(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end test in short mode")
	}

	// Build the binary
	tmpBin := t.TempDir() + "/mcpmu"
	cmd := exec.Command("go", "build", "-o", tmpBin, "../../cmd/mcpmu")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build binary: %v\n%s", err, output)
	}

	// Create initial config with one fake server
	tmpConfig := t.TempDir() + "/config.json"
	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				ID:      "srv1",
				Name:    "Server 1",
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
		Namespaces: []config.NamespaceConfig{},
	}
	if err := config.SaveTo(initialCfg, tmpConfig); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Start the server
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverCmd := exec.CommandContext(ctx, tmpBin, "serve", "--stdio", "--config", tmpConfig, "--log-level", "debug", "--expose-manager-tools")
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

	// Capture stderr for debugging
	stderrBuf := &bytes.Buffer{}
	go func() { _, _ = io.Copy(stderrBuf, stderr) }()

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		_ = stdin.Close()
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
		if t.Failed() {
			t.Logf("Server stderr:\n%s", stderrBuf.String())
		}
	})

	reader := bufio.NewReader(stdout)

	// Helper to send request and read response
	sendAndRead := func(req string) (json.RawMessage, error) {
		if _, err := stdin.Write([]byte(req + "\n")); err != nil {
			return nil, err
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		return json.RawMessage(line), nil
	}

	// Step 1: Initialize
	initResp, err := sendAndRead(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}`)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	var initResult struct {
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(initResp, &initResult); err != nil {
		t.Fatalf("Parse init response: %v", err)
	}
	if initResult.Error != nil {
		t.Fatalf("Initialize error: %v", initResult.Error)
	}
	t.Log("Initialize: OK")

	// Step 2: Get initial tools list
	toolsResp1, err := sendAndRead(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	var toolsResult1 struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(toolsResp1, &toolsResult1); err != nil {
		t.Fatalf("Parse tools/list response: %v", err)
	}
	if toolsResult1.Error != nil {
		t.Fatalf("tools/list error: %v", toolsResult1.Error)
	}

	// Count non-manager tools
	initialToolCount := 0
	for _, tool := range toolsResult1.Result.Tools {
		if !strings.HasPrefix(tool.Name, "mcpmu.") {
			initialToolCount++
		}
	}
	t.Logf("Initial tools/list: %d non-manager tools", initialToolCount)

	// Check that srv1.tool_a exists
	hasSrv1ToolA := false
	for _, tool := range toolsResult1.Result.Tools {
		if tool.Name == "srv1.tool_a" {
			hasSrv1ToolA = true
			break
		}
	}
	if !hasSrv1ToolA {
		t.Error("Expected srv1.tool_a in initial tools list")
	}

	// Step 3: Modify config - add a second server
	updatedCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {
				ID:      "srv1",
				Name:    "Server 1",
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
				ID:      "srv2",
				Name:    "Server 2",
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"tool_b","description":"Tool B"},{"name":"tool_c","description":"Tool C"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: []config.NamespaceConfig{},
	}
	if err := config.SaveTo(updatedCfg, tmpConfig); err != nil {
		t.Fatalf("Failed to save updated config: %v", err)
	}
	t.Log("Config updated: added srv2 with tool_b and tool_c")

	// Step 4: Wait for hot-reload (debounce 150ms + processing time)
	time.Sleep(800 * time.Millisecond)

	// Step 5: Get tools list again
	toolsResp2, err := sendAndRead(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if err != nil {
		t.Fatalf("tools/list after reload failed: %v", err)
	}
	var toolsResult2 struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(toolsResp2, &toolsResult2); err != nil {
		t.Fatalf("Parse tools/list response after reload: %v", err)
	}
	if toolsResult2.Error != nil {
		t.Fatalf("tools/list after reload error: %v", toolsResult2.Error)
	}

	// Count non-manager tools after reload
	reloadedToolCount := 0
	toolNames := make(map[string]bool)
	for _, tool := range toolsResult2.Result.Tools {
		if !strings.HasPrefix(tool.Name, "mcpmu.") {
			reloadedToolCount++
			toolNames[tool.Name] = true
		}
	}
	t.Logf("After reload tools/list: %d non-manager tools", reloadedToolCount)

	// Verify we now have tools from both servers
	expectedTools := []string{"srv1.tool_a", "srv2.tool_b", "srv2.tool_c"}
	for _, expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("Expected %s in tools list after reload, got: %v", expected, toolNames)
		}
	}

	// Verify the tool count increased
	if reloadedToolCount <= initialToolCount {
		t.Errorf("Expected more tools after reload (was %d, now %d)", initialToolCount, reloadedToolCount)
	}

	t.Log("Hot-reload integration test passed!")
}
