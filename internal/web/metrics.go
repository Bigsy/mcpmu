package web

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Bigsy/mcpmu/internal/metrics"
	"github.com/Bigsy/mcpmu/internal/server"
)

// nsNoneParam is the ns query value selecting calls recorded without an
// active namespace. ':' is invalid in namespace names, so it cannot collide.
const nsNoneParam = ":none:"

// nsNoneLabel is how the empty namespace renders in the UI.
const nsNoneLabel = "(none)"

// loadMetricsStore returns the parsed metrics store, cached by file
// mtime+size so htmx polling stays cheap. Errors degrade to an empty store —
// the metrics page is never worth a 500.
func (s *Server) loadMetricsStore() *metrics.Store {
	path, err := metrics.MetricsPath(s.configPath)
	if err != nil {
		log.Printf("metrics: resolve path: %v", err)
		return metrics.NewStore()
	}

	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	info, err := os.Stat(path)
	if err != nil {
		s.metricsStore = nil
		return metrics.NewStore()
	}
	if s.metricsStore != nil && info.ModTime().Equal(s.metricsMtime) && info.Size() == s.metricsSize {
		return s.metricsStore
	}

	store, err := metrics.Load(path)
	if err != nil {
		log.Printf("metrics: load %s: %v", path, err)
		return metrics.NewStore()
	}
	s.metricsStore = store
	s.metricsMtime = info.ModTime()
	s.metricsSize = info.Size()
	return store
}

// metricsQuery is the parsed, validated query state shared by the page, the
// fragments, and the JSON API.
type metricsQuery struct {
	NS   string // "" = all, nsNoneParam = no-namespace, else a namespace name
	Days int    // 7, 30, or 60
	Sort string // tool | calls | errors | p50 | p95 | last
	Dir  string // asc | desc
}

func parseMetricsQuery(r *http.Request) metricsQuery {
	q := metricsQuery{NS: r.URL.Query().Get("ns"), Days: 30, Sort: "calls", Dir: "desc"}
	switch r.URL.Query().Get("days") {
	case "7":
		q.Days = 7
	case "60":
		q.Days = 60
	}
	switch v := r.URL.Query().Get("sort"); v {
	case "tool", "calls", "errors", "p50", "p95", "last":
		q.Sort = v
	}
	if r.URL.Query().Get("dir") == "asc" {
		q.Dir = "asc"
	}
	return q
}

// filter converts the query into date-bounded metrics filter (window includes
// today).
func (q metricsQuery) filter() metrics.Filter {
	f := metrics.Filter{
		Since: time.Now().AddDate(0, 0, -(q.Days - 1)).Format("2006-01-02"),
		Until: time.Now().Format("2006-01-02"),
	}
	switch q.NS {
	case "":
	case nsNoneParam:
		f.NoNamespace = true
	default:
		f.Namespace = q.NS
	}
	return f
}

// values encodes the filter state (not sort) as query parameters.
func (q metricsQuery) values() url.Values {
	v := url.Values{}
	if q.NS != "" {
		v.Set("ns", q.NS)
	}
	v.Set("days", strconv.Itoa(q.Days))
	return v
}

func (q metricsQuery) sortValues() url.Values {
	v := q.values()
	v.Set("sort", q.Sort)
	v.Set("dir", q.Dir)
	return v
}

// --- View models ---

type metricsPageData struct {
	Page        string
	ConfigPath  string
	Enabled     bool
	HasData     bool
	Query       metricsQuery
	Namespaces  []nsOption
	DayChoices  []dayChoice
	Summary     metricsSummaryVM
	Chart       *chartVM
	Table       metricsTableVM
	Unused      []unusedNamespaceVM
	UnusedTotal int
	Recent      metricsRecentVM
	// RecentFragURL is polled by htmx to refresh the recent-calls panel.
	RecentFragURL string
}

type nsOption struct {
	Value, Label string
	Selected     bool
}

type dayChoice struct {
	Days   int
	Active bool
	URL    string
}

type metricsSummaryVM struct {
	TotalCalls   uint64
	ToolsUsed    int
	ToolsExposed int
	Errors       uint64
	ErrorRate    string
	Denied       uint64
	BusiestTool  string
	BusiestCalls uint64
}

type chartVM struct {
	W, H int
	Bars []chartBar
}

type chartBar struct {
	X, Y, W, H int
	EY, EH     int // error overlay (EH 0 = none)
	Title      string
	Stub       bool
}

type metricsTableVM struct {
	Rows    []metricsRowVM
	Headers []tableHeader
}

