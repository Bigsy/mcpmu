// Package process provides process lifecycle management for MCP servers.
package process

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	legacyPIDsFile = "pids.json"
	pidFileVersion = 2

	// MaxRetryCount is the maximum number of cleanup attempts before giving up.
	// This prevents the PID file from growing unbounded with unverifiable entries.
	MaxRetryCount = 5
)

// pidEntry stores PID and metadata for orphan detection.
type pidEntry struct {
	Instance          InstanceID `json:"instance"`
	PID               int        `json:"pid"`
	PGID              int        `json:"pgid,omitempty"`
	Command           string     `json:"command"`                     // Command used to start the process
	Args              []string   `json:"args,omitempty"`              // Arguments for better matching
	StartedAt         time.Time  `json:"startedAt"`                   // Wall clock time when we started it
	ProcessStartTicks int64      `json:"processStartTicks,omitempty"` // OS-level process start time (for PID reuse detection)
	RetryCount        int        `json:"retryCount,omitempty"`        // Number of failed verification attempts
}

type ownerIdentity struct {
	PID               int    `json:"pid"`
	ProcessStartTicks int64  `json:"processStartTicks"`
	Nonce             string `json:"nonce"`
}

type pidRegistry struct {
	Version int           `json:"version"`
	Owner   ownerIdentity `json:"owner"`
	Entries []pidEntry    `json:"entries"`
}

// PIDTracker tracks running server PIDs to detect and clean up orphans.
type PIDTracker struct {
	dir   string
	path  string
	owner ownerIdentity
	pids  map[InstanceID]pidEntry
	mu    sync.Mutex
}

// NewPIDTracker creates a new PID tracker using the default directory.
func NewPIDTracker() (*PIDTracker, error) {
	return NewPIDTrackerInDir("", "")
}

// NewPIDTrackerWithDir creates a new PID tracker storing its state in the given directory.
func NewPIDTrackerWithDir(dir string) (*PIDTracker, error) {
	return NewPIDTrackerInDir(dir, "")
}

// NewPIDTrackerInDir creates a per-owner PID tracker in the given directory.
// If dir is empty, uses the default ~/.config/mcpmu/ directory.
// The optional prefix is retained for operator readability; owner identity,
// rather than manager mode, provides concurrency-safe isolation.
func NewPIDTrackerInDir(dir, prefix string) (*PIDTracker, error) {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(home, ".config", "mcpmu")
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create PID registry directory: %w", err)
	}

	startTicks, err := getProcessStartTicks(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("identify PID registry owner: %w", err)
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("create PID registry owner nonce: %w", err)
	}
	owner := ownerIdentity{
		PID:               os.Getpid(),
		ProcessStartTicks: startTicks,
		Nonce:             hex.EncodeToString(nonceBytes),
	}

	filePrefix := "pids"
	if prefix != "" {
		filePrefix += "-" + prefix
	}
	fileName := fmt.Sprintf("%s-owner-%d-%s.json", filePrefix, owner.PID, owner.Nonce)

	pt := &PIDTracker{
		dir:   dir,
		path:  filepath.Join(dir, fileName),
		owner: owner,
		pids:  make(map[InstanceID]pidEntry),
	}

	return pt, nil
}

