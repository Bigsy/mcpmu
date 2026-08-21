// Package flock centralises mcpmu's file-locking and atomic-write helpers.
//
// Every persistent JSON file mcpmu writes (config.json, toolcache.json, the
// OAuth credential store, pidfiles, metrics) shares the same two hazards:
// concurrent writers tearing each other's output, and read-modify-write
// cycles that lose updates. The primitives here give both a single
// implementation:
//
//   - Lock/WithLock/Acquire take an exclusive advisory lock on a dedicated,
//     never-deleted lock path (flock on Unix, LockFileEx on Windows). The
//     lock file is never renamed or removed — deleting it reopens the
//     classic inode race where two processes end up locking different files.
//   - WriteAtomic installs bytes via temp file + fsync + rename, the same
//     pattern as the daemon's pidfile. Readers never need the lock.
//
// Callers that need a whole read-modify-write cycle protected compose them:
// hold WithLock around the read and the WriteAtomic install. Locking only
// the write would fix torn files but still lose concurrent updates.
package flock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultTimeout bounds how long WithLock waits for contention. The guarded
// sections are sub-millisecond writes; waiting this long means something is
// badly wrong, and failing beats hanging a request handler forever.
const DefaultTimeout = 5 * time.Second

// pollInterval is the retry cadence for blocking acquisition. The platform
// primitives used here are non-blocking, so waiting is polling.
const pollInterval = 20 * time.Millisecond

// lockPath derives the lock file for a data file.
func lockPath(path string) string {
	return path + ".lock"
}

// TryLock takes an exclusive advisory lock on an already-open file without
// blocking. The lock is held as long as the file descriptor stays open;
// closing it releases. EINTR is retried defensively — unreachable with
// LOCK_NB today, free insurance for future blocking variants.
func TryLock(file *os.File) error {
	for {
		err := tryLockFile(file)
		if errors.Is(err, errInterrupted) {
			continue
		}
		return err
	}
}

// Lock opens (or creates) path and acquires an exclusive lock on it, retrying
// until timeout. It returns a release function that drops the lock by closing
// the descriptor. The lock file is intentionally never deleted — see the
// package comment.
func Lock(path string, timeout time.Duration) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		lockErr := TryLock(f)
		if lockErr == nil {
			return func() { _ = f.Close() }, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: gave up after %s: %w", path, timeout, lockErr)
		}
		time.Sleep(pollInterval)
	}
}

// Acquire is Lock bounded by a context instead of a timeout, for callers that
// hold the lock for a long-lived operation and release it explicitly.
func Acquire(ctx context.Context, path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		lockErr := TryLock(f)
		if lockErr == nil {
			return func() { _ = f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// WithLock runs fn while holding the exclusive lock on lockPath(path).
//
// Do not nest: a fn that takes WithLock on the same path deadlocks against
// its own lock (flock conflicts between two open descriptions even within
// one process). Callers structure their code so one layer owns the lock —
// helpers inside the critical section use WriteAtomic or Update-free
// primitives.
func WithLock(path string, fn func() error) error {
	release, err := Lock(lockPath(path), DefaultTimeout)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// WriteAtomic installs data at path via temp file + fsync + rename, mirroring
// writePIDFile. A crash leaves either the old file or the new one, never a
// half-written mix; readers need no lock.
func WriteAtomic(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temp for %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp for %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}
