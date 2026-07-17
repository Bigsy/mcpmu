//go:build !windows

package shim

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

type fileLock struct {
	file *os.File
}

func acquireLock(ctx context.Context, path string) (*fileLock, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		lock, err := tryAcquireLock(path)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func tryAcquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &fileLock{file: file}, nil
}

func (lock *fileLock) Close() {
	if lock != nil && lock.file != nil {
		_ = lock.file.Close()
		lock.file = nil
	}
}