// save writes only this owner's registry using temp-file + atomic rename.
func (pt *PIDTracker) save() error {
	if len(pt.pids) == 0 {
		if err := os.Remove(pt.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	entries := make([]pidEntry, 0, len(pt.pids))
	for _, entry := range pt.pids {
		entries = append(entries, entry)
	}
	record := pidRegistry{Version: pidFileVersion, Owner: pt.owner, Entries: entries}
	return writeRegistryAtomic(pt.path, record)
}

// Add tracks a new PID for a server.
func (pt *PIDTracker) Add(serverID string, pid int, command string, args []string) error {
	return pt.AddInstance(SharedInstanceID(serverID), pid, pid, command, args)
}

// AddInstance tracks a process leader and its process group for one instance.
func (pt *PIDTracker) AddInstance(instance InstanceID, pid, pgid int, command string, args []string) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	entry := pidEntry{
		Instance:  instance,
		PID:       pid,
		PGID:      pgid,
		Command:   command,
		Args:      append([]string(nil), args...),
		StartedAt: time.Now(),
	}

	startTicks, err := getProcessStartTicks(pid)
	if err != nil {
		return fmt.Errorf("identify process leader %d: %w", pid, err)
	}
	entry.ProcessStartTicks = startTicks

	previous, hadPrevious := pt.pids[instance]
	pt.pids[instance] = entry
	if err := pt.save(); err != nil {
		if hadPrevious {
			pt.pids[instance] = previous
		} else {
			delete(pt.pids, instance)
		}
		return err
	}
	return nil
}

// Remove stops tracking a PID.
func (pt *PIDTracker) Remove(serverID string) error {
	return pt.RemoveInstance(SharedInstanceID(serverID))
}

// RemoveInstance stops tracking an instance in this owner's registry.
func (pt *PIDTracker) RemoveInstance(instance InstanceID) error {
	return pt.RemoveInstancePID(instance, 0)
}

// RemoveInstancePID removes an entry only if it still refers to the expected
// leader PID. A zero PID requests unconditional removal for compatibility.
func (pt *PIDTracker) RemoveInstancePID(instance InstanceID, expectedPID int) error {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	previous, exists := pt.pids[instance]
	if !exists || expectedPID != 0 && previous.PID != expectedPID {
		return nil
	}
	delete(pt.pids, instance)
	if err := pt.save(); err != nil {
		pt.pids[instance] = previous
		return err
	}
	return nil
}

// verifyResult represents the outcome of process ownership verification.
type verifyResult int

const (
	verifyConfirmedOwned  verifyResult = iota // We own this process - safe to kill
	verifyConfirmedReused                     // PID was reused by another process - safe to remove entry
	verifyProcessGone                         // Process no longer exists - safe to remove entry
	verifyUncertain                           // Can't verify ownership - keep entry and retry later
)

// CleanupOrphans scans other owners' registry files. Live, identity-matching
// owners are skipped; dead owners are cleaned conservatively.
func (pt *PIDTracker) CleanupOrphans() int {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	paths, err := filepath.Glob(filepath.Join(pt.dir, "pids*.json"))
	if err != nil {
		log.Printf("Failed to scan PID registries: %v", err)
		return 0
	}

	killed := 0
	for _, path := range paths {
		if path == pt.path {
			continue
		}
		count, err := pt.cleanupRegistry(path)
		if err != nil {
			log.Printf("Failed to clean PID registry %s: %v", path, err)
			continue
		}
		killed += count
	}
	return killed
}

func (pt *PIDTracker) cleanupRegistry(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var record pidRegistry
	if err := json.Unmarshal(data, &record); err != nil || record.Version != pidFileVersion {
		return pt.cleanupLegacyRegistry(path, data)
	}

	ownerResult := verifyIdentity(record.Owner.PID, record.Owner.ProcessStartTicks)
	if ownerResult == verifyConfirmedOwned {
		return 0, nil
	}
	if ownerResult == verifyUncertain {
		return 0, fmt.Errorf("cannot verify registry owner pid=%d", record.Owner.PID)
	}

	killed := 0
	remaining := record.Entries[:0]
	for _, entry := range record.Entries {
		resolved, terminated := pt.cleanupDeadOwnerEntry(entry)
		if terminated {
			killed++
		}
		if !resolved {
			remaining = append(remaining, entry)
		}
	}
	record.Entries = remaining
	if len(record.Entries) == 0 {
		return killed, removeRegistry(path)
	}
	return killed, writeRegistryAtomic(path, record)
}

func (pt *PIDTracker) cleanupDeadOwnerEntry(entry pidEntry) (resolved, terminated bool) {
	identity := pt.verifyProcessOwnership(entry)
	switch identity {
	case verifyConfirmedOwned:
		log.Printf("Found orphan process group: instance=%s pid=%d pgid=%d, terminating",
			entry.Instance, entry.PID, entry.PGID)
		if err := terminateTrackedProcess(entry); err != nil {
			log.Printf("Failed to terminate orphan instance=%s: %v", entry.Instance, err)
			return false, false
		}
		return true, true
	case verifyConfirmedReused:
		log.Printf("PID %d was reused; refusing to signal recorded group for instance=%s",
			entry.PID, entry.Instance)
		return true, false
	case verifyProcessGone:
		alive, err := processGroupAlive(entry.PGID)
		if err != nil {
			log.Printf("Cannot inspect leaderless group %d for instance=%s: %v",
				entry.PGID, entry.Instance, err)
			return false, false
		}
		if alive {
			log.Printf("Retaining unverifiable leaderless group %d for instance=%s; manual cleanup required",
				entry.PGID, entry.Instance)
			return false, false
		}
		return true, false
	case verifyUncertain:
		return false, false
	default:
		return false, false
	}
}

func terminateTrackedProcess(entry pidEntry) error {
	if entry.PGID > 0 {
		return terminateProcessGroup(entry.PGID, GracefulShutdownTimeout)
	}
	return killProcess(entry.PID)
}

func (pt *PIDTracker) cleanupLegacyRegistry(path string, data []byte) (int, error) {
	var entries map[string]pidEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return 0, fmt.Errorf("parse PID registry: %w", err)
	}

	killed := 0
	for serverName, entry := range entries {
		entry.Instance = SharedInstanceID(serverName)
		switch pt.verifyProcessOwnership(entry) {
		case verifyConfirmedOwned:
			if err := killProcess(entry.PID); err != nil {
				entry.RetryCount++
				entries[serverName] = entry
				continue
			}
			killed++
			delete(entries, serverName)
		case verifyConfirmedReused, verifyProcessGone:
			delete(entries, serverName)
		case verifyUncertain:
			entry.RetryCount++
			if entry.RetryCount >= MaxRetryCount {
				delete(entries, serverName)
			} else {
				entries[serverName] = entry
			}
		}
	}
	if len(entries) == 0 {
		return killed, removeRegistry(path)
	}
	updated, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return killed, err
	}
	return killed, writeBytesAtomic(path, updated)
}

