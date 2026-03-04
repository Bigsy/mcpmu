package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAddMethod_InitiallyHidden(t *testing.T) {
	m := NewAddMethod(theme.New())
	if m.IsVisible() {
		t.Error("expected add method selector to be hidden initially")
	}
}

func TestAddMethod_ShowHideLifecycle(t *testing.T) {
	m := NewAddMethod(theme.New())

	m.Show()
	if !m.IsVisible() {
		t.Error("expected visible after Show()")
	}

	m.Hide()
	if m.IsVisible() {
		t.Error("expected hidden after Hide()")
	}
}

func TestAddMethod_EscCancels(t *testing.T) {
	m := NewAddMethod(theme.New())
	m.Show()

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.IsVisible() {
		t.Error("expected hidden after Esc")
	}
	if cmd == nil {
		t.Fatal("expected command from Esc")
	}

	msg := cmd()
	result, ok := msg.(AddMethodResult)
	if !ok {
		t.Fatalf("expected AddMethodResult, got %T", msg)
	}
	if result.Submitted {
		t.Error("expected Submitted=false on Esc")
	}
}

func TestAddMethod_EnterOnManual(t *testing.T) {
	m := NewAddMethod(theme.New())
	m.Show()

	// Default selection is Manual (index 0)
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.IsVisible() {
		t.Error("expected hidden after Enter")
	}
	if cmd == nil {
		t.Fatal("expected command from Enter")
	}

	msg := cmd()
	result, ok := msg.(AddMethodResult)
	if !ok {
		t.Fatalf("expected AddMethodResult, got %T", msg)
	}
	if !result.Submitted {
		t.Error("expected Submitted=true")
	}
	if result.Method != "manual" {
		t.Errorf("expected Method='manual', got %q", result.Method)
	}
}

func TestAddMethod_DownThenEnterSelectsRegistry(t *testing.T) {
	m := NewAddMethod(theme.New())
	m.Show()

	// Move down to Registry
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Fatal("expected command from Enter")
	}

	msg := cmd()
	result, ok := msg.(AddMethodResult)
	if !ok {
		t.Fatalf("expected AddMethodResult, got %T", msg)
	}
	if !result.Submitted {
		t.Error("expected Submitted=true")
	}
	if result.Method != "registry" {
		t.Errorf("expected Method='registry', got %q", result.Method)
	}
}

func TestAddMethod_ShowResetsSelection(t *testing.T) {
	m := NewAddMethod(theme.New())
	m.Show()

	// Move to registry
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Show again should reset to manual
	m.Show()
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	msg := cmd()
	result := msg.(AddMethodResult)
	if result.Method != "manual" {
		t.Errorf("expected Method='manual' after Show() reset, got %q", result.Method)
	}
}

func TestAddMethod_UpDownBounds(t *testing.T) {
	m := NewAddMethod(theme.New())
	m.Show()

	// Up from 0 should stay at 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := cmd().(AddMethodResult)
	if result.Method != "manual" {
		t.Errorf("expected manual after up from 0, got %q", result.Method)
	}

	// Down from 1 should stay at 1
	m.Show()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown}) // extra down
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result = cmd().(AddMethodResult)
	if result.Method != "registry" {
		t.Errorf("expected registry after double down, got %q", result.Method)
	}
}

func TestAddMethod_NotVisibleIgnoresKeys(t *testing.T) {
	m := NewAddMethod(theme.New())
	// Not visible - should not respond
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected nil cmd when not visible")
	}
}
