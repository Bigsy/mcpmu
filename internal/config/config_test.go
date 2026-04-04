package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bigsy/mcpmu/internal/testutil"
)

func TestLoad_NonExistentFile(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.SchemaVersion != SchemaVersion {
		t.Errorf("expected schema version %d, got %d", SchemaVersion, cfg.SchemaVersion)
	}

	if len(cfg.Servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(cfg.Servers))
	}
}

func TestLoad_ValidConfig(t *testing.T) {
	home := testutil.SetupTestHome(t)

	// Write a valid config (new format: no id/name fields in body)
	configJSON := `{
		"schemaVersion": 1,
		"servers": {
			"test-server": {
				"kind": "stdio",
				"command": "echo"
			}
		},
		"namespaces": {}
	}`

	configPath := filepath.Join(home, ".config", "mcpmu", "config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(cfg.Servers))
	}

	srv, ok := cfg.Servers["test-server"]
	if !ok {
		t.Fatal("expected server 'test-server' to exist")
	}

	if srv.Kind != ServerKindStdio {
		t.Errorf("expected kind 'stdio', got %q", srv.Kind)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	home := testutil.SetupTestHome(t)

	configPath := filepath.Join(home, ".config", "mcpmu", "config.json")
	if err := os.WriteFile(configPath, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := NewConfig()
	cfg.Servers["test"] = ServerConfig{
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	err := Save(cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify the file was written
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after Save failed: %v", err)
	}

	if len(loaded.Servers) != 1 {
		t.Errorf("expected 1 server after Save/Load, got %d", len(loaded.Servers))
	}

	// Verify no temp file left behind
	path, _ := ConfigPath()
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("expected temp file to be cleaned up")
	}
}

func TestSave_CreatesDirectory(t *testing.T) {
	testutil.SetupTestHome(t)

	// Remove the config directory if it exists
	path, _ := ConfigPath()
	_ = os.RemoveAll(filepath.Dir(path))

	cfg := NewConfig()
	err := Save(cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Directory should be created
	if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
		t.Error("expected config directory to be created")
	}
}

func TestServerConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		enabled  *bool
		expected bool
	}{
		{
			name:     "nil means enabled",
			enabled:  nil,
			expected: true,
		},
		{
			name:     "true means enabled",
			enabled:  new(true),
			expected: true,
		},
		{
			name:     "false means disabled",
			enabled:  new(false),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := ServerConfig{Enabled: tt.enabled}
			if srv.IsEnabled() != tt.expected {
				t.Errorf("expected IsEnabled()=%v, got %v", tt.expected, srv.IsEnabled())
			}
		})
	}
}

func TestServerConfig_SetEnabled(t *testing.T) {
	srv := ServerConfig{}

	srv.SetEnabled(false)
	if srv.IsEnabled() {
		t.Error("expected server to be disabled after SetEnabled(false)")
	}

	srv.SetEnabled(true)
	if !srv.IsEnabled() {
		t.Error("expected server to be enabled after SetEnabled(true)")
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"my-server", false},
		{"test_server", false},
		{"server123", false},
		{"filesystem", false},
		{"", true},          // empty
		{"has.dot", true},   // dot not allowed (used as namespace separator)
		{"has:colon", true}, // colon not allowed (used as resource URI separator)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestConfig_AddServer(t *testing.T) {
	cfg := NewConfig()

	srv := ServerConfig{
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	err := cfg.AddServer("test-server", srv)
	if err != nil {
		t.Fatalf("AddServer failed: %v", err)
	}

	if _, ok := cfg.Servers["test-server"]; !ok {
		t.Error("expected server to be added to config")
	}
}

func TestConfig_AddServer_Duplicate(t *testing.T) {
	cfg := NewConfig()

	srv := ServerConfig{
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	err := cfg.AddServer("test", srv)
	if err != nil {
		t.Fatalf("first AddServer failed: %v", err)
	}

	// Try to add another server with same name
	srv2 := ServerConfig{
		Kind:    ServerKindStdio,
		Command: "cat",
	}
	err = cfg.AddServer("test", srv2)
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestConfig_UpdateServer(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["test"] = ServerConfig{
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	// Update with new command
	err := cfg.UpdateServer("test", ServerConfig{
		Kind:    ServerKindStdio,
		Command: "cat",
	})
	if err != nil {
		t.Fatalf("UpdateServer failed: %v", err)
	}

	srv, ok := cfg.GetServer("test")
	if !ok {
		t.Fatal("expected to find server 'test'")
	}
	if srv.Command != "cat" {
		t.Errorf("expected command 'cat', got %q", srv.Command)
	}
}

func TestConfig_UpdateServer_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.UpdateServer("nonexistent", ServerConfig{Command: "echo"})
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_DeleteServer(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["test"] = ServerConfig{
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	err := cfg.DeleteServer("test")
	if err != nil {
		t.Fatalf("DeleteServer failed: %v", err)
	}

	if _, ok := cfg.Servers["test"]; ok {
		t.Error("expected server to be deleted")
	}
}

func TestConfig_DeleteServer_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.DeleteServer("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_DeleteServer_CleansUpReferences(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}
	cfg.Namespaces["ns1"] = NamespaceConfig{
		ServerIDs:      []string{"srv1", "srv2"},
		ServerDefaults: map[string]bool{"srv1": true, "srv2": false},
	}
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "ns1", Server: "srv1", ToolName: "tool1", Enabled: true},
		{Namespace: "ns1", Server: "srv2", ToolName: "tool2", Enabled: true},
	}

	err := cfg.DeleteServer("srv1")
	if err != nil {
		t.Fatalf("DeleteServer failed: %v", err)
	}

	// Check namespace reference was removed
	ns, ok := cfg.GetNamespace("ns1")
	if !ok {
		t.Fatal("expected to find namespace 'ns1'")
	}
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv2" {
		t.Error("expected srv1 to be removed from namespace")
	}

	// Check tool permission was removed
	if len(cfg.ToolPermissions) != 1 {
		t.Error("expected srv1 tool permissions to be removed")
	}

	// Check server default was removed
	if _, ok := ns.ServerDefaults["srv1"]; ok {
		t.Error("expected srv1 server default to be removed")
	}
	if val, ok := ns.ServerDefaults["srv2"]; !ok || val != false {
		t.Error("expected srv2 server default to be preserved")
	}
}

func TestConfig_RenameServer(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["old-name"] = ServerConfig{Command: "echo"}
	cfg.Namespaces["ns1"] = NamespaceConfig{
		ServerIDs:      []string{"old-name", "other"},
		ServerDefaults: map[string]bool{"old-name": true},
	}
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "ns1", Server: "old-name", ToolName: "tool1", Enabled: true},
	}

	err := cfg.RenameServer("old-name", "new-name")
	if err != nil {
		t.Fatalf("RenameServer failed: %v", err)
	}

	// Check server was moved
	if _, ok := cfg.Servers["old-name"]; ok {
		t.Error("expected old-name to be removed")
	}
	if _, ok := cfg.Servers["new-name"]; !ok {
		t.Error("expected new-name to exist")
	}

	// Check namespace reference was updated
	ns, ok := cfg.GetNamespace("ns1")
	if !ok {
		t.Fatal("expected to find namespace 'ns1'")
	}
	found := false
	for _, sid := range ns.ServerIDs {
		if sid == "new-name" {
			found = true
		}
		if sid == "old-name" {
			t.Error("expected old-name to be removed from namespace")
		}
	}
	if !found {
		t.Error("expected new-name to be in namespace")
	}

	// Check tool permission was updated
	if cfg.ToolPermissions[0].Server != "new-name" {
		t.Error("expected tool permission server to be updated")
	}

	// Check server default was renamed
	if _, ok := ns.ServerDefaults["old-name"]; ok {
		t.Error("expected old-name server default to be removed")
	}
	if val, ok := ns.ServerDefaults["new-name"]; !ok || !val {
		t.Error("expected new-name server default to be set to true")
	}
}

