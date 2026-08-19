package metrics

import (
	"encoding/json"
	"testing"
	"time"
)

// seedStore builds a store with a known shape:
//
//	day -2: work/github/create_issue  3 ok (100ms each)
//	day -1: work/github/create_issue  1 tool_error (200ms)
//	day -1: work/github/list_issues   2 ok (50ms each)
//	day -1: play/dice/roll            1 ok (10ms)
//	day  0: ""/local/read_file        1 denied (0ms)
func seedStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	add := func(offset int, ns, server, tool string, ms uint64, outcome Outcome, n int) {
		key := BucketKey{Date: day(offset), Namespace: ns, Server: server, Tool: tool}
		c, ok := s.Rows[key]
		if !ok {
			c = newCounters()
			s.Rows[key] = c
		}
		for range n {
			c.addSample(ms, outcome)
		}
	}
	add(-2, "work", "github", "create_issue", 100, OutcomeOK, 3)
	add(-1, "work", "github", "create_issue", 200, OutcomeToolError, 1)
	add(-1, "work", "github", "list_issues", 50, OutcomeOK, 2)
	add(-1, "play", "dice", "roll", 10, OutcomeOK, 1)
	add(0, "", "local", "read_file", 0, OutcomeDenied, 1)

	base := time.Now()
	s.RecentCalls = []RecentCall{
		{Time: base, Namespace: "", Server: "local", Tool: "read_file", Outcome: OutcomeDenied},
		{Time: base.Add(-time.Hour), Namespace: "work", Server: "github", Tool: "create_issue", DurationMs: 200, Outcome: OutcomeToolError},
		{Time: base.Add(-2 * time.Hour), Namespace: "play", Server: "dice", Tool: "roll", DurationMs: 10, Outcome: OutcomeOK},
	}
	return s
}

func TestToolTable_MergesNamespaces(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	rows := s.ToolTable(Filter{Since: day(-2), Until: day(0)})
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %+v", len(rows), rows)
	}
	// Default sort: calls desc — create_issue (4) first.
	first := rows[0]
	if first.Server != "github" || first.Tool != "create_issue" {
		t.Errorf("first row = %s.%s, want github.create_issue", first.Server, first.Tool)
	}
	if first.Calls != 4 || first.Errors != 1 {
		t.Errorf("create_issue calls=%d errors=%d, want 4/1", first.Calls, first.Errors)
	}
	if first.LastCalled != day(-1) {
		t.Errorf("LastCalled = %s, want %s", first.LastCalled, day(-1))
	}
	if len(first.Daily) != 3 {
		t.Fatalf("Daily = %d entries, want 3 (window days)", len(first.Daily))
	}
	if first.Daily[0] != 3 || first.Daily[1] != 1 || first.Daily[2] != 0 {
		t.Errorf("Daily = %v, want [3 1 0]", first.Daily)
	}
}

func TestToolTable_NamespaceFilter(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	rows := s.ToolTable(Filter{Namespace: "play"})
	if len(rows) != 1 || rows[0].Server != "dice" {
		t.Fatalf("rows = %+v, want just dice.roll", rows)
	}

	noNS := s.ToolTable(Filter{NoNamespace: true})
	if len(noNS) != 1 || noNS[0].Server != "local" {
		t.Fatalf("NoNamespace rows = %+v, want just local.read_file", noNS)
	}
	if noNS[0].Denied != 1 {
		t.Errorf("denied = %d, want 1", noNS[0].Denied)
	}
}

func TestToolTable_ServerFilterAndDateBounds(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	rows := s.ToolTable(Filter{Server: "github", Since: day(-1), Until: day(-1)})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Server != "github" {
			t.Errorf("unexpected server %s", row.Server)
		}
	}
	// Only day -1 in the window: create_issue has 1 call there.
	for _, row := range rows {
		if row.Tool == "create_issue" && row.Calls != 1 {
			t.Errorf("create_issue calls = %d, want 1 (window-bounded)", row.Calls)
		}
	}
}

func TestDailyTotals(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	totals := s.DailyTotals(Filter{Since: day(-2), Until: day(0)})
	if len(totals) != 3 {
		t.Fatalf("totals = %d days, want 3", len(totals))
	}
	if totals[0].Date != day(-2) || totals[0].Calls != 3 {
		t.Errorf("day[-2] = %+v, want 3 calls", totals[0])
	}
	if totals[1].Calls != 4 || totals[1].Errors != 1 {
		t.Errorf("day[-1] = %+v, want 4 calls 1 error", totals[1])
	}
	if totals[2].Calls != 1 {
		t.Errorf("day[0] = %+v, want 1 call (denied counts as a call)", totals[2])
	}
}

