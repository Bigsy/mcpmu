//go:build linux

package daemon

import (
	"fmt"
	"path/filepath"
)

func processExecutablePath(pid int) (string, error) {
	path, err := filepath.EvalSymlinks(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	return path, nil
}
