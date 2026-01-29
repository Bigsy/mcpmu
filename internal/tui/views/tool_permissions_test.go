package views

import (
	"testing"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
)

func TestToolPermissions_Show(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	if perms.IsVisible() {
		t.Error("should not be visible initially")
	}

	serverTools := map[string][]events.McpTool{
		"s1": {
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
	}
	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
	}
	permissions := []config.ToolPermission{
		{NamespaceID: "ns1", ServerID: "s1", ToolName: "read_file", Enabled: true},
	}

	perms.Show("ns1", serverTools, servers, permissions, false)

	if !perms.IsVisible() {
		t.Error("should be visible after Show")
	}
	if perms.namespaceID != "ns1" {
		t.Errorf("expected namespace 'ns1', got %q", perms.namespaceID)
	}
	if perms.denyByDefault {
		t.Error("denyByDefault should be false")
	}

	// Check original permissions were loaded
	if !perms.originalPerms["s1:read_file"] {
		t.Error("expected s1:read_file to be enabled in original perms")
	}
}

func TestToolPermissions_Show_WithDenyByDefault(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	serverTools := map[string][]events.McpTool{
		"s1": {
			{Name: "read_file", Description: "Read a file"},
		},
	}
	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
	}

	perms.Show("ns1", serverTools, servers, nil, true)

	if !perms.denyByDefault {
		t.Error("denyByDefault should be true")
	}
}

func TestToolPermissions_Hide(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	serverTools := map[string][]events.McpTool{
		"s1": {{Name: "tool1"}},
	}
	perms.Show("ns1", serverTools, nil, nil, false)

	if !perms.IsVisible() {
		t.Fatal("should be visible")
	}

	perms.Hide()

	if perms.IsVisible() {
		t.Error("should not be visible after Hide")
	}
}

func TestToolPermItem_Interface(t *testing.T) {
	// Test regular tool item
	item := toolPermItem{
		serverID:    "s1",
		serverName:  "Server 1",
		toolName:    "read_file",
		description: "Read a file",
		enabled:     true,
		isHeader:    false,
	}

	if item.Title() != "read_file" {
		t.Errorf("expected title 'read_file', got %q", item.Title())
	}
	if item.Description() != "Read a file" {
		t.Errorf("expected description 'Read a file', got %q", item.Description())
	}
	if item.FilterValue() != "read_file" {
		t.Errorf("expected filter value 'read_file', got %q", item.FilterValue())
	}

	// Test header item
	header := toolPermItem{
		serverName: "Server 1",
		isHeader:   true,
	}

	if header.Title() != "Server 1" {
		t.Errorf("expected header title 'Server 1', got %q", header.Title())
	}
}

// Phase 3.2 Tests

func TestToolPermissions_ShowDiscovering(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	autoStarted := []string{"s1", "s2"}
	perms.ShowDiscovering("ns1", autoStarted)

	if !perms.IsVisible() {
		t.Error("should be visible after ShowDiscovering")
	}
	if !perms.IsDiscovering() {
		t.Error("should be in discovering mode")
	}
	if perms.namespaceID != "ns1" {
		t.Errorf("expected namespace 'ns1', got %q", perms.namespaceID)
	}

	got := perms.GetAutoStartedServers()
	if len(got) != 2 || got[0] != "s1" || got[1] != "s2" {
		t.Errorf("expected auto-started servers [s1, s2], got %v", got)
	}
}

func TestToolPermissions_FinishDiscovery(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	// Start in discovering mode
	perms.ShowDiscovering("ns1", []string{"s1"})

	if !perms.IsDiscovering() {
		t.Fatal("should be in discovering mode")
	}

	// Finish discovery with tools
	serverTools := map[string][]events.McpTool{
		"s1": {
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
	}
	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
	}

	perms.FinishDiscovery(serverTools, servers, nil, false)

	if perms.IsDiscovering() {
		t.Error("should not be in discovering mode after FinishDiscovery")
	}
	if !perms.IsVisible() {
		t.Error("should still be visible after FinishDiscovery")
	}

	// Check that items were loaded
	items := perms.list.Items()
	if len(items) != 3 { // 1 header + 2 tools
		t.Errorf("expected 3 items (1 header + 2 tools), got %d", len(items))
	}
}

func TestToolPermissions_BulkEnableSafe(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"s1": {
			{Name: "read_file", Description: "Read"},    // safe
			{Name: "write_file", Description: "Write"},  // unsafe
			{Name: "get_info", Description: "Get info"}, // safe
			{Name: "custom_tool", Description: "Custom"}, // unknown
		},
	}
	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
	}

	// Test with denyByDefault=true (need to explicitly allow safe tools)
	perms.Show("ns1", serverTools, servers, nil, true)
	perms.applyBulkEnableSafe()

	// Safe tools should be explicitly allowed
	if !perms.currentPerms["s1:read_file"] {
		t.Error("read_file should be enabled (safe tool)")
	}
	if !perms.currentPerms["s1:get_info"] {
		t.Error("get_info should be enabled (safe tool)")
	}

	// Unsafe and unknown tools should not be in currentPerms
	if _, exists := perms.currentPerms["s1:write_file"]; exists {
		t.Error("write_file should not be in currentPerms (unsafe tool)")
	}
	if _, exists := perms.currentPerms["s1:custom_tool"]; exists {
		t.Error("custom_tool should not be in currentPerms (unknown tool)")
	}
}

func TestToolPermissions_BulkDenyAll(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"s1": {
			{Name: "read_file", Description: "Read"},
			{Name: "write_file", Description: "Write"},
		},
	}
	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
	}

	// Test with denyByDefault=false (need to explicitly deny all)
	perms.Show("ns1", serverTools, servers, nil, false)
	perms.applyBulkDenyAll()

	// All tools should be explicitly denied
	if enabled, exists := perms.currentPerms["s1:read_file"]; !exists || enabled {
		t.Error("read_file should be explicitly denied")
	}
	if enabled, exists := perms.currentPerms["s1:write_file"]; !exists || enabled {
		t.Error("write_file should be explicitly denied")
	}
}

func TestToolPermissions_DiscoveryTimeout(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	perms.ShowDiscovering("ns1", []string{"s1"})
	perms.SetDiscoveryTimeout()

	if !perms.discoveryTimeout {
		t.Error("discovery timeout should be set")
	}
}

func TestToolPermissions_AutoStartedServersInResult(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"s1": {{Name: "tool1"}},
	}
	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
	}

	// Start with auto-started servers tracked
	perms.ShowDiscovering("ns1", []string{"s1", "s2"})
	perms.FinishDiscovery(serverTools, servers, nil, false)

	// Verify auto-started servers are still tracked
	got := perms.GetAutoStartedServers()
	if len(got) != 2 {
		t.Errorf("expected 2 auto-started servers, got %d", len(got))
	}
}
