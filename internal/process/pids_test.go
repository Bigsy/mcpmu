package process

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/testutil"
)

func writeLegacyPIDEntries(t *testing.T, pt *PIDTracker, entries map[string]pidEntry) string {
	t.Helper()
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy PID entries: %v", err)
	}
	path := filepath.Join(pt.dir, legacyPIDsFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write legacy PID entries: %v", err)
	}
	return path
}

func readLegacyPIDEntries(t *testing.T, path string) map[string]pidEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read legacy PID entries: %v", err)
	}
	var entries map[string]pidEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse legacy PID entries: %v", err)
	}
	return entries
}

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

	// Add the current PID. The tracker requires a real start identity so crash
	// cleanup can never rely on a bare, potentially reused PID.
	err = pt.Add("test-server", os.Getpid(), "/usr/bin/node", []string{"server.js"})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	data, err := os.ReadFile(pt.path)
	if err != nil {
		t.Fatalf("read owner registry: %v", err)
	}
	var record pidRegistry
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("parse owner registry: %v", err)
	}
	if len(record.Entries) != 1 {
		t.Fatalf("expected one tracked entry, got %d", len(record.Entries))
	}
	entry := record.Entries[0]
	if entry.Instance != SharedInstanceID("test-server") {
		t.Errorf("unexpected instance: %v", entry.Instance)
	}
	if entry.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), entry.PID)
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

	if _, err := os.Stat(pt.path); !os.IsNotExist(err) {
		t.Errorf("expected empty owner registry to be removed, stat err=%v", err)
	}
}

func TestPIDTracker_CleanupOrphans_ProcessGone(t *testing.T) {
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	// Add a PID that doesn't exist (high unlikely PID)
	path := writeLegacyPIDEntries(t, pt, map[string]pidEntry{"gone-server": {
		PID:               999999,
		Command:           "/usr/bin/fake",
		Args:              []string{},
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: 12345,
	}})

	killed := pt.CleanupOrphans()

	// Should not have killed anything (process doesn't exist)
	if killed != 0 {
		t.Errorf("expected 0 killed, got %d", killed)
	}

	// Entry should be removed
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected resolved legacy registry to be removed, stat err=%v", err)
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
	path := writeLegacyPIDEntries(t, pt, map[string]pidEntry{"uncertain-server": {
		PID:        os.Getpid(),
		Command:    "/some/nonexistent/command",
		Args:       []string{},
		StartedAt:  time.Now().Add(-time.Hour),
		RetryCount: 0,
		// No ProcessStartTicks - can't verify via start time
	}})

	// First cleanup - should increment retry count
	pt.CleanupOrphans()

	entries := readLegacyPIDEntries(t, path)
	entry, ok := entries["uncertain-server"]
	if !ok {
		t.Fatal("expected uncertain-server to still be tracked")
	}
	if entry.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", entry.RetryCount)
	}

	// Set retry count to max-1
	entry.RetryCount = MaxRetryCount - 1
	writeLegacyPIDEntries(t, pt, map[string]pidEntry{"uncertain-server": entry})

	// This cleanup should hit max and remove
	pt.CleanupOrphans()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected uncertain registry to retire after max retries, stat err=%v", err)
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

// TestPIDTracker_PIDReuse_DetectedViaStartTicks tests that when a PID is reused
// by a different process (detected via different start ticks), we correctly
// identify this and don't kill the wrong process.
func TestPIDTracker_PIDReuse_DetectedViaStartTicks(t *testing.T) {
	skipIfPsUnavailable(t)
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	// Use current process PID but with a DIFFERENT ProcessStartTicks
	// This simulates PID reuse: we tracked a process that died, and
	// a new process now has the same PID but a different start time.
	currentPID := os.Getpid()
	actualTicks, err := getProcessStartTicks(currentPID)
	if err != nil {
		t.Fatalf("getProcessStartTicks failed: %v", err)
	}

	// Record a fake entry with the same PID but different start ticks
	// This represents the "old" process that died and whose PID was reused
	path := writeLegacyPIDEntries(t, pt, map[string]pidEntry{"reused-server": {
		PID:               currentPID,
		Command:           "/some/old/command",
		Args:              []string{"old-arg"},
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: actualTicks + 99999, // Different from actual!
	}})

	// Run cleanup - should detect PID reuse and NOT kill the process
	killed := pt.CleanupOrphans()

	// Should NOT have killed anything - the process exists but isn't ours
	if killed != 0 {
		t.Errorf("expected 0 killed (PID reuse detected), got %d", killed)
	}

	// Entry should be removed from tracking (it's a stale entry)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected reused PID registry to be removed, stat err=%v", err)
	}
}

