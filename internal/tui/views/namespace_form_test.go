package views

import (
	"testing"

	"github.com/hedworth/mcp-studio-go/internal/config"
	"github.com/hedworth/mcp-studio-go/internal/tui/theme"
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
		ID:            "ns1",
		Name:          "Production",
		Description:   "Production servers",
		DenyByDefault: true,
	}

	form.ShowEdit(ns)

	if !form.IsVisible() {
		t.Error("form should be visible after ShowEdit")
	}
	if !form.isEdit {
		t.Error("isEdit should be true for edit mode")
	}
	if form.namespaceID != "ns1" {
		t.Errorf("expected ID 'ns1', got %q", form.namespaceID)
	}
	if form.name != "Production" {
		t.Errorf("expected name 'Production', got %q", form.name)
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

	ns := config.NamespaceConfig{
		ID:   "ns1",
		Name: "Original",
	}

	form.ShowEdit(ns)

	// Initially not dirty
	if form.isDirty() {
		t.Error("form should not be dirty initially")
	}

	// Change name
	form.name = "Modified"
	if !form.isDirty() {
		t.Error("form should be dirty after name change")
	}

	// Reset
	form.name = "Original"
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

	form.namespaceID = "ns1"
	form.name = "  Test Namespace  "
	form.description = "  Test description  "
	form.denyByDefault = true

	cfg := form.buildNamespaceConfig()

	if cfg.ID != "ns1" {
		t.Errorf("expected ID 'ns1', got %q", cfg.ID)
	}
	// Name should be trimmed
	if cfg.Name != "Test Namespace" {
		t.Errorf("expected trimmed name 'Test Namespace', got %q", cfg.Name)
	}
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
