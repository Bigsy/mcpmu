package daemon

import (
	"fmt"
	"os"
)

type fileLock struct {
	path string
	file *os.File
}

func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open daemon run lock: %w", err)
	}
	if err := tryFileLock(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another daemon already owns %s: %w", path, err)
	}
	return &fileLock{path: path, file: file}, nil
}

func (lock *fileLock) Close() {
	if lock != nil && lock.file != nil {
		_ = lock.file.Close()
		lock.file = nil
	}
}
