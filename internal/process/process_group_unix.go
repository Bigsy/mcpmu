//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func commandProcessGroupID(cmd *exec.Cmd) (int, error) {
	if cmd == nil || cmd.Process == nil {
		return 0, errors.New("process has not started")
	}
	return syscall.Getpgid(cmd.Process.Pid)
}

func signalProcessGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	err := syscall.Kill(-pgid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func terminateProcessGroupGracefully(pgid int) error {
	return signalProcessGroup(pgid, syscall.SIGTERM)
}

func killProcessGroup(pgid int) error {
	return signalProcessGroup(pgid, syscall.SIGKILL)
}

func processGroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func terminateProcessGroup(pgid int, grace time.Duration) error {
	if err := terminateProcessGroupGracefully(pgid); err != nil {
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive, err := processGroupAlive(pgid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := killProcessGroup(pgid); err != nil {
		return err
	}
	deadline = time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive, err := processGroupAlive(pgid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("process group remained after SIGKILL")
}

func processExitSignal(state *os.ProcessState) string {
	if state == nil {
		return ""
	}
	if waitStatus, ok := state.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
		return waitStatus.Signal().String()
	}
	return ""
}

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
