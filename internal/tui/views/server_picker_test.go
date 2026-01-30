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

	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
		{ID: "s2", Name: "Server 2"},
		{ID: "s3", Name: "Server 3"},
	}
	selectedIDs := []string{"s1", "s3"}

	picker.Show(servers, selectedIDs)

	if !picker.IsVisible() {
		t.Error("picker should be visible after Show")
	}

	// Check selection state
	if !picker.selected["s1"] {
		t.Error("s1 should be selected")
	}
	if picker.selected["s2"] {
		t.Error("s2 should not be selected")
	}
	if !picker.selected["s3"] {
		t.Error("s3 should be selected")
	}
}

func TestServerPicker_Hide(t *testing.T) {
	th := theme.New()
	picker := NewServerPicker(th)

	servers := []config.ServerConfig{
		{ID: "s1", Name: "Server 1"},
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
		server: config.ServerConfig{
			ID:      "s1",
			Name:    "Test Server",
			Command: "test-cmd",
		},
		selected: true,
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
