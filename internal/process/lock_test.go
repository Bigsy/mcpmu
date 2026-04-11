package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerLock_AcquireRelease(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	lock, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}

	// Acquire should succeed
	if err := lock.Acquire("tui"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Lock file should exist with our PID
	info, err := lock.readFile()
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), info.PID)
	}
	if info.Mode != "tui" {
		t.Errorf("expected mode tui, got %s", info.Mode)
	}

	// File descriptor should be held open
	if lock.file == nil {
		t.Fatal("expected file descriptor to be held open")
	}

	// Release should close fd but leave the file in place (flock safety)
	lock.Release()
	if lock.file != nil {
		t.Fatal("expected file descriptor to be nil after release")
	}
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err != nil {
		t.Fatal("lock file should persist after Release (flock invariant)")
	}
}

func TestManagerLock_DoubleAcquireFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	lock1, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}

	// First acquire succeeds
	if err := lock1.Acquire("tui"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock1.Release()

	// Second acquire from same process should fail because flock is held
	lock2, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}
	if err := lock2.Acquire("web"); err == nil {
		lock2.Release()
		t.Fatal("expected error on double acquire, flock should prevent it")
	}
}

func TestManagerLock_ReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	lock1, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}

	if err := lock1.Acquire("tui"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	lock1.Release()

	// After release, a new lock should be acquirable
	lock2, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}
	if err := lock2.Acquire("web"); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	defer lock2.Release()

	info, err := lock2.readFile()
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if info.Mode != "web" {
		t.Errorf("expected mode web, got %s", info.Mode)
	}
}

func TestManagerLock_DefaultConfigPath(t *testing.T) {
	// Empty config path should resolve to ~/.config/mcpmu/
	lock, err := NewManagerLock("")
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "mcpmu", lockFileName)
	if lock.path != expected {
		t.Errorf("expected path %q, got %q", expected, lock.path)
	}
}

func TestManagerLock_ReleaseIdempotent(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	lock, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}

	// Release without acquire should not panic
	lock.Release()
	lock.Release()
}

func TestManagerLock_ErrorMessageIncludesHolder(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")

	lock1, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}
	if err := lock1.Acquire("web"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock1.Release()

	lock2, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}
	err = lock2.Acquire("tui")
	if err == nil {
		lock2.Release()
		t.Fatal("expected error")
	}

	// Error message should mention the holder's mode and PID
	errMsg := err.Error()
	if !contains(errMsg, "web") {
		t.Errorf("error should mention holder mode 'web': %s", errMsg)
	}
}

func TestManagerLock_SameInodeAcrossReacquire(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	lockPath := filepath.Join(dir, lockFileName)

	lock1, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}
	if err := lock1.Acquire("tui"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Record inode of the lock file
	stat1, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	lock1.Release()

	// Reacquire — should open the same file (same inode)
	lock2, err := NewManagerLock(configPath)
	if err != nil {
		t.Fatalf("NewManagerLock: %v", err)
	}
	if err := lock2.Acquire("web"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock2.Release()

	stat2, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if !os.SameFile(stat1, stat2) {
		t.Error("lock file inode changed between release and reacquire — " +
			"flock on different inodes cannot provide mutual exclusion")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
