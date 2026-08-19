package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetricsPath(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}

	// Nonexistent paths cannot be symlink-resolved, so they pass through as
	// given.
	tests := []struct {
		configPath string
		want       string
	}{
		{"/nonexistent/custom/config.json", "/nonexistent/custom/metrics.json"},
		{"~/nonexistent-somewhere/config.json", filepath.Join(home, "nonexistent-somewhere", "metrics.json")},
	}
	for _, tt := range tests {
		got, err := MetricsPath(tt.configPath)
		if err != nil {
			t.Errorf("MetricsPath(%q): %v", tt.configPath, err)
			continue
		}
		if got != tt.want {
			t.Errorf("MetricsPath(%q) = %q, want %q", tt.configPath, got, tt.want)
		}
	}

	// The default path resolves symlinks when the default config exists (it
	// may be a symlink into a dotfiles repo), so only assert its shape here.
	got, err := MetricsPath("")
	if err != nil {
		t.Fatalf("MetricsPath(\"\"): %v", err)
	}
	if filepath.Base(got) != "metrics.json" || !filepath.IsAbs(got) {
		t.Errorf("MetricsPath(\"\") = %q, want an absolute path ending in metrics.json", got)
	}
}

func TestMetricsPath_ResolvesSymlinkedConfig(t *testing.T) {
	t.Parallel()
	realDir := t.TempDir()
	linkDir := t.TempDir()

	realCfg := filepath.Join(realDir, "config.json")
	if err := os.WriteFile(realCfg, []byte("{}"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	linkCfg := filepath.Join(linkDir, "config.json")
	if err := os.Symlink(realCfg, linkCfg); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	// The temp dirs may themselves sit behind symlinks (macOS /var →
	// /private/var), so resolve the expected side the same way.
	resolvedReal, err := filepath.EvalSymlinks(realCfg)
	if err != nil {
		t.Fatalf("resolve real config: %v", err)
	}
	want := filepath.Join(filepath.Dir(resolvedReal), "metrics.json")

	got, err := MetricsPath(linkCfg)
	if err != nil {
		t.Fatalf("MetricsPath: %v", err)
	}
	if got != want {
		t.Errorf("MetricsPath(symlink) = %q, want %q (writer and reader must agree)", got, want)
	}
}

func TestCounters_AddSampleAcrossOutcomes(t *testing.T) {
	t.Parallel()
	c := newCounters()
	c.addSample(100, OutcomeOK)
	c.addSample(200, OutcomeOK)
	c.addSample(50, OutcomeToolError)
	c.addSample(60000, OutcomeTimeout)
	c.addSample(0, OutcomeDenied)
	c.addSample(10, OutcomeError)

	if c.Calls != 6 {
		t.Errorf("Calls = %d, want 6", c.Calls)
	}
	if c.Outcomes[OutcomeOK] != 2 {
		t.Errorf("Outcomes[ok] = %d, want 2", c.Outcomes[OutcomeOK])
	}
	if c.Outcomes[OutcomeDenied] != 1 {
		t.Errorf("Outcomes[denied] = %d, want 1", c.Outcomes[OutcomeDenied])
	}
	if c.DurationMsSum != 100+200+50+60000+10 {
		t.Errorf("DurationMsSum = %d", c.DurationMsSum)
	}
	if c.DurationMsMax != 60000 {
		t.Errorf("DurationMsMax = %d, want 60000", c.DurationMsMax)
	}
	if got := c.errors(); got != 3 {
		t.Errorf("errors() = %d, want 3 (tool_error + timeout + error)", got)
	}
	// The denied call counts as a call but never reached an upstream, so it
	// stays out of the latency aggregates.
	if got := c.timedCalls(); got != 5 {
		t.Errorf("timedCalls() = %d, want 5 (6 calls minus the denial)", got)
	}
	var histTotal uint64
	for _, n := range c.Hist {
		histTotal += n
	}
	if histTotal != 5 {
		t.Errorf("histogram holds %d samples, want 5 — a denial has no latency", histTotal)
	}
}

func TestCounters_DeniedOnlyHasNoLatency(t *testing.T) {
	t.Parallel()
	c := newCounters()
	for range 500 {
		c.addSample(0, OutcomeDenied)
	}
	if c.Calls != 500 {
		t.Errorf("Calls = %d, want 500", c.Calls)
	}
	if c.timedCalls() != 0 {
		t.Errorf("timedCalls() = %d, want 0", c.timedCalls())
	}
	// Before denials were excluded these reported 10ms — the first histogram
	// bucket — for a tool that was never actually dispatched.
	if got := c.percentileMs(0.50); got != 0 {
		t.Errorf("p50 = %dms, want 0 for a tool that only ever got refused", got)
	}
	if got := c.percentileMs(0.95); got != 0 {
		t.Errorf("p95 = %dms, want 0", got)
	}
	if c.DurationMsSum != 0 || c.DurationMsMax != 0 {
		t.Errorf("duration sum/max = %d/%d, want 0/0", c.DurationMsSum, c.DurationMsMax)
	}
}

func TestHistIndex_Boundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ms   uint64
		want int
	}{
		{0, 0},
		{10, 0},     // exactly on first boundary → first bucket (<=)
		{11, 1},     // just past it → second bucket
		{25, 1},     // exactly on second boundary
		{60000, 11}, // exactly on last boundary → last bounded bucket
		{60001, 12}, // past it → overflow
	}
	for _, tt := range tests {
		if got := histIndex(tt.ms); got != tt.want {
			t.Errorf("histIndex(%d) = %d, want %d", tt.ms, got, tt.want)
		}
	}
}

