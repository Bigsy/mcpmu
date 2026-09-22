package metrics

import (
	"slices"
	"strings"
	"time"
)

// InteractionSample is one server-to-client request (an elicitation, a
// sampling request) that serve mode relayed to a client or answered with a
// fallback. Outcome is the client's action ("accept", "decline", "cancel")
// or why the fallback was sent ("fallback:timeout", ...). Names and outcomes
// only — never the request's content or the user's answer.
type InteractionSample struct {
	Time      time.Time
	Namespace string
	Server    string
	Method    string
	Outcome   string
}

// InteractionKey identifies one daily interaction counter.
type InteractionKey struct {
	Date      string
	Namespace string
	Server    string
	Method    string
	Outcome   string
}

type interactionRow struct {
	Date      string `json:"date"`
	Namespace string `json:"namespace,omitempty"`
	Server    string `json:"server"`
	Method    string `json:"method"`
	Outcome   string `json:"outcome"`
	Count     uint64 `json:"count"`
}

// RecordInteraction counts one relayed server-to-client request. Like Record
// it never does I/O.
func (r *Recorder) RecordInteraction(s InteractionSample) {
	if r == nil {
		return
	}
	key := InteractionKey{
		Date:      s.Time.Format(dateLayout),
		Namespace: s.Namespace,
		Server:    s.Server,
		Method:    s.Method,
		Outcome:   s.Outcome,
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.interactions[key]; !ok && len(r.interactions) >= maxDeltaKeys {
		return
	}
	if r.interactions == nil {
		r.interactions = make(map[InteractionKey]uint64)
	}
	r.interactions[key]++
}

// InteractionStats is one server/method/outcome total across a filter window.
type InteractionStats struct {
	Server  string
	Method  string
	Outcome string
	Count   uint64
}

// InteractionTotals sums interaction counters matching the filter's date
// range and namespace, largest first.
func (s *Store) InteractionTotals(f Filter) []InteractionStats {
	type key struct{ server, method, outcome string }
	totals := make(map[key]uint64)
	for k, n := range s.Interactions {
		if !f.matchKey(BucketKey{Date: k.Date, Namespace: k.Namespace, Server: k.Server}) {
			continue
		}
		totals[key{k.Server, k.Method, k.Outcome}] += n
	}
	stats := make([]InteractionStats, 0, len(totals))
	for k, n := range totals {
		stats = append(stats, InteractionStats{Server: k.server, Method: k.method, Outcome: k.outcome, Count: n})
	}
	slices.SortFunc(stats, func(a, b InteractionStats) int {
		if a.Count != b.Count {
			if a.Count > b.Count {
				return -1
			}
			return 1
		}
		if v := strings.Compare(a.Server, b.Server); v != 0 {
			return v
		}
		if v := strings.Compare(a.Method, b.Method); v != 0 {
			return v
		}
		return strings.Compare(a.Outcome, b.Outcome)
	})
	return stats
}