func TestConfig_RenameServer_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["existing"] = ServerConfig{Command: "echo"}
	cfg.Servers["other"] = ServerConfig{Command: "cat"}

	// Not found
	err := cfg.RenameServer("nonexistent", "new-name")
	if err == nil {
		t.Error("expected error for non-existent server")
	}

	// Duplicate
	err = cfg.RenameServer("existing", "other")
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestConfig_GetServer(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["test"] = ServerConfig{
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	srv, ok := cfg.GetServer("test")
	if !ok {
		t.Fatal("expected server to be found")
	}
	if srv.Command != "echo" {
		t.Errorf("expected command 'echo', got %q", srv.Command)
	}

	// Non-existent
	_, ok = cfg.GetServer("nonexistent")
	if ok {
		t.Error("expected false for non-existent server")
	}
}

func TestConfig_ServerEntries(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["server-a"] = ServerConfig{Command: "a"}
	cfg.Servers["server-b"] = ServerConfig{Command: "b"}

	entries := cfg.ServerEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 servers in list, got %d", len(entries))
	}

	// Check that entries have names
	for _, e := range entries {
		if e.Name == "" {
			t.Error("expected entry to have name")
		}
	}
}

// ============================================================================
// Namespace Tests
// ============================================================================

func TestConfig_AddNamespace(t *testing.T) {
	cfg := NewConfig()

	ns := NamespaceConfig{
		Description: "Dev environment",
	}

	err := cfg.AddNamespace("development", ns)
	if err != nil {
		t.Fatalf("AddNamespace failed: %v", err)
	}

	if len(cfg.Namespaces) != 1 {
		t.Error("expected namespace to be added to config")
	}

	if _, ok := cfg.Namespaces["development"]; !ok {
		t.Error("expected 'development' namespace to exist")
	}
}

func TestConfig_AddNamespace_Duplicate(t *testing.T) {
	cfg := NewConfig()

	ns1 := NamespaceConfig{}
	err := cfg.AddNamespace("dev", ns1)
	if err != nil {
		t.Fatalf("first AddNamespace failed: %v", err)
	}

	ns2 := NamespaceConfig{}
	err = cfg.AddNamespace("dev", ns2)
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestConfig_GetNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["development"] = NamespaceConfig{Description: "Dev env"}

	ns, ok := cfg.GetNamespace("development")
	if !ok {
		t.Fatal("expected namespace to be found")
	}
	if ns.Description != "Dev env" {
		t.Errorf("expected description 'Dev env', got %q", ns.Description)
	}

	_, ok = cfg.GetNamespace("nonexistent")
	if ok {
		t.Error("expected false for non-existent namespace")
	}
}

func TestConfig_UpdateNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["development"] = NamespaceConfig{Description: "Old"}

	err := cfg.UpdateNamespace("development", NamespaceConfig{Description: "New"})
	if err != nil {
		t.Fatalf("UpdateNamespace failed: %v", err)
	}

	ns, ok := cfg.GetNamespace("development")
	if !ok {
		t.Fatal("expected to find namespace 'development'")
	}
	if ns.Description != "New" {
		t.Errorf("expected description 'New', got %q", ns.Description)
	}
}

func TestConfig_UpdateNamespace_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.UpdateNamespace("nonexistent", NamespaceConfig{})
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}
}

func TestConfig_DeleteNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["development"] = NamespaceConfig{}
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "development", Server: "srv1", ToolName: "tool1", Enabled: true},
	}
	cfg.DefaultNamespace = "development"

	err := cfg.DeleteNamespace("development")
	if err != nil {
		t.Fatalf("DeleteNamespace failed: %v", err)
	}

	if len(cfg.Namespaces) != 0 {
		t.Error("expected namespace to be deleted")
	}

	if len(cfg.ToolPermissions) != 0 {
		t.Error("expected tool permissions to be cleaned up")
	}

	if cfg.DefaultNamespace != "" {
		t.Error("expected default namespace to be cleared")
	}
}

func TestConfig_DeleteNamespace_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.DeleteNamespace("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}
}

