//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package flock

import (
	"os"
	"syscall"
)

// errInterrupted is matched by TryLock's EINTR retry.
var errInterrupted = syscall.EINTR

func tryLockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
