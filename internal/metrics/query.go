package metrics

import (
	"slices"
	"strings"
	"time"
)

// Filter narrows read-side queries. Zero value matches everything.
type Filter struct {
	Since, Until string // inclusive date bounds ("" = unbounded)
	Namespace    string // "" = all namespaces
	NoNamespace  bool   // true = only rows recorded without an active namespace
	Server       string // "" = all servers
}

func (f Filter) matchDims(namespace, server string) bool {
	if f.NoNamespace {
		if namespace != "" {
			return false
		}
	} else if f.Namespace != "" && namespace != f.Namespace {
		return false
	}
	if f.Server != "" && server != f.Server {
		return false
	}
	return true
}

func (f Filter) matchDate(date string) bool {
	if f.Since != "" && date < f.Since {
		return false
	}
	if f.Until != "" && date > f.Until {
		return false
	}
	return true
}

func (f Filter) matchKey(k BucketKey) bool {
	return f.matchDate(k.Date) && f.matchDims(k.Namespace, k.Server)
}

// ToolStats is one row of the per-tool table.
type ToolStats struct {
	Namespace, Server, Tool string
	Calls, Errors           uint64 // Errors = tool_error + error + timeout
	Denied                  uint64
	// TimedCalls is how many calls the latency figures are based on (Calls
	// minus denials). Zero means the latency columns carry no signal.
	TimedCalls                 uint64
	AvgMs, P50Ms, P95Ms, MaxMs uint64
	LastCalled                 string   // max date with Calls > 0
	Daily                      []uint64 // calls per day across the filter window, oldest first
}

// DayTotal is one day of the overview chart.
type DayTotal struct {
	Date   string `json:"date"`
	Calls  uint64 `json:"calls"`
	Errors uint64 `json:"errors"`
}

// SummaryStats are the headline numbers for the metrics page.
type SummaryStats struct {
	TotalCalls    uint64
	DistinctTools int // distinct (server, tool) pairs with calls
	Errors        uint64
	Denied        uint64
	BusiestTool   string // "server.tool"
	BusiestCalls  uint64
}

// window resolves the filter's date range against the data: explicit bounds
// win; missing bounds fall back to the min/max dates present.
func (s *Store) window(f Filter) (since, until string) {
	since, until = f.Since, f.Until
	if since != "" && until != "" {
		return since, until
	}
	var minDate, maxDate string
	for key := range s.Rows {
		if !f.matchKey(key) {
			continue
		}
		if minDate == "" || key.Date < minDate {
			minDate = key.Date
		}
		if maxDate == "" || key.Date > maxDate {
			maxDate = key.Date
		}
	}
	if since == "" {
		since = minDate
	}
	if until == "" {
		until = maxDate
	}
	return since, until
}

// dateRange expands an inclusive date-string range into individual days.
func dateRange(since, until string) []string {
	if since == "" || until == "" || since > until {
		return nil
	}
	start, err := time.ParseInLocation(dateLayout, since, time.Local)
	if err != nil {
		return nil
	}
	end, err := time.ParseInLocation(dateLayout, until, time.Local)
	if err != nil {
		return nil
	}
	var days []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d.Format(dateLayout))
	}
	return days
}

// ToolTable returns one row per (server, tool), namespaces merged unless the
// filter pins one. Rows are sorted by calls descending.
func (s *Store) ToolTable(f Filter) []ToolStats {
	since, until := s.window(f)
	days := dateRange(since, until)
	dayIndex := make(map[string]int, len(days))
	for i, d := range days {
		dayIndex[d] = i
	}

	type agg struct {
		counters   *Counters
		lastCalled string
		daily      []uint64
	}
	type toolKey struct{ server, tool string }
	byTool := make(map[toolKey]*agg)

	for key, c := range s.Rows {
		if !f.matchKey(key) {
			continue
		}
		tk := toolKey{key.Server, key.Tool}
		a, ok := byTool[tk]
		if !ok {
			a = &agg{counters: newCounters(), daily: make([]uint64, len(days))}
			byTool[tk] = a
		}
		a.counters.merge(c)
		if c.Calls > 0 && key.Date > a.lastCalled {
			a.lastCalled = key.Date
		}
		if i, ok := dayIndex[key.Date]; ok {
			a.daily[i] += c.Calls
		}
	}

	rows := make([]ToolStats, 0, len(byTool))
	for tk, a := range byTool {
		c := a.counters
		timed := c.timedCalls()
		var avg uint64
		if timed > 0 {
			avg = c.DurationMsSum / timed
		}
		rows = append(rows, ToolStats{
			Namespace:  f.Namespace,
			Server:     tk.server,
			Tool:       tk.tool,
			Calls:      c.Calls,
			Errors:     c.errors(),
			Denied:     c.Outcomes[OutcomeDenied],
			TimedCalls: timed,
			AvgMs:      avg,
			P50Ms:      c.percentileMs(0.50),
			P95Ms:      c.percentileMs(0.95),
			MaxMs:      c.DurationMsMax,
			LastCalled: a.lastCalled,
			Daily:      a.daily,
		})
	}

	slices.SortFunc(rows, func(a, b ToolStats) int {
		if a.Calls != b.Calls {
			if a.Calls > b.Calls {
				return -1
			}
			return 1
		}
		if v := strings.Compare(a.Server, b.Server); v != 0 {
			return v
		}
		return strings.Compare(a.Tool, b.Tool)
	})
	return rows
}

