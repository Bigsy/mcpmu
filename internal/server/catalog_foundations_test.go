package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

func TestVerifiedCatalogRetainsLastGoodAndRejectsOldGeneration(t *testing.T) {
	catalog := newVerifiedCatalog()
	id := process.SharedInstanceID("tools")
	toolsCapability := &mcp.ToolsCapability{ListChanged: true}

	catalog.apply(process.DiscoveryResult{
		Instance: id, Generation: 2, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Tools: toolsCapability},
		Tools:        []mcp.Tool{{Name: "old", Description: "last good"}},
	})
	catalog.apply(process.DiscoveryResult{
		Instance: id, Generation: 2, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Tools: toolsCapability},
		Err:          errors.New("transient tools/list failure"),
	})
	failed := catalog.snapshot(id)
	if failed.state != catalogFailed {
		t.Fatalf("state = %s, want failed", failed.state)
	}
	if _, ok := failed.tools["old"]; !ok {
		t.Fatal("failed refresh erased last-good tools")
	}

	catalog.apply(process.DiscoveryResult{
		Instance: id, Generation: 1, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Tools: toolsCapability},
		Tools:        []mcp.Tool{{Name: "stale"}},
	})
	if _, ok := catalog.snapshot(id).tools["stale"]; ok {
		t.Fatal("late result from an old generation replaced the catalog")
	}

	catalog.apply(process.DiscoveryResult{
		Instance: id, Generation: 2, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Tools: toolsCapability},
		Tools:        []mcp.Tool{{Name: "new"}},
	})
	verified := catalog.snapshot(id)
	if verified.state != catalogVerified {
		t.Fatalf("state = %s, want verified", verified.state)
	}
	if _, ok := verified.tools["new"]; !ok {
		t.Fatal("successful retry did not replace the catalog")
	}
	if _, ok := verified.tools["old"]; ok {
		t.Fatal("full refresh retained a removed tool")
	}
}

func TestCatalogUnknownGenerationFailureWakesJoinersWithError(t *testing.T) {
	catalog := newVerifiedCatalog()
	id := process.SharedInstanceID("crashed")
	catalog.apply(process.DiscoveryResult{
		Instance: id, Generation: 7, Initialized: true,
		Tools: []mcp.Tool{{Name: "last_good"}},
	})
	catalog.invalidate(id, 7)
	ownerFlight, owner := catalog.begin(id)
	if !owner {
		t.Fatal("first caller did not own discovery flight")
	}
	joinedFlight, joinedOwner := catalog.begin(id)
	if joinedOwner || joinedFlight != ownerFlight {
		t.Fatal("concurrent caller did not join discovery flight")
	}

	wantErr := errors.New("restart executable missing")
	catalog.fail(id, 0, wantErr)
	catalog.finish(id, ownerFlight)
	if err := waitForFlight(context.Background(), joinedFlight); err != nil {
		t.Fatal(err)
	}
	entry := catalog.snapshot(id)
	if entry.state != catalogFailed || !errors.Is(catalogError(entry), wantErr) {
		t.Fatalf("catalog state = %s, error = %v", entry.state, catalogError(entry))
	}
	if _, ok := entry.tools["last_good"]; !ok {
		t.Fatal("failed restart erased last-good tools")
	}
}

func TestVerifiedCatalogDistinguishesVerifiedEmptyFromUnknown(t *testing.T) {
	catalog := newVerifiedCatalog()
	unknownID := process.SharedInstanceID("unknown")
	emptyID := process.SharedInstanceID("resources-only")
	catalog.apply(process.DiscoveryResult{
		Instance: emptyID, Generation: 1, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Resources: &mcp.ResourcesCapability{}},
		Tools:        []mcp.Tool{},
	})
	if got := catalog.snapshot(unknownID).state; got != catalogUnknown {
		t.Fatalf("unknown state = %s", got)
	}
	if got := catalog.snapshot(emptyID).state; got != catalogVerified {
		t.Fatalf("verified-empty state = %s", got)
	}
}

func TestAggregatorConcurrentColdDiscoveryIsSingleflight(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	requestLog := t.TempDir() + "/requests.log"
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"shared": fakeServerConfig(t, map[string]any{
			"tools":          []any{map[string]any{"name": "one"}},
			"delays":         map[string]int64{"tools/list": int64(50 * time.Millisecond)},
			"requestLogPath": requestLog,
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tools, listErr := core.currentAggregator().ListTools(ctx, []string{"shared"})
			if listErr != nil || len(tools) != 1 {
				errCh <- errors.New("cold list did not return one tool")
			}
		})
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	methods, err := os.ReadFile(requestLog)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if got := strings.Count(string(methods), "initialize\n"); got != 1 {
		t.Fatalf("initialize count = %d, want 1; log:\n%s", got, methods)
	}
	if got := strings.Count(string(methods), "tools/list\n"); got != 1 {
		t.Fatalf("tools/list count = %d, want 1; log:\n%s", got, methods)
	}
}

