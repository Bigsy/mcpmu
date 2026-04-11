package process

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const lockFileName = "manager.lock"

// ManagerLockInfo describes who holds the manager lock.
type ManagerLockInfo struct {
	PID  int    `json:"pid"`
	Mode string `json:"mode"` // "tui" or "web"
}

// ManagerLock manages the manager.lock file co-located with the config.
// Both TUI and web acquire this lock on startup; serve ignores it.
//
// Uses flock(LOCK_EX|LOCK_NB) for race-free mutual exclusion. The file
// descriptor is held open for the process lifetime so the OS enforces
// the lock even if two processes start simultaneously.
type ManagerLock struct {
	path string
	file *os.File // held open while lock is active
}

// NewManagerLock creates a lock manager for the given config path.
// The lock file is placed in the same directory as the config file.
func NewManagerLock(configPath string) (*ManagerLock, error) {
	dir, err := lockDir(configPath)
	if err != nil {
		return nil, err
	}
	return &ManagerLock{path: filepath.Join(dir, lockFileName)}, nil
}

// Acquire attempts to claim the manager lock for the given mode.
// Uses flock for atomic mutual exclusion — two concurrent callers
// cannot both succeed. If the lock is already held, returns an error
// with the holder's mode and PID.
func (l *ManagerLock) Acquire(mode string) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}

	// Open (or create) the lock file
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}

	// Try to acquire an exclusive non-blocking lock.
	// If another process holds the lock, this fails immediately.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Lock is held — read the file to report who holds it
		_ = f.Close()
		info, readErr := l.readFile()
		if readErr == nil {
			return fmt.Errorf("mcpmu is already running (%s, PID %d). Stop it first or use that instance", info.Mode, info.PID)
		}
		return fmt.Errorf("mcpmu is already running (another instance holds the lock)")
	}

	// We hold the flock. Write our info into the file.
	newInfo := ManagerLockInfo{
		PID:  os.Getpid(),
		Mode: mode,
	}
	data, err := json.Marshal(newInfo)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("marshal lock: %w", err)
	}

	// Truncate and write (we hold the flock, so this is safe)
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return fmt.Errorf("truncate lock: %w", err)
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		_ = f.Close()
		return fmt.Errorf("write lock: %w", err)
	}

	// Keep the file descriptor open — the flock is held as long as
	// the fd is open. On process crash, the OS releases the lock.
	l.file = f
	return nil
}

// Release drops the flock by closing the file descriptor. Safe to call
// multiple times. The lock file is intentionally NOT deleted — with flock,
// removing the path after close creates a window where a second process
// flocks the old inode while a third creates a new file at the same path,
// letting two managers coexist on different inodes. Leaving the file in
// place (like pids.json and toolcache.json) avoids this race entirely.
func (l *ManagerLock) Release() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// readFile reads the lock file contents without acquiring the lock.
func (l *ManagerLock) readFile() (ManagerLockInfo, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return ManagerLockInfo{}, err
	}
	var info ManagerLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ManagerLockInfo{}, err
	}
	return info, nil
}

// lockDir returns the directory where the lock file should live,
// co-located with the active config file. Follows the same pattern
// as ToolCachePath.
func lockDir(configPath string) (string, error) {
	if configPath != "" {
		expanded := configPath
		if strings.HasPrefix(expanded, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("get home dir: %w", err)
			}
			expanded = filepath.Join(home, expanded[2:])
		}
		return filepath.Dir(expanded), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".config", "mcpmu"), nil
}
