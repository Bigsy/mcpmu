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
	locks   sync.Map // resourceSubscriptionKey -> *sync.Mutex
}

func newResourceSubscriptions() *resourceSubscriptions {
	return &resourceSubscriptions{entries: make(map[resourceSubscriptionKey]*resourceSubscription)}
}

func (r *resourceSubscriptions) keyLock(key resourceSubscriptionKey) *sync.Mutex {
	lock, _ := r.locks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
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
	lock := r.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

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
	lock := r.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

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
	lock := r.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

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
	lock := r.keyLock(key)
	lock.Lock()
	defer lock.Unlock()
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry := r.entries[key]
	return entry != nil && len(entry.sessions) > 0
}

// clear invalidates every in-flight operation and drops all retained intent.
// Closing transports during reload removes the corresponding upstream state,
// so no unsubscribe RPCs are sent here.
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