// TestPIDTracker_VerifyProcessOwnership_PIDReuse directly tests the
// verifyProcessOwnership function returns verifyConfirmedReused when
// start ticks don't match.
func TestPIDTracker_VerifyProcessOwnership_PIDReuse(t *testing.T) {
	skipIfPsUnavailable(t)
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	currentPID := os.Getpid()
	actualTicks, err := getProcessStartTicks(currentPID)
	if err != nil {
		t.Fatalf("getProcessStartTicks failed: %v", err)
	}

	// Entry with mismatched start ticks (simulating PID reuse)
	entry := pidEntry{
		PID:               currentPID,
		Command:           "/any/command",
		Args:              nil,
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: actualTicks + 12345, // Wrong ticks!
	}

	result := pt.verifyProcessOwnership(entry)

	if result != verifyConfirmedReused {
		t.Errorf("expected verifyConfirmedReused, got %v", result)
	}
}

// TestPIDTracker_VerifyProcessOwnership_ConfirmedOwned tests that when
// start ticks match, the process is correctly identified as owned.
func TestPIDTracker_VerifyProcessOwnership_ConfirmedOwned(t *testing.T) {
	skipIfPsUnavailable(t)
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	currentPID := os.Getpid()
	actualTicks, err := getProcessStartTicks(currentPID)
	if err != nil {
		t.Fatalf("getProcessStartTicks failed: %v", err)
	}

	// Get actual cmdline for matching
	cmdline, err := getProcessCmdline(currentPID)
	if err != nil || len(cmdline) == 0 {
		t.Skipf("Cannot get cmdline: %v", err)
	}

	// Entry with matching start ticks and matching command
	entry := pidEntry{
		PID:               currentPID,
		Command:           cmdline[0], // Use actual command
		Args:              nil,
		StartedAt:         time.Now(),
		ProcessStartTicks: actualTicks, // Correct ticks!
	}

	result := pt.verifyProcessOwnership(entry)

	if result != verifyConfirmedOwned {
		t.Errorf("expected verifyConfirmedOwned, got %v", result)
	}
}

// TestPIDTracker_VerifyProcessOwnership_StartTicksMatchCmdlineDiffers tests
// that even when cmdline doesn't match but start ticks do match, we still
// consider it our process (handles interpreter wrappers like npx->node).
func TestPIDTracker_VerifyProcessOwnership_StartTicksMatchCmdlineDiffers(t *testing.T) {
	skipIfPsUnavailable(t)
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	currentPID := os.Getpid()
	actualTicks, err := getProcessStartTicks(currentPID)
	if err != nil {
		t.Fatalf("getProcessStartTicks failed: %v", err)
	}

	// Entry with matching start ticks but DIFFERENT command
	// This simulates npx starting node with a different cmdline
	entry := pidEntry{
		PID:               currentPID,
		Command:           "/usr/bin/npx",             // What we launched
		Args:              []string{"@anthropic/mcp"}, // Our args
		StartedAt:         time.Now(),
		ProcessStartTicks: actualTicks, // Correct ticks!
	}

	result := pt.verifyProcessOwnership(entry)

	// Should still be confirmed owned because start ticks match
	// The implementation is conservative and trusts start ticks
	if result != verifyConfirmedOwned {
		t.Errorf("expected verifyConfirmedOwned (start ticks match), got %v", result)
	}
}

// TestPIDTracker_CleanupOrphans_MultiplePIDStates tests CleanupOrphans with
// a mix of process states: gone, reused, and owned.
func TestPIDTracker_CleanupOrphans_MultiplePIDStates(t *testing.T) {
	skipIfPsUnavailable(t)
	testutil.SetupTestHome(t)

	pt, err := NewPIDTracker()
	if err != nil {
		t.Fatalf("NewPIDTracker failed: %v", err)
	}

	currentPID := os.Getpid()
	actualTicks, err := getProcessStartTicks(currentPID)
	if err != nil {
		t.Fatalf("getProcessStartTicks failed: %v", err)
	}

	entries := map[string]pidEntry{}

	// 1. Process gone (non-existent PID)
	entries["gone-server"] = pidEntry{
		PID:               999999,
		Command:           "/fake/cmd",
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: 12345,
	}

	// 2. PID reused (exists but different start ticks)
	entries["reused-server"] = pidEntry{
		PID:               currentPID,
		Command:           "/old/cmd",
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: actualTicks + 99999, // Wrong ticks
	}

	// 3. Uncertain (no start ticks, command mismatch)
	entries["uncertain-server"] = pidEntry{
		PID:               currentPID,
		Command:           "/different/command/entirely",
		StartedAt:         time.Now().Add(-time.Hour),
		ProcessStartTicks: 0, // No ticks recorded
	}

	path := writeLegacyPIDEntries(t, pt, entries)
	killed := pt.CleanupOrphans()

	// Gone and reused should be removed, uncertain should increment retry
	if killed != 0 {
		t.Errorf("expected 0 killed, got %d", killed)
	}

	// Gone: removed
	entries = readLegacyPIDEntries(t, path)
	if _, ok := entries["gone-server"]; ok {
		t.Error("gone-server should be removed")
	}

	// Reused: removed (PID reuse detected)
	if _, ok := entries["reused-server"]; ok {
		t.Error("reused-server should be removed (PID reuse detected)")
	}

	// Uncertain: still tracked with incremented retry count
	entry, ok := entries["uncertain-server"]
	if !ok {
		t.Error("uncertain-server should still be tracked")
	} else if entry.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", entry.RetryCount)
	}
}
