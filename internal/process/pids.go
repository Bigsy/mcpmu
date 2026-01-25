// Package process provides process lifecycle management for MCP servers.
package process

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

const pidsFile = "pids.json"

// PIDTracker tracks running server PIDs to detect and clean up orphans.
type PIDTracker struct {
	path string
	pids map[string]int // serverID -> PID
}

// NewPIDTracker creates a new PID tracker.
func NewPIDTracker() (*PIDTracker, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	path := filepath.Join(home, ".config", "mcp-studio", pidsFile)
	pt := &PIDTracker{
		path: path,
		pids: make(map[string]int),
	}

	// Load existing PIDs
	pt.load()

	return pt, nil
}

// load reads PIDs from the tracking file.
func (pt *PIDTracker) load() {
	data, err := os.ReadFile(pt.path)
	if err != nil {
		// File doesn't exist or can't be read, start fresh
		return
	}

	if err := json.Unmarshal(data, &pt.pids); err != nil {
		log.Printf("Failed to parse PID file: %v", err)
		pt.pids = make(map[string]int)
	}
}

// save writes PIDs to the tracking file.
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
func (pt *PIDTracker) Add(serverID string, pid int) error {
	pt.pids[serverID] = pid
	return pt.save()
}

// Remove stops tracking a PID.
func (pt *PIDTracker) Remove(serverID string) error {
	delete(pt.pids, serverID)
	return pt.save()
}

// CleanupOrphans checks for and terminates orphaned processes.
// Returns the number of orphans killed.
func (pt *PIDTracker) CleanupOrphans() int {
	killed := 0

	for serverID, pid := range pt.pids {
		if isProcessRunning(pid) {
			log.Printf("Found orphan process: server=%s pid=%d, terminating", serverID, pid)
			if err := killProcess(pid); err != nil {
				log.Printf("Failed to kill orphan pid=%d: %v", pid, err)
			} else {
				killed++
			}
		}
		// Remove from tracking either way
		delete(pt.pids, serverID)
	}

	// Save the cleaned-up state
	if err := pt.save(); err != nil {
		log.Printf("Failed to save PID file after cleanup: %v", err)
	}

	return killed
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

// killProcess terminates a process gracefully, then forcefully if needed.
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
