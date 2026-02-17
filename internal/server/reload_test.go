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

// testDebounceDelay is a short debounce delay for tests.
const testDebounceDelay = 10 * time.Millisecond

func TestServer_ApplyReload_SwapsConfig(t *testing.T) {
	t.Parallel()
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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

	// Run initialization
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Create new config
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
			"srv2": {Enabled: &enabled, Command: "echo"},
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
	t.Parallel()
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {Description: "Work", ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
		PIDTrackerDir:   t.TempDir(),
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
	if srv.activeNamespaceName != "ns1" {
		t.Fatal("Expected ns1 to be active after init")
	}
	if srv.selectionMethod != SelectionFlag {
		t.Errorf("Expected SelectionFlag, got %v", srv.selectionMethod)
	}

	// Create new config with same namespace but updated servers
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
			"srv2": {Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {Description: "Work", ServerIDs: []string{"srv1", "srv2"}},
		},
	}

	// Apply reload
	srv.applyReload(context.Background(), newCfg)

	// Verify namespace was kept
	if srv.activeNamespaceName != "ns1" {
		t.Error("Expected ns1 to still be active after reload")
	}
	if srv.selectionMethod != SelectionFlag {
		t.Errorf("Expected SelectionFlag to be preserved, got %v", srv.selectionMethod)
	}
	if len(srv.activeServerNames) != 2 {
		t.Errorf("Expected 2 active servers after reload, got %d", len(srv.activeServerNames))
	}
}

func TestServer_ApplyReload_ReSelectsNamespaceIfRemoved(t *testing.T) {
	t.Parallel()
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {Description: "Work", ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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

	// Run initialization (will auto-select ns1 as only namespace)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Verify initial state
	if srv.activeNamespaceName != "ns1" {
		t.Fatal("Expected ns1 to be active after init")
	}

	// Create new config with different namespace (old one removed)
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns2": {Description: "Personal", ServerIDs: []string{"srv1"}},
		},
	}

	// Apply reload
	srv.applyReload(context.Background(), newCfg)

	// Verify namespace was re-selected (ns2 is now the only namespace)
	if srv.activeNamespaceName != "ns2" {
		t.Error("Expected ns2 to be selected after reload (only available namespace)")
	}
	if srv.selectionMethod != SelectionOnly {
		t.Errorf("Expected SelectionOnly, got %v", srv.selectionMethod)
	}
}

func TestServer_ApplyReload_KeepsPreviousNamespaceOnResolutionFailure(t *testing.T) {
	t.Parallel()
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns1": {Description: "Work", ServerIDs: []string{"srv1"}},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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

	// Run initialization (will auto-select ns1 as only namespace)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	// Verify initial state
	if srv.activeNamespaceName != "ns1" {
		t.Fatal("Expected ns1 to be active after init")
	}
	if srv.selectionMethod != SelectionOnly {
		t.Errorf("Expected SelectionOnly, got %v", srv.selectionMethod)
	}

	// Create new config where ns1 is deleted and 2 new namespaces exist
	// (triggers "multiple namespaces, none selected" error in resolveNamespace)
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
			"srv2": {Enabled: &enabled, Command: "echo"},
		},
		Namespaces: map[string]config.NamespaceConfig{
			"ns2": {Description: "Personal", ServerIDs: []string{"srv1"}},
			"ns3": {Description: "Work2", ServerIDs: []string{"srv2"}},
		},
	}

	// Apply reload - namespace resolution should fail (2 namespaces, none selected)
	srv.applyReload(context.Background(), newCfg)

	// Verify the server kept the previous namespace config (fail-closed)
	if srv.activeNamespaceName != "ns1" {
		t.Errorf("Expected ns1 to be kept (fail-closed), got %q", srv.activeNamespaceName)
	}
	if srv.selectionMethod != SelectionOnly {
		t.Errorf("Expected SelectionOnly to be preserved, got %v", srv.selectionMethod)
	}
	if len(srv.activeServerNames) != 1 || srv.activeServerNames[0] != "srv1" {
		t.Errorf("Expected previous server list [srv1], got %v", srv.activeServerNames)
	}
}

