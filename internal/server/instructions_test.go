package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// runInitializeOnly starts a serve session over a single initialize request,
// after running prestart against its Core so already-running upstreams are
// visible to initialize, and returns the decoded initialize result as a map so
// tests can also assert that a key is absent.
func runInitializeOnly(t *testing.T, cfg *config.Config, prestart func(t *testing.T, s *Session)) map[string]any {
	t.Helper()

	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"test","version":"1.0"}}}
`)
	srv, err := New(Options{
		Config:        cfg,
		PIDTrackerDir: t.TempDir(),
		Stdin:         stdin,
		Stdout:        &stdout,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if prestart != nil {
		prestart(t, srv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Run(ctx)

	var resp struct {
		Result map[string]any `json:"result"`
		Error  *RPCError      `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &resp); err != nil {
		t.Fatalf("Unmarshal: %v\nOutput: %s", err, stdout.String())
	}
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	return resp.Result
}

// TestServer_Initialize_AggregatesRunningUpstreamInstructions verifies that
// the instructions of upstreams already running at initialize are surfaced
// downstream, one section per server, and that servers without instructions
// contribute no section.
func TestServer_Initialize_AggregatesRunningUpstreamInstructions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"alpha": fakeServerConfig(t, map[string]any{
				"instructions": "Call alpha.search before alpha.fetch.",
				"tools":        []map[string]any{{"name": "search"}, {"name": "fetch"}},
			}),
			"beta": fakeServerConfig(t, map[string]any{
				"tools": []map[string]any{{"name": "noop"}},
			}),
		},
	}

	result := runInitializeOnly(t, cfg, func(t *testing.T, s *Session) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, name := range []string{"alpha", "beta"} {
			if _, _, err := s.getOrStartHandle(ctx, name); err != nil {
				t.Fatalf("start %s: %v", name, err)
			}
		}
	})

	instructions, _ := result["instructions"].(string)
	if instructions == "" {
		t.Fatalf("expected instructions in initialize result, got %v", result)
	}
	for _, want := range []string{"mcpmu-test", "## alpha", "Call alpha.search before alpha.fetch."} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions missing %q:\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, "beta") {
		t.Errorf("server without instructions must not get a section:\n%s", instructions)
	}
}

// TestServer_Initialize_OmitsInstructionsWhenNoneRunning verifies the key is
// absent, not empty, when no upstream is running yet (the cold lazy-start
// case) — clients must not be handed an empty instructions string.
func TestServer_Initialize_OmitsInstructionsWhenNoneRunning(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"alpha": fakeServerConfig(t, map[string]any{
				"instructions": "never seen: not started",
			}),
		},
	}

	result := runInitializeOnly(t, cfg, nil)
	if _, present := result["instructions"]; present {
		t.Errorf("instructions must be omitted when no upstream is running, got %v", result["instructions"])
	}
}
