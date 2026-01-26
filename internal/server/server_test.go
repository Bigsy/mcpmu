package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hedworth/mcp-studio-go/internal/config"
)

func TestServer_Initialize(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
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

	// Run server - it will exit when stdin is exhausted
	err = srv.Run(ctx)
	if err != nil && err != context.Canceled {
		t.Logf("Run error (expected EOF): %v", err)
	}

	// Parse response
	output := stdout.String()
	t.Logf("Output: %s", output)

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Capabilities struct {
				Tools *struct{} `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v\nOutput: %s", err, output)
	}

	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}

	if resp.Result.ServerInfo.Name != "mcp-studio-test" {
		t.Errorf("ServerInfo.Name = %q, want %q", resp.Result.ServerInfo.Name, "mcp-studio-test")
	}

	if resp.Result.ProtocolVersion != "2024-11-05" {
		t.Errorf("ProtocolVersion = %q, want %q", resp.Result.ProtocolVersion, "2024-11-05")
	}

	if resp.Result.Capabilities.Tools == nil {
		t.Error("Expected tools capability to be present")
	}
}

func TestServer_ToolsList_NoServers(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
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

	// Parse the second response (tools/list)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected 2 responses, got %d: %s", len(lines), stdout.String())
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Tools []AggregatedTool `json:"tools"`
		} `json:"result"`
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal tools/list response: %v\nLine: %s", err, lines[1])
	}

	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}

	// Should have manager tools even with no servers
	managerTools := 0
	for _, tool := range resp.Result.Tools {
		if strings.HasPrefix(tool.Name, "mcp-studio.") {
			managerTools++
		}
	}

	if managerTools < 5 {
		t.Errorf("Expected at least 5 manager tools, got %d", managerTools)
	}
}

func TestServer_ToolsList_NotInitialized(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	// Send tools/list without initialize first
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}
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

	var resp struct {
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	if resp.Error == nil {
		t.Fatal("Expected error for tools/list without initialize")
	}

	if resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("Error code = %d, want %d", resp.Error.Code, ErrCodeInvalidRequest)
	}
}

func TestServer_MethodNotFound(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"unknown/method"}
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
		t.Fatal("Expected error for unknown method")
	}

	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("Error code = %d, want %d", resp.Error.Code, ErrCodeMethodNotFound)
	}
}

func TestServer_Ping(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
{"jsonrpc":"2.0","id":2,"method":"ping"}
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
		ID     int       `json:"id"`
		Result struct{}  `json:"result"`
		Error  *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("Unmarshal ping response: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("Unexpected error: %v", resp.Error)
	}

	if resp.ID != 2 {
		t.Errorf("Response ID = %d, want 2", resp.ID)
	}
}

func TestServer_NamespaceSelection_NoNamespaces(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"srv1": {ID: "srv1", Name: "Server 1", Enabled: &enabled, Command: "echo"},
			"srv2": {ID: "srv2", Name: "Server 2", Enabled: &enabled, Command: "echo"},
		},
		Namespaces: []config.NamespaceConfig{}, // No namespaces
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
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

	var resp struct {
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	// Should succeed - no namespaces means all servers exposed
	if resp.Error != nil {
		t.Errorf("Unexpected error: %v", resp.Error)
	}
}

func TestServer_NamespaceSelection_MultipleNamespacesNoDefault(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns1", Name: "Namespace 1"},
			{ID: "ns2", Name: "Namespace 2"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
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

	var resp struct {
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	// Should fail - multiple namespaces but none selected
	if resp.Error == nil {
		t.Fatal("Expected error for multiple namespaces with no selection")
	}

	if resp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("Error code = %d, want %d", resp.Error.Code, ErrCodeInvalidRequest)
	}
}

func TestServer_NamespaceSelection_WithDefault(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion:      1,
		DefaultNamespaceID: "ns1",
		Servers:            map[string]config.ServerConfig{},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns1", Name: "Namespace 1"},
			{ID: "ns2", Name: "Namespace 2"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
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

	var resp struct {
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	// Should succeed - default namespace is set
	if resp.Error != nil {
		t.Errorf("Unexpected error: %v", resp.Error)
	}
}

func TestServer_NamespaceSelection_ExplicitNamespace(t *testing.T) {
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
		Namespaces: []config.NamespaceConfig{
			{ID: "ns1", Name: "Namespace 1"},
			{ID: "ns2", Name: "Namespace 2"},
		},
	}

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}
`)

	srv, err := New(Options{
		Config:          cfg,
		Namespace:       "ns2", // Explicit selection
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

	var resp struct {
		Error *RPCError `json:"error"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}

	// Should succeed - explicit namespace selection
	if resp.Error != nil {
		t.Errorf("Unexpected error: %v", resp.Error)
	}
}

func TestParseToolName(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantServer string
		wantTool   string
		wantMgr    bool
	}{
		{"manager tool", "mcp-studio.servers_list", "", "mcp-studio.servers_list", true},
		{"regular tool", "filesystem.read_file", "filesystem", "read_file", false},
		{"no dot", "tool_name", "", "tool_name", false},
		{"empty", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, tool, isMgr := ParseToolName(tt.input)
			if server != tt.wantServer {
				t.Errorf("server = %q, want %q", server, tt.wantServer)
			}
			if tool != tt.wantTool {
				t.Errorf("tool = %q, want %q", tool, tt.wantTool)
			}
			if isMgr != tt.wantMgr {
				t.Errorf("isManager = %v, want %v", isMgr, tt.wantMgr)
			}
		})
	}
}