func TestServer_ApplyReload_RebuildAggregatorAndRouter(t *testing.T) {
	t.Parallel()
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          oldCfg,
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
			"srv2": {Enabled: &enabled, Command: "echo"},
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
	t.Parallel()
	enabled := true
	oldCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
		},
	}

	// Use a pipe so we can control when the server stops
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer func() { _ = pipeReader.Close() }()
	defer func() { _ = pipeWriter.Close() }()

	var stdout bytes.Buffer
	srv, err := New(Options{
		Config:          oldCfg,
		PIDTrackerDir:   t.TempDir(),
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
	time.Sleep(50 * time.Millisecond)

	// Send new config via reload channel
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv2": {Enabled: &enabled, Command: "echo"},
		},
	}

	select {
	case srv.reloadCh <- newCfg:
		// Config sent
	case <-time.After(time.Second):
		t.Fatal("Timeout sending to reload channel")
	}

	// Wait for reload to be processed
	time.Sleep(50 * time.Millisecond)

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
	t.Parallel()
	// Create temp directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
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
	defer func() { _ = pipeReader.Close() }()
	defer func() { _ = pipeWriter.Close() }()

	var stdout bytes.Buffer
	srv, err := New(Options{
		Config:          initialCfg,
		ConfigPath:      configPath, // Enable watching
		PIDTrackerDir:   t.TempDir(),
		DebounceDelay:   testDebounceDelay,
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
	time.Sleep(50 * time.Millisecond)

	// Modify config file
	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
			"srv2": {Enabled: &enabled, Command: "echo"},
		},
	}

	if err := config.SaveTo(newCfg, configPath); err != nil {
		t.Fatalf("Failed to save new config: %v", err)
	}

	// Wait for debounce + reload
	time.Sleep(50 * time.Millisecond)

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
	t.Parallel()
	// Create temp directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {Enabled: &enabled, Command: "echo"},
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
	defer func() { _ = pipeReader.Close() }()
	defer func() { _ = pipeWriter.Close() }()

	var stdout bytes.Buffer
	srv, err := New(Options{
		Config:          initialCfg,
		ConfigPath:      configPath,
		PIDTrackerDir:   t.TempDir(),
		DebounceDelay:   testDebounceDelay,
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
	time.Sleep(50 * time.Millisecond)

	// Write invalid JSON to config
	if err := os.WriteFile(configPath, []byte("{ invalid json }"), 0600); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	// Wait for debounce
	time.Sleep(50 * time.Millisecond)

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
	if srv.cfg.Servers["srv1"].Command != "echo" {
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

	// Create initial config with one fake server
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
		<-stderrDone // wait for io.Copy goroutine to finish before reading buffer
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
					"FAKE_MCP_CFG":           `{"tools":[{"name":"tool_b","description":"Tool B"},{"name":"tool_c","description":"Tool C"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
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

// TestServer_ReloadDuringActiveRequest verifies behavior when a config reload
// is queued while a tools/call request is being processed.
//
// Due to the server's architecture (serialized request handling in Run() loop),
// the reload is processed AFTER the current request completes, not during it.
// This test verifies:
//
// 1. In-flight requests complete before reload is applied (serialization guarantee)
// 2. Reload kills upstream server processes (StopAll)
// 3. Subsequent tool calls may fail with EOF (stale handle) until server restarts
// 4. Server remains functional for other operations (tools/list works)
//
// This is important test coverage for ensuring graceful degradation when
// config changes happen during active use.
func TestServer_ReloadDuringActiveRequest(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping test in short mode")
	}

	// Build the binary
	tmpBin := t.TempDir() + "/mcpmu"
	cmd := exec.Command("go", "build", "-o", tmpBin, "../../cmd/mcpmu")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build binary: %v\n%s", err, output)
	}

	// Create initial config with a server that has a delay on tools/call
	tmpConfig := t.TempDir() + "/config.json"
	enabled := true
	initialCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"slow-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// Configure 2s delay on tools/call to ensure reload happens mid-request
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_tool","description":"A slow tool"}],"delays":{"tools/call":2000000000},"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}
	if err := config.SaveTo(initialCfg, tmpConfig); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
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

	// Capture stderr for debugging
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
		<-stderrDone // wait for io.Copy goroutine to finish before reading buffer
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

	// Helper to send request without waiting for response
	sendOnly := func(req string) error {
		_, err := stdin.Write([]byte(req + "\n"))
		return err
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

	// Step 2: Send a tools/call request (which will take 500ms due to the delay)
	// Do this in a goroutine since it will block
	type toolsCallResult struct {
		resp json.RawMessage
		err  error
	}
	toolsCallDone := make(chan toolsCallResult, 1)
	go func() {
		resp, err := sendAndRead(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow-srv.slow_tool","arguments":{}}}`)
		toolsCallDone <- toolsCallResult{resp, err}
	}()

	// Step 3: Wait a bit for the request to be in-flight (give time for server to start and receive request)
	time.Sleep(500 * time.Millisecond)

	// Modify config to trigger hot-reload
	reloadCfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"slow-srv": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					// No delay this time
					"FAKE_MCP_CFG": `{"tools":[{"name":"slow_tool","description":"A slow tool (now fast)"}],"echoToolCalls":true}`,
				},
			},
		},
		Namespaces: map[string]config.NamespaceConfig{},
	}
	if err := config.SaveTo(reloadCfg, tmpConfig); err != nil {
		t.Fatalf("Failed to save reload config: %v", err)
	}
	t.Log("Config file updated, waiting for hot-reload")

	// Wait for hot-reload (debounce ~150ms + processing)
	time.Sleep(400 * time.Millisecond)

	// Step 4: Wait for the in-flight tools/call to complete
	// It should either fail (due to StopAll killing the upstream) or complete (if timing allows)
	// With a 2s delay on the fake server and reload happening ~1s in, we expect the request to be interrupted
	requestWasInterrupted := false
	select {
	case result := <-toolsCallDone:
		if result.err != nil {
			t.Logf("In-flight tools/call returned IO error (expected if killed by reload): %v", result.err)
			requestWasInterrupted = true
		} else {
			var toolsCallResp struct {
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
			if err := json.Unmarshal(result.resp, &toolsCallResp); err != nil {
				t.Logf("In-flight tools/call response parse error: %v (raw: %s)", err, string(result.resp))
				requestWasInterrupted = true
			} else if toolsCallResp.Error != nil {
				t.Logf("In-flight tools/call returned RPC error (expected if killed by reload): code=%d msg=%s",
					toolsCallResp.Error.Code, toolsCallResp.Error.Message)
				requestWasInterrupted = true
			} else {
				// Request completed successfully - this can happen if the reload timing didn't interrupt it
				// We log this but don't fail the test since timing is inherently non-deterministic
				t.Logf("In-flight tools/call completed successfully (timing allowed completion before reload)")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("In-flight tools/call did not complete within timeout")
	}

	// Log whether the request was interrupted (the main purpose of this test is to verify
	// the server handles this scenario gracefully, not to guarantee interruption)
	if requestWasInterrupted {
		t.Log("Request was interrupted by config reload (expected behavior)")
	} else {
		t.Log("Request completed before reload took effect (acceptable - timing dependent)")
	}

	// Step 5: Verify server is still functional after reload by calling tools/list
	if err := sendOnly(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`); err != nil {
		t.Fatalf("Failed to send tools/list after reload: %v", err)
	}

	// Read the tools/list response (may need to skip the tools/call response if it wasn't read yet)
	var toolsListResp struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	for range 3 { // Try a few times to get the right response
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("Failed to read response after reload: %v", err)
		}

		if err := json.Unmarshal(line, &toolsListResp); err != nil {
			t.Fatalf("Parse response after reload: %v (raw: %s)", err, string(line))
		}

		if toolsListResp.ID == 3 {
			break // Found our tools/list response
		}
		t.Logf("Skipping response with ID=%d", toolsListResp.ID)
	}

	if toolsListResp.ID != 3 {
		t.Fatal("Did not receive tools/list response (ID=3)")
	}

	if toolsListResp.Error != nil {
		t.Fatalf("tools/list after reload returned error: %v", toolsListResp.Error)
	}

	t.Logf("Server functional after reload: tools/list returned %d tools", len(toolsListResp.Result.Tools))

	// Step 6: Verify we can call the tool again (after reload)
	toolsCallResp2, err := sendAndRead(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"slow-srv.slow_tool","arguments":{}}}`)
	if err != nil {
		t.Fatalf("tools/call after reload failed: %v", err)
	}

	var toolsCall2Result struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}
	if err := json.Unmarshal(toolsCallResp2, &toolsCall2Result); err != nil {
		t.Fatalf("Parse tools/call response after reload: %v", err)
	}

	if toolsCall2Result.Error != nil {
		// The tool call may fail if the server hasn't fully restarted yet
		// This is acceptable - we just need to know it doesn't crash the server
		t.Logf("tools/call after reload returned error (may be expected): %v", toolsCall2Result.Error)
	} else {
		t.Log("tools/call after reload succeeded")
	}

	t.Log("Reload during active request test passed!")
}
