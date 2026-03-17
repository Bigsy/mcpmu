package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/registry"
	"github.com/Bigsy/mcpmu/internal/testutil"
	"github.com/Bigsy/mcpmu/internal/tui/views"
	tea "github.com/charmbracelet/bubbletea"
)

// newTestModel creates a Model with minimal dependencies for testing.
func newTestModel(t *testing.T) Model {
	t.Helper()
	testutil.SetupTestHome(t)

	cfg := config.NewConfig()
	bus := events.NewBus()
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: "file",
	})

	return NewModel(cfg, supervisor, bus, "", nil)
}

// updateModel is a helper that calls Update and returns the Model (with type assertion).
// Note: Update returns the same Model type (value receiver), so we just type assert.
func updateModel(m Model, msg tea.Msg) (Model, tea.Cmd) {
	newModel, cmd := m.Update(msg)
	// The return is tea.Model which can be Model or *Model depending on implementation
	switch v := newModel.(type) {
	case Model:
		return v, cmd
	case *Model:
		return *v, cmd
	default:
		panic("unexpected type from Update")
	}
}

func TestModel_TabSwitching(t *testing.T) {
	m := newTestModel(t)

	// Initial state should be Servers tab
	if m.activeTab != TabServers {
		t.Errorf("expected initial tab to be Servers, got %v", m.activeTab)
	}

	// Press '1' should stay on Servers
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if m.activeTab != TabServers {
		t.Errorf("expected tab to be Servers after '1', got %v", m.activeTab)
	}

	// Tab2 (Namespaces) is enabled
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if m.activeTab != TabNamespaces {
		t.Errorf("expected tab to be Namespaces after '2', got %v", m.activeTab)
	}

	// Press '1' to go back to Servers
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if m.activeTab != TabServers {
		t.Errorf("expected tab to be Servers after '1', got %v", m.activeTab)
	}
}

func TestModel_TabCyclesWithTabAndShiftTab(t *testing.T) {
	m := newTestModel(t)

	// tab: Servers -> Namespaces -> Servers
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.activeTab != TabNamespaces {
		t.Errorf("expected tab to be Namespaces after Tab, got %v", m.activeTab)
	}

	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.activeTab != TabServers {
		t.Errorf("expected tab to be Servers after Tab, got %v", m.activeTab)
	}

	// shift+tab: Servers -> Namespaces
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.activeTab != TabNamespaces {
		t.Errorf("expected tab to be Namespaces after Shift+Tab, got %v", m.activeTab)
	}
}

func TestModel_QuitKey(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Press 'q' with no running servers should quit
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	if cmd == nil {
		t.Fatal("expected quit command, got nil")
	}

	// Execute the command to get the message
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestModel_CtrlC_AlwaysQuits(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Ctrl+C should always quit, even with running servers
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd == nil {
		t.Fatal("expected quit command, got nil")
	}

	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", msg)
	}
}

func TestModel_HelpToggle(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Help should be hidden initially
	if m.helpOverlay.IsVisible() {
		t.Error("expected help to be hidden initially")
	}

	// Press '?' to show help
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.helpOverlay.IsVisible() {
		t.Error("expected help to be visible after '?'")
	}

	// Press '?' again to hide help
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.helpOverlay.IsVisible() {
		t.Error("expected help to be hidden after second '?'")
	}
}

func TestModel_HelpEscapeCloses(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Show help
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.helpOverlay.IsVisible() {
		t.Fatal("expected help to be visible")
	}

	// Press Escape to close help
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.helpOverlay.IsVisible() {
		t.Error("expected help to be hidden after Escape")
	}
}

func TestModel_ToggleLogs(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Logs should be hidden initially
	if m.logPanel.IsVisible() {
		t.Error("expected logs to be hidden initially")
	}

	// Press 'l' to show logs
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if !m.logPanel.IsVisible() {
		t.Error("expected logs to be visible after 'l'")
	}

	// Press 'l' again to hide logs
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if m.logPanel.IsVisible() {
		t.Error("expected logs to be hidden after second 'l'")
	}
}

func TestModel_EscapeFromDetail(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24
	m.currentView = ViewDetail

	// Press Escape to go back to list
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.currentView != ViewList {
		t.Errorf("expected ViewList after Escape, got %v", m.currentView)
	}
}

func TestModel_WindowResize(t *testing.T) {
	m := newTestModel(t)

	// Initial size should be 0
	if m.width != 0 || m.height != 0 {
		t.Errorf("expected initial size 0x0, got %dx%d", m.width, m.height)
	}

	// Send resize message
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("expected size 120x40, got %dx%d", m.width, m.height)
	}
}

