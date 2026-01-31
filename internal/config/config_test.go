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

	// Write a valid config
	configJSON := `{
		"schemaVersion": 1,
		"servers": {
			"test": {
				"id": "test",
				"name": "Test Server",
				"kind": "stdio",
				"command": "echo"
			}
		},
		"namespaces": []
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

	srv, ok := cfg.Servers["test"]
	if !ok {
		t.Fatal("expected server 'test' to exist")
	}

	if srv.Name != "Test Server" {
		t.Errorf("expected name 'Test Server', got %q", srv.Name)
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

func TestLoad_BackfillsServerID(t *testing.T) {
	home := testutil.SetupTestHome(t)

	// Config where server ID is only in the map key, not in the object
	configJSON := `{
		"schemaVersion": 1,
		"servers": {
			"abcd": {
				"name": "Test Server",
				"kind": "stdio",
				"command": "echo"
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

	srv := cfg.Servers["abcd"]
	if srv.ID != "abcd" {
		t.Errorf("expected ID to be backfilled to 'abcd', got %q", srv.ID)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	testutil.SetupTestHome(t)

	cfg := NewConfig()
	cfg.Servers["test"] = ServerConfig{
		ID:      "test",
		Name:    "Test Server",
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
	os.RemoveAll(filepath.Dir(path))

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

func TestValidateID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"abcd", false},
		{"1234", false},
		{"a1b2", false},
		{"abc", true},      // too short
		{"abcde", true},    // too long
		{"ABCD", true},     // uppercase not allowed
		{"ab.c", true},     // dot not allowed
		{"ab-c", true},     // hyphen not allowed
		{"ab_c", true},     // underscore not allowed
		{"", true},         // empty
		{"    ", true},     // spaces
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			err := ValidateID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestGenerateID(t *testing.T) {
	id := GenerateID()

	if err := ValidateID(id); err != nil {
		t.Errorf("GenerateID() produced invalid ID %q: %v", id, err)
	}

	// Generate multiple IDs and check for uniqueness
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateID()
		if ids[id] {
			t.Errorf("GenerateID() produced duplicate: %q", id)
		}
		ids[id] = true
	}
}

func TestConfig_AddServer(t *testing.T) {
	cfg := NewConfig()

	srv := ServerConfig{
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	id, err := cfg.AddServer(srv)
	if err != nil {
		t.Fatalf("AddServer failed: %v", err)
	}

	if id == "" {
		t.Error("expected non-empty ID")
	}

	if _, ok := cfg.Servers[id]; !ok {
		t.Error("expected server to be added to config")
	}
}

func TestConfig_AddServer_WithID(t *testing.T) {
	cfg := NewConfig()

	srv := ServerConfig{
		ID:      "abcd",
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	id, err := cfg.AddServer(srv)
	if err != nil {
		t.Fatalf("AddServer failed: %v", err)
	}

	if id != "abcd" {
		t.Errorf("expected ID 'abcd', got %q", id)
	}
}

func TestConfig_AddServer_Collision(t *testing.T) {
	cfg := NewConfig()

	srv := ServerConfig{
		ID:      "abcd",
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	_, err := cfg.AddServer(srv)
	if err != nil {
		t.Fatalf("first AddServer failed: %v", err)
	}

	// Try to add another server with same ID
	srv2 := ServerConfig{
		ID:      "abcd",
		Name:    "Different Name",
		Kind:    ServerKindStdio,
		Command: "echo",
	}
	_, err = cfg.AddServer(srv2)
	if err == nil {
		t.Error("expected error for duplicate ID")
	}
}

func TestConfig_AddServer_DuplicateName(t *testing.T) {
	cfg := NewConfig()

	srv1 := ServerConfig{
		Name:    "My Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	_, err := cfg.AddServer(srv1)
	if err != nil {
		t.Fatalf("first AddServer failed: %v", err)
	}

	// Try to add another server with same name (different ID)
	srv2 := ServerConfig{
		Name:    "My Server",
		Kind:    ServerKindStdio,
		Command: "cat",
	}
	_, err = cfg.AddServer(srv2)
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestConfig_FindServerByName(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["abcd"] = ServerConfig{
		ID:      "abcd",
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	// Found
	srv := cfg.FindServerByName("Test Server")
	if srv == nil {
		t.Fatal("expected server to be found")
	}
	if srv.ID != "abcd" {
		t.Errorf("expected ID 'abcd', got %q", srv.ID)
	}

	// Not found
	srv = cfg.FindServerByName("Nonexistent")
	if srv != nil {
		t.Error("expected nil for non-existent server name")
	}
}

func TestConfig_DeleteServerByName(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["abcd"] = ServerConfig{
		ID:      "abcd",
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	err := cfg.DeleteServerByName("Test Server")
	if err != nil {
		t.Fatalf("DeleteServerByName failed: %v", err)
	}

	if _, ok := cfg.Servers["abcd"]; ok {
		t.Error("expected server to be deleted")
	}
}

func TestConfig_DeleteServerByName_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.DeleteServerByName("Nonexistent")
	if err == nil {
		t.Error("expected error for non-existent server name")
	}
}

func TestConfig_DeleteServer(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["abcd"] = ServerConfig{
		ID:      "abcd",
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	err := cfg.DeleteServer("abcd")
	if err != nil {
		t.Fatalf("DeleteServer failed: %v", err)
	}

	if _, ok := cfg.Servers["abcd"]; ok {
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

func TestConfig_GetServer(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["abcd"] = ServerConfig{
		ID:      "abcd",
		Name:    "Test Server",
		Kind:    ServerKindStdio,
		Command: "echo",
	}

	srv := cfg.GetServer("abcd")
	if srv == nil {
		t.Fatal("expected server to be found")
	}
	if srv.Name != "Test Server" {
		t.Errorf("expected name 'Test Server', got %q", srv.Name)
	}

	// Non-existent
	srv = cfg.GetServer("nonexistent")
	if srv != nil {
		t.Error("expected nil for non-existent server")
	}
}

func TestConfig_ServerList(t *testing.T) {
	cfg := NewConfig()

	cfg.Servers["abcd"] = ServerConfig{ID: "abcd", Name: "Server A"}
	cfg.Servers["efgh"] = ServerConfig{ID: "efgh", Name: "Server B"}

	list := cfg.ServerList()
	if len(list) != 2 {
		t.Errorf("expected 2 servers in list, got %d", len(list))
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
		Name:        "development",
		Description: "Dev environment",
	}

	id, err := cfg.AddNamespace(ns)
	if err != nil {
		t.Fatalf("AddNamespace failed: %v", err)
	}

	if id == "" {
		t.Error("expected non-empty ID")
	}

	if len(cfg.Namespaces) != 1 {
		t.Error("expected namespace to be added to config")
	}
}

func TestConfig_AddNamespace_DuplicateName(t *testing.T) {
	cfg := NewConfig()

	ns1 := NamespaceConfig{Name: "dev"}
	_, err := cfg.AddNamespace(ns1)
	if err != nil {
		t.Fatalf("first AddNamespace failed: %v", err)
	}

	ns2 := NamespaceConfig{Name: "dev"}
	_, err = cfg.AddNamespace(ns2)
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestConfig_FindNamespaceByName(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "abcd", Name: "development"},
		{ID: "efgh", Name: "production"},
	}

	ns := cfg.FindNamespaceByName("development")
	if ns == nil {
		t.Fatal("expected namespace to be found")
	}
	if ns.ID != "abcd" {
		t.Errorf("expected ID 'abcd', got %q", ns.ID)
	}

	ns = cfg.FindNamespaceByName("nonexistent")
	if ns != nil {
		t.Error("expected nil for non-existent namespace")
	}
}

func TestConfig_FindNamespaceByID(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "abcd", Name: "development"},
	}

	ns := cfg.FindNamespaceByID("abcd")
	if ns == nil {
		t.Fatal("expected namespace to be found")
	}
	if ns.Name != "development" {
		t.Errorf("expected name 'development', got %q", ns.Name)
	}
}

func TestConfig_DeleteNamespaceByName(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "abcd", Name: "development"},
	}
	cfg.ToolPermissions = []ToolPermission{
		{NamespaceID: "abcd", ServerID: "srv1", ToolName: "tool1", Enabled: true},
	}
	cfg.DefaultNamespaceID = "abcd"

	err := cfg.DeleteNamespaceByName("development")
	if err != nil {
		t.Fatalf("DeleteNamespaceByName failed: %v", err)
	}

	if len(cfg.Namespaces) != 0 {
		t.Error("expected namespace to be deleted")
	}

	if len(cfg.ToolPermissions) != 0 {
		t.Error("expected tool permissions to be cleaned up")
	}

	if cfg.DefaultNamespaceID != "" {
		t.Error("expected default namespace to be cleared")
	}
}

func TestConfig_DeleteNamespaceByName_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.DeleteNamespaceByName("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}
}

func TestConfig_UpdateNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "development", Description: "Dev env"},
	}

	// Update description - should succeed
	err := cfg.UpdateNamespace(NamespaceConfig{
		ID:          "ns01",
		Name:        "development",
		Description: "Updated description",
	})
	if err != nil {
		t.Fatalf("UpdateNamespace failed: %v", err)
	}

	ns := cfg.FindNamespaceByID("ns01")
	if ns.Description != "Updated description" {
		t.Errorf("expected description 'Updated description', got %q", ns.Description)
	}
}

func TestConfig_UpdateNamespace_DuplicateName(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "development"},
		{ID: "ns02", Name: "production"},
	}

	// Try to rename ns01 to "production" - should fail
	err := cfg.UpdateNamespace(NamespaceConfig{
		ID:   "ns01",
		Name: "production",
	})
	if err == nil {
		t.Error("expected error when renaming to duplicate name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}

	// Verify ns01 wasn't changed
	ns := cfg.FindNamespaceByID("ns01")
	if ns.Name != "development" {
		t.Errorf("namespace should not have been renamed, got name %q", ns.Name)
	}
}

func TestConfig_UpdateNamespace_SameName(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "development", Description: "Old"},
	}

	// Update with same name - should succeed
	err := cfg.UpdateNamespace(NamespaceConfig{
		ID:          "ns01",
		Name:        "development",
		Description: "New",
	})
	if err != nil {
		t.Fatalf("UpdateNamespace with same name should succeed: %v", err)
	}

	ns := cfg.FindNamespaceByID("ns01")
	if ns.Description != "New" {
		t.Errorf("expected description 'New', got %q", ns.Description)
	}
}

func TestConfig_UpdateNamespace_NotFound(t *testing.T) {
	cfg := NewConfig()

	err := cfg.UpdateNamespace(NamespaceConfig{
		ID:   "nonexistent",
		Name: "test",
	})
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}
}

func TestConfig_AssignServerToNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "dev", ServerIDs: []string{}},
	}
	cfg.Servers["srv1"] = ServerConfig{ID: "srv1", Name: "Server 1"}

	err := cfg.AssignServerToNamespace("ns01", "srv1")
	if err != nil {
		t.Fatalf("AssignServerToNamespace failed: %v", err)
	}

	ns := cfg.FindNamespaceByID("ns01")
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv1" {
		t.Error("expected server to be assigned")
	}

	// Assigning again should be a no-op
	err = cfg.AssignServerToNamespace("ns01", "srv1")
	if err != nil {
		t.Fatalf("second AssignServerToNamespace failed: %v", err)
	}
	if len(ns.ServerIDs) != 1 {
		t.Error("expected no duplicate assignment")
	}
}

