package process

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/testutil"
)

// skipIfPsUnavailable skips the test if the ps command is unavailable or blocked.
// This is common in sandboxed CI environments on macOS/FreeBSD where ps may fail.
func skipIfPsUnavailable(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		return // Only darwin/freebsd use ps for process info
	}
	_, err := exec.Command("ps", "-p", fmt.Sprintf("%d", os.Getpid()), "-o", "lstart=").Output()
	if err != nil {
		t.Skipf("ps command unavailable or blocked: %v", err)
	}
}

func TestPIDTracker_AddAndRemove(t *testing.T) {
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	// Add a PID
	err = pt.Add("test-server", 12345, "/usr/bin/node", []string{"server.js"})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify it was saved
	pt2, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker (reload) failed: %v", err)
	}

	entry, ok := pt2.pids["test-server"]
	if !ok {
		t.Fatal("expected test-server to be tracked")
	}
	if entry.PID != 12345 {
		t.Errorf("expected PID 12345, got %d", entry.PID)
	}
	if entry.Command != "/usr/bin/node" {
		t.Errorf("expected command '/usr/bin/node', got %q", entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != "server.js" {
		t.Errorf("expected args ['server.js'], got %v", entry.Args)
	}

	// Remove
	err = pt.Remove("test-server")
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Verify removal was saved
	pt3, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker (reload after remove) failed: %v", err)
	}
	if _, ok := pt3.pids["test-server"]; ok {
		t.Error("expected test-server to be removed")
	}
}

func TestPIDTracker_CleanupOrphans_ProcessGone(t *testing.T) {
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	// Add a PID that doesn't exist (high unlikely PID)
	pt.pids["gone-server"] = pidEntry{
		PID:               999999,
		Command:           "/usr/bin/fake",
		Args:              []string{},
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: 12345,
	}

	killed := pt.CleanupOrphans()

	// Should not have killed anything (process doesn't exist)
	if killed != 0 {
		t.Errorf("expected 0 killed, got %d", killed)
	}

	// Entry should be removed
	if _, ok := pt.pids["gone-server"]; ok {
		t.Error("expected gone-server to be removed from tracking")
	}
}

func TestPIDTracker_CleanupOrphans_RetryCount(t *testing.T) {
	skipIfPsUnavailable(t)
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	// Use current process PID (it exists, but command won't match and no start ticks)
	// This will trigger verifyUncertain
	pt.pids["uncertain-server"] = pidEntry{
		PID:        os.Getpid(),
		Command:    "/some/nonexistent/command",
		Args:       []string{},
		StartedAt:  time.Now().Add(-time.Hour),
		RetryCount: 0,
		// No ProcessStartTicks - can't verify via start time
	}

	// First cleanup - should increment retry count
	pt.CleanupOrphans()

	entry, ok := pt.pids["uncertain-server"]
	if !ok {
		t.Fatal("expected uncertain-server to still be tracked")
	}
	if entry.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", entry.RetryCount)
	}

	// Set retry count to max-1
	entry.RetryCount = MaxRetryCount - 1
	pt.pids["uncertain-server"] = entry

	// This cleanup should hit max and remove
	pt.CleanupOrphans()

	if _, ok := pt.pids["uncertain-server"]; ok {
		t.Error("expected uncertain-server to be removed after max retries")
	}
}

func TestGetProcessStartTicks_CurrentProcess(t *testing.T) {
	skipIfPsUnavailable(t)
	// Test with current process - should succeed
	pid := os.Getpid()
	ticks, err := getProcessStartTicks(pid)
	if err != nil {
		t.Fatalf("getProcessStartTicks failed for current process: %v", err)
	}
	if ticks <= 0 {
		t.Errorf("expected positive start ticks, got %d", ticks)
	}

	// Call again - should get same value
	ticks2, err := getProcessStartTicks(pid)
	if err != nil {
		t.Fatalf("second getProcessStartTicks failed: %v", err)
	}
	if ticks != ticks2 {
		t.Errorf("start ticks changed: %d != %d", ticks, ticks2)
	}
}

func TestGetProcessCmdline_CurrentProcess(t *testing.T) {
	skipIfPsUnavailable(t)
	pid := os.Getpid()
	cmdline, err := getProcessCmdline(pid)
	if err != nil {
		t.Fatalf("getProcessCmdline failed: %v", err)
	}
	if len(cmdline) == 0 {
		t.Fatal("expected non-empty cmdline")
	}
	t.Logf("Current process cmdline: %v", cmdline)
}

func TestMatchesCmdline(t *testing.T) {
	skipIfPsUnavailable(t)
	pid := os.Getpid()

	// Get actual cmdline to craft a test
	cmdline, err := getProcessCmdline(pid)
	if err != nil {
		t.Skipf("Cannot get cmdline: %v", err)
	}
	if len(cmdline) == 0 {
		t.Skip("Empty cmdline")
	}

	// Test with actual command from cmdline
	firstArg := cmdline[0]
	if matchesCmdline(pid, firstArg, nil) {
		t.Log("Matched with actual command")
	} else {
		// This is expected since go test might have different cmdline
		t.Log("Did not match with actual command (expected for go test)")
	}

	// Test with definitely wrong command
	if matchesCmdline(pid, "/definitely/not/real/command", []string{}) {
		t.Error("Should not match with fake command")
	}
}

func TestVerifyResult_Constants(t *testing.T) {
	// Just verify the constants are distinct
	results := []verifyResult{
		verifyConfirmedOwned,
		verifyConfirmedReused,
		verifyProcessGone,
		verifyUncertain,
	}

	seen := make(map[verifyResult]bool)
	for _, r := range results {
		if seen[r] {
			t.Errorf("Duplicate verifyResult value: %d", r)
		}
		seen[r] = true
	}
}
