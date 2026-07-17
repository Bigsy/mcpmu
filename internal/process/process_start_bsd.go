//go:build freebsd

package process

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func getProcessStartTicksPlatform(pid int) (int64, error) {
	cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	timeString := strings.TrimSpace(string(out))
	if timeString == "" {
		return 0, fmt.Errorf("empty lstart for PID %d", pid)
	}
	started, err := time.Parse("Mon Jan  2 15:04:05 2006", timeString)
	if err != nil {
		started, err = time.Parse("Mon Jan 2 15:04:05 2006", timeString)
		if err != nil {
			return 0, fmt.Errorf("parse lstart %q: %w", timeString, err)
		}
	}
	return started.Unix(), nil
}