// DailyTotals returns per-day call and error totals across the filter window,
// oldest first. Days without calls are present with zero counts.
func (s *Store) DailyTotals(f Filter) []DayTotal {
	since, until := s.window(f)
	days := dateRange(since, until)
	if len(days) == 0 {
		return nil
	}
	dayIndex := make(map[string]int, len(days))
	totals := make([]DayTotal, len(days))
	for i, d := range days {
		dayIndex[d] = i
		totals[i].Date = d
	}
	for key, c := range s.Rows {
		if !f.matchKey(key) {
			continue
		}
		if i, ok := dayIndex[key.Date]; ok {
			totals[i].Calls += c.Calls
			totals[i].Errors += c.errors()
		}
	}
	return totals
}

// Summary computes the headline numbers for the filter.
func (s *Store) Summary(f Filter) SummaryStats {
	type toolKey struct{ server, tool string }
	callsByTool := make(map[toolKey]uint64)

	var stats SummaryStats
	for key, c := range s.Rows {
		if !f.matchKey(key) {
			continue
		}
		stats.TotalCalls += c.Calls
		stats.Errors += c.errors()
		stats.Denied += c.Outcomes[OutcomeDenied]
		if c.Calls > 0 {
			callsByTool[toolKey{key.Server, key.Tool}] += c.Calls
		}
	}
	stats.DistinctTools = len(callsByTool)
	for tk, calls := range callsByTool {
		name := tk.server + "." + tk.tool
		if calls > stats.BusiestCalls || (calls == stats.BusiestCalls && name < stats.BusiestTool) {
			stats.BusiestTool = name
			stats.BusiestCalls = calls
		}
	}
	return stats
}

// Recent returns up to n recent calls matching the filter, newest first.
func (s *Store) Recent(f Filter, n int) []RecentCall {
	matched := make([]RecentCall, 0, min(n, len(s.RecentCalls)))
	for _, rc := range s.RecentCalls {
		if !f.matchDims(rc.Namespace, rc.Server) {
			continue
		}
		if !f.matchDate(rc.Time.Format(dateLayout)) {
			continue
		}
		matched = append(matched, rc)
	}
	slices.SortStableFunc(matched, func(a, b RecentCall) int {
		return b.Time.Compare(a.Time)
	})
	if len(matched) > n {
		matched = matched[:n]
	}
	return matched
}

// HasNoNamespaceCalls reports whether the window holds calls recorded without
// an active namespace. The filter's namespace narrowing is ignored on purpose:
// the question is whether such rows exist at all. They do whenever serve ran
// before the first namespace was configured, and they outlive that change by
// the retention window.
func (s *Store) HasNoNamespaceCalls(f Filter) bool {
	for key, c := range s.Rows {
		if c.Calls == 0 || key.Namespace != "" || !f.matchDate(key.Date) {
			continue
		}
		if f.Server != "" && key.Server != f.Server {
			continue
		}
		return true
	}
	return false
}

// CalledSet returns the set of tools with at least one call in the filter,
// keyed "server\x00tool" (NUL separator: tool names come from arbitrary
// upstreams and may contain anything printable).
func (s *Store) CalledSet(f Filter) map[string]struct{} {
	set := make(map[string]struct{})
	for key, c := range s.Rows {
		if c.Calls == 0 || !f.matchKey(key) {
			continue
		}
		set[key.Server+"\x00"+key.Tool] = struct{}{}
	}
	return set
}

// CalledKey builds a CalledSet lookup key.
func CalledKey(server, tool string) string {
	return server + "\x00" + tool
}