func TestConfig_RenameNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["old-name"] = NamespaceConfig{Description: "Test"}
	cfg.DefaultNamespace = "old-name"
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "old-name", Server: "srv1", ToolName: "tool1", Enabled: true},
	}

	err := cfg.RenameNamespace("old-name", "new-name")
	if err != nil {
		t.Fatalf("RenameNamespace failed: %v", err)
	}

	// Check namespace was moved
	if _, ok := cfg.Namespaces["old-name"]; ok {
		t.Error("expected old-name to be removed")
	}
	if _, ok := cfg.Namespaces["new-name"]; !ok {
		t.Error("expected new-name to exist")
	}

	// Check default namespace was updated
	if cfg.DefaultNamespace != "new-name" {
		t.Error("expected default namespace to be updated")
	}

	// Check tool permission was updated
	if cfg.ToolPermissions[0].Namespace != "new-name" {
		t.Error("expected tool permission namespace to be updated")
	}
}

func TestConfig_RenameNamespace_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["existing"] = NamespaceConfig{}
	cfg.Namespaces["other"] = NamespaceConfig{}

	// Not found
	err := cfg.RenameNamespace("nonexistent", "new-name")
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}

	// Duplicate
	err = cfg.RenameNamespace("existing", "other")
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestConfig_DuplicateNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}
	cfg.Servers["srv2"] = ServerConfig{Command: "cat"}
	cfg.Namespaces["source"] = NamespaceConfig{
		Description:   "Source NS",
		ServerIDs:     []string{"srv1", "srv2"},
		DenyByDefault: true,
		ServerDefaults: map[string]bool{
			"srv1": true,
			"srv2": false,
		},
	}
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "source", Server: "srv1", ToolName: "tool-a", Enabled: true},
		{Namespace: "source", Server: "srv2", ToolName: "tool-b", Enabled: false},
		{Namespace: "other", Server: "srv1", ToolName: "tool-c", Enabled: true},
	}

	err := cfg.DuplicateNamespace("source", "copy")
	if err != nil {
		t.Fatalf("DuplicateNamespace failed: %v", err)
	}

	// Source still exists unchanged
	src := cfg.Namespaces["source"]
	if src.Description != "Source NS" || len(src.ServerIDs) != 2 || !src.DenyByDefault {
		t.Error("source namespace was mutated")
	}

	// Copy has correct fields
	cp := cfg.Namespaces["copy"]
	if cp.Description != "Source NS" {
		t.Errorf("expected description %q, got %q", "Source NS", cp.Description)
	}
	if !cp.DenyByDefault {
		t.Error("expected DenyByDefault to be true")
	}
	if len(cp.ServerIDs) != 2 || cp.ServerIDs[0] != "srv1" || cp.ServerIDs[1] != "srv2" {
		t.Errorf("unexpected ServerIDs: %v", cp.ServerIDs)
	}

	// ServerDefaults copied
	if len(cp.ServerDefaults) != 2 {
		t.Fatalf("expected 2 server defaults, got %d", len(cp.ServerDefaults))
	}
	if !cp.ServerDefaults["srv1"] {
		t.Error("expected srv1 server default to be true")
	}
	if cp.ServerDefaults["srv2"] {
		t.Error("expected srv2 server default to be false")
	}

	// ToolPermissions copied (2 from source + 1 unrelated = 3 original + 2 new = 5)
	if len(cfg.ToolPermissions) != 5 {
		t.Fatalf("expected 5 tool permissions, got %d", len(cfg.ToolPermissions))
	}
	copyPerms := cfg.GetToolPermissionsForNamespace("copy")
	if len(copyPerms) != 2 {
		t.Fatalf("expected 2 permissions for copy, got %d", len(copyPerms))
	}

	// "other" namespace permissions untouched
	otherPerms := cfg.GetToolPermissionsForNamespace("other")
	if len(otherPerms) != 1 {
		t.Errorf("expected 1 permission for other, got %d", len(otherPerms))
	}

	// Deep copy: mutating copy doesn't affect source
	cp.ServerIDs = append(cp.ServerIDs, "srv3")
	cp.ServerDefaults["srv3"] = true
	if len(src.ServerIDs) != 2 {
		t.Error("mutating copy ServerIDs affected source")
	}
	if len(src.ServerDefaults) != 2 {
		t.Error("mutating copy ServerDefaults affected source")
	}
}

func TestConfig_DuplicateNamespace_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["existing"] = NamespaceConfig{}
	cfg.Namespaces["other"] = NamespaceConfig{}

	// Source not found
	if err := cfg.DuplicateNamespace("nonexistent", "new"); err == nil {
		t.Error("expected error for non-existent source")
	}

	// Destination already exists
	if err := cfg.DuplicateNamespace("existing", "other"); err == nil {
		t.Error("expected error for duplicate destination")
	}

	// Invalid name
	if err := cfg.DuplicateNamespace("existing", ""); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestConfig_AssignServerToNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{ServerIDs: []string{}}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	err := cfg.AssignServerToNamespace("dev", "srv1")
	if err != nil {
		t.Fatalf("AssignServerToNamespace failed: %v", err)
	}

	ns, ok := cfg.GetNamespace("dev")
	if !ok {
		t.Fatal("expected to find namespace 'dev'")
	}
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv1" {
		t.Error("expected server to be assigned")
	}

	// Assigning again should be a no-op
	err = cfg.AssignServerToNamespace("dev", "srv1")
	if err != nil {
		t.Fatalf("second AssignServerToNamespace failed: %v", err)
	}
	ns, _ = cfg.GetNamespace("dev")
	if len(ns.ServerIDs) != 1 {
		t.Error("expected no duplicate assignment")
	}
}

func TestConfig_AssignServerToNamespace_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{ServerIDs: []string{}}

	// Non-existent namespace
	err := cfg.AssignServerToNamespace("nonexistent", "srv1")
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}

	// Non-existent server
	err = cfg.AssignServerToNamespace("dev", "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_UnassignServerFromNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{ServerIDs: []string{"srv1", "srv2"}}

	err := cfg.UnassignServerFromNamespace("dev", "srv1")
	if err != nil {
		t.Fatalf("UnassignServerFromNamespace failed: %v", err)
	}

	ns, ok := cfg.GetNamespace("dev")
	if !ok {
		t.Fatal("expected to find namespace 'dev'")
	}
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv2" {
		t.Error("expected server to be unassigned")
	}
}

