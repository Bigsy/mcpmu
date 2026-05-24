package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
)

// TestAggregator_ListTools_PartialDiscoveryFailure pins the per-server cache
// guarantees that compact-mode (and the rest of the routing layer) rely on:
//
//   - A discovery that times out for one server leaves *that server's* cache
//     entry empty (not poisoned with stale data) — but does not affect any
//     other server's entries.
//   - A server that succeeded in an earlier `DiscoverServer` call and is not
//     included in a later `ListTools` request keeps its cached tools.
//   - A subsequent successful `DiscoverServer` for the failed server
//     populates it without disturbing the others.
//
// This makes the asymmetry between `ListTools` (now per-server replace),
// `RefreshServerTools` (per-server replace) and `DiscoverServer` (per-server
// merge) explicit so a future regression flips a test, not production.
func TestAggregator_ListTools_PartialDiscoveryFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in short mode (subprocess startup)")
	}
	t.Parallel()

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			// srv_a: tools/list delayed 300ms — outlasts the 100ms ListTools ctx.
			"srv_a": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"a_tool","description":"tool from A"}],"delays":{"tools/list":300000000}}`,
				},
			},
			"srv_b": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"b_tool","description":"tool from B"}]}`,
				},
			},
			"srv_c": {
				Kind:    config.ServerKindStdio,
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           `{"tools":[{"name":"c_tool","description":"tool from C"}]}`,
				},
			},
		},
	}

	bus := events.NewBus()
	sup := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		PIDTrackerDir: t.TempDir(),
	})
	t.Cleanup(sup.StopAll)

	agg := NewAggregator(cfg, sup, false)

	// Pre-populate srv_c via DiscoverServer with a generous timeout.
	ctxC, cancelC := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelC()
	if _, err := agg.DiscoverServer(ctxC, "srv_c"); err != nil {
		t.Fatalf("pre-discover srv_c: %v", err)
	}
	if _, ok := agg.GetTool("srv_c.c_tool"); !ok {
		t.Fatalf("expected srv_c.c_tool in cache after pre-discovery")
	}

	// ListTools on [srv_a, srv_b] with a short ctx so srv_a's 300ms tools/list
	// misses the deadline. srv_b returns immediately.
	ctxList, cancelList := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelList()
	tools, _ := agg.ListTools(ctxList, []string{"srv_a", "srv_b"})

	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["srv_b.b_tool"] {
		t.Errorf("expected srv_b.b_tool in returned tools, got: %v", names)
	}
	if names["srv_a.a_tool"] {
		t.Errorf("did not expect srv_a.a_tool in returned tools (A timed out): %v", names)
	}
	// ListTools requested only A and B; C should not appear in the result.
	if names["srv_c.c_tool"] {
		t.Errorf("did not expect srv_c.c_tool in returned tools (not requested): %v", names)
	}

	// Cache assertions: A should be empty (not stale), B populated, C intact.
	if _, ok := agg.GetTool("srv_a.a_tool"); ok {
		t.Errorf("srv_a.a_tool unexpectedly in cache after timeout (entry should be empty)")
	}
	if _, ok := agg.GetTool("srv_b.b_tool"); !ok {
		t.Errorf("srv_b.b_tool missing from cache after successful discovery")
	}
	if _, ok := agg.GetTool("srv_c.c_tool"); !ok {
		t.Errorf("srv_c.c_tool wiped from cache by ListTools(A,B) — should be untouched")
	}

	// Re-discover srv_a with a generous timeout. Its first tools/list is by
	// now in flight (or completed) so this should succeed quickly.
	ctxA, cancelA := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelA()
	if _, err := agg.DiscoverServer(ctxA, "srv_a"); err != nil {
		t.Fatalf("re-discover srv_a: %v", err)
	}
	if _, ok := agg.GetTool("srv_a.a_tool"); !ok {
		t.Errorf("srv_a.a_tool not in cache after successful re-discovery")
	}
	if _, ok := agg.GetTool("srv_b.b_tool"); !ok {
		t.Errorf("srv_b.b_tool dropped from cache by re-discovering A")
	}
	if _, ok := agg.GetTool("srv_c.c_tool"); !ok {
		t.Errorf("srv_c.c_tool dropped from cache by re-discovering A")
	}
}
