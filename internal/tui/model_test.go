package tui

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/mcptest"
	"github.com/hedworth/mcp-studio-go/internal/process"
	"github.com/hedworth/mcp-studio-go/internal/testutil"
	"github.com/hedworth/mcp-studio-go/internal/tui/views"
)

// newTestModel creates a Model with minimal dependencies for testing.
func newTestModel(t *testing.T) Model {
	t.Helper()
	testutil.SetupTestHome(t)

	cfg := config.NewConfig()
	bus := events.NewBus()
	supervisor := process.NewSupervisor(bus)

	return NewModel(cfg, supervisor, bus)
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

	// Tab2 (Namespaces) is enabled, Tab3 (Proxies) is still disabled
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if m.activeTab != TabNamespaces {
		t.Errorf("expected tab to be Namespaces after '2', got %v", m.activeTab)
	}

	// Tab3 (Proxies) is disabled, pressing '3' should not change tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if m.activeTab != TabNamespaces {
		t.Errorf("expected tab to stay Namespaces (Proxies disabled), got %v", m.activeTab)
	}

	// Press '1' to go back to Servers
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if m.activeTab != TabServers {
		t.Errorf("expected tab to be Servers after '1', got %v", m.activeTab)
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

	srv := fakeServerConfig(t, "test", mcptest.DefaultConfig())
	m.cfg.Servers[srv.ID] = srv
	m.refreshServerList()
	m.currentView = ViewDetail

	t.Cleanup(func() {
		m.supervisor.StopAll()
	})

	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	if ok := collector.WaitForState(srv.ID, events.StateRunning, 2*time.Second); !ok {
		t.Fatal("expected server to reach running state after pressing 't' in detail view")
	}
}

func fakeServerConfig(t *testing.T, id string, fakeCfg mcptest.FakeServerConfig) config.ServerConfig {
	t.Helper()

	cfgJSON, err := json.Marshal(fakeCfg)
	if err != nil {
		t.Fatalf("marshal fake config: %v", err)
	}

	return config.ServerConfig{
		ID:      id,
		Name:    "test-" + id,
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

	// Add a namespace (ID will be auto-generated)
	ns := config.NamespaceConfig{Name: "Test Namespace", ServerIDs: []string{}}
	nsID, err := m.cfg.AddNamespace(ns)
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
	if m.detailNamespaceID != nsID {
		t.Errorf("expected detailNamespaceID %q, got %q", nsID, m.detailNamespaceID)
	}
}

func TestModel_NamespaceTab_EscapeFromDetail(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a namespace and go to detail
	ns := config.NamespaceConfig{Name: "Test Namespace", ServerIDs: []string{}}
	_, err := m.cfg.AddNamespace(ns)
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
	ns := config.NamespaceConfig{Name: "Test Namespace", ServerIDs: []string{}}
	nsID, err := m.cfg.AddNamespace(ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}
	m.refreshNamespaceList()

	// Switch to Namespaces tab
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})

	// Press 'D' to set as default
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	if m.cfg.DefaultNamespaceID != nsID {
		t.Errorf("expected default namespace %q, got %q", nsID, m.cfg.DefaultNamespaceID)
	}
}

func TestModel_NamespaceDetail_ServerPicker(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24

	// Add a server and namespace
	srv := config.ServerConfig{Name: "Server 1", Kind: config.ServerKindStdio, Command: "test"}
	_, err := m.cfg.AddServer(srv)
	if err != nil {
		t.Fatalf("failed to add server: %v", err)
	}

	ns := config.NamespaceConfig{Name: "Test Namespace", ServerIDs: []string{}}
	_, err = m.cfg.AddNamespace(ns)
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
	srv1 := config.ServerConfig{Name: "Server 1", Kind: config.ServerKindStdio, Command: "test1"}
	srv2 := config.ServerConfig{Name: "Server 2", Kind: config.ServerKindStdio, Command: "test2"}
	srv1ID, _ := m.cfg.AddServer(srv1)
	m.cfg.AddServer(srv2)

	// Add namespace with srv1 assigned
	ns := config.NamespaceConfig{Name: "Production", ServerIDs: []string{srv1ID}}
	_, err := m.cfg.AddNamespace(ns)
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
	if item.Config.ID == srv1ID {
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
		Namespace: config.NamespaceConfig{
			Name:        "New Namespace",
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
	if m.cfg.Namespaces[0].Name != "New Namespace" {
		t.Errorf("expected name 'New Namespace', got %q", m.cfg.Namespaces[0].Name)
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
	srv1 := config.ServerConfig{Name: "Server 1", Kind: config.ServerKindStdio, Command: "test"}
	srv2 := config.ServerConfig{Name: "Server 2", Kind: config.ServerKindStdio, Command: "test"}
	srv1ID, _ := m.cfg.AddServer(srv1)
	srv2ID, _ := m.cfg.AddServer(srv2)

	ns := config.NamespaceConfig{Name: "Test", ServerIDs: []string{}}
	nsID, err := m.cfg.AddNamespace(ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}

	m.detailNamespaceID = nsID

	result := views.ServerPickerResult{
		SelectedIDs: []string{srv1ID, srv2ID},
		Submitted:   true,
	}

	m, _ = updateModel(m, result)

	// Check servers were assigned
	nsAfter := m.cfg.FindNamespaceByID(nsID)
	if nsAfter == nil {
		t.Fatal("expected namespace to exist")
	}
	if len(nsAfter.ServerIDs) != 2 {
		t.Errorf("expected 2 servers assigned, got %d", len(nsAfter.ServerIDs))
	}
}

func TestModel_HandleToolPermissionsResult(t *testing.T) {
	m := newTestModel(t)

	// Setup: add server and namespace
	srv := config.ServerConfig{Name: "Server 1", Kind: config.ServerKindStdio, Command: "test"}
	srvID, _ := m.cfg.AddServer(srv)

	ns := config.NamespaceConfig{Name: "Test", ServerIDs: []string{srvID}}
	nsID, err := m.cfg.AddNamespace(ns)
	if err != nil {
		t.Fatalf("failed to add namespace: %v", err)
	}

	m.detailNamespaceID = nsID

	result := views.ToolPermissionsResult{
		Changes: map[string]bool{
			srvID + ":read_file":  true,
			srvID + ":write_file": false,
		},
		Submitted: true,
	}

	m, _ = updateModel(m, result)

	// Check permissions were set
	perms := m.cfg.GetToolPermissionsForNamespace(nsID)
	if len(perms) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(perms))
	}

	// Check read_file is enabled
	readEnabled, found := m.cfg.GetToolPermission(nsID, srvID, "read_file")
	if !found || !readEnabled {
		t.Error("expected read_file to be enabled")
	}

	// Check write_file is disabled
	writeEnabled, found := m.cfg.GetToolPermission(nsID, srvID, "write_file")
	if !found || writeEnabled {
		t.Error("expected write_file to be disabled")
	}
}
