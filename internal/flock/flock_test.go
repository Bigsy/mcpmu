package flock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockMutualExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.lock")

	release1, err := Lock(path, time.Second)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	if _, err := Lock(path, 50*time.Millisecond); err == nil {
		t.Fatal("second lock acquired while held")
	}

	release1()
	if _, err := Lock(path, time.Second); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
}

func TestWithLockSerialisesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")

	const writers = 8
	var inside atomic.Int32
	var violations atomic.Int32
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			err := WithLock(path, func() error {
				if inside.Add(1) != 1 {
					violations.Add(1)
				}
				// Hold long enough that a broken lock lets others in.
				time.Sleep(5 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
			if err != nil {
				t.Errorf("WithLock: %v", err)
			}
		})
	}
	wg.Wait()
	if violations.Load() != 0 {
		t.Fatalf("%d mutual-exclusion violations", violations.Load())
	}
}

func TestWithLockPropagatesErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	sentinel := errors.New("boom")
	if err := WithLock(path, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("WithLock err = %v, want sentinel", err)
	}
}

func TestAcquireRespectsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.lock")

	release, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire err = %v, want DeadlineExceeded", err)
	}
}

func TestWriteAtomicInstallsContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.json")

	if err := WriteAtomic(path, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("file = %q (%v)", got, err)
	}

	// Mode is restricted: these files carry config and credentials.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("mode = %o, want 600", perm)
	}

	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want just the file", len(entries))
	}
}
