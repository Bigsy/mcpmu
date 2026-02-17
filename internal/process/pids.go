// Package process provides process lifecycle management for MCP servers.
package process

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	pidsFile = "pids.json"

	// MaxRetryCount is the maximum number of cleanup attempts before giving up.
	// This prevents the PID file from growing unbounded with unverifiable entries.
	MaxRetryCount = 5
)

// pidEntry stores PID and metadata for orphan detection.
type pidEntry struct {
	PID               int       `json:"pid"`
	Command           string    `json:"command"`                     // Command used to start the process
	Args              []string  `json:"args,omitempty"`              // Arguments for better matching
	StartedAt         time.Time `json:"startedAt"`                   // Wall clock time when we started it
	ProcessStartTicks int64     `json:"processStartTicks,omitempty"` // OS-level process start time (for PID reuse detection)
	RetryCount        int       `json:"retryCount,omitempty"`        // Number of failed verification attempts
}

// PIDTracker tracks running server PIDs to detect and clean up orphans.
type PIDTracker struct {
	path string
	pids map[string]pidEntry // serverID -> entry
	mu   sync.Mutex
}

// NewPIDTracker creates a new PID tracker using the default directory.
func NewPIDTracker() (*PIDTracker, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return NewPIDTrackerWithDir(filepath.Join(home, ".config", "mcpmu"))
}

// NewPIDTrackerWithDir creates a new PID tracker storing its state in the given directory.
func NewPIDTrackerWithDir(dir string) (*PIDTracker, error) {
	pt := &PIDTracker{
		path: filepath.Join(dir, pidsFile),
		pids: make(map[string]pidEntry),
	}

	// Load existing PIDs
	pt.load()

	return pt, nil
}

// load reads PIDs from the tracking file (caller must hold lock or be in constructor).
func (pt *PIDTracker) load() {
	data, err := os.ReadFile(pt.path)
	if err != nil {
		// File doesn't exist or can't be read, start fresh
		return
	}

	if err := json.Unmarshal(data, &pt.pids); err != nil {
		log.Printf("Failed to parse PID file, starting fresh")
		pt.pids = make(map[string]pidEntry)
	}
}

