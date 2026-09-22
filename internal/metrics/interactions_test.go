package metrics

import (
	"path/filepath"
	"testing"
	"time"
)

// TestInteractionsRoundTrip: interaction counts survive flush and reload,
// merge across flushes, and are totalled per server/method/outcome.
func TestInteractionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := NewRecorder(path, 60)
	now := time.Now()
	for _, outcome := range []string{"accept", "accept", "fallback:timeout"} {
		r.RecordInteraction(InteractionSample{Time: now, Server: "srv", Method: "elicitation/create", Outcome: outcome})
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	r.RecordInteraction(InteractionSample{Time: now, Server: "srv", Method: "elicitation/create", Outcome: "accept"})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	totals := store.InteractionTotals(Filter{})
	want := map[string]uint64{"accept": 3, "fallback:timeout": 1}
	if len(totals) != len(want) {
		t.Fatalf("totals = %+v", totals)
	}
	for _, row := range totals {
		if row.Server != "srv" || row.Method != "elicitation/create" || row.Count != want[row.Outcome] {
			t.Errorf("row = %+v", row)
		}
	}
	if totals[0].Outcome != "accept" {
		t.Errorf("totals not sorted largest first: %+v", totals)
	}
	if got := store.InteractionTotals(Filter{Server: "other"}); len(got) != 0 {
		t.Errorf("server filter ignored: %+v", got)
	}
}

// TestInteractionWaitIsKeptOutOfLatency: a sample's interaction wait is
// summed separately and reported on the recent-call row.
func TestInteractionWaitIsKeptOutOfLatency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := NewRecorder(path, 60)
	r.Record(CallSample{Time: time.Now(), Server: "srv", Tool: "t", Duration: 40 * time.Millisecond, InteractionWait: 5 * time.Second, Outcome: OutcomeOK})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rows := store.ToolTable(Filter{})
	if len(rows) != 1 || rows[0].MaxMs != 40 || rows[0].InteractionWaitMs != 5000 {
		t.Fatalf("tool stats = %+v", rows)
	}
	if len(store.RecentCalls) != 1 || store.RecentCalls[0].InteractionWaitMs != 5000 {
		t.Errorf("recent = %+v", store.RecentCalls)
	}
}
