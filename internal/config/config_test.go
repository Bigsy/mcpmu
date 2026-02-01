package config

import (
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
			enabled:  boolPtr(true),
			expected: true,
		},
		{
			name:     "false means disabled",
			enabled:  boolPtr(false),
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
		{"", true},         // empty
		{"has.dot", true},  // dot not allowed (used as namespace separator)
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

	srv := cfg.GetServer("test")
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
	cfg.Namespaces["ns1"] = NamespaceConfig{ServerIDs: []string{"srv1", "srv2"}}
	cfg.ToolPermissions = []ToolPermission{
		{Namespace: "ns1", Server: "srv1", ToolName: "tool1", Enabled: true},
		{Namespace: "ns1", Server: "srv2", ToolName: "tool2", Enabled: true},
	}

	err := cfg.DeleteServer("srv1")
	if err != nil {
		t.Fatalf("DeleteServer failed: %v", err)
	}

	// Check namespace reference was removed
	ns := cfg.GetNamespace("ns1")
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv2" {
		t.Error("expected srv1 to be removed from namespace")
	}

	// Check tool permission was removed
	if len(cfg.ToolPermissions) != 1 {
		t.Error("expected srv1 tool permissions to be removed")
	}
}

func TestConfig_RenameServer(t *testing.T) {
	cfg := NewConfig()
	cfg.Servers["old-name"] = ServerConfig{Command: "echo"}
	cfg.Namespaces["ns1"] = NamespaceConfig{ServerIDs: []string{"old-name", "other"}}
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
	ns := cfg.GetNamespace("ns1")
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

	srv := cfg.GetServer("test")
	if srv == nil {
		t.Fatal("expected server to be found")
	}
	if srv.Command != "echo" {
		t.Errorf("expected command 'echo', got %q", srv.Command)
	}

	// Non-existent
	srv = cfg.GetServer("nonexistent")
	if srv != nil {
		t.Error("expected nil for non-existent server")
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

func boolPtr(b bool) *bool {
	return &b
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

	ns := cfg.GetNamespace("development")
	if ns == nil {
		t.Fatal("expected namespace to be found")
	}
	if ns.Description != "Dev env" {
		t.Errorf("expected description 'Dev env', got %q", ns.Description)
	}

	ns = cfg.GetNamespace("nonexistent")
	if ns != nil {
		t.Error("expected nil for non-existent namespace")
	}
}

func TestConfig_UpdateNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["development"] = NamespaceConfig{Description: "Old"}

	err := cfg.UpdateNamespace("development", NamespaceConfig{Description: "New"})
	if err != nil {
		t.Fatalf("UpdateNamespace failed: %v", err)
	}

	ns := cfg.GetNamespace("development")
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

func TestConfig_AssignServerToNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces["dev"] = NamespaceConfig{ServerIDs: []string{}}
	cfg.Servers["srv1"] = ServerConfig{Command: "echo"}

	err := cfg.AssignServerToNamespace("dev", "srv1")
	if err != nil {
		t.Fatalf("AssignServerToNamespace failed: %v", err)
	}

	ns := cfg.GetNamespace("dev")
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv1" {
		t.Error("expected server to be assigned")
	}

	// Assigning again should be a no-op
	err = cfg.AssignServerToNamespace("dev", "srv1")
	if err != nil {
		t.Fatalf("second AssignServerToNamespace failed: %v", err)
	}
	ns = cfg.GetNamespace("dev")
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

	ns := cfg.GetNamespace("dev")
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
