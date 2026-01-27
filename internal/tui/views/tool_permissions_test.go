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
