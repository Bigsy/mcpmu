package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/metrics"
	"github.com/Bigsy/mcpmu/internal/process"
)

// newMetricsTestServer builds a web server whose config dir holds a seeded
// toolcache.json and metrics.json:
//
//   - github (ns work): tools create_issue, list_issues, denied_tool
//     (denied_tool globally denied); create_issue called, list_issues not
//   - local (ns work): no toolcache entry (never discovered)
//   - dice (ns play): tool roll, called once
//   - one no-namespace call: github.create_issue
func newMetricsTestServer(t *testing.T) *Server {
	t.Helper()

	cfg := config.NewConfig()
	enabled := true
	_ = cfg.AddServer("github", config.ServerConfig{
		Command: "echo", Enabled: &enabled, DeniedTools: []string{"denied_tool"},
	})
	_ = cfg.AddServer("local", config.ServerConfig{Command: "echo", Enabled: &enabled})
	_ = cfg.AddServer("dice", config.ServerConfig{Command: "echo", Enabled: &enabled})
	_ = cfg.AddNamespace("work", config.NamespaceConfig{ServerIDs: []string{"github", "local"}})
	_ = cfg.AddNamespace("play", config.NamespaceConfig{ServerIDs: []string{"dice"}})

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}

	toolCache, err := config.NewToolCache(configPath)
	if err != nil {
		t.Fatalf("tool cache: %v", err)
	}
	if err := toolCache.Update("github", []config.CachedToolInput{
		{Name: "create_issue"}, {Name: "list_issues"}, {Name: "denied_tool"},
	}); err != nil {
		t.Fatalf("seed github tools: %v", err)
	}
	if err := toolCache.Update("dice", []config.CachedToolInput{{Name: "roll"}}); err != nil {
		t.Fatalf("seed dice tools: %v", err)
	}

	metricsPath := filepath.Join(dir, "metrics.json")
	rec := metrics.NewRecorder(metricsPath, 60)
	now := time.Now()
	rec.Record(metrics.CallSample{Time: now, Namespace: "work", Server: "github", Tool: "create_issue", Duration: 812 * time.Millisecond, Outcome: metrics.OutcomeOK})
	rec.Record(metrics.CallSample{Time: now.AddDate(0, 0, -1), Namespace: "work", Server: "github", Tool: "create_issue", Duration: 400 * time.Millisecond, Outcome: metrics.OutcomeOK})
	rec.Record(metrics.CallSample{Time: now, Namespace: "work", Server: "github", Tool: "create_issue", Duration: 90 * time.Millisecond, Outcome: metrics.OutcomeToolError})
	rec.Record(metrics.CallSample{Time: now, Namespace: "play", Server: "dice", Tool: "roll", Duration: 5 * time.Millisecond, Outcome: metrics.OutcomeOK})
	rec.Record(metrics.CallSample{Time: now, Namespace: "", Server: "github", Tool: "create_issue", Duration: 100 * time.Millisecond, Outcome: metrics.OutcomeOK})
	if err := rec.Flush(); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}

	bus := events.NewBus()
	t.Cleanup(bus.Close)

	srv, err := New(Options{
		Addr:       "127.0.0.1:0",
		Config:     cfg,
		ConfigPath: configPath,
		Supervisor: process.NewSupervisor(bus),
		Bus:        bus,
		ToolCache:  toolCache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func get(t *testing.T, srv *Server, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestMetricsPage_RendersSeededData(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, html := get(t, srv, "/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	for _, want := range []string{
		"Metrics",
		"github.create_issue", // tool table row
		"dice.roll",
		"Never called in this window",
		"list_issues",       // unused chip
		"no discovery data", // local has no toolcache entry
		"Recent calls",
		"metrics-chart", // calls-per-day SVG
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}

	// Globally denied tool is not exposed, so it must NOT be listed as unused.
	if strings.Contains(html, "denied_tool") {
		t.Error("globally denied tool appeared on the metrics page")
	}
}

func TestMetricsPage_NamespaceFilter(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, html := get(t, srv, "/metrics?ns=play")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(html, "dice.roll") {
		t.Error("play namespace missing dice.roll")
	}
	if strings.Contains(html, "github.create_issue") {
		t.Error("play namespace leaked github rows")
	}

	// The empty namespace shows only the no-namespace call.
	status, html = get(t, srv, "/metrics?ns="+nsNoneParam)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(html, "github.create_issue") {
		t.Error("(none) namespace missing the no-namespace call")
	}
	if strings.Contains(html, "dice.roll") {
		t.Error("(none) namespace leaked play rows")
	}
}

func TestMetricsFragment_Sort(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, html := get(t, srv, "/fragments/metrics/table?sort=calls&dir=desc")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	createIdx := strings.Index(html, "github.create_issue")
	rollIdx := strings.Index(html, "dice.roll")
	if createIdx < 0 || rollIdx < 0 {
		t.Fatalf("fragment missing rows: create=%d roll=%d", createIdx, rollIdx)
	}
	if createIdx > rollIdx {
		t.Error("calls desc: create_issue (4 calls) should sort before roll (1 call)")
	}

	_, html = get(t, srv, "/fragments/metrics/table?sort=calls&dir=asc")
	createIdx = strings.Index(html, "github.create_issue")
	rollIdx = strings.Index(html, "dice.roll")
	if createIdx < rollIdx {
		t.Error("calls asc: roll should sort before create_issue")
	}
}

func TestMetricsFragment_Recent(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, html := get(t, srv, "/fragments/metrics/recent")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(html, "github.create_issue") {
		t.Error("recent fragment missing calls")
	}
	if !strings.Contains(html, "pill-outcome-error") {
		t.Error("recent fragment missing tool_error pill")
	}
}

func TestMetricsAPI_Shape(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, body := get(t, srv, "/api/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	var resp struct {
		Enabled      bool   `json:"enabled"`
		Since        string `json:"since"`
		Until        string `json:"until"`
		TotalCalls   uint64 `json:"totalCalls"`
		ToolsUsed    int    `json:"toolsUsed"`
		ToolsExposed int    `json:"toolsExposed"`
		Errors       uint64 `json:"errors"`
		Daily        []struct {
			Date string `json:"date"`
		} `json:"daily"`
		Tools []struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
			Calls  uint64 `json:"calls"`
			P95Ms  uint64 `json:"p95Ms"`
		} `json:"tools"`
		UnusedCount int `json:"unusedCount"`
		Unused      []struct {
			Namespace string `json:"namespace"`
			Servers   []struct {
				Server      string   `json:"server"`
				Tools       []string `json:"tools"`
				NoDiscovery bool     `json:"noDiscovery"`
			} `json:"servers"`
		} `json:"unused"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}

	if !resp.Enabled {
		t.Error("enabled = false")
	}
	if resp.TotalCalls != 5 {
		t.Errorf("totalCalls = %d, want 5", resp.TotalCalls)
	}
	if resp.ToolsUsed != 2 {
		t.Errorf("toolsUsed = %d, want 2", resp.ToolsUsed)
	}
	// Exposed: github.create_issue + github.list_issues + dice.roll
	// (denied_tool is not exposed).
	if resp.ToolsExposed != 3 {
		t.Errorf("toolsExposed = %d, want 3", resp.ToolsExposed)
	}
	if resp.Errors != 1 {
		t.Errorf("errors = %d, want 1", resp.Errors)
	}
	if len(resp.Daily) != 30 {
		t.Errorf("daily = %d days, want 30", len(resp.Daily))
	}
	if len(resp.Tools) != 2 {
		t.Fatalf("tools = %d rows, want 2", len(resp.Tools))
	}
	if resp.Tools[0].Tool != "create_issue" || resp.Tools[0].Calls != 4 {
		t.Errorf("first tool = %+v, want create_issue with 4 calls", resp.Tools[0])
	}
	if resp.UnusedCount != 1 {
		t.Errorf("unusedCount = %d, want 1 (list_issues)", resp.UnusedCount)
	}

	var foundUnused, foundNoDiscovery, foundDenied bool
	for _, group := range resp.Unused {
		for _, s := range group.Servers {
			for _, tool := range s.Tools {
				if tool == "list_issues" {
					foundUnused = true
				}
				if tool == "denied_tool" {
					foundDenied = true
				}
			}
			if s.Server == "local" && s.NoDiscovery {
				foundNoDiscovery = true
			}
		}
	}
	if !foundUnused {
		t.Error("list_issues missing from unused")
	}
	if foundDenied {
		t.Error("globally denied tool listed as unused — it isn't exposed")
	}
	if !foundNoDiscovery {
		t.Error("local server missing its no-discovery marker")
	}
}

func TestMetricsPage_EmptyState(t *testing.T) {
	// Plain test server: no metrics.json exists.
	srv := newTestServer(t)

	status, html := get(t, srv, "/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(html, "No usage recorded yet") {
		t.Error("missing empty-state explainer")
	}
}

func TestMetricsPage_DisabledState(t *testing.T) {
	srv := newTestServer(t)
	disabled := false
	srv.cfg.Metrics = &config.MetricsConfig{Enabled: &disabled}

	status, html := get(t, srv, "/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(html, "Metrics are disabled") {
		t.Error("missing disabled-state explainer")
	}
}

func TestServerDetail_UsageSection(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, html := get(t, srv, "/servers/github")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.Contains(html, "Usage (last 30 days)") {
		t.Error("missing usage section")
	}
	if !strings.Contains(html, "create_issue") {
		t.Error("missing per-tool usage row")
	}
	// list_issues shows as unused inline on the detail page.
	if !strings.Contains(html, "Never called in this window") {
		t.Error("missing inline unused block")
	}
}

// recordCalls appends samples to the metrics file the web server reads, the way
// a serve process would, and drops the mtime-keyed store cache so the next
// request re-reads the file.
func recordCalls(t *testing.T, srv *Server, samples ...metrics.CallSample) {
	t.Helper()
	path, err := metrics.MetricsPath(srv.configPath)
	if err != nil {
		t.Fatalf("metrics path: %v", err)
	}
	rec := metrics.NewRecorder(path, 60)
	for _, sample := range samples {
		rec.Record(sample)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("record samples: %v", err)
	}
	srv.metricsMu.Lock()
	srv.metricsStore = nil
	srv.metricsMu.Unlock()
}

// Calls recorded before the first namespace existed keep an empty namespace for
// the whole retention window. The (none) view has to know what serve exposed at
// the time — every enabled server — or it reports calls against zero exposed
// tools and an empty unused panel.
func TestMetricsPage_NoNamespaceViewHasExposure(t *testing.T) {
	srv := newMetricsTestServer(t)

	status, body := get(t, srv, "/api/metrics?ns=:none:&days=30")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var resp struct {
		TotalCalls   uint64 `json:"totalCalls"`
		ToolsUsed    int    `json:"toolsUsed"`
		ToolsExposed int    `json:"toolsExposed"`
		UnusedCount  int    `json:"unusedCount"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	if resp.TotalCalls != 1 {
		t.Errorf("totalCalls = %d, want 1 (the no-namespace create_issue call)", resp.TotalCalls)
	}
	// Exposed without a namespace: github.create_issue, github.list_issues and
	// dice.roll (denied_tool is globally denied, local has no discovery data).
	if resp.ToolsExposed != 3 {
		t.Errorf("toolsExposed = %d, want 3", resp.ToolsExposed)
	}
	if resp.ToolsUsed != 1 {
		t.Errorf("toolsUsed = %d, want 1", resp.ToolsUsed)
	}
	if resp.UnusedCount != 2 {
		t.Errorf("unusedCount = %d, want 2 (list_issues, roll)", resp.UnusedCount)
	}

	_, html := get(t, srv, "/metrics?ns=:none:&days=30")
	if strings.Contains(html, "of 0 exposed") {
		t.Error("the (none) view still claims zero exposed tools")
	}
	if !strings.Contains(html, nsNoneLabel) {
		t.Error("the (none) filter option is missing while no-namespace calls exist")
	}
}

// A denied call records against a tool that is by definition not exposed, so it
// must not count toward the coverage tile — otherwise "tools used" can equal or
// exceed "exposed" while the unused panel still lists uncalled tools.
func TestMetricsAPI_DeniedCallDoesNotCountAsUsed(t *testing.T) {
	srv := newMetricsTestServer(t)
	recordCalls(t, srv, metrics.CallSample{
		Time: time.Now(), Namespace: "work", Server: "github",
		Tool: "denied_tool", Outcome: metrics.OutcomeDenied,
	})

	_, body := get(t, srv, "/api/metrics?days=30")
	var resp struct {
		TotalCalls   uint64 `json:"totalCalls"`
		ToolsUsed    int    `json:"toolsUsed"`
		ToolsExposed int    `json:"toolsExposed"`
		ToolsCalled  int    `json:"toolsCalled"`
		Denied       uint64 `json:"denied"`
		UnusedCount  int    `json:"unusedCount"`
		Daily        []struct {
			Date  string `json:"date"`
			Calls uint64 `json:"calls"`
		} `json:"daily"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, body)
	}
	if resp.TotalCalls != 6 || resp.Denied != 1 {
		t.Errorf("totalCalls/denied = %d/%d, want 6/1", resp.TotalCalls, resp.Denied)
	}
	if resp.ToolsExposed != 3 {
		t.Errorf("toolsExposed = %d, want 3", resp.ToolsExposed)
	}
	if resp.ToolsUsed != 2 {
		t.Errorf("toolsUsed = %d, want 2 (create_issue, roll) — the denied tool is not exposed", resp.ToolsUsed)
	}
	if resp.ToolsUsed > resp.ToolsExposed {
		t.Errorf("toolsUsed %d exceeds toolsExposed %d", resp.ToolsUsed, resp.ToolsExposed)
	}
	if resp.ToolsCalled != 3 {
		t.Errorf("toolsCalled = %d, want 3 (every tool with a row, denials included)", resp.ToolsCalled)
	}
	if resp.UnusedCount != 1 {
		t.Errorf("unusedCount = %d, want 1 (list_issues is still uncalled)", resp.UnusedCount)
	}
	// daily rows are camelCase like the rest of the payload.
	if len(resp.Daily) != 30 {
		t.Fatalf("daily = %d days, want 30", len(resp.Daily))
	}
	if resp.Daily[len(resp.Daily)-1].Calls == 0 {
		t.Error(`daily[].calls did not decode — check the JSON tags on DayTotal`)
	}

	// The denied tool has no dispatched call behind it, so its latency columns
	// report nothing rather than the first histogram bucket.
	table := srv.buildMetricsTable(srv.loadMetricsStore(), metricsQuery{Days: 30, Sort: "calls", Dir: "desc"})
	var found bool
	for _, row := range table.Rows {
		if row.Tool != "denied_tool" {
			continue
		}
		found = true
		if row.Denied != 1 {
			t.Errorf("denied_tool row = %+v, want 1 denied", row)
		}
		if row.P50 != "—" || row.P95 != "—" {
			t.Errorf("denied_tool latency = p50 %q p95 %q, want em dashes", row.P50, row.P95)
		}
	}
	if !found {
		t.Error("denied_tool missing from the per-tool table")
	}
}

// Switching the window must not throw away the column the user sorted by.
func TestMetricsPage_WindowLinksKeepSort(t *testing.T) {
	srv := newMetricsTestServer(t)
	_, html := get(t, srv, "/metrics?days=30&sort=p95&dir=asc")
	for _, want := range []string{"days=7", "sort=p95", "dir=asc"} {
		if !strings.Contains(html, want) {
			t.Errorf("window links dropped %q", want)
		}
	}
	if strings.Contains(html, `href="/metrics?days=7"`) {
		t.Error("the 7d link carries no sort state")
	}
}
