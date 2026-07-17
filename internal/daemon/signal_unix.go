//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func signalDaemon(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}