// ============================================================================
// Tool Permission Tests
// ============================================================================

func TestConfig_SetToolPermission(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	err := cfg.SetToolPermission("dev", "srv1", "read_file", true)
	if err != nil {
		t.Fatalf("SetToolPermission failed: %v", err)
	}

	if len(cfg.ToolPermissions) != 1 {
		t.Fatal("expected 1 permission")
	}

	tp := cfg.ToolPermissions[0]
	if tp.Namespace != "dev" || tp.Server != "srv1" || tp.ToolName != "read_file" || !tp.Enabled {
		t.Error("permission not set correctly")
	}

	// Update existing permission
	err = cfg.SetToolPermission("dev", "srv1", "read_file", false)
	if err != nil {
		t.Fatalf("SetToolPermission update failed: %v", err)
	}

	if len(cfg.ToolPermissions) != 1 {
		t.Error("expected permission to be updated, not added")
	}

	if cfg.ToolPermissions[0].Enabled != false {
		t.Error("expected permission to be updated to false")
	}
}

func TestConfig_SetToolPermission_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	// Non-existent namespace
	err := cfg.SetToolPermission("nonexistent", "srv1", "tool", true)
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}

	// Non-existent server
	err = cfg.SetToolPermission("dev", "nonexistent", "tool", true)
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_UnsetToolPermission(t *testing.T) {
	cfg := NewConfig()
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "dev", Server: "srv1", ToolName: "read_file", Enabled: true},
		{Namespace: "dev", Server: "srv1", ToolName: "write_file", Enabled: false},
	}

	err := cfg.UnsetToolPermission("dev", "srv1", "read_file")
	if err != nil {
		t.Fatalf("UnsetToolPermission failed: %v", err)
	}

	if len(cfg.ToolPermissions) != 1 {
		t.Error("expected 1 permission remaining")
	}

	// Unset non-existent is not an error
	err = cfg.UnsetToolPermission("dev", "srv1", "nonexistent")
	if err != nil {
		t.Error("expected no error for non-existent permission")
	}
}

func TestConfig_GetToolPermission(t *testing.T) {
	cfg := NewConfig()
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "dev", Server: "srv1", ToolName: "read_file", Enabled: true},
		{Namespace: "dev", Server: "srv1", ToolName: "write_file", Enabled: false},
	}

	enabled, found := cfg.GetToolPermission("dev", "srv1", "read_file")
	if !found {
		t.Error("expected permission to be found")
	}
	if !enabled {
		t.Error("expected permission to be enabled")
	}

	enabled, found = cfg.GetToolPermission("dev", "srv1", "write_file")
	if !found {
		t.Error("expected permission to be found")
	}
	if enabled {
		t.Error("expected permission to be disabled")
	}

	_, found = cfg.GetToolPermission("dev", "srv1", "nonexistent")
	if found {
		t.Error("expected permission not to be found")
	}
}

func TestConfig_GetToolPermissionsForNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "ns01", Server: "srv1", ToolName: "tool1", Enabled: true},
		{Namespace: "ns01", Server: "srv2", ToolName: "tool2", Enabled: false},
		{Namespace: "ns02", Server: "srv1", ToolName: "tool1", Enabled: true},
	}

	perms := cfg.GetToolPermissionsForNamespace("ns01")
	if len(perms) != 2 {
		t.Errorf("expected 2 permissions, got %d", len(perms))
	}

	perms = cfg.GetToolPermissionsForNamespace("ns02")
	if len(perms) != 1 {
		t.Errorf("expected 1 permission, got %d", len(perms))
	}

	perms = cfg.GetToolPermissionsForNamespace("nonexistent")
	if len(perms) != 0 {
		t.Errorf("expected 0 permissions, got %d", len(perms))
	}
}

func TestConfig_NamespaceEntries(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["ns-a"] = NamespaceConfig{}
	cfg.Namespaces["ns-b"] = NamespaceConfig{}

	entries := cfg.NamespaceEntries()
	if len(entries) != 2 {
		t.Errorf("expected 2 namespaces, got %d", len(entries))
	}

	// Verify sorting
	if entries[0].Name > entries[1].Name {
		t.Error("expected entries to be sorted by name")
	}
}

// Test that tool permission field names use new format
func TestToolPermission_FieldNames(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}
	_ = cfg.SetToolPermission("dev", "srv1", "tool1", true)

	tp := cfg.ToolPermissions[0]

	// These should compile - verifying the field names are correct
	if tp.Namespace != "dev" {
		t.Errorf("expected Namespace 'dev', got %q", tp.Namespace)
	}
	if tp.Server != "srv1" {
		t.Errorf("expected Server 'srv1', got %q", tp.Server)
	}
	if tp.ToolName != "tool1" {
		t.Errorf("expected ToolName 'tool1', got %q", tp.ToolName)
	}
}

// Test that ValidateName rejects dots (used as namespace separator)
func TestValidateName_RejectsDots(t *testing.T) {
	err := ValidateName("server.name")
	if err == nil {
		t.Error("expected error for name with dot")
	}
	if !strings.Contains(err.Error(), ".") {
		t.Errorf("expected error to mention dot, got: %v", err)
	}
}

// Test that ValidateName rejects colons (used as resource URI separator)
func TestValidateName_RejectsColons(t *testing.T) {
	err := ValidateName("server:name")
	if err == nil {
		t.Error("expected error for name with colon")
	}
	if !strings.Contains(err.Error(), ":") {
		t.Errorf("expected error to mention colon, got: %v", err)
	}
}

// ============================================================================
// ServerConfig Validation Tests
// ============================================================================

