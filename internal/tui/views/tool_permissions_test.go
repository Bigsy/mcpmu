package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

func TestToolPermissions_Show(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	if perms.IsVisible() {
		t.Error("should not be visible initially")
	}

	serverTools := map[string][]events.McpTool{
		"Server 1": {
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd"}},
	}
	permissions := []config.ToolPermission{
		{Namespace: "ns1", Server: "Server 1", ToolName: "read_file", Enabled: true},
	}

	perms.Show("ns1", serverTools, servers, permissions, false, nil, nil)

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
	if !perms.originalPerms["Server 1:read_file"] {
		t.Error("expected Server 1:read_file to be enabled in original perms")
	}
}

func TestToolPermissions_Show_WithDenyByDefault(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	serverTools := map[string][]events.McpTool{
		"Server 1": {
			{Name: "read_file", Description: "Read a file"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd"}},
	}

	perms.Show("ns1", serverTools, servers, nil, true, nil, nil)

	if !perms.denyByDefault {
		t.Error("denyByDefault should be true")
	}
}

func TestToolPermissions_Hide(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	serverTools := map[string][]events.McpTool{
		"Server 1": {{Name: "tool1"}},
	}
	perms.Show("ns1", serverTools, nil, nil, false, nil, nil)

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
		serverID:    "Server 1",
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

	autoStarted := []string{"Server 1", "Server 2"}
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
	if len(got) != 2 || got[0] != "Server 1" || got[1] != "Server 2" {
		t.Errorf("expected auto-started servers [Server 1, Server 2], got %v", got)
	}
}

func TestToolPermissions_FinishDiscovery(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	// Start in discovering mode
	perms.ShowDiscovering("ns1", []string{"Server 1"})

	if !perms.IsDiscovering() {
		t.Fatal("should be in discovering mode")
	}

	// Finish discovery with tools
	serverTools := map[string][]events.McpTool{
		"Server 1": {
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd"}},
	}

	perms.FinishDiscovery(serverTools, servers, nil, false, nil, nil)

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
		"Server 1": {
			{Name: "read_file", Description: "Read"},     // safe
			{Name: "write_file", Description: "Write"},   // unsafe
			{Name: "get_info", Description: "Get info"},  // safe
			{Name: "custom_tool", Description: "Custom"}, // unknown
		},
	}
	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd"}},
	}

	// Test with denyByDefault=true (need to explicitly allow safe tools)
	perms.Show("ns1", serverTools, servers, nil, true, nil, nil)
	perms.applyBulkEnableSafe()

	// Safe tools should be explicitly allowed
	if !perms.currentPerms["Server 1:read_file"] {
		t.Error("read_file should be enabled (safe tool)")
	}
	if !perms.currentPerms["Server 1:get_info"] {
		t.Error("get_info should be enabled (safe tool)")
	}

	// Unsafe and unknown tools should not be in currentPerms
	if _, exists := perms.currentPerms["Server 1:write_file"]; exists {
		t.Error("write_file should not be in currentPerms (unsafe tool)")
	}
	if _, exists := perms.currentPerms["Server 1:custom_tool"]; exists {
		t.Error("custom_tool should not be in currentPerms (unknown tool)")
	}
}

func TestToolPermissions_BulkDenyAll(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"Server 1": {
			{Name: "read_file", Description: "Read"},
			{Name: "write_file", Description: "Write"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd"}},
	}

	// Test with denyByDefault=false (need to explicitly deny all)
	perms.Show("ns1", serverTools, servers, nil, false, nil, nil)
	perms.applyBulkDenyAll()

	// All tools should be explicitly denied
	if enabled, exists := perms.currentPerms["Server 1:read_file"]; !exists || enabled {
		t.Error("read_file should be explicitly denied")
	}
	if enabled, exists := perms.currentPerms["Server 1:write_file"]; !exists || enabled {
		t.Error("write_file should be explicitly denied")
	}
}

func TestToolPermissions_DiscoveryTimeout(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)

	perms.ShowDiscovering("ns1", []string{"Server 1"})
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
		"Server 1": {{Name: "tool1"}},
	}
	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd"}},
	}

	// Start with auto-started servers tracked
	perms.ShowDiscovering("ns1", []string{"Server 1", "Server 2"})
	perms.FinishDiscovery(serverTools, servers, nil, false, nil, nil)

	// Verify auto-started servers are still tracked
	got := perms.GetAutoStartedServers()
	if len(got) != 2 {
		t.Errorf("expected 2 auto-started servers, got %d", len(got))
	}
}

