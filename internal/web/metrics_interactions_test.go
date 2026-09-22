package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/metrics"
	"github.com/Bigsy/mcpmu/internal/process"
)

// newSeededMetricsServer builds a web server over a config with servers
// "browser" and "agent" whose metrics.json holds whatever seed records.
func newSeededMetricsServer(t *testing.T, seed func(rec *metrics.Recorder, now time.Time)) *Server {
	t.Helper()
	cfg := config.NewConfig()
	_ = cfg.AddServer("browser", config.ServerConfig{Command: "echo"})
	_ = cfg.AddServer("agent", config.ServerConfig{Command: "echo"})
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := config.SaveTo(cfg, configPath); err != nil {
		t.Fatalf("save config: %v", err)
	}
	rec := metrics.NewRecorder(filepath.Join(dir, "metrics.json"), 60)
	seed(rec, time.Now())
	if err := rec.Close(); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	srv, err := New(Options{
		Addr: "127.0.0.1:0", Config: cfg, ConfigPath: configPath,
		Supervisor: process.NewSupervisor(bus), Bus: bus,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func seedInteractions(rec *metrics.Recorder, now time.Time) {
	add := func(server, method, outcome string, n int) {
		for range n {
			rec.RecordInteraction(metrics.InteractionSample{Time: now, Server: server, Method: method, Outcome: outcome})
		}
	}
	add("browser", "elicitation/create", "accept", 3)
	add("browser", "elicitation/create", "decline", 1)
	add("browser", "elicitation/create", "fallback:timeout", 2)
	add("agent", "sampling/createMessage", "completed", 1)
	add("agent", "sampling/createMessage", "fallback:unroutable", 1)
	add("agent", "sampling/createMessage", "fallback:from-the-future", 1)
	rec.Record(metrics.CallSample{Time: now, Server: "browser", Tool: "navigate", Duration: 300 * time.Millisecond,
		InteractionWait: 42 * time.Second, Outcome: metrics.OutcomeOK})
	rec.Record(metrics.CallSample{Time: now, Server: "browser", Tool: "screenshot", Duration: 100 * time.Millisecond, Outcome: metrics.OutcomeOK})
}

func TestMetricsPage_ShowsClientRequests(t *testing.T) {
	srv := newSeededMetricsServer(t, seedInteractions)
	status, html := get(t, srv, "/metrics")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	for _, want := range []string{
		"Client requests",
		"4 not relayed", // 2 timeouts + unroutable + the unknown fallback
		"elicitation/create",
		"sampling/createMessage",
		"accepted &times;3",
		"declined &times;1",
		"timed out &times;2",
		"completed &times;1",
		"unroutable &times;1",
		"fallback:from-the-future &times;1", // unknown outcomes shown verbatim
		"Nobody answered within the interaction timeout",
		// Waited column appears because a call waited, with its total.
		">Waited<",
		"42.0s",
		// Recent calls show the wait next to the duration.
		"+42.0s waiting",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Outcome order: answers before fallbacks.
	if strings.Index(html, "accepted &times;3") > strings.Index(html, "timed out &times;2") {
		t.Error("fallbacks listed before the client's answers")
	}
}

func TestMetricsPage_NoClientRequestsNoCard(t *testing.T) {
	srv := newSeededMetricsServer(t, func(rec *metrics.Recorder, now time.Time) {
		rec.Record(metrics.CallSample{Time: now, Server: "browser", Tool: "navigate", Duration: time.Millisecond, Outcome: metrics.OutcomeOK})
	})
	_, html := get(t, srv, "/metrics")
	for _, unwanted := range []string{"Client requests", ">Waited<", "waiting"} {
		if strings.Contains(html, unwanted) {
			t.Errorf("page shows %q with no relayed interactions", unwanted)
		}
	}
}

func TestMetricsPage_InteractionsAloneAreData(t *testing.T) {
	srv := newSeededMetricsServer(t, func(rec *metrics.Recorder, now time.Time) {
		rec.RecordInteraction(metrics.InteractionSample{Time: now, Server: "browser", Method: "elicitation/create", Outcome: "fallback:unroutable"})
	})
	_, html := get(t, srv, "/metrics")
	if strings.Contains(html, "No usage recorded yet") || !strings.Contains(html, "Client requests") {
		t.Error("a window with only interactions rendered as empty")
	}
}

func TestMetricsFragment_SortByWaited(t *testing.T) {
	srv := newSeededMetricsServer(t, seedInteractions)
	_, html := get(t, srv, "/fragments/metrics/table?sort=waited&dir=desc")
	if i, j := strings.Index(html, "browser.navigate"), strings.Index(html, "browser.screenshot"); i < 0 || j < 0 || i > j {
		t.Errorf("sorting by waited did not put the waiting call first")
	}
}

func TestMetricsAPI_Interactions(t *testing.T) {
	srv := newSeededMetricsServer(t, seedInteractions)
	_, body := get(t, srv, "/api/metrics")
	var resp struct {
		Tools []struct {
			Tool              string `json:"tool"`
			InteractionWaitMs uint64 `json:"interactionWaitMs"`
		} `json:"tools"`
		Interactions []struct {
			Server, Method, Outcome string
			Count                   uint64
		} `json:"interactions"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if len(resp.Interactions) != 6 {
		t.Fatalf("interactions = %+v", resp.Interactions)
	}
	found := false
	for _, in := range resp.Interactions {
		if in.Server == "browser" && in.Method == "elicitation/create" && in.Outcome == "accept" && in.Count == 3 {
			found = true
		}
	}
	if !found {
		t.Errorf("browser accept count missing: %+v", resp.Interactions)
	}
	for _, tool := range resp.Tools {
		if tool.Tool == "navigate" && tool.InteractionWaitMs != 42000 {
			t.Errorf("navigate interactionWaitMs = %d", tool.InteractionWaitMs)
		}
	}
}