type tableHeader struct {
	Label   string
	URL     string // full-page fallback link
	FragURL string // htmx fragment swap
	Active  bool
	Dir     string
}

type metricsRowVM struct {
	Server, Tool string
	Calls        uint64
	Errors       uint64
	Denied       uint64
	P50, P95     string
	LastCalled   string
	Spark        *chartVM
}

type unusedNamespaceVM struct {
	Namespace string // display label; nsNoneLabel for the empty namespace
	Count     int
	Servers   []unusedServerVM
}

type unusedServerVM struct {
	Server      string
	Tools       []string
	NoDiscovery bool
}

type metricsRecentVM struct {
	Rows []recentRowVM
}

type recentRowVM struct {
	Time      string
	Namespace string
	Qualified string
	Duration  string
	Outcome   string
	PillClass string
}

// --- Builders ---

func formatMs(ms uint64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%.1fm", float64(ms)/60000)
	}
}

// formatLatency renders a latency figure, or an em dash when no dispatched
// call stands behind it — a tool whose every call was denied never reached an
// upstream and has no latency to report.
func formatLatency(ms, timedCalls uint64) string {
	if timedCalls == 0 {
		return "\u2014"
	}
	return formatMs(ms)
}

func outcomePillClass(o metrics.Outcome) string {
	switch o {
	case metrics.OutcomeOK:
		return "pill-outcome-ok"
	case metrics.OutcomeToolError, metrics.OutcomeError:
		return "pill-outcome-error"
	case metrics.OutcomeTimeout:
		return "pill-outcome-timeout"
	case metrics.OutcomeDenied:
		return "pill-outcome-denied"
	case metrics.OutcomeCancelled:
		return "pill-outcome-cancelled"
	default:
		return "pill-outcome-ok"
	}
}

func (s *Server) buildMetricsPageData(q metricsQuery) metricsPageData {
	store := s.loadMetricsStore()
	f := q.filter()

	data := metricsPageData{
		Page:          "metrics",
		ConfigPath:    s.configPathDisplay(),
		Enabled:       s.cfg.MetricsEnabled(),
		HasData:       len(store.Rows) > 0 || len(store.RecentCalls) > 0,
		Query:         q,
		Namespaces:    s.buildNSOptions(q, store.HasNoNamespaceCalls(f)),
		DayChoices:    buildDayChoices(q),
		Chart:         buildChart(store.DailyTotals(f)),
		Table:         s.buildMetricsTable(store, q),
		Recent:        buildRecentVM(store.Recent(f, 50)),
		RecentFragURL: "/fragments/metrics/recent?" + q.values().Encode(),
	}

	coverage := s.buildUnused(store, f, q.NS, "")
	data.Unused = coverage.Groups
	data.UnusedTotal = coverage.Unused

	sum := store.Summary(f)
	rate := ""
	if sum.TotalCalls > 0 {
		rate = fmt.Sprintf("%.1f%%", float64(sum.Errors)/float64(sum.TotalCalls)*100)
	}
	// Coverage counts the exposed tools that were called, not every tool with a
	// row: a denied call records against a tool that is by definition not
	// exposed, and a tool called before it was denied or unassigned is no
	// longer exposed either. Counting rows would let "tools used" exceed
	// "exposed" and contradict the unused panel below it.
	data.Summary = metricsSummaryVM{
		TotalCalls:   sum.TotalCalls,
		ToolsUsed:    coverage.Used,
		ToolsExposed: coverage.Exposed,
		Errors:       sum.Errors,
		ErrorRate:    rate,
		Denied:       sum.Denied,
		BusiestTool:  sum.BusiestTool,
		BusiestCalls: sum.BusiestCalls,
	}
	return data
}

// buildNSOptions lists the namespace filter choices. The (none) entry only
// appears when it can select something — rows recorded without an active
// namespace — or when it is already the current selection, so a bookmarked URL
// still shows what it filtered by.
func (s *Server) buildNSOptions(q metricsQuery, hasNoNamespaceCalls bool) []nsOption {
	options := []nsOption{{Value: "", Label: "All namespaces", Selected: q.NS == ""}}
	for _, entry := range s.cfg.NamespaceEntries() {
		options = append(options, nsOption{Value: entry.Name, Label: entry.Name, Selected: q.NS == entry.Name})
	}
	if len(s.cfg.Namespaces) > 0 && (hasNoNamespaceCalls || q.NS == nsNoneParam) {
		options = append(options, nsOption{Value: nsNoneParam, Label: nsNoneLabel, Selected: q.NS == nsNoneParam})
	}
	return options
}