// ============================================================================
// Server Default TUI Tests
// ============================================================================

func TestToolPermissions_Show_WithServerDefaults(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},
			{Name: "write_file", Description: "Write"},
		},
		"srv2": {
			{Name: "get_time", Description: "Get time"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
		{Name: "srv2", Config: config.ServerConfig{Command: "cmd"}},
	}

	// Namespace allows by default, but srv1 denies by default
	serverDefaults := map[string]bool{"srv1": true}
	perms.Show("ns1", serverTools, servers, nil, false, serverDefaults, nil)

	// srv1 tools should default to denied (server default deny)
	// No explicit permissions, so they should not be in currentPerms
	if _, exists := perms.currentPerms["srv1:read_file"]; exists {
		t.Error("srv1:read_file should not have explicit permission")
	}

	// Verify defaultAllowed works correctly
	if perms.defaultAllowed("srv1") {
		t.Error("srv1 should default to denied (server default)")
	}
	if !perms.defaultAllowed("srv2") {
		t.Error("srv2 should default to allowed (namespace default)")
	}

	// Check items: srv1 tools should show as disabled, srv2 as enabled
	for _, item := range perms.list.Items() {
		ti, ok := item.(toolPermItem)
		if !ok || ti.isHeader {
			continue
		}
		if ti.serverID == "srv1" && ti.enabled {
			t.Errorf("srv1 tool %q should be disabled (server default deny)", ti.toolName)
		}
		if ti.serverID == "srv2" && !ti.enabled {
			t.Errorf("srv2 tool %q should be enabled (namespace default allow)", ti.toolName)
		}
	}
}

func TestToolPermissions_Toggle_WithServerDefault(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
	}

	// srv1 denies by default
	serverDefaults := map[string]bool{"srv1": true}
	perms.Show("ns1", serverTools, servers, nil, false, serverDefaults, nil)

	// Initially no explicit perm; default is deny
	if _, exists := perms.currentPerms["srv1:read_file"]; exists {
		t.Fatal("should have no explicit permission initially")
	}

	// Move cursor to the tool (skip past header at index 0)
	perms.list.Select(1)

	// First toggle (space): deny→allow, creates explicit allow
	spaceMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	perms.Update(spaceMsg)

	if enabled, exists := perms.currentPerms["srv1:read_file"]; !exists || !enabled {
		t.Fatal("first toggle should create explicit allow")
	}

	// Second toggle (space): allow→deny, which matches server default deny
	// so the explicit permission should be removed (reverted to default)
	perms.Update(spaceMsg)

	if _, exists := perms.currentPerms["srv1:read_file"]; exists {
		t.Error("second toggle should revert to server default (remove explicit permission)")
	}
}

func TestToolPermissions_BulkEnableSafe_WithServerDefaults(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},   // safe
			{Name: "write_file", Description: "Write"}, // unsafe
		},
		"srv2": {
			{Name: "get_info", Description: "Get info"}, // safe
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
		{Name: "srv2", Config: config.ServerConfig{Command: "cmd"}},
	}

	// srv1 denies by default, namespace allows by default
	serverDefaults := map[string]bool{"srv1": true}
	perms.Show("ns1", serverTools, servers, nil, false, serverDefaults, nil)
	perms.applyBulkEnableSafe()

	// srv1:read_file should be explicitly allowed (server default is deny)
	if enabled, exists := perms.currentPerms["srv1:read_file"]; !exists || !enabled {
		t.Error("srv1:read_file should be explicitly allowed (safe tool, server default deny)")
	}

	// srv2:get_info should NOT have explicit perm (namespace default is allow, so safe tool already allowed)
	if _, exists := perms.currentPerms["srv2:get_info"]; exists {
		t.Error("srv2:get_info should not have explicit perm (already allowed by namespace default)")
	}
}

// ============================================================================
// Global Deny TUI Tests
// ============================================================================

func TestToolPermissions_Show_WithGlobalDeny(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},
			{Name: "delete_file", Description: "Delete"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
	}
	globalDenied := map[string][]string{"srv1": {"delete_file"}}

	perms.Show("ns1", serverTools, servers, nil, false, nil, globalDenied)

	// Both tools should appear in the list (globally denied ones are visible but locked)
	items := perms.list.Items()
	if len(items) != 3 { // 1 header + 2 tools
		t.Errorf("expected 3 items (1 header + 2 tools), got %d", len(items))
	}
}

