package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/events"
	"github.com/hedworth/mcp-studio-go/internal/process"
	"github.com/hedworth/mcp-studio-go/internal/testutil"
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

	// Note: Tab2 and Tab3 are disabled in Phase 1, so they don't change the tab
	// but they should still be handled without error
	m, _ = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	// Tab stays on Servers since Namespaces is disabled
	if m.activeTab != TabServers {
		t.Errorf("expected tab to stay Servers (disabled), got %v", m.activeTab)
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
