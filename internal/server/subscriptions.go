package server

import (
	"sync"

	"github.com/Bigsy/mcpmu/internal/process"
)

// resourceSubscriptionKey identifies one upstream subscription. URI alone is
// insufficient because different sessions may resolve the same URI to
// different upstream instances.
type resourceSubscriptionKey struct {
	Instance process.InstanceID
	URI      string
}

type resourceSubscription struct {
	sessions   map[*Session]struct{}
	generation uint64
}

// resourceSubscriptions owns daemon-wide subscription intent. Operations for
// one key are serialized across the upstream RPC and local state transition so
// an immediately following resources/updated notification observes the
// completed transition. The state mutex is never held during upstream I/O.
type resourceSubscriptions struct {
	mu      sync.RWMutex
	entries map[resourceSubscriptionKey]*resourceSubscription
	epoch   uint64

	// lockGuard guards the locks map and the entries' refs counters. It is only
	// ever held for the few instructions needed to find or drop an entry, never
	// across a key lock acquisition or upstream I/O.
	lockGuard sync.Mutex
	locks     map[resourceSubscriptionKey]*keyLockEntry
}

// keyLockEntry is the per-key mutex plus a count of the operations that need it.
//
// The count exists so the map cannot grow without bound: keys are
// (InstanceID, URI) with client-supplied URIs, and hasSubscribers mints one for
// every notification URI too, so in a daemon running for hours a map that only
// ever grew would retain a mutex for every URI ever mentioned. Retention now
// tracks concurrent operations instead.
//
// Dropping the entry cannot simply happen when an operation finishes: another
// goroutine may already be blocked on this mutex, and replacing it would let two
// operations on one key run at once — exactly the serialization hasSubscribers
// relies on. refs is therefore incremented under lockGuard *before* the mutex is
// taken, so an entry reachable by any operation always has refs > 0 and only the
// last one out removes it.
type keyLockEntry struct {
	mu   sync.Mutex
	refs int
}

func newResourceSubscriptions() *resourceSubscriptions {
	return &resourceSubscriptions{
		entries: make(map[resourceSubscriptionKey]*resourceSubscription),
		locks:   make(map[resourceSubscriptionKey]*keyLockEntry),
	}
}

// lockKey acquires the lock for one key. Every call must be paired with
// unlockKey on the returned entry.
func (r *resourceSubscriptions) lockKey(key resourceSubscriptionKey) *keyLockEntry {
	r.lockGuard.Lock()
	entry := r.locks[key]
	if entry == nil {
		entry = &keyLockEntry{}
		r.locks[key] = entry
	}
	entry.refs++
	r.lockGuard.Unlock()

	entry.mu.Lock()
	return entry
}

// unlockKey releases the key lock and discards the entry once no operation
// still refers to it.
func (r *resourceSubscriptions) unlockKey(key resourceSubscriptionKey, entry *keyLockEntry) {
	entry.mu.Unlock()

	r.lockGuard.Lock()
	entry.refs--
	if entry.refs == 0 {
		// Only delete the entry still registered under this key. clear() may
		// have replaced the map, in which case this one is already unreachable.
		if current := r.locks[key]; current == entry {
			delete(r.locks, key)
		}
	}
	r.lockGuard.Unlock()
}