func TestModel_ConfirmDialogKeys(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24
	m.showConfirm = true
	m.confirmMessage = "Test message"

	// Press 'n' to close without action
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.showConfirm {
		t.Error("expected confirm dialog to be closed after 'n'")
	}

	// Show it again
	m.showConfirm = true

	// Press Escape to close
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.showConfirm {
		t.Error("expected confirm dialog to be closed after Escape")
	}
}

func TestModel_ViewList_InitialState(t *testing.T) {
	m := newTestModel(t)

	if m.currentView != ViewList {
		t.Errorf("expected initial view to be ViewList, got %v", m.currentView)
	}

	if m.activeTab != TabServers {
		t.Errorf("expected initial tab to be TabServers, got %v", m.activeTab)
	}
}

// TestHelperProcess is the entry point for the fake MCP server subprocess.
func TestHelperProcess(t *testing.T) {
	mcptest.RunHelperProcess(t)
}

func TestModel_TestKeyInDetailStartsServer(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	collector := testutil.NewEventCollector()
	m.bus.Subscribe(collector.Handler)

	serverName := "test-server"
	srv := fakeServerConfig(t, mcptest.DefaultConfig())
	m.cfg.Servers[serverName] = srv
	m.refreshServerList()
	m.currentView = ViewDetail

	t.Cleanup(func() {
		m.supervisor.StopAll()
	})

	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	if ok := collector.WaitForState(serverName, events.StateRunning, 2*time.Second); !ok {
		t.Fatal("expected server to reach running state after pressing 't' in detail view")
	}
}

func fakeServerConfig(t *testing.T, fakeCfg mcptest.FakeServerConfig) config.ServerConfig {
	t.Helper()

	cfgJSON, err := json.Marshal(fakeCfg)
	if err != nil {
		t.Fatalf("marshal fake config: %v", err)
	}

	return config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"FAKE_MCP_CFG":           string(cfgJSON),
		},
	}
}

// ============================================================================
// Namespace Tests
// ============================================================================

func TestModel_NamespaceTab_AddKey(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Switch to Namespaces tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if m.activeTab != TabNamespaces {
		t.Fatal("expected Namespaces tab")
	}

	// Press 'a' to add - should show form
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !m.namespaceForm.IsVisible() {
		t.Error("expected namespace form to be visible after 'a'")
	}
}

func TestModel_NamespaceTab_EnterOpensDetail(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a namespace
	nsName := "test-namespace"
	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err := m.cfg.AddNamespace(nsName, ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshNamespaceList()

	// Switch to Namespaces tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})

	// Press Enter to view detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.currentView != ViewDetail {
		t.Errorf("expected ViewDetail, got %v", m.currentView)
	}
	if m.detailNamespaceID != nsName {
		t.Errorf("expected detailNamespaceID %q, got %q", nsName, m.detailNamespaceID)
	}
}