func TestServerConfig_Validate_StdioValid(t *testing.T) {
	srv := ServerConfig{
		Command: "echo",
		Args:    []string{"hello"},
	}
	if err := srv.Validate(); err != nil {
		t.Errorf("expected valid stdio config, got error: %v", err)
	}
}

func TestServerConfig_Validate_HTTPValid(t *testing.T) {
	srv := ServerConfig{
		URL:               "https://example.com/mcp",
		BearerTokenEnvVar: "MY_TOKEN",
	}
	if err := srv.Validate(); err != nil {
		t.Errorf("expected valid http config, got error: %v", err)
	}
}

func TestServerConfig_Validate_BothCommandAndURL(t *testing.T) {
	srv := ServerConfig{
		Command: "echo",
		URL:     "https://example.com/mcp",
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error when both command and url are set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected error to mention mutually exclusive, got: %v", err)
	}
}

func TestServerConfig_Validate_NeitherCommandNorURL(t *testing.T) {
	srv := ServerConfig{
		Cwd: "/tmp",
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error when neither command nor url is set")
	}
	if !strings.Contains(err.Error(), "must set either") {
		t.Errorf("expected error to mention must set either, got: %v", err)
	}
}

func TestServerConfig_Validate_KindMismatch(t *testing.T) {
	// Kind says stdio but URL is set
	srv := ServerConfig{
		Kind: ServerKindStdio,
		URL:  "https://example.com/mcp",
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error for kind mismatch")
	}

	// Kind says HTTP but command is set
	srv = ServerConfig{
		Kind:    ServerKindStreamableHTTP,
		Command: "echo",
	}
	err = srv.Validate()
	if err == nil {
		t.Error("expected error for kind mismatch")
	}
}

func TestServerConfig_Validate_HTTPFieldsOnStdio(t *testing.T) {
	tests := []struct {
		name string
		srv  ServerConfig
	}{
		{
			name: "bearer_token_env_var",
			srv:  ServerConfig{Command: "echo", BearerTokenEnvVar: "TOKEN"},
		},
		{
			name: "http_headers",
			srv:  ServerConfig{Command: "echo", HTTPHeaders: map[string]string{"X-Custom": "value"}},
		},
		{
			name: "env_http_headers",
			srv:  ServerConfig{Command: "echo", EnvHTTPHeaders: map[string]string{"X-Custom": "ENV_VAR"}},
		},
		{
			name: "oauth",
			srv:  ServerConfig{Command: "echo", OAuth: &OAuthConfig{Scopes: []string{"read", "write"}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.srv.Validate()
			if err == nil {
				t.Errorf("expected error for %s on stdio server", tc.name)
			}
		})
	}
}

func TestServerConfig_Validate_StdioFieldsOnHTTP(t *testing.T) {
	srv := ServerConfig{
		URL:  "https://example.com/mcp",
		Args: []string{"arg1", "arg2"},
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error for args on http server")
	}
	if !strings.Contains(err.Error(), "args is only valid for stdio") {
		t.Errorf("expected error to mention args, got: %v", err)
	}
}

func TestConfig_Validate_AllServers(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["valid-stdio"] = ServerConfig{Command: "echo"}
	cfg.Servers["valid-http"] = ServerConfig{URL: "https://example.com/mcp"}

	if err := cfg.Validate(); err != nil {
		t.Errorf("expected config to be valid, got error: %v", err)
	}
}

func TestConfig_Validate_InvalidServer(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["valid"] = ServerConfig{Command: "echo"}
	cfg.Servers["invalid"] = ServerConfig{} // Neither command nor URL

	err := cfg.Validate()
	if err == nil {
		t.Error("expected error for invalid server")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error to mention server name, got: %v", err)
	}
}

func TestLoad_InvalidServerConfig(t *testing.T) {
	home := testutil.SetupTestHome(t)

	// Write a config with invalid server (both command and url)
	configJSON := `{
		"schemaVersion": 1,
		"servers": {
			"invalid-server": {
				"command": "echo",
				"url": "https://example.com/mcp"
			}
		}
	}`

	configPath := filepath.Join(home, ".config", "mcpmu", "config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for invalid config")
	}
	if !strings.Contains(err.Error(), "invalid config") {
		t.Errorf("expected error to mention invalid config, got: %v", err)
	}
}

func TestAddServer_ValidatesConfig(t *testing.T) {
	cfg := NewConfig()

	// Valid server should succeed
	err := cfg.AddServer("valid", ServerConfig{Command: "echo"})
	if err != nil {
		t.Errorf("expected valid server to be added, got error: %v", err)
	}

	// Invalid server (no command or url) should fail
	err = cfg.AddServer("invalid", ServerConfig{})
	if err == nil {
		t.Error("expected error for invalid server config")
	}
	if !strings.Contains(err.Error(), "invalid server config") {
		t.Errorf("expected error to mention invalid server config, got: %v", err)
	}
}

func TestUpdateServer_ValidatesConfig(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["test"] = ServerConfig{Command: "echo"}

	// Valid update should succeed
	err := cfg.UpdateServer("test", ServerConfig{Command: "cat"})
	if err != nil {
		t.Errorf("expected valid update, got error: %v", err)
	}

	// Invalid update should fail
	err = cfg.UpdateServer("test", ServerConfig{})
	if err == nil {
		t.Error("expected error for invalid server config")
	}
}

// ============================================================================
// Server Default Tests
// ============================================================================

