package tui

import (
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
	tea "github.com/charmbracelet/bubbletea"
)

// newTestModelWithSize creates a Model with dimensions set for view testing.
func newTestModelWithSize(t *testing.T, width, height int) Model {
	t.Helper()
	testutil.SetupTestHome(t)

	cfg := config.NewConfig()
	bus := events.NewBus()
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: "file",
	})

	m := NewModel(cfg, supervisor, bus, "")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return newModel.(Model)
}

func TestView_Loading(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := config.NewConfig()
	bus := events.NewBus()
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: "file",
	})
	m := NewModel(cfg, supervisor, bus, "")

	// Before WindowSizeMsg, width/height are 0
	view := m.View()
	if view != "Loading..." {
		t.Errorf("expected 'Loading...' before resize, got %q", view)
	}
}

func TestView_ContainsTabBar(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	view := testutil.StripANSI(m.View())

	// Should contain all tab labels
	if !strings.Contains(view, "[1]Servers") {
		t.Error("expected view to contain '[1]Servers'")
	}
	if !strings.Contains(view, "[2]Namespaces") {
		t.Error("expected view to contain '[2]Namespaces'")
	}
}

func TestView_ContainsTitle(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	view := testutil.StripANSI(m.View())

	if !strings.Contains(view, "mcpmu") {
		t.Error("expected view to contain 'mcpmu' title")
	}
}

func TestView_ContainsStatusBar(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	view := testutil.StripANSI(m.View())

	// Status bar should show running count
	if !strings.Contains(view, "0/0 servers running") {
		t.Error("expected view to contain '0/0 servers running'")
	}

	// Status bar should show key hints
	if !strings.Contains(view, "?:help") {
		t.Error("expected view to contain '?:help'")
	}
}

func TestView_StatusBarKeyHints_ListContext(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)
	m.currentView = ViewList

	view := testutil.StripANSI(m.View())

	// List view should show keybindings
	if !strings.Contains(view, "t:test") {
		t.Error("expected list view to show 't:test'")
	}
	if !strings.Contains(view, "E:enable") {
		t.Error("expected list view to show 'E:enable'")
	}
	if !strings.Contains(view, "a:add") {
		t.Error("expected list view to show 'a:add'")
	}
	if !strings.Contains(view, "e:edit") {
		t.Error("expected list view to show 'e:edit'")
	}
	if !strings.Contains(view, "d:delete") {
		t.Error("expected list view to show 'd:delete'")
	}
	if !strings.Contains(view, "l:logs") {
		t.Error("expected list view to show 'l:logs'")
	}
}

func TestView_StatusBarKeyHints_DetailContext(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)
	m.currentView = ViewDetail

	view := testutil.StripANSI(m.View())

	// Detail view should show back key
	if !strings.Contains(view, "esc:back") {
		t.Error("expected detail view to show 'esc:back'")
	}
}

func TestView_EmptyServerList(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	view := testutil.StripANSI(m.View())

	// Empty server list should still render without error
	// The bubbles list component shows its own empty message
	if view == "" {
		t.Error("expected view to render even with empty server list")
	}

	// Should still show the title
	if !strings.Contains(view, "Servers") {
		t.Error("expected view to show 'Servers' title when empty")
	}
}

func TestView_WithServers(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := config.NewConfig()
	cfg.Servers["Test Server"] = config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "echo",
	}

	bus := events.NewBus()
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: "file",
	})

	m := NewModel(cfg, supervisor, bus, "")
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newModel.(Model)

	view := testutil.StripANSI(m.View())

	// Should show the server name
	if !strings.Contains(view, "Test Server") {
		t.Error("expected view to contain 'Test Server'")
	}
}

func TestView_ConfirmOverlay(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)
	m.showConfirm = true
	m.confirmMessage = "Test confirm message"

	view := testutil.StripANSI(m.View())

	// Should show the confirm dialog
	if !strings.Contains(view, "Confirm") {
		t.Error("expected view to contain 'Confirm' when dialog is shown")
	}
	if !strings.Contains(view, "Test confirm message") {
		t.Error("expected view to contain the confirm message")
	}
	if !strings.Contains(view, "[y]es") {
		t.Error("expected view to show '[y]es' option")
	}
	if !strings.Contains(view, "[n]o") {
		t.Error("expected view to show '[n]o' option")
	}
}

func TestView_RendersWithSmallTerminal(t *testing.T) {
	// Test that the view doesn't panic with small dimensions
	m := newTestModelWithSize(t, 40, 10)

	view := m.View()

	// Should render something without panicking
	if view == "" {
		t.Error("expected non-empty view even with small terminal")
	}
}

func TestView_RendersWithVeryLargeTerminal(t *testing.T) {
	// Test that the view handles very large dimensions
	m := newTestModelWithSize(t, 300, 100)

	view := m.View()

	if view == "" {
		t.Error("expected non-empty view with large terminal")
	}
}

func TestView_StatusBar_OAuthServerShowsLoginLogout(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	// Add an OAuth HTTP server (no bearer token)
	m.cfg.Servers["oauth-server"] = config.ServerConfig{
		Kind: config.ServerKindStreamableHTTP,
		URL:  "https://mcp.example.com/v1",
	}
	m.refreshServerList()

	// List view
	view := testutil.StripANSI(m.View())
	if !strings.Contains(view, "L:login") {
		t.Error("expected status bar to show 'L:login' for OAuth HTTP server in list view")
	}
	if !strings.Contains(view, "O:logout") {
		t.Error("expected status bar to show 'O:logout' for OAuth HTTP server in list view")
	}

	// Detail view
	m.currentView = ViewDetail
	m.detailServerID = "oauth-server"
	view = testutil.StripANSI(m.View())
	if !strings.Contains(view, "L:login") {
		t.Error("expected status bar to show 'L:login' for OAuth HTTP server in detail view")
	}
	if !strings.Contains(view, "O:logout") {
		t.Error("expected status bar to show 'O:logout' for OAuth HTTP server in detail view")
	}
}

func TestView_StatusBar_StdioServerNoLoginLogout(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	// Add a stdio server
	m.cfg.Servers["stdio-server"] = config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "echo",
	}
	m.refreshServerList()

	view := testutil.StripANSI(m.View())
	if strings.Contains(view, "L:login") {
		t.Error("expected status bar NOT to show 'L:login' for stdio server")
	}
	if strings.Contains(view, "O:logout") {
		t.Error("expected status bar NOT to show 'O:logout' for stdio server")
	}
}

func TestView_StatusBar_BearerTokenServerNoLoginLogout(t *testing.T) {
	m := newTestModelWithSize(t, 120, 40)

	// Add an HTTP server with bearer token
	m.cfg.Servers["bearer-server"] = config.ServerConfig{
		Kind:              config.ServerKindStreamableHTTP,
		URL:               "https://mcp.example.com/v1",
		BearerTokenEnvVar: "MY_TOKEN",
	}
	m.refreshServerList()

	view := testutil.StripANSI(m.View())
	if strings.Contains(view, "L:login") {
		t.Error("expected status bar NOT to show 'L:login' for bearer token server")
	}
	if strings.Contains(view, "O:logout") {
		t.Error("expected status bar NOT to show 'O:logout' for bearer token server")
	}
}