func TestModel_NamespaceTab_EscapeFromDetail(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a namespace and go to detail
	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err := m.cfg.AddNamespace("test-namespace", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshNamespaceList()

	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.currentView != ViewDetail {
		t.Fatal("expected to be in detail view")
	}

	// Press Escape to go back
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.currentView != ViewList {
		t.Errorf("expected ViewList after Escape, got %v", m.currentView)
	}
	if m.detailNamespaceID != "" {
		t.Errorf("expected detailNamespaceID to be cleared, got %q", m.detailNamespaceID)
	}
}

func TestModel_NamespaceTab_SetDefault(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a namespace
	nsName := "Test Namespace"
	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err := m.cfg.AddNamespace(nsName, ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshNamespaceList()

	// Switch to Namespaces tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})

	// Press 'D' to set as default
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	if m.cfg.DefaultNamespace != nsName {
		t.Errorf("expected default namespace %q, got %q", nsName, m.cfg.DefaultNamespace)
	}
}

func TestModel_NamespaceDetail_ServerPicker(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a server and namespace
	srv := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	err := m.cfg.AddServer("Server 1", srv)
	if err != nil {
		t.Fatalf("failed to add server: %v", err)
	}

	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err = m.cfg.AddNamespace("Test Namespace", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshServerList()
	m.refreshNamespaceList()

	// Go to namespace detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.currentView != ViewDetail {
		t.Fatal("expected to be in detail view")
	}

	// Press 's' to open server picker
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !m.serverPicker.IsVisible() {
		t.Error("expected server picker to be visible after 's'")
	}
}

func TestModel_RefreshServerList_IncludesNamespaces(t *testing.T) {
	m := newTestModel(t)

	// Add servers
	srv1 := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test1"}
	srv2 := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test2"}
	_ = m.cfg.AddServer("Server 1", srv1)
	_ = m.cfg.AddServer("Server 2", srv2)

	// Add namespace with srv1 assigned
	ns := config.NamespaceConfig{ServerIDs: []string{"Server 1"}}
	err := m.cfg.AddNamespace("Production", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}

	m.refreshServerList()

	// Check that we have items
	item := m.serverList.SelectedItem()
	if item == nil {
		t.Fatal("expected at least one item in server list")
	}

	// The first server should have namespace info
	if item.Name == "Server 1" {
		if len(item.Namespaces) != 1 || item.Namespaces[0] != "Production" {
			t.Errorf("expected server to have namespace 'Production', got %v", item.Namespaces)
		}
	}
}

func TestModel_HandleNamespaceFormResult_Add(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	result := views.NamespaceFormResult{
		Name: "New Namespace",
		Namespace: config.NamespaceConfig{
			Description: "A new namespace",
		},
		Submitted: true,
		IsEdit:    false,
	}

	m, _ = updateModel(m, result)

	// Check namespace was added
	if len(m.cfg.Namespaces) != 1 {
		t.Errorf("expected 1 namespace, got %d", len(m.cfg.Namespaces))
	}
	ns, ok := m.cfg.GetNamespace("New Namespace")
	if !ok {
		t.Fatal("expected namespace 'New Namespace' to exist")
	}
	if ns.Description != "A new namespace" {
		t.Errorf("expected description 'A new namespace', got %q", ns.Description)
	}
}

func TestModel_HandleNamespaceFormResult_Cancelled(t *testing.T) {
	m := newTestModel(t)

	result := views.NamespaceFormResult{
		Submitted: false,
	}

	m, _ = updateModel(m, result)

	// Check no namespace was added
	if len(m.cfg.Namespaces) != 0 {
		t.Errorf("expected 0 namespaces, got %d", len(m.cfg.Namespaces))
	}
}

func TestModel_HandleServerPickerResult(t *testing.T) {
	m := newTestModel(t)

	// Setup: add servers and namespace
	srv1 := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	srv2 := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	_ = m.cfg.AddServer("Server 1", srv1)
	_ = m.cfg.AddServer("Server 2", srv2)

	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err := m.cfg.AddNamespace("Test", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}

	m.detailNamespaceID = "Test"

	result := views.ServerPickerResult{
		SelectedIDs: []string{"Server 1", "Server 2"},
		Submitted:   true,
	}

	m, _ = updateModel(m, result)

	// Check servers were assigned
	nsAfter, ok := m.cfg.GetNamespace("Test")
	if !ok {
		t.Fatal("expected namespace to exist")
	}
	if len(nsAfter.ServerIDs) != 2 {
		t.Errorf("expected 2 servers assigned, got %d", len(nsAfter.ServerIDs))
	}
}

func TestModel_HandleToolPermissionsResult(t *testing.T) {
	m := newTestModel(t)

	// Setup: add server and namespace
	srv := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	_ = m.cfg.AddServer("Server 1", srv)

	ns := config.NamespaceConfig{ServerIDs: []string{"Server 1"}}
	err := m.cfg.AddNamespace("Test", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}

	m.detailNamespaceID = "Test"

	result := views.ToolPermissionsResult{
		Changes: map[string]bool{
			"Server 1:read_file":  true,
			"Server 1:write_file": false,
		},
		Submitted: true,
	}

	m, _ = updateModel(m, result)

	// Check permissions were set
	perms := m.cfg.GetToolPermissionsForNamespace("Test")
	if len(perms) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(perms))
	}

	// Check read_file is enabled
	readEnabled, found := m.cfg.GetToolPermission("Test", "Server 1", "read_file")
	if !found || !readEnabled {
		t.Error("expected read_file to be enabled")
	}

	// Check write_file is disabled
	writeEnabled, found := m.cfg.GetToolPermission("Test", "Server 1", "write_file")
	if !found || writeEnabled {
		t.Error("expected write_file to be disabled")
	}
}

func TestModel_ServerPicker_EnterClosesPicker(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a server and namespace
	srv := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	err := m.cfg.AddServer("Server 1", srv)
	if err != nil {
		t.Fatalf("failed to add server: %v", err)
	}

	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err = m.cfg.AddNamespace("Test Namespace", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshServerList()
	m.refreshNamespaceList()

	// Go to namespace detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.currentView != ViewDetail {
		t.Fatal("expected to be in detail view")
	}

	// Press 's' to open server picker
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !m.serverPicker.IsVisible() {
		t.Fatal("expected server picker to be visible")
	}

	// Press Enter to confirm (even with no changes)
	m, cmd := updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Picker should be hidden after Enter
	if m.serverPicker.IsVisible() {
		t.Error("expected server picker to be hidden after Enter")
	}

	// Execute returned command to get result message
	if cmd != nil {
		msg := cmd()
		// Process the result
		m, _ = updateModel(m, msg)
	}
}

