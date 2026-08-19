package process

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockFileBlocking_MutualExclusion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	release1, err := LockFileBlocking(path, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		release2, err := LockFileBlocking(path, 5*time.Second)
		if err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		acquired.Store(true)
		release2()
	}()

	// The second acquirer must block while the first holds the lock.
	time.Sleep(150 * time.Millisecond)
	if acquired.Load() {
		t.Fatal("second acquire succeeded while lock was held")
	}

	release1()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second acquire did not complete after release")
	}
	if !acquired.Load() {
		t.Fatal("second acquire never succeeded")
	}
}

func TestLockFileBlocking_Timeout(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	release, err := LockFileBlocking(path, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	start := time.Now()
	_, err = LockFileBlocking(path, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error while lock is held")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("gave up too early: %s", elapsed)
	}
}

func TestLockFileBlocking_Sequential(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.lock")

	for range 3 {
		release, err := LockFileBlocking(path, time.Second)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		release()
	}
}