func buildDayChoices(q metricsQuery) []dayChoice {
	choices := make([]dayChoice, 0, 3)
	for _, days := range []int{7, 30, 60} {
		alt := q
		alt.Days = days
		choices = append(choices, dayChoice{
			Days:   days,
			Active: q.Days == days,
			// sortValues, not values: changing the window must not silently
			// reset the column the user sorted by.
			URL: "/metrics?" + alt.sortValues().Encode(),
		})
	}
	return choices
}

// buildChart renders daily totals into bar geometry for a fixed-viewBox SVG.
// Zero-call days get a 1-unit baseline stub so gaps stay visible.
func buildChart(totals []metrics.DayTotal) *chartVM {
	if len(totals) == 0 {
		return nil
	}
	const barSlot, barGap, height, topPad = 10, 2, 120, 8
	chart := &chartVM{W: len(totals) * barSlot, H: height}

	var maxCalls uint64 = 1
	for _, day := range totals {
		maxCalls = max(maxCalls, day.Calls)
	}

	plotH := height - topPad
	for i, day := range totals {
		bar := chartBar{
			X: i*barSlot + barGap/2,
			W: barSlot - barGap,
			Title: fmt.Sprintf("%s: %d calls, %d errors",
				day.Date, day.Calls, day.Errors),
		}
		h := int(day.Calls * uint64(plotH) / maxCalls)
		if day.Calls > 0 && h < 2 {
			h = 2
		}
		if day.Calls == 0 {
			h = 1
			bar.Stub = true
		}
		bar.H = h
		bar.Y = height - h
		if day.Errors > 0 {
			eh := max(int(day.Errors*uint64(plotH)/maxCalls), 2)
			bar.EH = eh
			bar.EY = height - eh
		}
		chart.Bars = append(chart.Bars, bar)
	}
	return chart
}

// buildSparkline renders a per-tool daily series into a compact SVG.
func buildSparkline(daily []uint64) *chartVM {
	if len(daily) == 0 {
		return nil
	}
	const width, height = 120, 20
	barSlot := max(width/len(daily), 1)
	chart := &chartVM{W: len(daily) * barSlot, H: height}

	var maxCalls uint64 = 1
	for _, calls := range daily {
		maxCalls = max(maxCalls, calls)
	}
	for i, calls := range daily {
		h := int(calls * uint64(height-2) / maxCalls)
		if calls > 0 && h < 2 {
			h = 2
		}
		stub := false
		if calls == 0 {
			h = 1
			stub = true
		}
		w := max(barSlot-1, 1)
		chart.Bars = append(chart.Bars, chartBar{
			X: i * barSlot, Y: height - h, W: w, H: h, Stub: stub,
		})
	}
	return chart
}

func (s *Server) buildMetricsTable(store *metrics.Store, q metricsQuery) metricsTableVM {
	rows := store.ToolTable(q.filter())
	sortToolStats(rows, q.Sort, q.Dir)

	vm := metricsTableVM{Headers: buildTableHeaders(q)}
	for _, row := range rows {
		vm.Rows = append(vm.Rows, metricsRowVM{
			Server:     row.Server,
			Tool:       row.Tool,
			Calls:      row.Calls,
			Errors:     row.Errors,
			Denied:     row.Denied,
			P50:        formatLatency(row.P50Ms, row.TimedCalls),
			P95:        formatLatency(row.P95Ms, row.TimedCalls),
			LastCalled: row.LastCalled,
			Spark:      buildSparkline(row.Daily),
		})
	}
	return vm
}

func buildTableHeaders(q metricsQuery) []tableHeader {
	columns := []struct{ key, label string }{
		{"tool", "Tool"},
		{"calls", "Calls"},
		{"errors", "Errors"},
		{"p50", "p50"},
		{"p95", "p95"},
		{"last", "Last called"},
	}
	headers := make([]tableHeader, 0, len(columns))
	for _, col := range columns {
		alt := q
		alt.Sort = col.key
		// Clicking the active column toggles direction; a new column gets its
		// natural default (names ascend, numbers descend).
		if q.Sort == col.key {
			if q.Dir == "desc" {
				alt.Dir = "asc"
			} else {
				alt.Dir = "desc"
			}
		} else if col.key == "tool" {
			alt.Dir = "asc"
		} else {
			alt.Dir = "desc"
		}
		params := alt.sortValues().Encode()
		headers = append(headers, tableHeader{
			Label:   col.label,
			URL:     "/metrics?" + params,
			FragURL: "/fragments/metrics/table?" + params,
			Active:  q.Sort == col.key,
			Dir:     q.Dir,
		})
	}
	return headers
}