func TestToolPermissions_Toggle_GlobalDenyIsNoOp(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "delete_file", Description: "Delete"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
	}
	globalDenied := map[string][]string{"srv1": {"delete_file"}}

	perms.Show("ns1", serverTools, servers, nil, false, nil, globalDenied)

	// Move cursor to the tool (skip past header at index 0)
	perms.list.Select(1)

	// Toggle should be a no-op
	spaceMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	perms.Update(spaceMsg)

	if _, exists := perms.currentPerms["srv1:delete_file"]; exists {
		t.Error("toggle on globally denied tool should be a no-op")
	}
}

func TestToolPermissions_BulkEnableSafe_SkipsGlobalDeny(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},    // safe
			{Name: "get_info", Description: "Get info"}, // safe but globally denied
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
	}
	globalDenied := map[string][]string{"srv1": {"get_info"}}

	perms.Show("ns1", serverTools, servers, nil, true, nil, globalDenied)
	perms.applyBulkEnableSafe()

	// read_file should be explicitly allowed (safe, not globally denied)
	if !perms.currentPerms["srv1:read_file"] {
		t.Error("read_file should be enabled (safe tool, not globally denied)")
	}

	// get_info should NOT be in currentPerms (globally denied, skipped)
	if _, exists := perms.currentPerms["srv1:get_info"]; exists {
		t.Error("get_info should not be in currentPerms (globally denied)")
	}
}

func TestToolPermissions_BulkDenyAll_SkipsGlobalDeny(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},
			{Name: "delete_file", Description: "Delete"}, // globally denied
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
	}
	globalDenied := map[string][]string{"srv1": {"delete_file"}}

	perms.Show("ns1", serverTools, servers, nil, false, nil, globalDenied)
	perms.applyBulkDenyAll()

	// read_file should be explicitly denied
	if enabled, exists := perms.currentPerms["srv1:read_file"]; !exists || enabled {
		t.Error("read_file should be explicitly denied")
	}

	// delete_file should NOT be in currentPerms (globally denied, skipped)
	if _, exists := perms.currentPerms["srv1:delete_file"]; exists {
		t.Error("delete_file should not be in currentPerms (globally denied)")
	}
}

// ============================================================================
// Filter Tests
// ============================================================================

