package server

import (
	"fmt"
	"sync"
	"testing"

	"github.com/Bigsy/mcpmu/internal/process"
)

// countKeyLocks reports how many per-key mutexes the registry is retaining.
func countKeyLocks(r *resourceSubscriptions) int {
	r.lockGuard.Lock()
	defer r.lockGuard.Unlock()
	return len(r.locks)
}

// TestKeyLocksDoNotAccumulate guards against the per-key mutex map growing
// without bound.
//
// The map is keyed by (InstanceID, URI) and URIs come from the client, in a
// daemon built to run for hours. Nothing ever removed an entry: a completed
// subscribe/unsubscribe cycle left its mutex behind, and clear() reset entries
// and bumped epoch without touching locks. 5000 cycles retained 5000 mutexes
// against entries=0.
//
// The mutexes are now reference counted and dropped when the last operation on
// a key finishes, so retention tracks in-flight operations rather than every
// URI ever seen.
func TestKeyLocksDoNotAccumulate(t *testing.T) {
	t.Parallel()
	registry := newResourceSubscriptions()
	session := bareSubscriptionSession()
	instance := process.SharedInstanceID("files")

	const cycles = 5000
	for i := range cycles {
		key := resourceSubscriptionKey{
			Instance: instance,
			URI:      fmt.Sprintf("file:///doc-%d.txt", i),
		}
		if _, err := registry.subscribe(session, key, 1, func() error { return nil }); err != nil {
			t.Fatalf("subscribe %d: %v", i, err)
		}
		if err := registry.unsubscribe(session, key, func(uint64) error { return nil }); err != nil {
			t.Fatalf("unsubscribe %d: %v", i, err)
		}
	}

	if got := len(registry.entries); got != 0 {
		t.Errorf("entries = %d, want 0 after every cycle unsubscribed", got)
	}
	if got := countKeyLocks(registry); got != 0 {
		t.Errorf("retained %d key locks after %d completed cycles, want 0", got, cycles)
	}

	// hasSubscribers is on the notification path and takes the key lock too; a
	// miss must not leave a mutex behind either.
	for i := range cycles {
		registry.hasSubscribers(resourceSubscriptionKey{
			Instance: instance,
			URI:      fmt.Sprintf("file:///missing-%d.txt", i),
		})
	}
	if got := countKeyLocks(registry); got != 0 {
		t.Errorf("retained %d key locks after %d hasSubscribers misses, want 0", got, cycles)
	}

	// clear() drops retained intent; it must not leave the lock map populated.
	for i := range 100 {
		key := resourceSubscriptionKey{Instance: instance, URI: fmt.Sprintf("file:///kept-%d.txt", i)}
		if _, err := registry.subscribe(session, key, 1, func() error { return nil }); err != nil {
			t.Fatalf("subscribe kept-%d: %v", i, err)
		}
	}
	registry.clear()
	if got := countKeyLocks(registry); got != 0 {
		t.Errorf("retained %d key locks after clear(), want 0", got)
	}
}

// TestKeyLockSerializesSameKey is the invariant that makes the refcounting
// non-trivial: recycling a mutex must never let two operations on one key run
// concurrently. If a mutex were dropped while another goroutine still needed it,
// the two would serialize on different mutexes and this would detect the
// overlap.
//
// The registry deliberately serializes each key's upstream RPC with its local
// state transition, so that a resources/updated notification arriving right
// after a response observes the completed transition — hasSubscribers depends on
// it.
func TestKeyLockSerializesSameKey(t *testing.T) {
	t.Parallel()
	registry := newResourceSubscriptions()
	instance := process.SharedInstanceID("files")
	key := resourceSubscriptionKey{Instance: instance, URI: "file:///contended.txt"}

	var (
		mu       sync.Mutex
		inFlight int
		maxSeen  int
	)
	enter := func() {
		mu.Lock()
		inFlight++
		maxSeen = max(maxSeen, inFlight)
		mu.Unlock()
	}
	leave := func() {
		mu.Lock()
		inFlight--
		mu.Unlock()
	}

	// Churn the same key from many goroutines: subscribes, unsubscribes and
	// notification-path lookups all contend for its lock, and every completed
	// operation is a chance to drop and recreate the mutex.
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			session := bareSubscriptionSession()
			for range 200 {
				_, _ = registry.subscribe(session, key, 1, func() error {
					enter()
					defer leave()
					return nil
				})
				_ = registry.unsubscribe(session, key, func(uint64) error {
					enter()
					defer leave()
					return nil
				})
				if g%2 == 0 {
					registry.hasSubscribers(key)
				}
			}
		})
	}
	wg.Wait()

	if maxSeen > 1 {
		t.Errorf("observed %d concurrent upstream callbacks for one key, want 1: "+
			"per-key serialization was lost", maxSeen)
	}
	if got := countKeyLocks(registry); got != 0 {
		t.Errorf("retained %d key locks after the churn settled, want 0", got)
	}
}
