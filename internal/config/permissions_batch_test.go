package config

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
)

func permissionBatchConfig() *Config {
	c := NewConfig()
	c.Servers["s"] = ServerConfig{Command: "echo"}
	c.Namespaces["n"] = NamespaceConfig{}
	c.Namespaces["other"] = NamespaceConfig{}
	c.ToolPermissions = []ToolPermission{
		{Namespace: "n", Server: "s", ToolName: "edit", Enabled: false},
		{Namespace: "n", Server: "s", ToolName: "remove", Enabled: true},
		{Namespace: "other", Server: "s", ToolName: "keep", Enabled: true},
	}
	return c
}

func TestPermissionBatchAtomic(t *testing.T) {
	for _, tt := range []struct {
		name, namespace string
		changes         []PermissionChange
		kind            error
	}{
		{"missing-server", "n", []PermissionChange{{Server: "s", Tool: "edit", Enabled: true}, {Server: "missing", Tool: "x"}}, ErrNotFound},
		{"missing-removal-server", "n", []PermissionChange{{Server: "s", Tool: "remove", Remove: true}, {Server: "missing", Tool: "x", Remove: true}}, ErrNotFound},
		{"missing-namespace", "missing", nil, ErrNotFound},
		{"empty-tool", "n", []PermissionChange{{Server: "s", Tool: ""}}, ErrInvalidInput},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := permissionBatchConfig()
			alias := c.ToolPermissions
			before := slices.Clone(alias)
			if err := c.ApplyPermissionChanges(tt.namespace, tt.changes); !errors.Is(err, tt.kind) {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(before, c.ToolPermissions) || !reflect.DeepEqual(before, alias) {
				t.Fatal("failed batch changed receiver or aliased slice")
			}
			path := writeTestConfig(t, c)
			diskBefore, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			adopted, err := Mutate(path, func(fresh *Config) error { return fresh.ApplyPermissionChanges(tt.namespace, tt.changes) })
			if !errors.Is(err, tt.kind) || adopted != nil {
				t.Fatalf("Mutate = %v, %v", adopted, err)
			}
			diskAfter, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(diskBefore, diskAfter) {
				t.Fatal("failed batch saved config")
			}
		})
	}
}

func TestPermissionBatchValid(t *testing.T) {
	c := permissionBatchConfig()
	if err := c.ApplyPermissionChanges("n", []PermissionChange{
		{Server: "s", Tool: "edit", Enabled: true}, {Server: "s", Tool: "deny:with:colons", Enabled: false},
		{Server: "s", Tool: "remove", Remove: true}, {Server: "s", Tool: "absent", Remove: true},
	}); err != nil {
		t.Fatal(err)
	}
	want := []ToolPermission{
		{Namespace: "n", Server: "s", ToolName: "edit", Enabled: true},
		{Namespace: "other", Server: "s", ToolName: "keep", Enabled: true},
		{Namespace: "n", Server: "s", ToolName: "deny:with:colons", Enabled: false},
	}
	if !reflect.DeepEqual(c.ToolPermissions, want) {
		t.Fatalf("permissions = %#v", c.ToolPermissions)
	}
}

func TestReplacePermissionsPreservesDuplicates(t *testing.T) {
	c := permissionBatchConfig()
	replacement := []ToolPermission{{Server: "s", ToolName: "duplicate", Enabled: true}, {Server: "s", ToolName: "duplicate", Enabled: false}}
	if err := c.ReplaceToolPermissions("n", replacement); err != nil {
		t.Fatal(err)
	}
	got := c.GetToolPermissionsForNamespace("n")
	if len(got) != 2 || !got[0].Enabled || got[1].Enabled || got[0].Namespace != "n" {
		t.Fatalf("replacement = %#v", got)
	}
	if len(c.GetToolPermissionsForNamespace("other")) != 1 {
		t.Fatal("lost unrelated permissions")
	}
	before := slices.Clone(c.ToolPermissions)
	if err := c.ReplaceToolPermissions("n", []ToolPermission{{Server: "missing", ToolName: "x"}}); err == nil {
		t.Fatal("accepted missing server")
	}
	if !reflect.DeepEqual(before, c.ToolPermissions) {
		t.Fatal("failed replacement changed permissions")
	}
}
