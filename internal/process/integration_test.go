//go:build integration

package process_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

// serverConfig creates a minimal server config for testing.
// Uses the test binary as a fake MCP server via TestHelperProcess.
func serverConfig(id string) config.ServerConfig {
	return config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "cat", // Simple command that reads stdin and exits when closed
		Args:    []string{},
	}
}

func TestConcurrentStopAll(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	supervisor := process.NewSupervisor(bus)

	// We can't easily start real MCP servers in this test, but we can
	// verify the supervisor handles concurrent stop calls without races.
	// The -race flag will detect any data races.

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervisor.StopAll()
		}()
	}

	wg.Wait()

	// Should complete without races
	count := supervisor.RunningCount()
	if count != 0 {
		t.Errorf("expected 0 running servers after StopAll, got %d", count)
	}
}

func TestPIDTracking_Creation(t *testing.T) {
	testutil.SetupTestHome(t)

	// Creating a supervisor should not panic or error with isolated $HOME
	bus := events.NewBus()
	supervisor := process.NewSupervisor(bus)

	if supervisor == nil {
		t.Fatal("NewSupervisor returned nil")
	}

	// Should have no running servers initially
	if supervisor.RunningCount() != 0 {
		t.Errorf("expected 0 running servers, got %d", supervisor.RunningCount())
	}

	ids := supervisor.RunningServers()
	if len(ids) != 0 {
		t.Errorf("expected no running server IDs, got %v", ids)
	}
}

func TestSupervisor_StartNonExistentCommand(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	supervisor := process.NewSupervisor(bus)

	// Collect events
	var statusEvents []events.StatusChangedEvent
	var mu sync.Mutex
	bus.Subscribe(func(e events.Event) {
		if se, ok := e.(events.StatusChangedEvent); ok {
			mu.Lock()
			statusEvents = append(statusEvents, se)
			mu.Unlock()
		}
	})

	cfg := config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: "/nonexistent/command/that/does/not/exist",
		Args:    []string{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := supervisor.Start(ctx, "nonexistent", cfg)

	// Should fail - either command not found or MCP init failure
	if err == nil {
		// If it somehow started, make sure we stop it
		supervisor.Stop("nonexistent")
		t.Fatal("expected error starting nonexistent command")
	}

	t.Logf("Got expected error: %v", err)
}

func TestSupervisor_StopNonExistent(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	supervisor := process.NewSupervisor(bus)

	err := supervisor.Stop("nonexistent-id")
	if err == nil {
		t.Error("expected error stopping non-existent server")
	}
}

func TestSupervisor_GetNonExistent(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	supervisor := process.NewSupervisor(bus)

	handle := supervisor.Get("nonexistent-id")
	if handle != nil {
		t.Error("expected nil for non-existent server")
	}
}
