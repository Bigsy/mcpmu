package views

import (
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/theme"
)

func TestNamespaceForm_ShowAdd(t *testing.T) {
	th := theme.New()
	form := NewNamespaceForm(th)

	if form.IsVisible() {
		t.Error("form should not be visible initially")
	}

	form.ShowAdd()

	if !form.IsVisible() {
		t.Error("form should be visible after ShowAdd")
	}
	if form.isEdit {
		t.Error("isEdit should be false for add mode")
	}
	if form.name != "" {
		t.Errorf("name should be empty, got %q", form.name)
	}
}

func TestNamespaceForm_ShowEdit(t *testing.T) {
	th := theme.New()
	form := NewNamespaceForm(th)

	ns := config.NamespaceConfig{
		Description:   "Production servers",
		DenyByDefault: true,
	}

	form.ShowEdit("production", ns)

	if !form.IsVisible() {
		t.Error("form should be visible after ShowEdit")
	}
	if !form.isEdit {
		t.Error("isEdit should be true for edit mode")
	}
	if form.originalName != "production" {
		t.Errorf("expected originalName 'production', got %q", form.originalName)
	}
	if form.name != "production" {
		t.Errorf("expected name 'production', got %q", form.name)
	}
	if form.description != "Production servers" {
		t.Errorf("expected description 'Production servers', got %q", form.description)
	}
	if !form.denyByDefault {
		t.Error("expected denyByDefault to be true")
	}
}

func TestNamespaceForm_IsDirty(t *testing.T) {
	th := theme.New()
	form := NewNamespaceForm(th)

	ns := config.NamespaceConfig{}

	form.ShowEdit("original", ns)

	// Initially not dirty
	if form.isDirty() {
		t.Error("form should not be dirty initially")
	}

	// Change name
	form.name = "modified"
	if !form.isDirty() {
		t.Error("form should be dirty after name change")
	}

	// Reset
	form.name = "original"
	if form.isDirty() {
		t.Error("form should not be dirty after resetting name")
	}

	// Change description
	form.description = "New description"
	if !form.isDirty() {
		t.Error("form should be dirty after description change")
	}
}

func TestNamespaceForm_BuildConfig(t *testing.T) {
	th := theme.New()
	form := NewNamespaceForm(th)

	form.name = "  Test Namespace  "
	form.description = "  Test description  "
	form.denyByDefault = true

	cfg := form.buildNamespaceConfig()

	// Description should be trimmed
	if cfg.Description != "Test description" {
		t.Errorf("expected trimmed description 'Test description', got %q", cfg.Description)
	}
	if !cfg.DenyByDefault {
		t.Error("expected DenyByDefault to be true")
	}
}

func TestNamespaceForm_Hide(t *testing.T) {
	th := theme.New()
	form := NewNamespaceForm(th)

	form.ShowAdd()
	if !form.IsVisible() {
		t.Fatal("form should be visible")
	}

	form.Hide()
	if form.IsVisible() {
		t.Error("form should not be visible after Hide")
	}
}
