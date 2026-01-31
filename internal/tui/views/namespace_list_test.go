package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
)

func TestNamespaceList_SetItems(t *testing.T) {
	th := theme.New()
	list := NewNamespaceList(th)

	items := []NamespaceItem{
		{
			Name:      "Production",
			Config:    config.NamespaceConfig{ServerIDs: []string{"s1", "s2"}},
			IsDefault: true,
		},
		{
			Name:      "Development",
			Config:    config.NamespaceConfig{ServerIDs: []string{"s3"}},
			IsDefault: false,
		},
	}

	list.SetItems(items)

	// Should have 2 items
	if list.list.Items() == nil || len(list.list.Items()) != 2 {
		t.Errorf("expected 2 items, got %d", len(list.list.Items()))
	}
}

func TestNamespaceList_SelectedItem(t *testing.T) {
	th := theme.New()
	list := NewNamespaceList(th)

	// Empty list should return nil
	if list.SelectedItem() != nil {
		t.Error("expected nil for empty list")
	}

	items := []NamespaceItem{
		{
			Name:      "Production",
			Config:    config.NamespaceConfig{},
			IsDefault: true,
		},
	}
	list.SetItems(items)

	selected := list.SelectedItem()
	if selected == nil {
		t.Fatal("expected selected item, got nil")
	}
	if selected.Name != "Production" {
		t.Errorf("expected Name 'Production', got %q", selected.Name)
	}
	if !selected.IsDefault {
		t.Error("expected IsDefault to be true")
	}
}

func TestNamespaceItem_Interface(t *testing.T) {
	item := NamespaceItem{
		Name: "Test Namespace",
		Config: config.NamespaceConfig{
			Description: "A test namespace",
		},
	}

	if item.Title() != "Test Namespace" {
		t.Errorf("expected title 'Test Namespace', got %q", item.Title())
	}
	if item.Description() != "A test namespace" {
		t.Errorf("expected description 'A test namespace', got %q", item.Description())
	}
	if item.FilterValue() != "Test Namespace" {
		t.Errorf("expected filter value 'Test Namespace', got %q", item.FilterValue())
	}
}