func sortToolStats(rows []metrics.ToolStats, sortKey, dir string) {
	byName := func(a, b metrics.ToolStats) int {
		if v := strings.Compare(a.Server, b.Server); v != 0 {
			return v
		}
		return strings.Compare(a.Tool, b.Tool)
	}
	cmpU64 := func(x, y uint64) int {
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		default:
			return 0
		}
	}
	// primary returns 0 on a tie so the name tie-break can be applied after
	// the direction flip — descending by calls should still read A→Z within
	// equal counts.
	primary := func(a, b metrics.ToolStats) int {
		switch sortKey {
		case "tool":
			return byName(a, b)
		case "errors":
			return cmpU64(a.Errors, b.Errors)
		case "p50":
			return cmpU64(a.P50Ms, b.P50Ms)
		case "p95":
			return cmpU64(a.P95Ms, b.P95Ms)
		case "last":
			return strings.Compare(a.LastCalled, b.LastCalled)
		default: // calls
			return cmpU64(a.Calls, b.Calls)
		}
	}
	slices.SortStableFunc(rows, func(a, b metrics.ToolStats) int {
		v := primary(a, b)
		if dir == "desc" {
			v = -v
		}
		if v != 0 {
			return v
		}
		return byName(a, b)
	})
}

func buildRecentVM(calls []metrics.RecentCall) metricsRecentVM {
	vm := metricsRecentVM{}
	today := time.Now().Format("2006-01-02")
	for _, rc := range calls {
		timeFmt := rc.Time.Format("15:04:05")
		if rc.Time.Format("2006-01-02") != today {
			timeFmt = rc.Time.Format("Jan 2 15:04:05")
		}
		ns := rc.Namespace
		if ns == "" {
			ns = nsNoneLabel
		}
		vm.Rows = append(vm.Rows, recentRowVM{
			Time:      timeFmt,
			Namespace: ns,
			Qualified: rc.Server + "." + rc.Tool,
			Duration:  formatMs(rc.DurationMs),
			Outcome:   string(rc.Outcome),
			PillClass: outcomePillClass(rc.Outcome),
		})
	}
	return vm
}

// unusedResult is what buildUnused computes: the groups for the unused panel
// plus coverage counts deduped over (server, tool) pairs.
type unusedResult struct {
	Groups  []unusedNamespaceVM
	Unused  int // unused tools summed across the groups
	Exposed int // distinct exposed (server, tool) pairs
	Used    int // distinct exposed pairs with at least one recorded call
}

