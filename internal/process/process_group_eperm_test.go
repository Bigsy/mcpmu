//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// foreignProcessGroup finds a live process group that this test process is not
// permitted to signal — i.e. one whose members are all owned by another user.
// This is the state a recorded PGID lands in once the OS recycles it: macOS wraps
// the PID space at 99999 and reissues low PIDs, which collide with the root-owned
// boot daemon range.
func foreignProcessGroup(t *testing.T) int {
	t.Helper()

	out, err := exec.Command("ps", "-A", "-o", "pgid=,uid=").Output()
	if err != nil {
		t.Skipf("cannot enumerate processes: %v", err)
	}

	selfUID := syscall.Getuid()
	seen := map[int]bool{}
	for line := range strings.Lines(string(out)) {
		var pgid, uid int
		if n, err := fmt.Sscan(line, &pgid, &uid); err != nil || n != 2 {
			continue
		}
		if pgid <= 0 || uid == selfUID || seen[pgid] {
			continue
		}
		seen[pgid] = true
		// Confirm the kernel actually refuses us, rather than trusting ps.
		if errors.Is(syscall.Kill(-pgid, 0), syscall.EPERM) {
			return pgid
		}
	}
	t.Skip("no foreign process group available to signal (running as root?)")
	return 0
}

// TestSignalProcessGroupTreatsEPERMAsRetired pins the fix for the "retire process
// group N: operation not permitted" failure. We only ever signal groups we
// spawned, so EPERM means the PGID was recycled and our group is already gone.
// Reporting it as an error made Handle.Stop fail for a server that had in fact
// exited cleanly.
func TestSignalProcessGroupTreatsEPERMAsRetired(t *testing.T) {
	pgid := foreignProcessGroup(t)

	if err := terminateProcessGroupGracefully(pgid); err != nil {
		t.Errorf("terminateProcessGroupGracefully(%d) = %v, want nil for a recycled PGID", pgid, err)
	}
	if err := killProcessGroup(pgid); err != nil {
		t.Errorf("killProcessGroup(%d) = %v, want nil for a recycled PGID", pgid, err)
	}
}

// TestProcessGroupAliveTreatsEPERMAsGone covers the other half. Answering "alive"
// for a group we cannot signal made terminateProcessGroup spin the whole grace
// period and then escalate to SIGKILL for nothing, and made the PID registry
// retain the orphan entry forever as "manual cleanup required".
func TestProcessGroupAliveTreatsEPERMAsGone(t *testing.T) {
	pgid := foreignProcessGroup(t)

	alive, err := processGroupAlive(pgid)
	if err != nil {
		t.Fatalf("processGroupAlive(%d) error = %v, want nil", pgid, err)
	}
	if alive {
		t.Errorf("processGroupAlive(%d) = true, want false: a group we cannot signal is not ours", pgid)
	}
}

// TestTerminateProcessGroupReturnsPromptlyForForeignGroup is the end-to-end
// assertion: the call must succeed immediately instead of burning two grace
// periods and then failing.
func TestTerminateProcessGroupReturnsPromptlyForForeignGroup(t *testing.T) {
	pgid := foreignProcessGroup(t)

	start := time.Now()
	err := terminateProcessGroup(pgid, GracefulShutdownTimeout)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("terminateProcessGroup(%d) = %v, want nil", pgid, err)
	}
	if elapsed > time.Second {
		t.Errorf("terminateProcessGroup took %v; a recycled PGID must not consume the grace period", elapsed)
	}
}

// TestZombieOnlyGroupCountsAsRetired covers the exact race behind the flake: the
// group's last member has exited but not yet been reaped, which macOS reports as
// EPERM rather than ESRCH.
func TestZombieOnlyGroupCountsAsRetired(t *testing.T) {
	// Deliberately do not Wait, so the exited leader stays a zombie in our group.
	cmd := exec.Command("/bin/sh", "-c", `exit 0`)
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pgid, err := commandProcessGroupID(cmd)
	if err != nil {
		t.Fatalf("commandProcessGroupID: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.EPERM) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !errors.Is(syscall.Kill(-pgid, 0), syscall.EPERM) {
		t.Skip("platform does not report EPERM for a zombie-only process group")
	}

	alive, err := processGroupAlive(pgid)
	if err != nil {
		t.Fatalf("processGroupAlive: %v", err)
	}
	if alive {
		t.Error("a zombie-only group reported as alive: nothing in it is still running")
	}

	start := time.Now()
	if err := terminateProcessGroup(pgid, GracefulShutdownTimeout); err != nil {
		t.Errorf("terminateProcessGroup on a zombie-only group = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("terminateProcessGroup took %v on a zombie-only group; want prompt success", elapsed)
	}
}

// TestTerminateProcessGroupStillReapsOwnGroup guards against the fix being too
// permissive: a real group we own must still be terminated, not waved through.
// The leader is reaped concurrently so this mirrors Handle.watchProcess.
func TestTerminateProcessGroupStillReapsOwnGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/child.pid"

	cmd := exec.Command("/bin/sh", "-c",
		`sleep 30 & child=$!; echo "$child" > "$CHILD_PID_FILE"; cat >/dev/null`)
	cmd.Env = append(cmd.Environ(), "CHILD_PID_FILE="+pidFile)
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	pgid, err := commandProcessGroupID(cmd)
	if err != nil {
		t.Fatalf("commandProcessGroupID: %v", err)
	}
	childPID := waitForPIDFile(t, pidFile)

	alive, err := processGroupAlive(pgid)
	if err != nil {
		t.Fatalf("processGroupAlive: %v", err)
	}
	if !alive {
		t.Fatal("our own live process group reported as gone")
	}

	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		_ = cmd.Wait()
	}()

	if err := terminateProcessGroup(pgid, GracefulShutdownTimeout); err != nil {
		t.Fatalf("terminateProcessGroup on our own group: %v", err)
	}
	<-reaped

	// The worker must actually be dead — EPERM handling must not become a way to
	// declare victory over a group that is still running.
	waitForProcessGone(t, childPID)
}
