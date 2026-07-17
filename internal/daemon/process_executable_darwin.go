//go:build darwin

package daemon

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func processExecutablePath(pid int) (string, error) {
	// lsof's text mapping is the kernel-backed executable vnode on macOS. It
	// remains available when the original path was replaced during an upgrade.
	output, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
	if err != nil {
		return "", fmt.Errorf("query process executable with lsof: %w", err)
	}
	for line := range bytes.SplitSeq(output, []byte{'\n'}) {
		if len(line) > 1 && line[0] == 'n' {
			path := strings.TrimSpace(string(line[1:]))
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return "", resolveErr
			}
			return resolved, nil
		}
	}
	return "", fmt.Errorf("lsof returned no executable for PID %d", pid)
}
