//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

func ensurePrivateRuntimeDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create daemon runtime directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("inspect daemon runtime directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect daemon runtime directory owner: unsupported file metadata")
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("daemon runtime directory %s is owned by UID %d, want %d", dir, stat.Uid, os.Getuid())
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("secure daemon runtime directory: %w", err)
		}
	}
	return nil
}

// validatePrivateRuntimeDir checks the same ownership boundary as setup without
// creating directories or repairing permissions during read-only inspection.
func validatePrivateRuntimeDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("daemon runtime directory must be owned by the current user with mode 0700")
	}
	return nil
}