func TestPercentiles_KnownDistribution(t *testing.T) {
	t.Parallel()
	// 100 calls uniformly at 30ms: all land in the (25, 50] bucket.
	c := newCounters()
	for range 100 {
		c.addSample(30, OutcomeOK)
	}
	// p50 interpolates halfway through the (25, 50] bucket: 25 + 0.5*25 ≈ 37-38.
	p50 := c.percentileMs(0.50)
	if p50 < 25 || p50 > 50 {
		t.Errorf("p50 = %d, want within bucket (25, 50]", p50)
	}
	// p95 must be within the same bucket too.
	p95 := c.percentileMs(0.95)
	if p95 < 25 || p95 > 50 {
		t.Errorf("p95 = %d, want within bucket (25, 50]", p95)
	}
	if p95 < p50 {
		t.Errorf("p95 (%d) < p50 (%d)", p95, p50)
	}

	// Bimodal: 90 fast (5ms), 10 slow (2000ms).
	c2 := newCounters()
	for range 90 {
		c2.addSample(5, OutcomeOK)
	}
	for range 10 {
		c2.addSample(2000, OutcomeOK)
	}
	if p50 := c2.percentileMs(0.50); p50 > 10 {
		t.Errorf("bimodal p50 = %d, want <= 10", p50)
	}
	if p95 := c2.percentileMs(0.95); p95 <= 1000 || p95 > 2500 {
		t.Errorf("bimodal p95 = %d, want in (1000, 2500]", p95)
	}

	// Empty counters report 0.
	if got := newCounters().percentileMs(0.95); got != 0 {
		t.Errorf("empty p95 = %d, want 0", got)
	}

	// Overflow bucket interpolates up to the observed max.
	c3 := newCounters()
	for range 10 {
		c3.addSample(90000, OutcomeOK)
	}
	if p95 := c3.percentileMs(0.95); p95 < 60000 || p95 > 90000 {
		t.Errorf("overflow p95 = %d, want in [60000, 90000]", p95)
	}
}

func day(offset int) string {
	return time.Now().AddDate(0, 0, offset).Format(dateLayout)
}

