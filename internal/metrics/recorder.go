package metrics

import (
	"log"
	"sync"
	"time"
)

const (
	// flushInterval is how often the accumulated delta is merged to disk.
	flushInterval = 30 * time.Second
	// maxDeltaKeys is a soft cardinality cap: tool names come from arbitrary
	// upstreams, so refuse new bucket keys past this many per delta so a
	// hostile or buggy upstream can't balloon the file.
	maxDeltaKeys = 5000
)

// Recorder accumulates call samples in memory and periodically flushes the
// delta into metrics.json. A nil *Recorder is valid: Record, Flush, and Close
// are no-ops, keeping every call site unconditional (tests, metrics disabled,
// no config path).
type Recorder struct {
	mu            sync.Mutex
	path          string
	retentionDays int
	delta         map[BucketKey]*Counters // since last successful flush
	recent        []RecentCall            // since last successful flush, oldest first
	capWarned     bool

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	closeErr error
}

// NewRecorder creates a recorder writing to path and starts the background
// flush ticker.
func NewRecorder(path string, retentionDays int) *Recorder {
	r := &Recorder{
		path:          path,
		retentionDays: retentionDays,
		delta:         make(map[BucketKey]*Counters),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	go r.flushLoop()
	return r
}

// SetRetentionDays updates the retention window applied on the next flush.
func (r *Recorder) SetRetentionDays(days int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.retentionDays = days
	r.mu.Unlock()
}

// Record accumulates one call sample. It never does I/O and never blocks a
// tool call beyond the mutex.
func (r *Recorder) Record(s CallSample) {
	if r == nil {
		return
	}
	key := BucketKey{
		Date:      s.Time.Format(dateLayout),
		Namespace: s.Namespace,
		Server:    s.Server,
		Tool:      s.Tool,
	}
	ms := durationMs(s.Duration)

	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.delta[key]
	if !ok {
		if len(r.delta) >= maxDeltaKeys {
			if !r.capWarned {
				r.capWarned = true
				log.Printf("Warning: metrics delta reached %d distinct buckets; dropping new buckets until next flush", maxDeltaKeys)
			}
			return
		}
		c = newCounters()
		r.delta[key] = c
	}
	c.addSample(ms, s.Outcome)

	r.recent = append(r.recent, RecentCall{
		Time:       s.Time,
		Namespace:  s.Namespace,
		Server:     s.Server,
		Tool:       s.Tool,
		DurationMs: ms,
		Outcome:    s.Outcome,
	})
	if len(r.recent) > recentCap {
		r.recent = r.recent[len(r.recent)-recentCap:]
	}
}

// Flush merges the accumulated delta into the metrics file. The delta is
// swapped out before doing I/O; on save failure it is merged back so nothing
// is lost.
func (r *Recorder) Flush() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if len(r.delta) == 0 && len(r.recent) == 0 {
		r.mu.Unlock()
		return nil
	}
	delta, recent, retention := r.delta, r.recent, r.retentionDays
	r.delta = make(map[BucketKey]*Counters)
	r.recent = nil
	r.mu.Unlock()

	if err := mergeAndSave(r.path, delta, recent, retention); err != nil {
		r.mu.Lock()
		for key, c := range delta {
			if existing, ok := r.delta[key]; ok {
				existing.merge(c)
			} else {
				r.delta[key] = c
			}
		}
		// Restored entries are older than anything recorded since the swap.
		merged := append(recent, r.recent...)
		if len(merged) > recentCap {
			merged = merged[len(merged)-recentCap:]
		}
		r.recent = merged
		r.mu.Unlock()
		return err
	}

	r.mu.Lock()
	r.capWarned = false
	r.mu.Unlock()
	return nil
}

// Close stops the flush ticker and performs a final flush. Idempotent.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
		<-r.doneCh
		r.closeErr = r.Flush()
	})
	return r.closeErr
}

func (r *Recorder) flushLoop() {
	defer close(r.doneCh)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			if err := r.Flush(); err != nil {
				log.Printf("Warning: metrics flush failed (will retry): %v", err)
			}
		}
	}
}