func TestModel_ServerPicker_EscClosesPicker(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a server and namespace
	srv := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	err := m.cfg.AddServer("Server 1", srv)
	if err != nil {
		t.Fatalf("failed to add server: %v", err)
	}

	ns := config.NamespaceConfig{ServerIDs: []string{}}
	err = m.cfg.AddNamespace("Test Namespace", ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshServerList()
	m.refreshNamespaceList()

	// Go to namespace detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press 's' to open server picker
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !m.serverPicker.IsVisible() {
		t.Fatal("expected server picker to be visible")
	}

	// Press Escape to cancel
	m, cmd := updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})

	// Picker should be hidden after Escape
	if m.serverPicker.IsVisible() {
		t.Error("expected server picker to be hidden after Escape")
	}

	// Execute returned command
	if cmd != nil {
		msg := cmd()
		m, _ = updateModel(m, msg)
	}
}

// newTestModelWithCredStore creates a Model with a file-backed credential store for OAuth tests.
func newTestModelWithCredStore(t *testing.T) Model {
	t.Helper()
	testutil.SetupTestHome(t)

	cfg := config.NewConfig()
	bus := events.NewBus()
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: "file",
	})

	m := NewModel(cfg, supervisor, bus, "", nil)
	m.width = 80
	m.height = 24
	return m
}

func TestModel_LoginKey_OAuthHTTPServer_NeedsAuth(t *testing.T) {
	m := newTestModelWithCredStore(t)

	// Add an OAuth HTTP server (no bearer token)
	srv := config.ServerConfig{
		Kind: config.ServerKindStreamableHTTP,
		URL:  "https://mcp.example.com/v1",
	}
	_ = m.cfg.AddServer("oauth-server", srv)
	// Set server status to needs-auth so L triggers the login flow
	m.serverStatuses["oauth-server"] = events.ServerStatus{State: events.StateNeedsAuth}
	m.refreshServerList()

	// Navigate to detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.currentView != ViewDetail {
		t.Fatal("expected detail view")
	}

	// Press L — should show info toast (browser opening)
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible")
	}
	view := testutil.StripANSI(m.View())
	if strings.Contains(view, "only applies to") || strings.Contains(view, "not awaiting") {
		t.Errorf("L on needs-auth OAuth server should show info toast, got: %s", view)
	}
}

func TestModel_LoginKey_OAuthHTTPServer_NotNeedsAuth(t *testing.T) {
	m := newTestModelWithCredStore(t)

	// Add an OAuth HTTP server (no bearer token), not in needs-auth state
	srv := config.ServerConfig{
		Kind: config.ServerKindStreamableHTTP,
		URL:  "https://mcp.example.com/v1",
	}
	_ = m.cfg.AddServer("oauth-server", srv)
	m.refreshServerList()

	// Navigate to detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press L — should show error about not awaiting auth
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible")
	}
	view := testutil.StripANSI(m.View())
	// Toast may be truncated in the status bar; check that it's an error (not info/success)
	// and not the "only applies to" error (which would mean the OAuth check failed)
	if strings.Contains(view, "only applies to") {
		t.Error("L on OAuth HTTP server should not show 'only applies to' error")
	}
	if strings.Contains(view, "Opening browser") {
		t.Error("L on non-needs-auth server should not trigger login flow")
	}
}

func TestModel_LogoutKey_OAuthHTTPServer_DetailView(t *testing.T) {
	m := newTestModelWithCredStore(t)

	// Add an OAuth HTTP server
	srv := config.ServerConfig{
		Kind: config.ServerKindStreamableHTTP,
		URL:  "https://mcp.example.com/v1",
	}
	_ = m.cfg.AddServer("oauth-server", srv)
	m.refreshServerList()

	// Navigate to detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press O — should succeed (show success toast)
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible")
	}
	view := testutil.StripANSI(m.View())
	if !strings.Contains(view, "Logged") {
		t.Errorf("expected 'Logged out' toast, got: %s", view)
	}
}

func TestModel_LoginKey_StdioServer_RejectsWithError(t *testing.T) {
	m := newTestModelWithCredStore(t)

	// Add a stdio server
	srv := config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "echo",
	}
	_ = m.cfg.AddServer("stdio-server", srv)
	m.refreshServerList()

	// Navigate to detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press L — should show error toast
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible")
	}
	view := testutil.StripANSI(m.View())
	if !strings.Contains(view, "OAuth login only applies") && !strings.Contains(view, "OAuth logout only applies") {
		t.Errorf("expected error toast for stdio server login, got: %s", view)
	}
}

