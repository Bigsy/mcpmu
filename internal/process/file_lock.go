package process

import (
	"fmt"
	"os"
	"time"
)

// LockFileBlocking opens (or creates) the file at path and acquires an
// exclusive lock on it, retrying the non-blocking platform primitive until
// timeout. It returns a release function that drops the lock by closing the
// file descriptor. The lock file is intentionally never deleted — see
// ManagerLock.Release for the flock inode race that deletion would open.
func LockFileBlocking(path string, timeout time.Duration) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		lockErr := tryLockFile(f)
		if lockErr == nil {
			return func() { _ = f.Close() }, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: gave up after %s: %w", path, timeout, lockErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
