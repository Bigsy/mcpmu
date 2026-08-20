//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package process

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
)

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(data))
			if value == "" {
				// Shell redirection creates the file before echo writes the PID.
				// Treat existence with no content as not ready yet.
				time.Sleep(10 * time.Millisecond)
				continue
			}
			pid, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				t.Fatalf("parse child PID: %v", parseErr)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child PID file %s", path)
	return 0
}

func waitForProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessRunning(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}

func wrapperServerConfig(pidFile, script string) config.ServerConfig {
	return config.ServerConfig{
		Command: "/bin/sh",
		Args:    []string{"-c", script},
		Env:     map[string]string{"CHILD_PID_FILE": pidFile},
	}
}

func TestSupervisorStopTerminatesWrapperProcessGroup(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	supervisor := NewSupervisorWithOptions(bus, SupervisorOptions{PIDTrackerDir: t.TempDir()})
	t.Cleanup(supervisor.StopAll)

	handle, err := supervisor.Start(context.Background(), "wrapper", wrapperServerConfig(
		pidFile,
		`sleep 30 & child=$!; echo "$child" > "$CHILD_PID_FILE"; cat >/dev/null`,
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childPID := waitForPIDFile(t, pidFile)
	if handle.pgid <= 0 {
		t.Fatal("stdio handle has no retained process group ID")
	}
	if err := supervisor.Stop("wrapper"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForProcessGone(t, childPID)
	alive, err := processGroupAlive(handle.pgid)
	if err != nil {
		t.Fatalf("inspect retired group: %v", err)
	}
	if alive {
		t.Fatalf("process group %d survived Stop", handle.pgid)
	}
}

func TestWatcherTerminatesGroupAfterWrapperLeaderExits(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	supervisor := NewSupervisorWithOptions(bus, SupervisorOptions{PIDTrackerDir: t.TempDir()})
	t.Cleanup(supervisor.StopAll)

	handle, err := supervisor.Start(context.Background(), "leader-exit", wrapperServerConfig(
		pidFile,
		`sleep 30 & child=$!; echo "$child" > "$CHILD_PID_FILE"; sleep 0.2; exit 0`,
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childPID := waitForPIDFile(t, pidFile)
	select {
	case <-handle.done:
	case <-time.After(4 * time.Second):
		t.Fatal("watcher did not retire leaderless process group")
	}
	waitForProcessGone(t, childPID)
	if _, ok := supervisor.pidTracker.pids[SharedInstanceID("leader-exit")]; ok {
		t.Fatal("retired process group remains in owner registry")
	}

	// A lazy restart must create a fresh group only after the old worker and
	// registry identity have retired.
	restartPIDFile := t.TempDir() + "/restart-child.pid"
	_, err = supervisor.Start(context.Background(), "leader-exit", wrapperServerConfig(
		restartPIDFile,
		`sleep 30 & child=$!; echo "$child" > "$CHILD_PID_FILE"; cat >/dev/null`,
	))
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	restartedChild := waitForPIDFile(t, restartPIDFile)
	if !isProcessRunning(restartedChild) {
		t.Fatal("restarted worker exited unexpectedly")
	}
	if err := supervisor.Stop("leader-exit"); err != nil {
		t.Fatalf("stop restarted instance: %v", err)
	}
	waitForProcessGone(t, restartedChild)
}

func TestSupervisorStopAllTerminatesProcessGroups(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	supervisor := NewSupervisorWithOptions(bus, SupervisorOptions{PIDTrackerDir: t.TempDir()})

	_, err := supervisor.Start(context.Background(), "shutdown", wrapperServerConfig(
		pidFile,
		`sleep 30 & child=$!; echo "$child" > "$CHILD_PID_FILE"; cat >/dev/null`,
	))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childPID := waitForPIDFile(t, pidFile)
	supervisor.StopAll()
	waitForProcessGone(t, childPID)
}

func TestTrackingFailureStopsNewProcessGroup(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	supervisor := NewSupervisorWithOptions(bus, SupervisorOptions{PIDTrackerDir: t.TempDir()})
	// Renaming an atomic temp file over an existing directory is guaranteed to fail.
	supervisor.pidTracker.path = t.TempDir()

	_, err := supervisor.Start(context.Background(), "untracked", wrapperServerConfig(
		pidFile,
		`sleep 30 & child=$!; echo "$child" > "$CHILD_PID_FILE"; cat >/dev/null`,
	))
	if err == nil || !strings.Contains(err.Error(), "persist process identity") {
		t.Fatalf("Start error = %v, want tracking failure", err)
	}
	if _, exists := supervisor.pidTracker.pids[SharedInstanceID("untracked")]; exists {
		t.Fatal("failed persistence left an in-memory PID entry")
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr == nil {
		childPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil {
			waitForProcessGone(t, childPID)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read child PID file: %v", readErr)
	}
}