func newPermEditorWithTools(t *testing.T) ToolPermissionsModel {
	t.Helper()
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
		"srv2": {
			{Name: "read_resource", Description: "Read a resource"},
			{Name: "get_time", Description: "Get time"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
		{Name: "srv2", Config: config.ServerConfig{Command: "cmd"}},
	}
	perms.Show("ns1", serverTools, servers, nil, false, nil, nil)
	return perms
}

func sendPermRune(perms *ToolPermissionsModel, r rune) {
	perms.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

func enterPermFilterMode(perms *ToolPermissionsModel) {
	sendPermRune(perms, '/')
}

func TestToolPermissions_FilterShowsMatchingHeaders(t *testing.T) {
	perms := newPermEditorWithTools(t)

	enterPermFilterMode(&perms)
	for _, r := range "read_f" {
		sendPermRune(&perms, r)
	}

	// Should match "read_file" from srv1 only
	items := perms.list.Items()
	var headers, tools int
	for _, item := range items {
		ti := item.(toolPermItem)
		if ti.isHeader {
			headers++
			if ti.serverName != "srv1" {
				t.Errorf("expected only srv1 header, got %q", ti.serverName)
			}
		} else {
			tools++
			if ti.toolName != "read_file" {
				t.Errorf("expected read_file, got %q", ti.toolName)
			}
		}
	}
	if headers != 1 {
		t.Errorf("expected 1 header, got %d", headers)
	}
	if tools != 1 {
		t.Errorf("expected 1 tool, got %d", tools)
	}
}

func TestToolPermissions_FilterClearRestoresAll(t *testing.T) {
	perms := newPermEditorWithTools(t)
	originalCount := len(perms.list.Items())

	enterPermFilterMode(&perms)
	for _, r := range "read" {
		sendPermRune(&perms, r)
	}

	// Exit filter mode
	perms.Update(tea.KeyMsg{Type: tea.KeyEsc})
	// Clear filter in action mode
	perms.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if perms.filterInput.Value() != "" {
		t.Error("filter text should be cleared")
	}
	if len(perms.list.Items()) != originalCount {
		t.Errorf("expected %d items restored, got %d", originalCount, len(perms.list.Items()))
	}
}

func TestToolPermissions_BulkOpsAffectAllItemsWhenFiltered(t *testing.T) {
	perms := newPermEditorWithTools(t)

	// Filter to show only "read" tools
	enterPermFilterMode(&perms)
	for _, r := range "read" {
		sendPermRune(&perms, r)
	}

	// Exit filter mode to action mode
	perms.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Now press 'd' to deny all — should affect ALL tools, not just filtered
	sendPermRune(&perms, 'd')

	// Check that all tools across both servers are denied
	for _, item := range perms.allItems {
		ti := item.(toolPermItem)
		if ti.isHeader {
			continue
		}
		key := ti.serverID + ":" + ti.toolName
		enabled, exists := perms.currentPerms[key]
		if !exists {
			// Default is allow (denyByDefault=false), so should have explicit deny
			t.Errorf("expected explicit deny for %s, but not in currentPerms", key)
		} else if enabled {
			t.Errorf("expected %s to be denied, but it's enabled", key)
		}
	}
}

func TestToolPermissions_ActionKeysWorkInActionMode(t *testing.T) {
	perms := newPermEditorWithTools(t)

	// Press 'a' in action mode — should trigger enable-safe, not enter filter
	sendPermRune(&perms, 'a')

	if perms.filterFocused {
		t.Error("filter should not be focused after 'a' in action mode")
	}
	// read_file is safe, should not have explicit perm (already allowed by default)
	if _, exists := perms.currentPerms["srv1:read_file"]; exists {
		t.Error("read_file should not have explicit perm (already allowed by namespace default)")
	}
}

func TestToolPermissions_FilterNoMatchesEmptyState(t *testing.T) {
	perms := newPermEditorWithTools(t)

	enterPermFilterMode(&perms)
	for _, r := range "xyznonexistent" {
		sendPermRune(&perms, r)
	}

	if len(perms.list.Items()) != 0 {
		t.Errorf("expected 0 items, got %d", len(perms.list.Items()))
	}

	// Space should be a no-op (no panic)
	perms.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
}

func TestToolPermissions_FilterSkipsHeaderOnCursor(t *testing.T) {
	perms := newPermEditorWithTools(t)

	// Filter to something that matches tools in one server
	enterPermFilterMode(&perms)
	for _, r := range "read" {
		sendPermRune(&perms, r)
	}

	// Cursor should be on the first tool, not the header
	item := perms.list.SelectedItem()
	if item == nil {
		t.Fatal("expected a selected item")
	}
	ti := item.(toolPermItem)
	if ti.isHeader {
		t.Error("cursor should skip past the header to the first tool")
	}

	// Space should toggle successfully (not be a no-op on a header)
	perms.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	key := ti.serverID + ":" + ti.toolName
	if _, exists := perms.currentPerms[key]; !exists {
		// Default is allow; toggling should create explicit deny
		t.Errorf("expected toggle to create explicit permission for %s", key)
	}
}

func TestToolPermissions_SlashInFilterMode(t *testing.T) {
	perms := newPermEditorWithTools(t)

	enterPermFilterMode(&perms)
	for _, r := range "test" {
		sendPermRune(&perms, r)
	}

	// Type '/' while in filter mode — should be appended as text
	sendPermRune(&perms, '/')

	if perms.filterInput.Value() != "test/" {
		t.Errorf("expected filter text 'test/', got %q", perms.filterInput.Value())
	}
	if !perms.filterFocused {
		t.Error("should still be in filter mode")
	}
}

func TestToolPermissions_BulkDenyAll_WithServerDefaults(t *testing.T) {
	th := theme.New()
	perms := NewToolPermissions(th)
	perms.SetSize(100, 50)

	serverTools := map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},
		},
		"srv2": {
			{Name: "get_time", Description: "Get time"},
		},
	}
	servers := []config.ServerEntry{
		{Name: "srv1", Config: config.ServerConfig{Command: "cmd"}},
		{Name: "srv2", Config: config.ServerConfig{Command: "cmd"}},
	}

	// srv1 denies by default, namespace allows by default
	serverDefaults := map[string]bool{"srv1": true}
	perms.Show("ns1", serverTools, servers, nil, false, serverDefaults, nil)
	perms.applyBulkDenyAll()

	// srv1:read_file - server default is deny, so bulk deny should remove explicit (revert to default deny)
	if _, exists := perms.currentPerms["srv1:read_file"]; exists {
		t.Error("srv1:read_file should not have explicit perm (server default already denies)")
	}

	// srv2:get_time - namespace default is allow, so bulk deny should explicitly deny
	if enabled, exists := perms.currentPerms["srv2:get_time"]; !exists || enabled {
		t.Error("srv2:get_time should be explicitly denied")
	}
}
