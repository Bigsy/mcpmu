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

// ============================================================================
// Filter Tests
// ============================================================================

func newDenyEditorWithTools(t *testing.T) ToolDenyEditorModel {
	t.Helper()
	th := theme.New()
	editor := NewToolDenyEditor(th)
	editor.SetSize(100, 50)
	tools := []mcp.Tool{
		{Name: "read_file", Description: "Read a file"},
		{Name: "read_resource", Description: "Read a resource"},
		{Name: "write_file", Description: "Write a file"},
		{Name: "delete_file", Description: "Delete a file"},
	}
	editor.Show("srv1", tools, nil)
	return editor
}

func sendRune(editor *ToolDenyEditorModel, r rune) {
	editor.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

func enterFilterMode(editor *ToolDenyEditorModel) {
	sendRune(editor, '/')
}

func TestToolDenyEditor_FilterReducesList(t *testing.T) {
	editor := newDenyEditorWithTools(t)

	if len(editor.list.Items()) != 4 {
		t.Fatalf("expected 4 items, got %d", len(editor.list.Items()))
	}

	// Enter filter mode and type "read"
	enterFilterMode(&editor)
	for _, r := range "read" {
		sendRune(&editor, r)
	}

	items := editor.list.Items()
	if len(items) != 2 {
		t.Errorf("expected 2 filtered items, got %d", len(items))
	}
	for _, item := range items {
		ti := item.(toolDenyItem)
		if ti.toolName != "read_file" && ti.toolName != "read_resource" {
			t.Errorf("unexpected item %q in filtered list", ti.toolName)
		}
	}
}

func TestToolDenyEditor_FilterClearOnEsc(t *testing.T) {
	editor := newDenyEditorWithTools(t)

	// Enter filter mode, type, then exit filter mode
	enterFilterMode(&editor)
	for _, r := range "read" {
		sendRune(&editor, r)
	}
	// Esc exits filter mode (keeps text)
	editor.Update(tea.KeyMsg{Type: tea.KeyEsc})
	// Now in action mode with filter text — esc clears filter
	editor.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if editor.filterInput.Value() != "" {
		t.Error("filter text should be cleared")
	}
	if len(editor.list.Items()) != 4 {
		t.Errorf("expected all 4 items restored, got %d", len(editor.list.Items()))
	}
}

func TestToolDenyEditor_FilterModeEscKeepsText(t *testing.T) {
	editor := newDenyEditorWithTools(t)

	enterFilterMode(&editor)
	for _, r := range "read" {
		sendRune(&editor, r)
	}

	// Esc should exit filter mode but keep text
	editor.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if editor.filterFocused {
		t.Error("filter should not be focused after esc")
	}
	if editor.filterInput.Value() != "read" {
		t.Errorf("expected filter text 'read', got %q", editor.filterInput.Value())
	}
	// List should still be filtered
	if len(editor.list.Items()) != 2 {
		t.Errorf("expected 2 items (still filtered), got %d", len(editor.list.Items()))
	}
}

func TestToolDenyEditor_SpaceWorksInFilterMode(t *testing.T) {
	editor := newDenyEditorWithTools(t)

	enterFilterMode(&editor)
	for _, r := range "delete" {
		sendRune(&editor, r)
	}

	// Should have 1 item: delete_file
	if len(editor.list.Items()) != 1 {
		t.Fatalf("expected 1 filtered item, got %d", len(editor.list.Items()))
	}

	// Space should toggle the selected item
	editor.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if !editor.denied["delete_file"] {
		t.Error("delete_file should be denied after space toggle in filter mode")
	}
}

func TestToolDenyEditor_FilterNoMatches(t *testing.T) {
	editor := newDenyEditorWithTools(t)

	enterFilterMode(&editor)
	for _, r := range "xyznonexistent" {
		sendRune(&editor, r)
	}

	if len(editor.list.Items()) != 0 {
		t.Errorf("expected 0 items, got %d", len(editor.list.Items()))
	}

	// Space should be a no-op (no panic)
	editor.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
}

func TestToolDenyEditor_BackspaceToEmpty(t *testing.T) {
	editor := newDenyEditorWithTools(t)

	enterFilterMode(&editor)
	for _, r := range "ab" {
		sendRune(&editor, r)
	}

	// Backspace twice to empty
	editor.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	editor.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if editor.filterInput.Value() != "" {
		t.Errorf("expected empty filter, got %q", editor.filterInput.Value())
	}
	if len(editor.list.Items()) != 4 {
		t.Errorf("expected all 4 items restored, got %d", len(editor.list.Items()))
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
