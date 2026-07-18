//go:build !windows

package shim

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func spawnDetached(executable string, args []string, logPath string) error {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open null input: %w", err)
	}
	defer func() { _ = devNull.Close() }()

	command := exec.Command(executable, args...)
	command.Stdin = devNull
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	// Reap an early daemon failure while this shim is still alive. The wait
	// goroutine does not keep the shim process running; a healthy detached
	// daemon is reparented normally when the shim exits.
	go func() { _ = command.Wait() }()
	return nil
}