func TestConfig_AssignServerToNamespace_Errors(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "dev", ServerIDs: []string{}},
	}

	// Non-existent namespace
	err := cfg.AssignServerToNamespace("nonexistent", "srv1")
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}

	// Non-existent server
	err = cfg.AssignServerToNamespace("ns01", "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_UnassignServerFromNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "dev", ServerIDs: []string{"srv1", "srv2"}},
	}

	err := cfg.UnassignServerFromNamespace("ns01", "srv1")
	if err != nil {
		t.Fatalf("UnassignServerFromNamespace failed: %v", err)
	}

	ns := cfg.FindNamespaceByID("ns01")
	if len(ns.ServerIDs) != 1 || ns.ServerIDs[0] != "srv2" {
		t.Error("expected server to be unassigned")
	}
}

// ============================================================================
// Tool Permission Tests
// ============================================================================

func TestConfig_SetToolPermission(t *testing.T) {
	cfg := NewConfig()
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "dev"},
	}
	cfg.Servers["srv1"] = ServerConfig{ID: "srv1", Name: "Server 1"}

	err := cfg.SetToolPermission("ns01", "srv1", "read_file", true)
	if err != nil {
		t.Fatalf("SetToolPermission failed: %v", err)
	}

	if len(cfg.ToolPermissions) != 1 {
		t.Fatal("expected 1 permission")
	}

	tp := cfg.ToolPermissions[0]
	if tp.NamespaceID != "ns01" || tp.ServerID != "srv1" || tp.ToolName != "read_file" || !tp.Enabled {
		t.Error("permission not set correctly")
	}

	// Update existing permission
	err = cfg.SetToolPermission("ns01", "srv1", "read_file", false)
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
	cfg.Namespaces = []NamespaceConfig{
		{ID: "ns01", Name: "dev"},
	}
	cfg.Servers["srv1"] = ServerConfig{ID: "srv1", Name: "Server 1"}

	// Non-existent namespace
	err := cfg.SetToolPermission("nonexistent", "srv1", "tool", true)
	if err == nil {
		t.Error("expected error for non-existent namespace")
	}

	// Non-existent server
	err = cfg.SetToolPermission("ns01", "nonexistent", "tool", true)
	if err == nil {
		t.Error("expected error for non-existent server")
	}
}