func TestModel_LogoutKey_StdioServer_RejectsWithError(t *testing.T) {
	m := newTestModelWithCredStore(t)

	// Add a stdio server
	srv := config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "echo",
	}
	_ = m.cfg.AddServer("stdio-server", srv)
	m.refreshServerList()

	// Navigate to detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press O — should show error toast
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible")
	}
	view := testutil.StripANSI(m.View())
	if !strings.Contains(view, "OAuth login only applies") && !strings.Contains(view, "OAuth logout only applies") {
		t.Errorf("expected error toast for stdio server logout, got: %s", view)
	}
}

func TestModel_LoginLogout_BearerTokenServer_RejectsWithError(t *testing.T) {
	m := newTestModelWithCredStore(t)

	// Add an HTTP server with bearer token (not OAuth)
	srv := config.ServerConfig{
		Kind:              config.ServerKindStreamableHTTP,
		URL:               "https://mcp.example.com/v1",
		BearerTokenEnvVar: "MY_TOKEN",
	}
	_ = m.cfg.AddServer("bearer-server", srv)
	m.refreshServerList()

	// Navigate to detail
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press L — should show error toast
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible for login")
	}
	view := testutil.StripANSI(m.View())
	if !strings.Contains(view, "OAuth login only applies") && !strings.Contains(view, "OAuth logout only applies") {
		t.Errorf("expected error toast for bearer token server login, got: %s", view)
	}

	// Dismiss toast by pressing any key, then press O
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	// The O key first dismisses the toast (line 286-288), then is handled as a key.
	// But the handler also sets a new toast, so it should be visible again.
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible for logout")
	}
	view = testutil.StripANSI(m.View())
	if !strings.Contains(view, "OAuth login only applies") && !strings.Contains(view, "OAuth logout only applies") {
		t.Errorf("expected error toast for bearer token server logout, got: %s", view)
	}
}

func TestModel_LogoutOAuth_ServerNotFound(t *testing.T) {
	m := newTestModelWithCredStore(t)

	err := m.logoutOAuth("nonexistent-server")
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
	if !strings.Contains(err.Error(), "server not found") {
		t.Errorf("expected 'server not found' error, got: %v", err)
	}
}

func TestModel_LogoutKey_ErrorToast_OnFailure(t *testing.T) {
	m := newTestModelWithCredStore(t)
	// Use wider terminal so the error toast isn't truncated beyond recognition
	m.width = 160
	m.height = 24

	// Add an OAuth HTTP server
	srv := config.ServerConfig{
		Kind: config.ServerKindStreamableHTTP,
		URL:  "https://mcp.example.com/v1",
	}
	_ = m.cfg.AddServer("oauth-server", srv)
	m.refreshServerList()

	// Remove the server from config AFTER refreshing the list. The list view's
	// cached item still passes the OAuth-eligibility check, but logoutOAuth
	// fails because GetServer returns not-found.
	delete(m.cfg.Servers, "oauth-server")

	// Press O in list view — logoutOAuth returns "server not found", handler shows error toast
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'O'}})
	if !m.toast.IsVisible() {
		t.Fatal("expected toast to be visible")
	}
	view := testutil.StripANSI(m.View())
	if !strings.Contains(view, "OAuth logout failed") {
		t.Errorf("expected 'OAuth logout failed' error toast, got: %s", view)
	}
}

// newTestModelWithToolCache creates a Model with a real ToolCache backed by a temp dir.
func newTestModelWithToolCache(t *testing.T) Model {
	t.Helper()
	testutil.SetupTestHome(t)

	dir := t.TempDir()
	configPath := dir + "/config.json"

	cfg := config.NewConfig()
	bus := events.NewBus()
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode: "file",
	})

	tc, err := config.NewToolCache(configPath)
	if err != nil {
		t.Fatalf("NewToolCache: %v", err)
	}

	m := NewModel(cfg, supervisor, bus, configPath, tc)
	m.width = 80
	m.height = 24
	return m
}

