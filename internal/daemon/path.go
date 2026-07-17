package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maxUnixSocketPath = 103

// Paths contains every per-config daemon rendezvous artifact.
type Paths struct {
	RuntimeDir string
	Socket     string
	RunLock    string
	PIDFile    string
	LogFile    string
}

// CanonicalConfigPath resolves all existing symlink components while keeping
// a not-yet-created config path usable.
func CanonicalConfigPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("config path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("get home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make config path absolute: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve config path: %w", err)
	}

	ancestor := abs
	var suffix []string
	for {
		if _, err := os.Stat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect config path ancestor %q: %w", ancestor, err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("no existing ancestor for config path %q", abs)
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolve config path ancestor %q: %w", ancestor, err)
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	return filepath.Clean(resolved), nil
}

// RuntimePaths derives short rendezvous names from the canonical config path.
// The short hash is only a filename hint; handshakes and pidfiles retain the
// complete path as the authoritative identity.
func RuntimePaths(canonicalConfigPath string) (Paths, error) {
	if runtime.GOOS == "windows" {
		return Paths{}, fmt.Errorf("shared daemon transport is unsupported on windows")
	}
	dir := ""
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		dir = filepath.Join(xdg, "mcpmu")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("get home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state", "mcpmu")
	}
	if err := ensurePrivateRuntimeDir(dir); err != nil {
		return Paths{}, err
	}
	sum := sha256.Sum256([]byte(canonicalConfigPath))
	name := hex.EncodeToString(sum[:4])
	paths := Paths{
		RuntimeDir: dir,
		Socket:     filepath.Join(dir, name+".sock"),
		RunLock:    filepath.Join(dir, name+".run.lock"),
		PIDFile:    filepath.Join(dir, name+".pid"),
		LogFile:    filepath.Join(dir, name+".log"),
	}
	if len(paths.Socket) > maxUnixSocketPath {
		return Paths{}, fmt.Errorf("daemon socket path is too long (%d > %d): %s", len(paths.Socket), maxUnixSocketPath, paths.Socket)
	}
	return paths, nil
}
