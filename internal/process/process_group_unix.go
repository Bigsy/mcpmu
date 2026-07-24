//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"errors"
	"log"
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

// signalProcessGroup signals every member of a process group this Supervisor
// created. ESRCH means the group is already gone, which is the goal.
//
// EPERM means the same thing in practice, and reaching it is routine rather than
// exotic. kill(2) with a negative pid succeeds if the caller may signal *any*
// member, so EPERM says no member is signallable by us. Two ways that happens:
//
//   - A zombie. On macOS a group whose only remaining member is an exited but
//     unreaped process answers EPERM, not ESRCH. This is the common case: when a
//     wrapper leader dies, its workers are reparented to launchd, and until
//     launchd reaps them they linger in our group as unsignallable zombies. That
//     race is why "retire process group N: operation not permitted" showed up
//     intermittently while stopping a perfectly healthy server.
//   - A recycled PGID. macOS wraps the PID space at 99999 and reissues low PIDs,
//     which collide with the root-owned boot daemon range (logd, smd, configd
//     and friends sit around 540-560), so a stale PGID can come to designate a
//     root-owned group.
//
// Either way nothing of ours is still running, a refused signal has no effect,
// and there is nothing for the caller to retry or report. The residual case — a
// live member that made itself unsignallable, e.g. by exec'ing a setuid binary —
// is one we could not terminate under any error handling.
func signalProcessGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("invalid process group")
	}
	err := syscall.Kill(-pgid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if errors.Is(err, syscall.EPERM) {
		log.Printf("Process group %d has no signallable members (zombie or recycled PGID); treating as retired", pgid)
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

// processGroupAlive reports whether the group we recorded still has a member we
// could actually signal.
//
// EPERM answers false, not true: it means nothing in the group is signallable by
// us, so there is nothing left running that we own (see signalProcessGroup for
// how a group gets there). Reporting it as alive made terminateProcessGroup burn
// the full grace period, escalate to SIGKILL against a group it can never
// signal, and then fail — and made the PID registry retain the orphan entry
// forever as "unverifiable, manual cleanup required", retried on every startup.
func processGroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, nil
	}
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH), errors.Is(err, syscall.EPERM):
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
