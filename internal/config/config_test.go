package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hedworth/mcp-studio-go/internal/testutil"
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
		"namespaces": [],
		"proxies": []
	}`

	configPath := filepath.Join(home, ".config", "mcp-studio", "config.json")
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

	configPath := filepath.Join(home, ".config", "mcp-studio", "config.json")
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

	configPath := filepath.Join(home, ".config", "mcp-studio", "config.json")
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
	_, err = cfg.AddServer(srv)
	if err == nil {
		t.Error("expected error for duplicate ID")
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
