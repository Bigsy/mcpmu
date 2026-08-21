package process

import (
	"time"

	"github.com/Bigsy/mcpmu/internal/flock"
)

// LockFileBlocking opens (or creates) the file at path and acquires an
// exclusive lock on it, retrying the non-blocking platform primitive until
// timeout. It returns a release function that drops the lock by closing the
// file descriptor. The lock file is intentionally never deleted — see
// ManagerLock.Release for the flock inode race that deletion would open.
//
// The implementation lives in internal/flock alongside the atomic-write
// helpers; this wrapper keeps the historical entry point for metrics and
// tests.
func LockFileBlocking(path string, timeout time.Duration) (release func(), err error) {
	return flock.Lock(path, timeout)
}
