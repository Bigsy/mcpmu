// Package testutil provides common test utilities.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// SetupTestHome creates an isolated $HOME directory for tests.
// This is critical because:
// - PIDTracker reads/writes ~/.config/mcpmu/pids.json
// - Config reads/writes ~/.config/mcpmu/config.json
// - Orphan cleanup runs on NewSupervisor() and could kill real processes
//
// The temp directory is automatically cleaned up when the test ends.
func SetupTestHome(t *testing.T) string {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	// Also set XDG_CONFIG_HOME to be safe
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	// TMPDIR for macOS
	t.Setenv("TMPDIR", tmpHome)

	// Create the config directory
	configDir := filepath.Join(tmpHome, ".config", "mcpmu")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create test config dir: %v", err)
	}

	return tmpHome
}

// WriteTestConfig writes a test configuration file to the isolated $HOME.
func WriteTestConfig(t *testing.T, configJSON string) string {
	t.Helper()

	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set - call SetupTestHome first")
	}

	configPath := filepath.Join(home, ".config", "mcpmu", "config.json")
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}

	return configPath
}