func sampleAt(offset int, ns, server, tool string, d time.Duration, outcome Outcome) CallSample {
	return CallSample{
		Time:      time.Now().AddDate(0, 0, offset),
		Namespace: ns,
		Server:    server,
		Tool:      tool,
		Duration:  d,
		Outcome:   outcome,
	}
}

func TestRecorder_FlushAndLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := NewRecorder(path, 60)
	defer func() { _ = r.Close() }()

	r.Record(sampleAt(0, "work", "github", "create_issue", 100*time.Millisecond, OutcomeOK))
	r.Record(sampleAt(0, "work", "github", "create_issue", 200*time.Millisecond, OutcomeToolError))
	r.Record(sampleAt(0, "", "local", "read_file", 5*time.Millisecond, OutcomeOK))

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := BucketKey{Date: day(0), Namespace: "work", Server: "github", Tool: "create_issue"}
	c, ok := store.Rows[key]
	if !ok {
		t.Fatalf("bucket %+v not found; rows: %v", key, store.Rows)
	}
	if c.Calls != 2 || c.Outcomes[OutcomeOK] != 1 || c.Outcomes[OutcomeToolError] != 1 {
		t.Errorf("counters = %+v", c)
	}
	if len(store.RecentCalls) != 3 {
		t.Errorf("recent = %d entries, want 3", len(store.RecentCalls))
	}

	// Second flush with no new samples is a no-op.
	if err := r.Flush(); err != nil {
		t.Fatalf("empty Flush: %v", err)
	}
}