func TestSummary(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	sum := s.Summary(Filter{})
	if sum.TotalCalls != 8 {
		t.Errorf("TotalCalls = %d, want 8", sum.TotalCalls)
	}
	if sum.DistinctTools != 4 {
		t.Errorf("DistinctTools = %d, want 4", sum.DistinctTools)
	}
	if sum.Errors != 1 {
		t.Errorf("Errors = %d, want 1", sum.Errors)
	}
	if sum.Denied != 1 {
		t.Errorf("Denied = %d, want 1", sum.Denied)
	}
	if sum.BusiestTool != "github.create_issue" || sum.BusiestCalls != 4 {
		t.Errorf("Busiest = %s (%d), want github.create_issue (4)", sum.BusiestTool, sum.BusiestCalls)
	}
}

func TestRecent_FilterAndLimit(t *testing.T) {
	t.Parallel()
	s := seedStore(t)

	all := s.Recent(Filter{}, 10)
	if len(all) != 3 {
		t.Fatalf("recent = %d, want 3", len(all))
	}
	if all[0].Server != "local" {
		t.Errorf("newest = %s, want local (newest first)", all[0].Server)
	}

	limited := s.Recent(Filter{}, 2)
	if len(limited) != 2 {
		t.Errorf("limited = %d, want 2", len(limited))
	}

	work := s.Recent(Filter{Namespace: "work"}, 10)
	if len(work) != 1 || work[0].Server != "github" {
		t.Errorf("work recent = %+v", work)
	}

	none := s.Recent(Filter{NoNamespace: true}, 10)
	if len(none) != 1 || none[0].Server != "local" {
		t.Errorf("no-namespace recent = %+v", none)
	}
}

func TestCalledSet(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	set := s.CalledSet(Filter{Namespace: "work"})
	if _, ok := set[CalledKey("github", "create_issue")]; !ok {
		t.Error("create_issue missing from work called set")
	}
	if _, ok := set[CalledKey("dice", "roll")]; ok {
		t.Error("dice.roll leaked into work called set")
	}
	if len(set) != 2 {
		t.Errorf("set = %d entries, want 2", len(set))
	}
}

// The seeded store records local/read_file as a single denied call: no upstream
// was reached, so the latency columns must carry no signal for it.
func TestToolTable_DeniedRowHasNoLatency(t *testing.T) {
	t.Parallel()
	rows := seedStore(t).ToolTable(Filter{Server: "local"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Calls != 1 || row.Denied != 1 {
		t.Fatalf("row = %+v, want 1 call, 1 denied", row)
	}
	if row.TimedCalls != 0 {
		t.Errorf("TimedCalls = %d, want 0", row.TimedCalls)
	}
	if row.AvgMs != 0 || row.P50Ms != 0 || row.P95Ms != 0 || row.MaxMs != 0 {
		t.Errorf("latency = avg %d p50 %d p95 %d max %d, want all 0",
			row.AvgMs, row.P50Ms, row.P95Ms, row.MaxMs)
	}
}

func TestHasNoNamespaceCalls(t *testing.T) {
	t.Parallel()
	s := seedStore(t)
	// day 0 holds the ""-namespace denial of local/read_file.
	if !s.HasNoNamespaceCalls(Filter{}) {
		t.Error("HasNoNamespaceCalls = false, want true")
	}
	// Namespace narrowing in the filter must not hide the answer.
	if !s.HasNoNamespaceCalls(Filter{Namespace: "work"}) {
		t.Error("a namespace filter suppressed the no-namespace answer")
	}
	// Date bounds and server narrowing still apply.
	if s.HasNoNamespaceCalls(Filter{Since: day(-2), Until: day(-1)}) {
		t.Error("a window without the no-namespace row reported true")
	}
	if s.HasNoNamespaceCalls(Filter{Server: "github"}) {
		t.Error("github has no ''-namespace rows but reported true")
	}
}

func TestDayTotal_JSONKeys(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(DayTotal{Date: "2026-08-19", Calls: 3, Errors: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The whole API payload is camelCase; these two used to serialize as
	// "Calls"/"Errors" because the fields carried no tags.
	if got := string(data); got != `{"date":"2026-08-19","calls":3,"errors":1}` {
		t.Errorf("DayTotal JSON = %s", got)
	}
}

func TestToolTable_EmptyStore(t *testing.T) {
	t.Parallel()
	s := NewStore()
	if rows := s.ToolTable(Filter{}); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	if totals := s.DailyTotals(Filter{}); totals != nil {
		t.Errorf("totals = %+v, want nil", totals)
	}
}
