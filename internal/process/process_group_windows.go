//go:build windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/windows"
)

func configureProcessGroup(_ *exec.Cmd) {}

func commandProcessGroupID(cmd *exec.Cmd) (int, error) {
	if cmd == nil || cmd.Process == nil {
		return 0, errors.New("process has not started")
	}
	return cmd.Process.Pid, nil
}

func terminateProcessGroupGracefully(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func killProcessGroup(pid int) error {
	return terminateProcessGroupGracefully(pid)
}

func processGroupAlive(pid int) (bool, error) {
	return processRunning(pid), nil
}

func processExitSignal(_ *os.ProcessState) string { return "" }

func processRunning(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

func terminateProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func terminateProcessGroup(pid int, _ time.Duration) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
