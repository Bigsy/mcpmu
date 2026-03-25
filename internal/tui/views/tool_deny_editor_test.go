package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

func TestToolDenyEditor_Show(t *testing.T) {
	th := theme.New()
	editor := NewToolDenyEditor(th)

	if editor.IsVisible() {
		t.Error("should not be visible initially")
	}

	tools := []mcp.Tool{
		{Name: "read_file", Description: "Read a file"},
		{Name: "delete_file", Description: "Delete a file"},
	}
	denied := []string{"delete_file"}

	editor.Show("srv1", tools, denied)

	if !editor.IsVisible() {
		t.Error("should be visible after Show")
	}
	if editor.serverName != "srv1" {
		t.Errorf("expected server 'srv1', got %q", editor.serverName)
	}
	if !editor.denied["delete_file"] {
		t.Error("delete_file should be denied")
	}
	if editor.denied["read_file"] {
		t.Error("read_file should not be denied")
	}

	items := editor.list.Items()
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestToolDenyEditor_Toggle(t *testing.T) {
	th := theme.New()
	editor := NewToolDenyEditor(th)
	editor.SetSize(100, 50)

	tools := []mcp.Tool{
		{Name: "read_file", Description: "Read"},
		{Name: "delete_file", Description: "Delete"},
	}
	editor.Show("srv1", tools, nil)

	// Select first tool
	editor.list.Select(0)

	// Toggle: should deny read_file
	spaceMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	editor.Update(spaceMsg)

	if !editor.denied["read_file"] {
		t.Error("read_file should be denied after toggle")
	}

	// Toggle again: should un-deny
	editor.Update(spaceMsg)

	if editor.denied["read_file"] {
		t.Error("read_file should not be denied after second toggle")
	}
}

func TestToolDenyEditor_Submit(t *testing.T) {
	th := theme.New()
	editor := NewToolDenyEditor(th)
	editor.SetSize(100, 50)

	tools := []mcp.Tool{
		{Name: "read_file", Description: "Read"},
		{Name: "delete_file", Description: "Delete"},
	}
	editor.Show("srv1", tools, []string{"delete_file"})

	// Press enter to submit
	enterMsg := tea.KeyMsg{Type: tea.KeyEnter}
	cmd := editor.Update(enterMsg)

	if editor.IsVisible() {
		t.Error("should not be visible after submit")
	}

	if cmd == nil {
		t.Fatal("expected a command from submit")
	}

	// Execute the command to get the result message
	msg := cmd()
	result, ok := msg.(ToolDenyResult)
	if !ok {
		t.Fatalf("expected ToolDenyResult, got %T", msg)
	}
	if !result.Submitted {
		t.Error("expected Submitted=true")
	}
	if result.ServerName != "srv1" {
		t.Errorf("expected server 'srv1', got %q", result.ServerName)
	}
	if len(result.DeniedTools) != 1 || result.DeniedTools[0] != "delete_file" {
		t.Errorf("expected [delete_file], got %v", result.DeniedTools)
	}
}

func TestToolDenyEditor_Cancel(t *testing.T) {
	th := theme.New()
	editor := NewToolDenyEditor(th)
	editor.SetSize(100, 50)

	tools := []mcp.Tool{{Name: "tool1"}}
	editor.Show("srv1", tools, nil)

	// Press esc to cancel
	escMsg := tea.KeyMsg{Type: tea.KeyEsc}
	cmd := editor.Update(escMsg)

	if editor.IsVisible() {
		t.Error("should not be visible after cancel")
	}

	if cmd == nil {
		t.Fatal("expected a command from cancel")
	}

	msg := cmd()
	result, ok := msg.(ToolDenyResult)
	if !ok {
		t.Fatalf("expected ToolDenyResult, got %T", msg)
	}
	if result.Submitted {
		t.Error("expected Submitted=false on cancel")
	}
}

func TestToolDenyEditor_ShowWithEmptyDenyList(t *testing.T) {
	th := theme.New()
	editor := NewToolDenyEditor(th)

	tools := []mcp.Tool{
		{Name: "read_file"},
		{Name: "write_file"},
	}
	editor.Show("srv1", tools, nil)

	// No tools should be denied
	for name, denied := range editor.denied {
		if denied {
			t.Errorf("expected no tools denied, but %q is denied", name)
		}
	}
}