// subscribe applies a transition for one downstream session. The upstream
// callback runs only for 0→1 or when retained intent must be established on a
// new process generation. A failed first subscribe leaves no local state. If
// re-establishing retained intent fails, all prior subscribers are dropped and
// returned so the Core can notify them to re-resolve resources.
func (r *resourceSubscriptions) subscribe(
	session *Session,
	key resourceSubscriptionKey,
	generation uint64,
	upstream func() error,
) (dropped []*Session, err error) {
	lock := r.lockKey(key)
	defer r.unlockKey(key, lock)

	r.mu.RLock()
	epoch := r.epoch
	entry := r.entries[key]
	alreadySubscribed := entry != nil && entry.sessions != nil
	if alreadySubscribed {
		_, alreadySubscribed = entry.sessions[session]
	}
	currentGeneration := entry != nil && entry.generation == generation
	hasIntent := entry != nil && len(entry.sessions) > 0
	r.mu.RUnlock()

	if alreadySubscribed && currentGeneration {
		return nil, nil
	}
	if !hasIntent || !currentGeneration {
		if err := upstream(); err != nil {
			if hasIntent && !currentGeneration {
				r.mu.Lock()
				if epoch == r.epoch {
					if current := r.entries[key]; current != nil && current.generation != generation {
						dropped = sessionsFromSubscription(current)
						delete(r.entries, key)
						for _, subscribedSession := range dropped {
							subscribedSession.deleteSubscription(key)
						}
					}
				}
				r.mu.Unlock()
			}
			return dropped, err
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// A config reload invalidated this operation while its upstream request
	// was in flight. The soon-to-stop transport owns any upstream-side state;
	// do not resurrect cleared local intent.
	if epoch != r.epoch {
		return nil, nil
	}
	entry = r.entries[key]
	if entry == nil {
		entry = &resourceSubscription{sessions: make(map[*Session]struct{})}
		r.entries[key] = entry
	}
	entry.generation = generation
	entry.sessions[session] = struct{}{}
	session.setSubscription(key)
	return nil, nil
}

// unsubscribe removes one session's intent. The upstream callback runs only
// on 1→0. Local removal is unconditional even when the upstream RPC fails.
func (r *resourceSubscriptions) unsubscribe(
	session *Session,
	key resourceSubscriptionKey,
	upstream func(generation uint64) error,
) error {
	lock := r.lockKey(key)
	defer r.unlockKey(key, lock)

	r.mu.RLock()
	entry := r.entries[key]
	var subscribed bool
	if entry != nil {
		_, subscribed = entry.sessions[session]
	}
	last := subscribed && len(entry.sessions) == 1
	var generation uint64
	if entry != nil {
		generation = entry.generation
	}
	r.mu.RUnlock()
	if !subscribed {
		session.deleteSubscription(key)
		return nil
	}

	var err error
	if last && upstream != nil {
		err = upstream(generation)
	}

	r.mu.Lock()
	if current := r.entries[key]; current != nil {
		delete(current.sessions, session)
		if len(current.sessions) == 0 {
			delete(r.entries, key)
		}
	}
	session.deleteSubscription(key)
	r.mu.Unlock()
	return err
}

// replay establishes retained subscription intent on a new upstream process
// generation. Failure drops the key for every affected session.
func (r *resourceSubscriptions) replay(
	key resourceSubscriptionKey,
	generation uint64,
	upstream func() error,
) (dropped []*Session, err error) {
	lock := r.lockKey(key)
	defer r.unlockKey(key, lock)

	r.mu.RLock()
	epoch := r.epoch
	entry := r.entries[key]
	if entry == nil || len(entry.sessions) == 0 || entry.generation == generation {
		r.mu.RUnlock()
		return nil, nil
	}
	r.mu.RUnlock()

	if err := upstream(); err != nil {
		r.mu.Lock()
		if epoch == r.epoch {
			if current := r.entries[key]; current != nil && current.generation != generation {
				dropped = sessionsFromSubscription(current)
				delete(r.entries, key)
				for _, session := range dropped {
					session.deleteSubscription(key)
				}
			}
		}
		r.mu.Unlock()
		return dropped, err
	}

	r.mu.Lock()
	if epoch == r.epoch {
		if current := r.entries[key]; current != nil && len(current.sessions) > 0 {
			current.generation = generation
		}
	}
	r.mu.Unlock()
	return nil, nil
}

func (r *resourceSubscriptions) keysForInstance(id process.InstanceID) []resourceSubscriptionKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]resourceSubscriptionKey, 0)
	for key, entry := range r.entries {
		if key.Instance == id && len(entry.sessions) > 0 {
			keys = append(keys, key)
		}
	}
	return keys
}

// hasSubscribers synchronizes with subscribe/unsubscribe RPC transitions so
// notifications emitted immediately after a response see committed state.
func (r *resourceSubscriptions) hasSubscribers(key resourceSubscriptionKey) bool {
	lock := r.lockKey(key)
	defer r.unlockKey(key, lock)
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry := r.entries[key]
	return entry != nil && len(entry.sessions) > 0
}

// clear invalidates every in-flight operation and drops all retained intent.
// Closing transports during reload removes the corresponding upstream state,
// so no unsubscribe RPCs are sent here.
//
// The key locks are deliberately left alone. They are refcounted, so the map
// holds only what in-flight operations are using and drains by itself as those
// finish. Replacing it here would hand a new operation a different mutex for a
// key an older one is still holding, which is the one way to get two concurrent
// operations on a single key.
func (r *resourceSubscriptions) clear() {
	r.mu.Lock()
	r.epoch++
	r.entries = make(map[resourceSubscriptionKey]*resourceSubscription)
	r.mu.Unlock()
}

func sessionsFromSubscription(entry *resourceSubscription) []*Session {
	sessions := make([]*Session, 0, len(entry.sessions))
	for session := range entry.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}