func TestConfig_SetServerDefault(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{ServerIDs: []string{}}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	// Set server default
	err := cfg.SetServerDefault("dev", "srv1", true)
	if err != nil {
		t.Fatalf("SetServerDefault failed: %v", err)
	}

	ns, _ := cfg.GetNamespace("dev")
	if ns.ServerDefaults == nil {
		t.Fatal("expected ServerDefaults to be initialized")
	}
	if val, ok := ns.ServerDefaults["srv1"]; !ok || !val {
		t.Error("expected srv1 server default to be true")
	}

	// Overwrite
	err = cfg.SetServerDefault("dev", "srv1", false)
	if err != nil {
		t.Fatalf("SetServerDefault overwrite failed: %v", err)
	}
	ns, _ = cfg.GetNamespace("dev")
	if val, ok := ns.ServerDefaults["srv1"]; !ok || val {
		t.Error("expected srv1 server default to be false after overwrite")
	}

	// Round-trip save/load
	err = Save(cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	loadedNs, ok := loaded.GetNamespace("dev")
	if !ok {
		t.Fatal("expected to find namespace 'dev' after load")
	}
	if val, ok := loadedNs.ServerDefaults["srv1"]; !ok || val {
		t.Error("expected srv1 server default to survive save/load round-trip")
	}
}

func TestConfig_SetServerDefault_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	// Non-existent namespace
	err := cfg.SetServerDefault("nonexistent", "srv1", true)
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}

	// Non-existent server
	err = cfg.SetServerDefault("dev", "nonexistent", true)
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_GetServerDefault(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{
		ServerDefaults: map[string]bool{"srv1": true},
	}

	// Found
	val, found := cfg.GetServerDefault("dev", "srv1")
	if !found {
		t.Error("expected to find server default")
	}
	if !val {
		t.Error("expected server default to be true")
	}

	// Not found
	_, found = cfg.GetServerDefault("dev", "srv2")
	if found {
		t.Error("expected not to find server default for srv2")
	}

	// Nil map
	cfg.Namespaces["empty"] = NamespaceConfig{}
	_, found = cfg.GetServerDefault("empty", "srv1")
	if found {
		t.Error("expected not to find server default with nil map")
	}

	// Non-existent namespace
	_, found = cfg.GetServerDefault("nonexistent", "srv1")
	if found {
		t.Error("expected not to find server default for non-existent namespace")
	}
}

func TestConfig_UnsetServerDefault(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{
		ServerDefaults: map[string]bool{"srv1": true, "srv2": false},
	}

	// Remove
	err := cfg.UnsetServerDefault("dev", "srv1")
	if err != nil {
		t.Fatalf("UnsetServerDefault failed: %v", err)
	}
	ns, _ := cfg.GetNamespace("dev")
	if _, ok := ns.ServerDefaults["srv1"]; ok {
		t.Error("expected srv1 to be removed")
	}
	if _, ok := ns.ServerDefaults["srv2"]; !ok {
		t.Error("expected srv2 to still exist")
	}

	// Idempotent
	err = cfg.UnsetServerDefault("dev", "srv1")
	if err != nil {
		t.Fatalf("second UnsetServerDefault should not error: %v", err)
	}

	// Remove last entry - map should be nil
	err = cfg.UnsetServerDefault("dev", "srv2")
	if err != nil {
		t.Fatalf("UnsetServerDefault for srv2 failed: %v", err)
	}
	ns, _ = cfg.GetNamespace("dev")
	if ns.ServerDefaults != nil {
		t.Error("expected ServerDefaults to be nil after removing all entries")
	}
}

func TestConfig_UnassignServer_CleansUpServerDefault(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{
		ServerIDs:      []string{"srv1", "srv2"},
		ServerDefaults: map[string]bool{"srv1": true, "srv2": false},
	}

	err := cfg.UnassignServerFromNamespace("dev", "srv1")
	if err != nil {
		t.Fatalf("UnassignServerFromNamespace failed: %v", err)
	}

	ns, _ := cfg.GetNamespace("dev")
	if _, ok := ns.ServerDefaults["srv1"]; ok {
		t.Error("expected srv1 server default to be removed on unassign")
	}
	if _, ok := ns.ServerDefaults["srv2"]; !ok {
		t.Error("expected srv2 server default to be preserved")
	}
}

// ============================================================================
// OAuth Config Tests
// ============================================================================

func TestServerConfig_Validate_OAuthOnStdio(t *testing.T) {
	srv := ServerConfig{
		Command: "echo",
		OAuth:   &OAuthConfig{ClientID: "test"},
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error for oauth on stdio server")
	}
	if !strings.Contains(err.Error(), "oauth is only valid for http") {
		t.Errorf("expected error about oauth, got: %v", err)
	}
}

func TestServerConfig_Validate_BearerAndOAuthMutuallyExclusive(t *testing.T) {
	srv := ServerConfig{
		URL:               "https://example.com/mcp",
		BearerTokenEnvVar: "TOKEN",
		OAuth:             &OAuthConfig{ClientID: "test"},
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error for bearer + oauth")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected error about mutually exclusive, got: %v", err)
	}
}

func TestServerConfig_Validate_OAuthCallbackPortRange(t *testing.T) {
	invalidPort := 0
	srv := ServerConfig{
		URL:   "https://example.com/mcp",
		OAuth: &OAuthConfig{CallbackPort: &invalidPort},
	}
	err := srv.Validate()
	if err == nil {
		t.Error("expected error for invalid callback port")
	}
	if !strings.Contains(err.Error(), "callback_port must be 1-65535") {
		t.Errorf("expected port range error, got: %v", err)
	}

	highPort := 70000
	srv.OAuth.CallbackPort = &highPort
	err = srv.Validate()
	if err == nil {
		t.Error("expected error for port > 65535")
	}

	validPort := 3118
	srv.OAuth.CallbackPort = &validPort
	err = srv.Validate()
	if err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}
}

func TestServerConfig_Validate_OAuthValidHTTP(t *testing.T) {
	port := 3118
	srv := ServerConfig{
		URL: "https://example.com/mcp",
		OAuth: &OAuthConfig{
			ClientID:     "my-client-id",
			CallbackPort: &port,
			Scopes:       []string{"read", "write"},
		},
	}
	if err := srv.Validate(); err != nil {
		t.Errorf("expected valid oauth config, got: %v", err)
	}
}

