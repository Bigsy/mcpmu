//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func tryFileLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