// buildUnused computes exposed-but-never-called tools grouped namespace →
// server. "Exposed" reflects the last discovery snapshot in toolcache.json,
// filtered through the same permission resolution serve mode applies; a
// server that has never started has no cached tools and is reported as "no
// discovery data" rather than "all unused".
//
// nsParam narrows to one namespace ("" = all, nsNoneParam = the empty
// namespace); serverFilter narrows to one server ("" = all).
func (s *Server) buildUnused(store *metrics.Store, f metrics.Filter, nsParam, serverFilter string) unusedResult {
	type nsGroup struct {
		name    string
		servers []string
	}
	var groups []nsGroup
	for _, entry := range s.cfg.NamespaceEntries() {
		groups = append(groups, nsGroup{name: entry.Name, servers: entry.Config.ServerIDs})
	}
	// The empty namespace is what serve exposes when the config has no
	// namespaces at all: every enabled server. Rows recorded that way outlive
	// the creation of the first namespace by the retention window, so the
	// (none) view needs the group too — otherwise it reports calls against
	// zero exposed tools. The all-namespaces view stays about configured
	// exposure, which is the only thing serve can expose today.
	if len(groups) == 0 || nsParam == nsNoneParam {
		var all []string
		for _, entry := range s.cfg.ServerEntries() {
			if entry.Config.IsEnabled() {
				all = append(all, entry.Name)
			}
		}
		groups = append(groups, nsGroup{name: "", servers: all})
	}

	exposedSet := make(map[string]struct{})
	usedSet := make(map[string]struct{})
	var result []unusedNamespaceVM
	var unusedTotal int

	for _, group := range groups {
		switch nsParam {
		case "":
		case nsNoneParam:
			if group.name != "" {
				continue
			}
		default:
			if group.name != nsParam {
				continue
			}
		}

		calledFilter := metrics.Filter{Since: f.Since, Until: f.Until}
		if group.name == "" {
			calledFilter.NoNamespace = true
		} else {
			calledFilter.Namespace = group.name
		}
		called := store.CalledSet(calledFilter)

		label := group.name
		if label == "" {
			label = nsNoneLabel
		}
		nsVM := unusedNamespaceVM{Namespace: label}

		servers := slices.Sorted(slices.Values(group.servers))
		for _, serverName := range servers {
			if serverFilter != "" && serverName != serverFilter {
				continue
			}
			if _, ok := s.cfg.GetServer(serverName); !ok {
				continue
			}
			cached, ok := s.cachedTools(serverName)
			if !ok || len(cached) == 0 {
				nsVM.Servers = append(nsVM.Servers, unusedServerVM{Server: serverName, NoDiscovery: true})
				continue
			}
			var unusedTools []string
			for _, tool := range cached {
				allowed, _ := server.IsToolAllowed(s.cfg, group.name, serverName, tool)
				if !allowed {
					continue
				}
				key := metrics.CalledKey(serverName, tool)
				exposedSet[key] = struct{}{}
				if _, wasCalled := called[key]; wasCalled {
					usedSet[key] = struct{}{}
				} else {
					unusedTools = append(unusedTools, tool)
				}
			}
			if len(unusedTools) > 0 {
				slices.Sort(unusedTools)
				nsVM.Servers = append(nsVM.Servers, unusedServerVM{Server: serverName, Tools: unusedTools})
				nsVM.Count += len(unusedTools)
			}
		}
		if len(nsVM.Servers) > 0 {
			unusedTotal += nsVM.Count
			result = append(result, nsVM)
		}
	}
	return unusedResult{
		Groups:  result,
		Unused:  unusedTotal,
		Exposed: len(exposedSet),
		Used:    len(usedSet),
	}
}

// serverUsageVM is the compact usage block on the server detail page.
type serverUsageVM struct {
	Enabled    bool
	HasData    bool
	Calls      uint64
	Errors     uint64
	Denied     uint64
	LastCalled string
	Rows       []metricsRowVM
	Unused     []unusedNamespaceVM
}

// buildServerUsage builds the last-30-days usage block for one server,
// reusing the metrics-page view-model builders with the server filter set.
func (s *Server) buildServerUsage(serverName string) serverUsageVM {
	vm := serverUsageVM{Enabled: s.cfg.MetricsEnabled()}
	if !vm.Enabled {
		return vm
	}
	store := s.loadMetricsStore()
	q := metricsQuery{Days: 30, Sort: "calls", Dir: "desc"}
	f := q.filter()
	f.Server = serverName

	rows := store.ToolTable(f)
	sortToolStats(rows, q.Sort, q.Dir)
	for _, row := range rows {
		if row.LastCalled > vm.LastCalled {
			vm.LastCalled = row.LastCalled
		}
		vm.Rows = append(vm.Rows, metricsRowVM{
			Server:     row.Server,
			Tool:       row.Tool,
			Calls:      row.Calls,
			Errors:     row.Errors,
			Denied:     row.Denied,
			P50:        formatLatency(row.P50Ms, row.TimedCalls),
			P95:        formatLatency(row.P95Ms, row.TimedCalls),
			LastCalled: row.LastCalled,
			Spark:      buildSparkline(row.Daily),
		})
	}
	vm.HasData = len(rows) > 0

	sum := store.Summary(f)
	vm.Calls = sum.TotalCalls
	vm.Errors = sum.Errors
	vm.Denied = sum.Denied

	vm.Unused = s.buildUnused(store, f, "", serverName).Groups
	return vm
}

// cachedTools returns the discovered tool names for a server from the shared
// tool cache.
func (s *Server) cachedTools(serverName string) ([]string, bool) {
	if s.toolCache == nil {
		return nil, false
	}
	cached, ok := s.toolCache.Get(serverName)
	if !ok {
		return nil, false
	}
	names := make([]string, 0, len(cached))
	for _, tool := range cached {
		names = append(names, tool.Name)
	}
	return names, true
}

// --- Handlers ---

func (s *Server) handleMetricsPage(w http.ResponseWriter, r *http.Request) {
	q := parseMetricsQuery(r)
	s.render(w, "metrics.html", s.buildMetricsPageData(q))
}

