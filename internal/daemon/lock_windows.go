//go:build windows

package daemon

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryFileLock(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
}
