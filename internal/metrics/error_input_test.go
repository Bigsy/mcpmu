package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactErrorInput(t *testing.T) {
	input := json.RawMessage(`{"query":"select * from example","id":9007199254740993,"nested":[{"Authorization":"Bearer hidden","client_secret":"hidden","X-API-Key":"hidden","credentials":{"user":"hidden"},"dbPassword":"hidden","refreshToken":"hidden","safe":"visible"}]}`)
	original := string(input)
	got := redactErrorInput(input)
	if !json.Valid([]byte(got)) {
		t.Fatalf("invalid JSON: %s", got)
	}
	for _, want := range []string{"select * from example", "9007199254740993", "visible", "[REDACTED]"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "hidden") {
		t.Fatalf("secret leaked: %s", got)
	}
	if string(input) != original {
		t.Fatal("input was modified before forwarding")
	}
	if got := redactErrorInput(json.RawMessage(`{"password":"hidden"`)); strings.Contains(got, "hidden") || !strings.Contains(got, "omitted") {
		t.Fatalf("invalid JSON fallback: %s", got)
	}
}

func TestRecorderInputsOnlyOnFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	rec := NewRecorder(path, 60)
	defer func() { _ = rec.Close() }()
	for _, outcome := range []Outcome{OutcomeOK, OutcomeDenied, OutcomeCancelled, OutcomeToolError, OutcomeError, OutcomeTimeout} {
		rec.Record(CallSample{Time: time.Now(), Server: "own", Tool: string(outcome), Outcome: outcome, ErrorInput: json.RawMessage(`{"query":"debug-` + string(outcome) + `","password":"never-persist"}`)})
	}
	// The in-memory error buffer must also contain only redacted inputs.
	rec.mu.Lock()
	for _, call := range rec.errors {
		if strings.Contains(call.Input, "never-persist") {
			t.Error("buffer contains secret")
		}
	}
	rec.mu.Unlock()
	if err := rec.Flush(); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.ErrorCalls) != 3 {
		t.Fatalf("error count: %d", len(store.ErrorCalls))
	}
	for _, call := range store.ErrorCalls {
		if !strings.Contains(call.Input, "debug-"+string(call.Outcome)) || !strings.Contains(call.Input, "[REDACTED]") {
			t.Errorf("incorrect input: %+v", call)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"never-persist", "debug-ok", "debug-denied", "debug-cancelled"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("persisted %q", forbidden)
		}
	}
	// Retention removes inputs along with their errors.
	store.ErrorCalls[0].Time = time.Now().AddDate(0, 0, -61)
	expired := store.ErrorCalls[0].Input
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
	if len(store.ErrorCalls) != 2 {
		t.Fatalf("expired input retained: %+v", store.ErrorCalls)
	}
	for _, call := range store.ErrorCalls {
		if call.Input == expired {
			t.Fatal("expired input retained")
		}
	}
}
