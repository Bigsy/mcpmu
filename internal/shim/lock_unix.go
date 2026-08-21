//go:build !windows

package shim

import (
	"context"
	"os"

	"github.com/Bigsy/mcpmu/internal/flock"
)

type fileLock struct {
	release func()
}

// acquireLock blocks until the lock is held or ctx is done.
func acquireLock(ctx context.Context, path string) (*fileLock, error) {
	release, err := flock.Acquire(ctx, path)
	if err != nil {
		return nil, err
	}
	return &fileLock{release: release}, nil
}

// tryAcquireLock makes one non-blocking attempt.
func tryAcquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := flock.TryLock(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &fileLock{release: func() { _ = file.Close() }}, nil
}

func (lock *fileLock) Close() {
	if lock != nil && lock.release != nil {
		lock.release()
		lock.release = nil
	}
}