func TestInitializeWithoutToolsCapabilityVerifiesEmptyWithoutListing(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	requestLog := t.TempDir() + "/requests.log"
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"resources-only": fakeServerConfig(t, map[string]any{
			"advertiseTools":     false,
			"advertiseResources": true,
			"errors": map[string]any{
				"tools/list": map[string]any{"code": -32601, "message": "method not found"},
			},
			"requestLogPath": requestLog,
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)
	tools, _ := core.currentAggregator().ListTools(context.Background(), []string{"resources-only"})
	if len(tools) != 0 {
		t.Fatalf("tools = %v, want verified empty", tools)
	}
	entry := core.currentAggregator().catalog.snapshot(process.SharedInstanceID("resources-only"))
	if entry.state != catalogVerified || entry.capabilities.Resources == nil {
		t.Fatalf("catalog entry = state %s capabilities %+v", entry.state, entry.capabilities)
	}
	methods, err := os.ReadFile(requestLog)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if strings.Contains(string(methods), "tools/list\n") {
		t.Fatalf("tools/list was called despite absent tools capability; log:\n%s", methods)
	}
}

func TestFailedToolDiscoveryRetriesWithoutRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	requestLog := t.TempDir() + "/requests.log"
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"retryable": fakeServerConfig(t, map[string]any{
			"tools":          []any{map[string]any{"name": "recovered"}},
			"failOnAttempt":  map[string]int{"tools/list": 1},
			"requestLogPath": requestLog,
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)
	aggregator := core.currentAggregator()
	first, _ := aggregator.ListTools(context.Background(), []string{"retryable"})
	if len(first) != 0 {
		t.Fatalf("first list = %v, want failed empty result", first)
	}
	if got := aggregator.catalog.snapshot(process.SharedInstanceID("retryable")).state; got != catalogFailed {
		t.Fatalf("state after first list = %s, want failed", got)
	}
	second, _ := aggregator.ListTools(context.Background(), []string{"retryable"})
	if len(second) != 1 || second[0].Name != "retryable.recovered" {
		t.Fatalf("second list = %v, want recovered tool", second)
	}
	methods, err := os.ReadFile(requestLog)
	if err != nil {
		t.Fatalf("read request log: %v", err)
	}
	if got := strings.Count(string(methods), "initialize\n"); got != 1 {
		t.Fatalf("initialize count = %d, want 1; log:\n%s", got, methods)
	}
	if got := strings.Count(string(methods), "tools/list\n"); got != 2 {
		t.Fatalf("tools/list count = %d, want 2; log:\n%s", got, methods)
	}
}

func TestUpstreamToolsListChangedRefreshesBeforeFanout(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess test")
	}
	testutil.SetupTestHome(t)
	cfg := &config.Config{SchemaVersion: 1, Servers: map[string]config.ServerConfig{
		"changing": fakeServerConfig(t, map[string]any{
			"tools":                              []any{map[string]any{"name": "old"}},
			"toolsAfterListChanged":              []any{map[string]any{"name": "new"}},
			"emitToolsListChangedAfterFirstList": true,
		}),
	}}
	core, err := NewCore(Options{Config: cfg, PIDTrackerDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)
	sink := &recordingNotificationSink{notifications: make(chan process.UpstreamNotification, 2)}
	unsubscribe, err := core.notifications.Subscribe(sink)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(unsubscribe)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := core.currentAggregator().ListTools(ctx, []string{"changing"}); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	select {
	case notification := <-sink.notifications:
		if notification.Method != "notifications/tools/list_changed" {
			t.Fatalf("notification = %s", notification.Method)
		}
		if _, ok := core.currentAggregator().GetTool("changing.new"); !ok {
			t.Fatal("list_changed fanned out before refreshed catalog was visible")
		}
		if _, ok := core.currentAggregator().GetTool("changing.old"); ok {
			t.Fatal("refresh retained tool removed by upstream")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for refresh-and-fanout (possible reader deadlock)")
	}
}

func TestCapabilityScopedFanoutDecision(t *testing.T) {
	testutil.SetupTestHome(t)
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{PIDTrackerDir: t.TempDir()})
	t.Cleanup(supervisor.StopAll)
	aggregator := NewAggregator(&config.Config{}, supervisor, false)
	aggregator.applyDiscovery(process.DiscoveryResult{
		Instance: process.SharedInstanceID("tools-only"), Generation: 1, Initialized: true,
		Capabilities: mcp.ServerCapabilities{Tools: &mcp.ToolsCapability{}}, Tools: []mcp.Tool{},
	})
	if aggregator.shouldQueryCapability("tools-only", catalogResources) {
		t.Fatal("verified tools-only server should be skipped by resources/list fan-out")
	}
	if aggregator.shouldQueryCapability("tools-only", catalogPrompts) {
		t.Fatal("verified tools-only server should be skipped by prompts/list fan-out")
	}
	if !aggregator.shouldQueryCapability("unknown", catalogResources) {
		t.Fatal("unknown server capability must be probed")
	}
}