func TestRecorder_NilIsValid(t *testing.T) {
	t.Parallel()
	var r *Recorder
	r.Record(sampleAt(0, "", "srv", "tool", time.Millisecond, OutcomeOK))
	if err := r.Flush(); err != nil {
		t.Errorf("nil Flush: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
	r.SetRetentionDays(30)
}

func TestRecorder_RecentCapAndOrdering(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := NewRecorder(path, 60)
	defer func() { _ = r.Close() }()

	base := time.Now()
	for i := range 250 {
		r.Record(CallSample{
			Time:    base.Add(time.Duration(i) * time.Second),
			Server:  "srv",
			Tool:    fmt.Sprintf("tool%d", i%3),
			Outcome: OutcomeOK,
		})
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(store.RecentCalls) != recentCap {
		t.Fatalf("recent = %d entries, want cap %d", len(store.RecentCalls), recentCap)
	}
	// Newest first, and the newest overall must have survived the cap.
	if !store.RecentCalls[0].Time.After(store.RecentCalls[1].Time) {
		t.Error("recent not sorted newest-first")
	}
	wantNewest := base.Add(249 * time.Second)
	if !store.RecentCalls[0].Time.Equal(wantNewest) {
		t.Errorf("newest recent = %v, want %v", store.RecentCalls[0].Time, wantNewest)
	}
}

func TestStore_PruneAtRetentionEdge(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := NewRecorder(path, 60)
	defer func() { _ = r.Close() }()

	r.Record(sampleAt(-61, "", "srv", "too_old", time.Millisecond, OutcomeOK))
	r.Record(sampleAt(-60, "", "srv", "at_edge", time.Millisecond, OutcomeOK))
	r.Record(sampleAt(-59, "", "srv", "inside", time.Millisecond, OutcomeOK))
	r.Record(sampleAt(0, "", "srv", "today", time.Millisecond, OutcomeOK))

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	find := func(tool string) bool {
		for key := range store.Rows {
			if key.Tool == tool {
				return true
			}
		}
		return false
	}
	if find("too_old") {
		t.Error("row older than retention survived prune")
	}
	if !find("at_edge") {
		t.Error("row exactly at the retention edge was pruned")
	}
	if !find("inside") || !find("today") {
		t.Error("rows inside the window were pruned")
	}
}

func TestMergeAndSave_CorruptFileMovedAside(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	delta := map[BucketKey]*Counters{
		{Date: day(0), Server: "srv", Tool: "tool"}: func() *Counters {
			c := newCounters()
			c.addSample(10, OutcomeOK)
			return c
		}(),
	}
	if err := mergeAndSave(path, delta, nil, 60); err != nil {
		t.Fatalf("mergeAndSave over corrupt file: %v", err)
	}

	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("corrupt file not moved aside: %v", err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load after fresh start: %v", err)
	}
	if len(store.Rows) != 1 {
		t.Errorf("rows = %d, want 1", len(store.Rows))
	}
}

func TestMergeAndSave_WrongVersionMovedAside(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")
	if err := os.WriteFile(path, []byte(`{"version": 99, "rows": []}`), 0600); err != nil {
		t.Fatalf("seed wrong-version file: %v", err)
	}
	if err := mergeAndSave(path, nil, nil, 60); err != nil {
		t.Fatalf("mergeAndSave: %v", err)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("wrong-version file not moved aside: %v", err)
	}
}

func TestConcurrentWriters_CountsSum(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.json")

	const perRecorder = 50
	const flushes = 5

	var wg sync.WaitGroup
	for w := range 2 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := NewRecorder(path, 60)
			for f := range flushes {
				for i := range perRecorder / flushes {
					r.Record(CallSample{
						Time:     time.Now(),
						Server:   "srv",
						Tool:     "shared_tool",
						Duration: time.Duration(w*10+i) * time.Millisecond,
						Outcome:  OutcomeOK,
					})
				}
				_ = f
				if err := r.Flush(); err != nil {
					t.Errorf("writer %d flush: %v", w, err)
				}
			}
			if err := r.Close(); err != nil {
				t.Errorf("writer %d close: %v", w, err)
			}
		}(w)
	}
	wg.Wait()

	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var total uint64
	for key, c := range store.Rows {
		if key.Tool == "shared_tool" {
			total += c.Calls
		}
	}
	if total != 2*perRecorder {
		t.Errorf("total calls = %d, want %d", total, 2*perRecorder)
	}
}

func TestRecorder_FlushFailureRestoresDelta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Metrics path inside a subdirectory we make unwritable.
	sub := filepath.Join(dir, "locked")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(sub, "metrics.json")

	r := NewRecorder(path, 60)
	defer func() { _ = r.Close() }()
	r.Record(sampleAt(0, "", "srv", "tool", time.Millisecond, OutcomeOK))

	if err := os.Chmod(sub, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0700) })

	if err := r.Flush(); err == nil {
		t.Fatal("expected flush to fail in unwritable dir")
	}

	// Restore the directory and flush again: the restored delta must land.
	if err := os.Chmod(sub, 0700); err != nil {
		t.Fatalf("chmod restore: %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("flush after restore: %v", err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := BucketKey{Date: day(0), Server: "srv", Tool: "tool"}
	if c, ok := store.Rows[key]; !ok || c.Calls != 1 {
		t.Errorf("restored delta not flushed: rows=%v", store.Rows)
	}
	if len(store.RecentCalls) != 1 {
		t.Errorf("restored recent not flushed: %d entries", len(store.RecentCalls))
	}
}

func TestStore_FileShape(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metrics.json")
	r := NewRecorder(path, 60)
	defer func() { _ = r.Close() }()
	r.Record(sampleAt(0, "work", "github", "create_issue", 812*time.Millisecond, OutcomeOK))
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var file struct {
		Version int `json:"version"`
		Rows    []map[string]any
		Recent  []map[string]any
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if file.Version != StoreVersion {
		t.Errorf("version = %d", file.Version)
	}
	if len(file.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(file.Rows))
	}
	row := file.Rows[0]
	for _, field := range []string{"date", "namespace", "server", "tool", "calls", "outcomes", "durationMsSum", "durationMsMax", "hist"} {
		if _, ok := row[field]; !ok {
			t.Errorf("row missing field %q", field)
		}
	}
	// Privacy: the serialized file must never contain argument-like fields.
	if strings.Contains(string(data), "arguments") || strings.Contains(string(data), "result") {
		t.Error("metrics file contains forbidden fields")
	}
}