func TestGetServerToolsForDetail_EmptyLiveToolsNotFallingBackToCache(t *testing.T) {
	m := newTestModelWithToolCache(t)

	// Add a server
	_ = m.cfg.AddServer("srv", config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "echo",
	})

	// Populate cache with stale tools (simulating a previous run)
	_ = m.toolCache.Update("srv", []config.CachedToolInput{
		{Name: "old_tool", Description: "stale tool from prior run"},
	})

	// Simulate the server reporting zero tools (empty but present in map)
	m.serverTools["srv"] = []events.McpTool{}

	tools, _, fromCache := m.getServerToolsForDetail("srv")

	if fromCache {
		t.Error("expected fromCache=false when live tools are present (even if empty)")
	}
	if len(tools) != 0 {
		t.Errorf("expected 0 live tools, got %d — stale cache leaked through", len(tools))
	}
}

func TestGetServerToolsForDetail_FallsBackToCacheWhenNoLiveData(t *testing.T) {
	m := newTestModelWithToolCache(t)

	// Add a server
	_ = m.cfg.AddServer("srv", config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "echo",
	})

	// Populate cache (server not running, no live data)
	_ = m.toolCache.Update("srv", []config.CachedToolInput{
		{Name: "cached_tool", Description: "from cache"},
	})

	// serverTools has no entry for "srv" at all (never started this session)
	tools, toolTokens, fromCache := m.getServerToolsForDetail("srv")

	if !fromCache {
		t.Error("expected fromCache=true when no live data exists")
	}
	if len(tools) != 1 || tools[0].Name != "cached_tool" {
		t.Errorf("expected 1 cached tool 'cached_tool', got %v", tools)
	}
	if toolTokens["cached_tool"] <= 0 {
		t.Errorf("expected positive token count for cached tool, got %d", toolTokens["cached_tool"])
	}
}

// ============================================================================
// Registry browser integration tests
// ============================================================================

func TestModel_AddKeyOpensAddMethodSelector(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Press 'a' on server list tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !m.addMethod.IsVisible() {
		t.Error("expected add method selector to be visible after 'a' key")
	}
}

func TestModel_AddMethodManualOpensServerForm(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Send AddMethodResult for manual
	m, _ = updateModel(m, views.AddMethodResult{Method: "manual", Submitted: true})
	if !m.serverForm.IsVisible() {
		t.Error("expected server form to be visible after manual selection")
	}
}

func TestModel_AddMethodRegistryOpensBrowser(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Send AddMethodResult for registry
	m, _ = updateModel(m, views.AddMethodResult{Method: "registry", Submitted: true})
	if !m.registryBrowser.IsVisible() {
		t.Error("expected registry browser to be visible after registry selection")
	}
}

func TestModel_AddMethodCancelDoesNothing(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Send cancelled AddMethodResult
	m, _ = updateModel(m, views.AddMethodResult{Submitted: false})
	if m.serverForm.IsVisible() {
		t.Error("expected server form NOT to be visible after cancel")
	}
	if m.registryBrowser.IsVisible() {
		t.Error("expected registry browser NOT to be visible after cancel")
	}
}

func TestModel_AddKeyOnNamespaceTabStillOpensNamespaceForm(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Switch to namespaces tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})

	// Press 'a' on namespaces tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.addMethod.IsVisible() {
		t.Error("expected add method selector NOT to be visible on namespaces tab")
	}
	if !m.namespaceForm.IsVisible() {
		t.Error("expected namespace form to be visible after 'a' on namespaces tab")
	}
}

func TestModel_RegistryBrowserResult_SubmittedSetsPendingInstall(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Open browser
	m.registryBrowser.Show()
	if !m.registryBrowser.IsVisible() {
		t.Fatal("precondition: browser should be visible")
	}

	// Simulate browser result
	m, _ = updateModel(m, views.RegistryBrowserResult{
		Submitted: true,
		Spec: registry.InstallSpec{
			Name:         "brave-search",
			CommandOrURL: "npx",
			Args:         "-y @brave/brave-search-mcp-server",
			Env:          map[string]string{"BRAVE_API_KEY": "<your-BRAVE_API_KEY>"},
		},
	})

	if m.registryBrowser.IsVisible() {
		t.Error("expected browser to be hidden after result")
	}

	// Next update should trigger the pending install and open the server form
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}}) // any msg triggers pending
	if !m.serverForm.IsVisible() {
		t.Error("expected server form to be visible after pending registry install")
	}
}

func TestModel_RegistryBrowserResult_NotSubmittedJustCloses(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m.registryBrowser.Show()

	m, _ = updateModel(m, views.RegistryBrowserResult{Submitted: false})

	if m.registryBrowser.IsVisible() {
		t.Error("expected browser to be hidden after cancel result")
	}
	if m.serverForm.IsVisible() {
		t.Error("expected server form NOT to be visible after cancel")
	}
}

