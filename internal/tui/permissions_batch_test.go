package tui

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/tui/views"
)

func TestPermissionEditorFailureIsAtomic(t *testing.T) {
	for _, scenario := range []string{"removed-server", "removed-namespace", "malformed", "empty-server", "empty-tool", "malformed-deletion"} {
		t.Run(scenario, func(t *testing.T) {
			m := newTestModel(t)
			if err := m.cfg.AddServer("s", config.ServerConfig{Command: "echo"}); err != nil {
				t.Fatal(err)
			}
			if err := m.cfg.AddNamespace("n", config.NamespaceConfig{}); err != nil {
				t.Fatal(err)
			}
			if err := m.cfg.SetToolPermission("n", "s", "keep", true); err != nil {
				t.Fatal(err)
			}
			persistConfig(t, m)
			m.detailNamespaceID = "n"
			result := views.ToolPermissionsResult{Submitted: true, Changes: map[string]bool{"s:keep": false}}
			switch scenario {
			case "removed-server":
				if _, err := config.Mutate(m.configPath, func(c *config.Config) error { return c.DeleteServer("s") }); err != nil {
					t.Fatal(err)
				}
			case "removed-namespace":
				if _, err := config.Mutate(m.configPath, func(c *config.Config) error { return c.DeleteNamespace("n") }); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				result.Changes["bad"] = true
			case "empty-server":
				result.Changes[":tool"] = true
			case "empty-tool":
				result.Changes["s:"] = true
			case "malformed-deletion":
				result.Deletions = []string{"bad"}
			}
			before, err := os.ReadFile(m.configPath)
			if err != nil {
				t.Fatal(err)
			}
			adopted := m.cfg
			perms := append([]config.ToolPermission(nil), m.cfg.ToolPermissions...)
			m, _ = updateModel(m, result)
			if m.cfg != adopted || !reflect.DeepEqual(perms, m.cfg.ToolPermissions) {
				t.Error("failed editor save changed adopted config")
			}
			after, err := os.ReadFile(m.configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("failed editor save wrote config")
			}
			view := m.toast.View()
			if !strings.Contains(view, "Failed to save") || strings.Contains(view, "Tool permissions updated") {
				t.Errorf("wrong toast: %s", view)
			}
		})
	}
}

func TestPermissionEditorColonToolAndIdempotentRemoval(t *testing.T) {
	m := newTestModel(t)
	if err := m.cfg.AddServer("s", config.ServerConfig{Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := m.cfg.AddNamespace("n", config.NamespaceConfig{}); err != nil {
		t.Fatal(err)
	}
	persistConfig(t, m)
	m.detailNamespaceID = "n"
	m, _ = updateModel(m, views.ToolPermissionsResult{Submitted: true, Changes: map[string]bool{"s:tool:with:colons": true}, Deletions: []string{"s:absent"}})
	if enabled, ok := m.cfg.GetToolPermission("n", "s", "tool:with:colons"); !ok || !enabled {
		t.Fatal("tool name was not retained")
	}
	if !strings.Contains(m.toast.View(), "Tool permissions updated") {
		t.Errorf("wrong toast: %s", m.toast.View())
	}
}