func TestServerConfig_UnmarshalJSON_MigrateFlatFields(t *testing.T) {
	// Old config with flat scopes and oauth_client_id
	jsonData := `{
		"url": "https://example.com/mcp",
		"scopes": ["read", "write"],
		"oauth_client_id": "old-client-id"
	}`

	var srv ServerConfig
	if err := json.Unmarshal([]byte(jsonData), &srv); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if srv.OAuth == nil {
		t.Fatal("expected OAuth to be populated from flat fields")
	}
	if srv.OAuth.ClientID != "old-client-id" {
		t.Errorf("expected ClientID 'old-client-id', got %q", srv.OAuth.ClientID)
	}
	if len(srv.OAuth.Scopes) != 2 || srv.OAuth.Scopes[0] != "read" {
		t.Errorf("expected Scopes [read write], got %v", srv.OAuth.Scopes)
	}
}

func TestServerConfig_UnmarshalJSON_NestedTakesPrecedence(t *testing.T) {
	// Both flat and nested present - nested should win
	jsonData := `{
		"url": "https://example.com/mcp",
		"scopes": ["old-scope"],
		"oauth_client_id": "old-id",
		"oauth": {
			"client_id": "new-id",
			"scopes": ["new-scope"]
		}
	}`

	var srv ServerConfig
	if err := json.Unmarshal([]byte(jsonData), &srv); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if srv.OAuth == nil {
		t.Fatal("expected OAuth to be populated")
	}
	if srv.OAuth.ClientID != "new-id" {
		t.Errorf("expected nested ClientID to take precedence, got %q", srv.OAuth.ClientID)
	}
	if len(srv.OAuth.Scopes) != 1 || srv.OAuth.Scopes[0] != "new-scope" {
		t.Errorf("expected nested Scopes to take precedence, got %v", srv.OAuth.Scopes)
	}
}

func TestServerConfig_UnmarshalJSON_NestedOnly(t *testing.T) {
	jsonData := `{
		"url": "https://example.com/mcp",
		"oauth": {
			"client_id": "my-id",
			"client_secret": "my-secret",
			"callback_port": 3118,
			"scopes": ["channels:read"]
		}
	}`

	var srv ServerConfig
	if err := json.Unmarshal([]byte(jsonData), &srv); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if srv.OAuth == nil {
		t.Fatal("expected OAuth to be populated")
	}
	if srv.OAuth.ClientID != "my-id" {
		t.Errorf("expected ClientID 'my-id', got %q", srv.OAuth.ClientID)
	}
	if srv.OAuth.ClientSecret != "my-secret" {
		t.Errorf("expected ClientSecret 'my-secret', got %q", srv.OAuth.ClientSecret)
	}
	if srv.OAuth.CallbackPort == nil || *srv.OAuth.CallbackPort != 3118 {
		t.Errorf("expected CallbackPort 3118, got %v", srv.OAuth.CallbackPort)
	}
	if len(srv.OAuth.Scopes) != 1 || srv.OAuth.Scopes[0] != "channels:read" {
		t.Errorf("expected Scopes [channels:read], got %v", srv.OAuth.Scopes)
	}
}

func TestConfig_OAuthRoundTrip(t *testing.T) {
	testutil.SetupTestHome(t)

	port := 3118
	cfg := NewConfig()
	cfg.Servers["slack"] = ServerConfig{
		URL: "https://mcp.slack.com/mcp",
		OAuth: &OAuthConfig{
			ClientID:     "1601185624273.8899143856786",
			CallbackPort: &port,
			Scopes:       []string{"channels:read"},
		},
	}

	// Save and reload
	err := Save(cfg)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	srv, ok := loaded.GetServer("slack")
	if !ok {
		t.Fatal("expected server 'slack' to exist")
	}
	if srv.OAuth == nil {
		t.Fatal("expected OAuth config to survive round-trip")
	}
	if srv.OAuth.ClientID != "1601185624273.8899143856786" {
		t.Errorf("ClientID: got %q", srv.OAuth.ClientID)
	}
	if srv.OAuth.CallbackPort == nil || *srv.OAuth.CallbackPort != 3118 {
		t.Errorf("CallbackPort: got %v", srv.OAuth.CallbackPort)
	}
	if len(srv.OAuth.Scopes) != 1 || srv.OAuth.Scopes[0] != "channels:read" {
		t.Errorf("Scopes: got %v", srv.OAuth.Scopes)
	}
}