func TestModel_RegistryBrowserResult_EmptySpecShowsToast(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m.registryBrowser.Show()

	// Submit with empty CommandOrURL (unsupported server)
	m, _ = updateModel(m, views.RegistryBrowserResult{
		Submitted: true,
		Spec:      registry.InstallSpec{}, // empty CommandOrURL
	})

	// Process pending install
	m, cmd := updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}})
	if m.serverForm.IsVisible() {
		t.Error("expected server form NOT to be visible for empty spec")
	}
	// The toast error command should have been returned
	if cmd != nil {
		msg := cmd()
		// Just verify it produced a message (toast message)
		if msg == nil {
			t.Error("expected toast message from empty spec")
		}
	}
}

func TestModel_RegistryBrowserOverlayRendersInView(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m.registryBrowser.Show()
	m.registryBrowser.SetSize(120, 40)

	view := m.View()
	stripped := testutil.StripANSI(view)
	if !strings.Contains(stripped, "Install from Registry") {
		t.Error("expected 'Install from Registry' in view when browser is visible")
	}
}

func TestModel_StatusBarShowsAddHint(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	view := m.View()
	stripped := testutil.StripANSI(view)
	if !strings.Contains(stripped, "a:add") {
		t.Error("expected 'a:add' in status bar on server list tab")
	}
	if strings.Contains(stripped, "i:install") {
		t.Error("expected 'i:install' NOT in status bar")
	}
}

