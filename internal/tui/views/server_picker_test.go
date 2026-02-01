package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
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