// save writes PIDs to the tracking file (caller must hold lock).
func (pt *PIDTracker) save() error {
	// Ensure directory exists
	dir := filepath.Dir(pt.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(pt.pids, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(pt.path, data, 0600)
}

// Add tracks a new PID for a server.
func (pt *PIDTracker) Add(serverID string, pid int, command string, args []string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	entry := pidEntry{
		PID:       pid,
		Command:   command,
		Args:      args,
		StartedAt: time.Now(),
	}

	// Capture process start time for PID reuse detection
	if startTicks, err := getProcessStartTicks(pid); err == nil {
		entry.ProcessStartTicks = startTicks
	} else {
		log.Printf("Warning: could not get start ticks for PID %d: %v", pid, err)
	}

	pt.pids[serverID] = entry
	return pt.save()
}

// Remove stops tracking a PID.
func (pt *PIDTracker) Remove(serverID string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	delete(pt.pids, serverID)
	return pt.save()
}

// verifyResult represents the outcome of process ownership verification.
type verifyResult int

const (
	verifyConfirmedOwned  verifyResult = iota // We own this process - safe to kill
	verifyConfirmedReused                     // PID was reused by another process - safe to remove entry
	verifyProcessGone                         // Process no longer exists - safe to remove entry
	verifyUncertain                           // Can't verify ownership - keep entry and retry later
)

// CleanupOrphans checks for and terminates orphaned processes.
// Returns the number of orphans killed.
func (pt *PIDTracker) CleanupOrphans() int {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	killed := 0
	toDelete := make([]string, 0)
	toUpdate := make(map[string]pidEntry)

	for serverID, entry := range pt.pids {
		result := pt.verifyProcessOwnership(entry)

		switch result {
		case verifyProcessGone:
			log.Printf("Process %d (server=%s) no longer running, removing from tracking",
				entry.PID, serverID)
			toDelete = append(toDelete, serverID)

		case verifyConfirmedReused:
			log.Printf("PID %d was reused by different process (server=%s), removing from tracking",
				entry.PID, serverID)
			toDelete = append(toDelete, serverID)

		case verifyConfirmedOwned:
			log.Printf("Found orphan process: server=%s pid=%d cmd=%s, terminating",
				serverID, entry.PID, entry.Command)
			if err := killProcess(entry.PID); err != nil {
				log.Printf("Failed to kill orphan pid=%d: %v", entry.PID, err)
			} else {
				killed++
			}
			toDelete = append(toDelete, serverID)

		case verifyUncertain:
			entry.RetryCount++
			if entry.RetryCount >= MaxRetryCount {
				log.Printf("Max retries (%d) reached for PID %d (server=%s), giving up",
					MaxRetryCount, entry.PID, serverID)
				toDelete = append(toDelete, serverID)
			} else {
				log.Printf("Cannot verify ownership of PID %d (server=%s), will retry (attempt %d/%d)",
					entry.PID, serverID, entry.RetryCount, MaxRetryCount)
				toUpdate[serverID] = entry
			}
		}
	}

	// Apply deletions
	for _, serverID := range toDelete {
		delete(pt.pids, serverID)
	}

	// Apply updates (retry count increments)
	maps.Copy(pt.pids, toUpdate)

	// Save the updated state
	if err := pt.save(); err != nil {
		log.Printf("Failed to save PID file after cleanup: %v", err)
	}

	return killed
}

// verifyProcessOwnership determines if we still own the process at the given PID.
func (pt *PIDTracker) verifyProcessOwnership(entry pidEntry) verifyResult {
	// First check if process is even running
	if !isProcessRunning(entry.PID) {
		return verifyProcessGone
	}

	// Primary verification: process start time (most reliable for PID reuse detection)
	if entry.ProcessStartTicks > 0 {
		currentTicks, err := getProcessStartTicks(entry.PID)
		if err != nil {
			log.Printf("Cannot get start ticks for PID %d: %v", entry.PID, err)
			// Fall through to secondary verification
		} else if currentTicks != entry.ProcessStartTicks {
			// Start time differs - PID was definitely reused
			log.Printf("PID %d start ticks mismatch: recorded=%d current=%d",
				entry.PID, entry.ProcessStartTicks, currentTicks)
			return verifyConfirmedReused
		} else {
			// Start time matches - this is our process, verify command as sanity check
			if matchesCmdline(entry.PID, entry.Command, entry.Args) {
				return verifyConfirmedOwned
			}
			// Start time matches but cmdline doesn't - could be interpreter (npx->node)
			// This is likely still our process, but we'll be conservative
			log.Printf("PID %d start time matches but cmdline doesn't - likely interpreter wrapper",
				entry.PID)
			return verifyConfirmedOwned
		}
	}

	// Secondary verification: cmdline matching (when start ticks unavailable)
	if matchesCmdline(entry.PID, entry.Command, entry.Args) {
		return verifyConfirmedOwned
	}

	// Can't confirm ownership
	return verifyUncertain
}

// isProcessRunning checks if a process with the given PID exists.
func isProcessRunning(pid int) bool {
	// Signal 0 doesn't send a signal but checks if the process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// getProcessStartTicks returns the OS-level process start time.
// On Linux: clock ticks since boot from /proc/<pid>/stat field 22
// On macOS: process start time as Unix timestamp from ps
func getProcessStartTicks(pid int) (int64, error) {
	switch runtime.GOOS {
	case "linux":
		return getProcessStartTicksLinux(pid)
	case "darwin", "freebsd":
		return getProcessStartTicksDarwin(pid)
	default:
		return 0, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// getProcessStartTicksLinux reads starttime from /proc/<pid>/stat (field 22).
func getProcessStartTicksLinux(pid int) (int64, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, err
	}

	// Format: pid (comm) state ppid ... starttime ...
	// Field 22 is starttime (0-indexed from after the comm field)
	// The comm field can contain spaces and parens, so find the last ')' first
	content := string(data)
	lastParen := strings.LastIndex(content, ")")
	if lastParen == -1 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}

	// Fields after the comm field
	fields := strings.Fields(content[lastParen+1:])
	// starttime is field 22 overall, which is index 19 after comm (fields 1-2 are pid and comm)
	// After ')', we have: state(3) ppid(4) pgrp(5) session(6) tty_nr(7) tpgid(8) flags(9)
	// minflt(10) cminflt(11) majflt(12) cmajflt(13) utime(14) stime(15) cutime(16) cstime(17)
	// priority(18) nice(19) num_threads(20) itrealvalue(21) starttime(22)
	// So starttime is at index 19 (0-based) in the fields after ')'
	if len(fields) < 20 {
		return 0, fmt.Errorf("not enough fields in /proc/%d/stat", pid)
	}

	starttime, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse starttime: %w", err)
	}

	return starttime, nil
}

// getProcessStartTicksDarwin uses ps to get process start time as Unix epoch.
func getProcessStartTicksDarwin(pid int) (int64, error) {
	// ps -p <pid> -o lstart= gives something like "Sat Jan 25 19:00:00 2026"
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "lstart=").Output()
	if err != nil {
		return 0, err
	}

	timeStr := strings.TrimSpace(string(out))
	if timeStr == "" {
		return 0, fmt.Errorf("empty lstart for PID %d", pid)
	}

	// Parse the time format: "Mon Jan  2 15:04:05 2006"
	t, err := time.Parse("Mon Jan  2 15:04:05 2006", timeStr)
	if err != nil {
		// Try alternate format with single-digit day
		t, err = time.Parse("Mon Jan 2 15:04:05 2006", timeStr)
		if err != nil {
			return 0, fmt.Errorf("parse lstart %q: %w", timeStr, err)
		}
	}

	return t.Unix(), nil
}

// getProcessCmdline returns the full command line of a process.
func getProcessCmdline(pid int) ([]string, error) {
	switch runtime.GOOS {
	case "linux":
		return getProcessCmdlineLinux(pid)
	case "darwin", "freebsd":
		return getProcessCmdlineDarwin(pid)
	default:
		return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// getProcessCmdlineLinux reads /proc/<pid>/cmdline (null-separated).
func getProcessCmdlineLinux(pid int) ([]string, error) {
	cmdlinePath := fmt.Sprintf("/proc/%d/cmdline", pid)
	data, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return nil, err
	}

	// Split by null bytes, filter empty strings
	parts := strings.Split(string(data), "\x00")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result, nil
}

// getProcessCmdlineDarwin uses ps to get full command line.
func getProcessCmdlineDarwin(pid int) ([]string, error) {
	// ps -p <pid> -o args= gives the full command line
	out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "args=").Output()
	if err != nil {
		return nil, err
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, fmt.Errorf("empty cmdline for PID %d", pid)
	}

	// Split by whitespace (this is imperfect for args with spaces, but good enough)
	return strings.Fields(line), nil
}

// matchesCmdline checks if the process cmdline contains our expected command.
// Uses tokenized matching: checks if any cmdline arg's basename matches the expected command basename,
// or if any of our args appear in the cmdline.
func matchesCmdline(pid int, expectedCmd string, expectedArgs []string) bool {
	actualCmdline, err := getProcessCmdline(pid)
	if err != nil {
		log.Printf("Cannot get cmdline for PID %d: %v", pid, err)
		return false
	}

	if len(actualCmdline) == 0 {
		return false
	}

	expectedBase := filepath.Base(expectedCmd)

	// Check if expected command basename appears in any cmdline token
	for _, arg := range actualCmdline {
		argBase := filepath.Base(arg)
		if argBase == expectedBase {
			return true
		}
	}

	// Check if any of our expected args appear (helps with interpreter wrappers)
	// e.g., for "npx @anthropic/mcp-server", we'd find "@anthropic/mcp-server" in cmdline
	for _, expectedArg := range expectedArgs {
		expectedArgBase := filepath.Base(expectedArg)
		for _, actualArg := range actualCmdline {
			actualArgBase := filepath.Base(actualArg)
			if actualArgBase == expectedArgBase && expectedArgBase != "" {
				return true
			}
		}
	}

	log.Printf("PID %d cmdline mismatch: expected cmd=%q args=%v, actual=%v",
		pid, expectedCmd, expectedArgs, actualCmdline)
	return false
}

// killProcess terminates a process gracefully.
func killProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	// Try SIGTERM first
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be gone
		return nil
	}

	// We don't wait here since we're cleaning up orphans on startup
	// and don't want to block. The process will be cleaned up by the OS.
	return nil
}

// Legacy compatibility: Add with old signature (for existing callers)
// Deprecated: Use AddWithArgs instead.
func (pt *PIDTracker) AddLegacy(serverID string, pid int, command string) error {
	return pt.Add(serverID, pid, command, nil)
}