// TestModel_RegistryBrowserInstallE2E exercises the real key→cmd→msg cycle
// that goes through the browser component, producing a deferred result.
// This catches the bug where the browser hides itself before the result cmd
// is executed, so the result arrives when browser.IsVisible() is false.
func TestModel_RegistryBrowserInstallE2E(t *testing.T) {
	m := newTestModel(t)
	m.width = 120
	m.height = 40
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Open browser and populate with search results
	m.registryBrowser.Show()
	m.registryBrowser.SetSize(120, 40)

	// Inject search results via test helper
	m.registryBrowser.SetTestServers("brave", []registry.Server{
		{
			Name:        "io.github.brave/brave-search-mcp-server",
			Title:       "Brave Search MCP Server",
			Description: "Web search",
			Version:     "2.0.75",
			Packages: []registry.Package{
				{
					RegistryType: "npm",
					Identifier:   "@brave/brave-search-mcp-server",
					Version:      "2.0.75",
					RuntimeHint:  "npx",
					Transport:    registry.Transport{Type: "stdio"},
					EnvironmentVariables: []registry.EnvironmentVar{
						{Name: "BRAVE_API_KEY", IsRequired: true, IsSecret: true},
					},
				},
			},
		},
	})

	// Press Enter to go to detail view
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Press Enter again to install — this produces a cmd (not a direct message)
	m, cmd := updateModel(m, tea.KeyMsg{Type: tea.KeyEnter})

	// Browser should have hidden itself
	if m.registryBrowser.IsVisible() {
		t.Error("expected browser hidden after install Enter")
	}

	// Execute the returned command to get the RegistryBrowserResult
	if cmd == nil {
		t.Fatal("expected a command from install Enter")
	}
	msg := cmd()
	result, ok := msg.(views.RegistryBrowserResult)
	if !ok {
		t.Fatalf("expected RegistryBrowserResult from cmd, got %T", msg)
	}
	if !result.Submitted {
		t.Error("expected Submitted=true")
	}

	// Feed the result back to the model (this is what Bubble Tea does)
	m, _ = updateModel(m, result)

	// Now on next tick, pending install should trigger the form
	m, _ = updateModel(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	if !m.serverForm.IsVisible() {
		t.Error("expected server form to open from registry install E2E flow")
	}
}

func TestModel_ServerDefaults_AffectsTokenCounts(t *testing.T) {
	m := newTestModelWithToolCache(t)

	// Add servers
	srv1 := config.ServerConfig{Kind: config.ServerKindStdio, Command: "echo"}
	_ = m.cfg.AddServer("srv1", srv1)
	srv2 := config.ServerConfig{Kind: config.ServerKindStdio, Command: "echo"}
	_ = m.cfg.AddServer("srv2", srv2)

	// Add namespace with server default deny for srv1
	ns := config.NamespaceConfig{
		ServerIDs:      []string{"srv1", "srv2"},
		ServerDefaults: map[string]bool{"srv1": true}, // deny by default
	}
	_ = m.cfg.AddNamespace("test-ns", ns)

	// Explicitly allow one srv1 tool
	_ = m.cfg.SetToolPermission("test-ns", "srv1", "read_file", true)

	// Populate tool cache for both servers
	_ = m.toolCache.Update("srv1", []config.CachedToolInput{
		{Name: "read_file", Description: "Read a file"},
		{Name: "write_file", Description: "Write a file"},
		{Name: "delete_file", Description: "Delete a file"},
	})
	_ = m.toolCache.Update("srv2", []config.CachedToolInput{
		{Name: "get_time", Description: "Get time"},
		{Name: "set_tz", Description: "Set timezone"},
	})

	// Get token counts
	serverTokens := m.getServerTokensForNamespace("test-ns")

	// srv1: only read_file should be counted (explicitly allowed; others denied by server default)
	srv1Tokens := serverTokens["srv1"]
	// srv2: all tools should be counted (namespace allows by default, no server override)
	srv2Tokens := serverTokens["srv2"]

	// srv1 should have fewer tokens than srv2 proportionally
	// (1 tool vs 2 tools, with srv1 having 3 tools total but 2 denied)
	if srv1Tokens <= 0 {
		t.Error("expected srv1 to have some tokens (read_file allowed)")
	}
	if srv2Tokens <= 0 {
		t.Error("expected srv2 to have some tokens (all allowed)")
	}

	// The key assertion: srv1 should have less than if all 3 tools were counted
	// Get what srv1 tokens would be with NO server default (all allowed)
	delete(ns.ServerDefaults, "srv1")
	m.cfg.Namespaces["test-ns"] = ns
	_ = m.cfg.UnsetToolPermission("test-ns", "srv1", "read_file") // remove explicit allow too

	allAllowedTokens := m.getServerTokensForNamespace("test-ns")
	if srv1Tokens >= allAllowedTokens["srv1"] {
		t.Errorf("expected srv1 tokens with server default deny (%d) to be less than all allowed (%d)",
			srv1Tokens, allAllowedTokens["srv1"])
	}
}

func TestModel_PermissionEditor_WithServerDefaults(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Setup: add server and namespace with server defaults
	srv := config.ServerConfig{Kind: config.ServerKindStdio, Command: "test"}
	_ = m.cfg.AddServer("srv1", srv)
	_ = m.cfg.AddServer("srv2", srv)

	ns := config.NamespaceConfig{
		ServerIDs:      []string{"srv1", "srv2"},
		ServerDefaults: map[string]bool{"srv1": true}, // srv1 denies by default
	}
	_ = m.cfg.AddNamespace("test-ns", ns)

	m.detailNamespaceID = "test-ns"

	// Simulate servers running with tools available
	m.serverStatuses = map[string]events.ServerStatus{
		"srv1": {State: events.StateRunning},
		"srv2": {State: events.StateRunning},
	}
	m.serverTools = map[string][]events.McpTool{
		"srv1": {
			{Name: "read_file", Description: "Read"},
			{Name: "write_file", Description: "Write"},
		},
		"srv2": {
			{Name: "get_time", Description: "Get time"},
		},
	}

	// Call startToolPermissionEditor
	nsRef := ns
	handled, _, _ := m.startToolPermissionEditor("test-ns", &nsRef)
	if !handled {
		t.Fatal("expected startToolPermissionEditor to handle the request")
	}

	if !m.toolPerms.IsVisible() {
		t.Fatal("expected tool permissions editor to be visible")
	}

	// Size the editor so the list renders items
	m.toolPerms.SetSize(80, 40)

	// Verify the permission editor received server defaults by checking rendered output.
	// The overlay should contain "(server default)" for srv1 tools and the
	// server policy footer line.
	overlay := m.toolPerms.RenderOverlay("base", 80, 40)
	if overlay == "base" {
		t.Fatal("expected overlay to be rendered (not just base)")
	}

	// The overlay should show the server policy line when a srv1 tool is selected
	if !strings.Contains(overlay, "server") {
		t.Error("expected overlay to reference server defaults for srv1 tools")
	}

	// The overlay itself renders a list with checkbox markers.
	// Check the overlay (which handles its own sizing) for the checkbox markers.
	if !strings.Contains(overlay, "[-]") {
		t.Errorf("expected [-] (denied) markers in overlay for srv1 tools with server default deny, got: %q", overlay)
	}
	if !strings.Contains(overlay, "[+]") {
		t.Errorf("expected [+] (allowed) markers in overlay for srv2 tools with namespace default allow, got: %q", overlay)
	}
}

func TestFormatEnvMap(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nil", nil, ""},
		{"empty", map[string]string{}, ""},
		{"single", map[string]string{"KEY": "val"}, "KEY=val"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatEnvMap(tt.env)
			if tt.want != "" && got != tt.want {
				t.Errorf("formatEnvMap() = %q, want %q", got, tt.want)
			}
			if tt.want == "" && got != "" {
				t.Errorf("formatEnvMap() = %q, want empty", got)
			}
		})
	}
}
