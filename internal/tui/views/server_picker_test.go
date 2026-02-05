package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

func TestServerPicker_Show(t *testing.T) {
	th := theme.New()
	picker := NewServerPicker(th)

	if picker.IsVisible() {
		t.Error("picker should not be visible initially")
	}

	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd1"}},
		{Name: "Server 2", Config: config.ServerConfig{Command: "cmd2"}},
		{Name: "Server 3", Config: config.ServerConfig{Command: "cmd3"}},
	}
	selectedNames := []string{"Server 1", "Server 3"}

	picker.Show(servers, selectedNames)

	if !picker.IsVisible() {
		t.Error("picker should be visible after Show")
	}

	// Check selection state
	if !picker.selected["Server 1"] {
		t.Error("Server 1 should be selected")
	}
	if picker.selected["Server 2"] {
		t.Error("Server 2 should not be selected")
	}
	if !picker.selected["Server 3"] {
		t.Error("Server 3 should be selected")
	}
}

func TestServerPicker_Hide(t *testing.T) {
	th := theme.New()
	picker := NewServerPicker(th)

	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd1"}},
	}
	picker.Show(servers, nil)

	if !picker.IsVisible() {
		t.Fatal("picker should be visible")
	}

	picker.Hide()

	if picker.IsVisible() {
		t.Error("picker should not be visible after Hide")
	}
}

func TestServerPickerItem_Interface(t *testing.T) {
	item := serverPickerItem{
		name: "Test Server",
		config: config.ServerConfig{
			Command: "test-cmd",
		},
	}

	if item.Title() != "Test Server" {
		t.Errorf("expected title 'Test Server', got %q", item.Title())
	}
	if item.Description() != "test-cmd" {
		t.Errorf("expected description 'test-cmd', got %q", item.Description())
	}
	if item.FilterValue() != "Test Server" {
		t.Errorf("expected filter value 'Test Server', got %q", item.FilterValue())
	}
}

func TestServerPicker_EnterConfirms(t *testing.T) {
	th := theme.New()
	picker := NewServerPicker(th)

	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd1"}},
		{Name: "Server 2", Config: config.ServerConfig{Command: "cmd2"}},
	}
	// Pre-select both servers
	picker.Show(servers, []string{"Server 1", "Server 2"})
	picker.SetSize(80, 24)

	if !picker.IsVisible() {
		t.Fatal("picker should be visible")
	}

	// Press Enter to confirm
	cmd := picker.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should return a command")
	}

	// Picker should be hidden
	if picker.IsVisible() {
		t.Error("picker should be hidden after Enter")
	}

	// Execute the command to get the result
	msg := cmd()
	result, ok := msg.(ServerPickerResult)
	if !ok {
		t.Fatalf("expected ServerPickerResult, got %T", msg)
	}

	if !result.Submitted {
		t.Error("result should have Submitted=true")
	}

	// Should have both servers selected
	if len(result.SelectedIDs) != 2 {
		t.Errorf("expected 2 selected IDs, got %d: %v", len(result.SelectedIDs), result.SelectedIDs)
	}
}

func TestServerPicker_EscCancels(t *testing.T) {
	th := theme.New()
	picker := NewServerPicker(th)

	servers := []config.ServerEntry{
		{Name: "Server 1", Config: config.ServerConfig{Command: "cmd1"}},
	}
	picker.Show(servers, []string{"Server 1"})
	picker.SetSize(80, 24)

	if !picker.IsVisible() {
		t.Fatal("picker should be visible")
	}

	// Press Escape to cancel
	cmd := picker.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Escape should return a command")
	}

	// Picker should be hidden
	if picker.IsVisible() {
		t.Error("picker should be hidden after Escape")
	}

	// Execute the command to get the result
	msg := cmd()
	result, ok := msg.(ServerPickerResult)
	if !ok {
		t.Fatalf("expected ServerPickerResult, got %T", msg)
	}

	if result.Submitted {
		t.Error("result should have Submitted=false for cancel")
	}
}