func TestConfig_BackwardCompatMigrationRoundTrip(t *testing.T) {
	home := testutil.SetupTestHome(t)

	// Write config in old format with flat fields
	configJSON := `{
		"schemaVersion": 1,
		"servers": {
			"atlassian": {
				"url": "https://mcp.atlassian.com/mcp",
				"scopes": ["read", "write"],
				"oauth_client_id": "old-client"
			}
		}
	}`

	configPath := filepath.Join(home, ".config", "mcpmu", "config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	srv, ok := cfg.GetServer("atlassian")
	if !ok {
		t.Fatal("expected server 'atlassian' to exist")
	}
	if srv.OAuth == nil {
		t.Fatal("expected flat fields to be migrated into OAuth")
	}
	if srv.OAuth.ClientID != "old-client" {
		t.Errorf("expected migrated ClientID 'old-client', got %q", srv.OAuth.ClientID)
	}
	if len(srv.OAuth.Scopes) != 2 {
		t.Errorf("expected migrated Scopes [read write], got %v", srv.OAuth.Scopes)
	}
}

// ============================================================================
// DeniedTools / Global Deny Tests
// ============================================================================

func TestServerConfig_IsToolDenied(t *testing.T) {
	srv := ServerConfig{Command: "echo", DeniedTools: []string{"delete_file", "move_file"}}
	if !srv.IsToolDenied("delete_file") {
		t.Error("expected delete_file to be denied")
	}
	if !srv.IsToolDenied("move_file") {
		t.Error("expected move_file to be denied")
	}
	if srv.IsToolDenied("read_file") {
		t.Error("expected read_file to NOT be denied")
	}

	// Empty deny list
	empty := ServerConfig{Command: "echo"}
	if empty.IsToolDenied("anything") {
		t.Error("expected no tools denied on empty list")
	}
}

func TestConfig_DenyTool(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	// Deny a tool
	if err := cfg.DenyTool("srv1", "delete_file"); err != nil {
		t.Fatalf("DenyTool failed: %v", err)
	}
	srv := cfg.Servers["srv1"]
	if len(srv.DeniedTools) != 1 || srv.DeniedTools[0] != "delete_file" {
		t.Errorf("expected [delete_file], got %v", srv.DeniedTools)
	}

	// Idempotent: deny same tool again
	if err := cfg.DenyTool("srv1", "delete_file"); err != nil {
		t.Fatalf("DenyTool (idempotent) failed: %v", err)
	}
	srv = cfg.Servers["srv1"]
	if len(srv.DeniedTools) != 1 {
		t.Errorf("expected idempotent deny, got %v", srv.DeniedTools)
	}

	// Deny another tool — result should be sorted
	if err := cfg.DenyTool("srv1", "a_tool"); err != nil {
		t.Fatalf("DenyTool failed: %v", err)
	}
	srv = cfg.Servers["srv1"]
	if len(srv.DeniedTools) != 2 || srv.DeniedTools[0] != "a_tool" || srv.DeniedTools[1] != "delete_file" {
		t.Errorf("expected sorted [a_tool, delete_file], got %v", srv.DeniedTools)
	}
}

func TestConfig_DenyTool_ServerNotFound(t *testing.T) {
	cfg := NewConfig()
	err := cfg.DenyTool("nonexistent", "tool")
	if err == nil {
		t.Fatal("expected error for non-existent server")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestConfig_AllowTool(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo", DeniedTools: []string{"delete_file", "move_file"}}

	// Remove a denied tool
	if err := cfg.AllowTool("srv1", "delete_file"); err != nil {
		t.Fatalf("AllowTool failed: %v", err)
	}
	srv := cfg.Servers["srv1"]
	if len(srv.DeniedTools) != 1 || srv.DeniedTools[0] != "move_file" {
		t.Errorf("expected [move_file], got %v", srv.DeniedTools)
	}

	// Remove non-denied tool (no-op)
	if err := cfg.AllowTool("srv1", "read_file"); err != nil {
		t.Fatalf("AllowTool (no-op) failed: %v", err)
	}
	srv = cfg.Servers["srv1"]
	if len(srv.DeniedTools) != 1 {
		t.Errorf("expected no-op, got %v", srv.DeniedTools)
	}

	// Remove last tool — DeniedTools becomes nil
	if err := cfg.AllowTool("srv1", "move_file"); err != nil {
		t.Fatalf("AllowTool failed: %v", err)
	}
	srv = cfg.Servers["srv1"]
	if srv.DeniedTools != nil {
		t.Errorf("expected nil DeniedTools, got %v", srv.DeniedTools)
	}
}

func TestConfig_GetDeniedTools(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo", DeniedTools: []string{"z_tool", "a_tool"}}

	tools, err := cfg.GetDeniedTools("srv1")
	if err != nil {
		t.Fatalf("GetDeniedTools failed: %v", err)
	}
	if len(tools) != 2 || tools[0] != "a_tool" || tools[1] != "z_tool" {
		t.Errorf("expected sorted [a_tool, z_tool], got %v", tools)
	}

	// Returns copy — modifying shouldn't affect config
	tools[0] = "modified"
	srv := cfg.Servers["srv1"]
	if srv.DeniedTools[0] == "modified" {
		t.Error("GetDeniedTools should return a copy")
	}
}

func TestConfig_GetDeniedTools_ServerNotFound(t *testing.T) {
	cfg := NewConfig()
	_, err := cfg.GetDeniedTools("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent server")
	}
}

func TestConfig_RenameServer_PreservesDeniedTools(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["old"] = ServerConfig{Command: "echo", DeniedTools: []string{"delete_file"}}

	if err := cfg.RenameServer("old", "new"); err != nil {
		t.Fatalf("RenameServer failed: %v", err)
	}

	srv, ok := cfg.Servers["new"]
	if !ok {
		t.Fatal("expected new server to exist")
	}
	if len(srv.DeniedTools) != 1 || srv.DeniedTools[0] != "delete_file" {
		t.Errorf("expected DeniedTools to travel with server, got %v", srv.DeniedTools)
	}
}

func TestConfig_DeleteServer_CleansDeniedTools(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo", DeniedTools: []string{"delete_file"}}

	if err := cfg.DeleteServer("srv1"); err != nil {
		t.Fatalf("DeleteServer failed: %v", err)
	}

	if _, ok := cfg.Servers["srv1"]; ok {
		t.Error("expected server to be deleted")
	}
}

func TestLoad_ServerWithDeniedTools(t *testing.T) {
	home := testutil.SetupTestHome(t)

	configJSON := `{
		"schemaVersion": 1,
		"servers": {
			"filesystem": {
				"command": "echo",
				"deniedTools": ["delete_file", "move_file"]
			}
		}
	}`

	configPath := filepath.Join(home, ".config", "mcpmu", "config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	srv, ok := cfg.GetServer("filesystem")
	if !ok {
		t.Fatal("expected server 'filesystem' to exist")
	}
	if len(srv.DeniedTools) != 2 {
		t.Errorf("expected 2 denied tools, got %d", len(srv.DeniedTools))
	}
	if !srv.IsToolDenied("delete_file") {
		t.Error("expected delete_file to be denied")
	}
	if !srv.IsToolDenied("move_file") {
		t.Error("expected move_file to be denied")
	}
}

func TestSave_ServerWithDeniedTools(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo", DeniedTools: []string{"delete_file"}}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load after Save failed: %v", err)
	}

	srv := loaded.Servers["srv1"]
	if len(srv.DeniedTools) != 1 || srv.DeniedTools[0] != "delete_file" {
		t.Errorf("expected [delete_file] after round-trip, got %v", srv.DeniedTools)
	}
}

func TestSave_ServerWithDeniedTools_OmittedWhenEmpty(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := NewConfig()
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	if err := Save(cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Read raw JSON and verify no deniedTools key
	path, _ := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if strings.Contains(string(data), "deniedTools") {
		t.Error("expected omitempty to suppress deniedTools key in JSON")
	}
}