func (s *Server) handleFragmentMetricsTable(w http.ResponseWriter, r *http.Request) {
	q := parseMetricsQuery(r)
	s.renderFragment(w, "metrics_table", s.buildMetricsTable(s.loadMetricsStore(), q))
}

func (s *Server) handleFragmentMetricsRecent(w http.ResponseWriter, r *http.Request) {
	q := parseMetricsQuery(r)
	s.renderFragment(w, "metrics_recent", buildRecentVM(s.loadMetricsStore().Recent(q.filter(), 50)))
}

// handleAPIMetrics returns the same summary + tool table + unused view as the
// page, as JSON, honouring the same query parameters.
func (s *Server) handleAPIMetrics(w http.ResponseWriter, r *http.Request) {
	q := parseMetricsQuery(r)
	store := s.loadMetricsStore()
	f := q.filter()

	rows := store.ToolTable(f)
	sortToolStats(rows, q.Sort, q.Dir)
	coverage := s.buildUnused(store, f, q.NS, "")

	type apiUnusedServer struct {
		Server      string   `json:"server"`
		Tools       []string `json:"tools,omitempty"`
		NoDiscovery bool     `json:"noDiscovery,omitempty"`
	}
	type apiUnusedNamespace struct {
		Namespace string            `json:"namespace"`
		Count     int               `json:"count"`
		Servers   []apiUnusedServer `json:"servers"`
	}
	type apiTool struct {
		Server     string   `json:"server"`
		Tool       string   `json:"tool"`
		Calls      uint64   `json:"calls"`
		Errors     uint64   `json:"errors"`
		Denied     uint64   `json:"denied,omitempty"`
		TimedCalls uint64   `json:"timedCalls"`
		AvgMs      uint64   `json:"avgMs"`
		P50Ms      uint64   `json:"p50Ms"`
		P95Ms      uint64   `json:"p95Ms"`
		MaxMs      uint64   `json:"maxMs"`
		LastCalled string   `json:"lastCalled,omitempty"`
		Daily      []uint64 `json:"daily"`
	}

	resp := struct {
		Enabled      bool                 `json:"enabled"`
		Since        string               `json:"since"`
		Until        string               `json:"until"`
		TotalCalls   uint64               `json:"totalCalls"`
		ToolsUsed    int                  `json:"toolsUsed"`
		ToolsExposed int                  `json:"toolsExposed"`
		ToolsCalled  int                  `json:"toolsCalled"`
		Errors       uint64               `json:"errors"`
		Denied       uint64               `json:"denied"`
		BusiestTool  string               `json:"busiestTool,omitempty"`
		Daily        []metrics.DayTotal   `json:"daily"`
		Tools        []apiTool            `json:"tools"`
		UnusedCount  int                  `json:"unusedCount"`
		Unused       []apiUnusedNamespace `json:"unused"`
	}{
		Enabled: s.cfg.MetricsEnabled(),
		Since:   f.Since,
		Until:   f.Until,
		Daily:   store.DailyTotals(f),
		Tools:   []apiTool{},
		Unused:  []apiUnusedNamespace{},
	}

	sum := store.Summary(f)
	resp.TotalCalls = sum.TotalCalls
	// toolsUsed is the exposed-and-called intersection (what the coverage tile
	// shows); toolsCalled is every distinct tool with a row, which also counts
	// denied and no-longer-exposed tools.
	resp.ToolsUsed = coverage.Used
	resp.ToolsExposed = coverage.Exposed
	resp.ToolsCalled = sum.DistinctTools
	resp.Errors = sum.Errors
	resp.Denied = sum.Denied
	resp.BusiestTool = sum.BusiestTool
	resp.UnusedCount = coverage.Unused

	for _, row := range rows {
		resp.Tools = append(resp.Tools, apiTool{
			Server: row.Server, Tool: row.Tool,
			Calls: row.Calls, Errors: row.Errors, Denied: row.Denied,
			TimedCalls: row.TimedCalls,
			AvgMs:      row.AvgMs, P50Ms: row.P50Ms, P95Ms: row.P95Ms, MaxMs: row.MaxMs,
			LastCalled: row.LastCalled, Daily: row.Daily,
		})
	}
	for _, group := range coverage.Groups {
		apiGroup := apiUnusedNamespace{Namespace: group.Namespace, Count: group.Count}
		for _, srv := range group.Servers {
			apiGroup.Servers = append(apiGroup.Servers, apiUnusedServer(srv))
		}
		resp.Unused = append(resp.Unused, apiGroup)
	}

	jsonOK(w, resp)
}
