package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func ExecutableIdentity() (path string, build string, err error) {
	path, err = os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("locate executable: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve executable: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("open executable: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", "", fmt.Errorf("hash executable: %w", err)
	}
	return path, hex.EncodeToString(hash.Sum(nil)), nil
}
