package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestErrorHistoryRetentionAndRecentIndependence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	rec := NewRecorder(path, 7)
	defer func() { _ = rec.Close() }()
	now := time.Now()
	for range 250 {
		rec.Record(CallSample{Time: now.AddDate(0, 0, -40), Namespace: "work", Server: "own", Tool: "debug", Outcome: OutcomeToolError, ErrorResponse: "structured failure"})
	}
	rec.Record(CallSample{Time: now.AddDate(0, 0, -61), Server: "own", Tool: "debug", Outcome: OutcomeError, ErrorResponse: "expired response"})
	for _, outcome := range []Outcome{OutcomeOK, OutcomeDenied, OutcomeCancelled} {
		rec.Record(CallSample{Time: now, Server: "own", Tool: "debug", Outcome: outcome, ErrorResponse: "must not be stored"})
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ErrorCalls) != 250 {
		t.Fatalf("error history = %d, want 250", len(store.ErrorCalls))
	}
	if len(store.RecentCalls) != 3 {
		t.Fatalf("recent = %d", len(store.RecentCalls))
	}
	calls, total := store.Errors(Filter{Namespace: "work", Server: "own"}, "debug", 200, 50)
	if total != 250 || len(calls) != 50 || calls[0].Response != "structured failure" {
		t.Fatalf("page: total %d, calls %v", total, calls)
	}
	for _, f := range []Filter{{NoNamespace: true}, {Namespace: "other"}, {Server: "other"}, {Since: now.AddDate(0, 0, -7).Format(dateLayout)}} {
		if _, total := store.Errors(f, "debug", 0, 50); total != 0 {
			t.Fatalf("filter %+v: %d", f, total)
		}
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "expired response") || strings.Contains(string(data), "must not be stored") {
		t.Fatal("unexpected response persisted")
	}
	// An idle flush still removes expired history.
	store.ErrorCalls[0].Time = now.AddDate(0, 0, -61)
	if err := store.saveAtomic(path); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	store, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ErrorCalls) != 249 {
		t.Fatalf("idle prune: %d", len(store.ErrorCalls))
	}
}

func TestErrorHistoryFailedFlushAndMultipleWriters(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(filepath.Join(blocked, "metrics.json"), 60)
	defer func() { _ = rec.Close() }()
	rec.Record(CallSample{Time: time.Now(), Server: "own", Tool: "debug", Outcome: OutcomeError, ErrorResponse: "first"})
	if err := rec.Flush(); err == nil {
		t.Fatal("expected write failure")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	other := NewRecorder(rec.path, 60)
	defer func() { _ = other.Close() }()
	other.Record(CallSample{Time: time.Now(), Server: "own", Tool: "debug", Outcome: OutcomeTimeout, ErrorResponse: "second"})
	if err := other.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	store, err := Load(rec.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ErrorCalls) != 2 || store.ErrorCalls[0].Response != "second" || store.ErrorCalls[1].Response != "first" {
		t.Fatalf("history: %+v", store.ErrorCalls)
	}
}
