package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPIDTrackersUseIndependentOwnerFiles(t *testing.T) {
	dir := t.TempDir()
	first, err := NewPIDTrackerInDir(dir, "serve")
	if err != nil {
		t.Fatalf("first tracker: %v", err)
	}
	second, err := NewPIDTrackerInDir(dir, "serve")
	if err != nil {
		t.Fatalf("second tracker: %v", err)
	}
	if first.path == second.path || first.owner.Nonce == second.owner.Nonce {
		t.Fatal("per-owner registries did not receive unique nonce identities")
	}

	if err := first.Add("one", os.Getpid(), "test", nil); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := second.Add("two", os.Getpid(), "test", nil); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if _, err := os.Stat(first.path); err != nil {
		t.Fatalf("first registry missing: %v", err)
	}
	if _, err := os.Stat(second.path); err != nil {
		t.Fatalf("second registry missing: %v", err)
	}

	if killed := first.CleanupOrphans(); killed != 0 {
		t.Fatalf("live-owner scan killed %d processes", killed)
	}
	if _, err := os.Stat(second.path); err != nil {
		t.Fatalf("live second owner registry was modified: %v", err)
	}
}

func TestPIDTrackerReapsIdentityValidatedDeadOwnerGroup(t *testing.T) {
	dir := t.TempDir()
	tracker, err := NewPIDTrackerInDir(dir, "current")
	if err != nil {
		t.Fatalf("NewPIDTrackerInDir: %v", err)
	}

	cmd := exec.Command("/bin/sleep", "30")
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orphan candidate: %v", err)
	}
	pgid, err := commandProcessGroupID(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("process group: %v", err)
	}
	started, err := getProcessStartTicks(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("process identity: %v", err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	path := filepath.Join(dir, "pids-dead-owner.json")
	record := pidRegistry{
		Version: pidFileVersion,
		Owner: ownerIdentity{
			PID:               999999,
			ProcessStartTicks: 1,
			Nonce:             "dead-owner",
		},
		Entries: []pidEntry{{
			Instance:          SharedInstanceID("orphan"),
			PID:               cmd.Process.Pid,
			PGID:              pgid,
			Command:           "/bin/sleep",
			Args:              []string{"30"},
			ProcessStartTicks: started,
		}},
	}
	if err := writeRegistryAtomic(path, record); err != nil {
		t.Fatalf("write dead-owner registry: %v", err)
	}

	if killed := tracker.CleanupOrphans(); killed != 1 {
		t.Fatalf("CleanupOrphans killed %d groups, want 1", killed)
	}
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("orphan process was not reaped")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("resolved dead-owner registry remains, stat err=%v", err)
	}
}

func TestInstanceIDSharedIdentityIsStable(t *testing.T) {
	id := SharedInstanceID("browser")
	if id.Server != "browser" || !id.IsShared() || id.String() != "browser" {
		t.Fatalf("unexpected shared identity: %#v (%q)", id, id.String())
	}
}

func TestPIDTrackerRetirementCannotRemoveNewLeader(t *testing.T) {
	tracker, err := NewPIDTrackerInDir(t.TempDir(), "serve")
	if err != nil {
		t.Fatalf("NewPIDTrackerInDir: %v", err)
	}
	id := SharedInstanceID("shared")
	if err := tracker.AddInstance(id, os.Getpid(), os.Getpid(), "test", nil); err != nil {
		t.Fatalf("AddInstance: %v", err)
	}
	if err := tracker.RemoveInstancePID(id, os.Getpid()+1); err != nil {
		t.Fatalf("stale retirement: %v", err)
	}
	if _, exists := tracker.pids[id]; !exists {
		t.Fatal("stale watcher removed the current leader entry")
	}
	if err := tracker.RemoveInstancePID(id, os.Getpid()); err != nil {
		t.Fatalf("current retirement: %v", err)
	}
	if _, exists := tracker.pids[id]; exists {
		t.Fatal("matching watcher did not retire its leader entry")
	}
}