func TestConfig_UnsetToolPermission(t *testing.T) {
	cfg := NewConfig()
	cfg.ToolPermissions = []ToolPermission{
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "read_file", Enabled: true},
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "write_file", Enabled: false},
	}

	err := cfg.UnsetToolPermission("ns01", "srv1", "read_file")
	if err != nil {
		t.Fatalf("UnsetToolPermission failed: %v", err)
	}

	if len(cfg.ToolPermissions) != 1 {
		t.Error("expected 1 permission remaining")
	}

	// Unset non-existent is not an error
	err = cfg.UnsetToolPermission("ns01", "srv1", "nonexistent")
	if err != nil {
		t.Error("expected no error for non-existent permission")
	}
}

func TestConfig_GetToolPermission(t *testing.T) {
	cfg := NewConfig()
	cfg.ToolPermissions = []ToolPermission{
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "read_file", Enabled: true},
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "write_file", Enabled: false},
	}

	enabled, found := cfg.GetToolPermission("ns01", "srv1", "read_file")
	if !found {
		t.Error("expected permission to be found")
	}
	if !enabled {
		t.Error("expected permission to be enabled")
	}

	enabled, found = cfg.GetToolPermission("ns01", "srv1", "write_file")
	if !found {
		t.Error("expected permission to be found")
	}
	if enabled {
		t.Error("expected permission to be disabled")
	}

	_, found = cfg.GetToolPermission("ns01", "srv1", "nonexistent")
	if found {
		t.Error("expected permission not to be found")
	}
}

func TestConfig_GetToolPermissionsForNamespace(t *testing.T) {
	cfg := NewConfig()
	cfg.ToolPermissions = []ToolPermission{
		{NamespaceID: "ns01", ServerID: "srv1", ToolName: "tool1", Enabled: true},
		{NamespaceID: "ns01", ServerID: "srv2", ToolName: "tool2", Enabled: false},
		{NamespaceID: "ns02", ServerID: "srv1", ToolName: "tool1", Enabled: true},
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