func writeRegistryAtomic(path string, record pidRegistry) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeBytesAtomic(path, data)
}

func writeBytesAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pids-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func removeRegistry(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func verifyIdentity(pid int, startTicks int64) verifyResult {
	if !isProcessRunning(pid) {
		return verifyProcessGone
	}
	if startTicks <= 0 {
		return verifyUncertain
	}
	current, err := getProcessStartTicks(pid)
	if err != nil {
		return verifyUncertain
	}
	if current != startTicks {
		return verifyConfirmedReused
	}
	return verifyConfirmedOwned
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
	return processRunning(pid)
}

// getProcessStartTicks returns the OS-level process start time.
// On Linux: clock ticks since boot from /proc/<pid>/stat field 22
// On macOS: native kernel process start time from sysctl.
func getProcessStartTicks(pid int) (int64, error) {
	return getProcessStartTicksPlatform(pid)
}

// ProcessStartIdentity returns the OS-provided start identity for pid. The
// value is only meaningful when compared with another observation from the
// same platform; it is used to reject reused PIDs before signalling them.
func ProcessStartIdentity(pid int) (int64, error) {
	return getProcessStartTicks(pid)
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
	return terminateProcess(pid)
}

// Legacy compatibility: Add with old signature (for existing callers)
// Deprecated: Use AddWithArgs instead.
func (pt *PIDTracker) AddLegacy(serverID string, pid int, command string) error {
	return pt.Add(serverID, pid, command, nil)
}
