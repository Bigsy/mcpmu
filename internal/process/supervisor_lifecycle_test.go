//go:build integration

package process_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/process"
	"github.com/Bigsy/mcpmu/internal/testutil"
)

// TestHelperProcess is the entry point for the fake MCP server subprocess.
// When tests spawn subprocesses using os.Args[0] with -test.run=TestHelperProcess,
// this function runs the fake server instead of the actual tests.
func TestHelperProcess(t *testing.T) {
	mcptest.RunHelperProcess(t)
}

// fakeServerConfig creates a config.ServerConfig that spawns a fake MCP server.
// It uses the test re-exec pattern: the test binary is executed with
// -test.run=TestHelperProcess and environment variables to configure the fake server.
func fakeServerConfig(t *testing.T, id string, fakeCfg mcptest.FakeServerConfig) config.ServerConfig {
	t.Helper()

	cfgJSON, err := json.Marshal(fakeCfg)
	if err != nil {
		t.Fatalf("marshal fake config: %v", err)
	}

	return config.ServerConfig{
		Kind:    config.ServerKindStdio,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"FAKE_MCP_CFG":           string(cfgJSON),
		},
	}
}

// lifecycleTestCase defines a table-driven test case for supervisor lifecycle tests.
type lifecycleTestCase struct {
	name           string
	fakeCfg        mcptest.FakeServerConfig
	ctxTimeout     time.Duration // 0 means no timeout
	expectState    events.RuntimeState
	expectTools    int  // -1 means don't check
	expectError    bool // expect Start() to return error
	stateSequence  []events.RuntimeState
	mustObserve    events.RuntimeState // if non-zero, this state must appear somewhere in observed states
	skipFinalCheck bool                // skip final state check (for cases where final state is non-deterministic)
}

func TestSupervisor_Lifecycle(t *testing.T) {
	testutil.SetupTestHome(t)

	testCases := []lifecycleTestCase{
		{
			name:          "happy_path",
			fakeCfg:       mcptest.DefaultConfig(),
			expectState:   events.StateRunning,
			expectTools:   2,
			expectError:   false,
			stateSequence: []events.RuntimeState{events.StateStarting, events.StateRunning},
		},
		{
			name:          "init_retry_fail_first",
			fakeCfg:       mcptest.FailOnAttemptConfig("initialize", 1),
			expectState:   events.StateRunning,
			expectTools:   1,
			expectError:   false,
			stateSequence: []events.RuntimeState{events.StateStarting, events.StateRunning},
		},
		{
			name:        "init_timeout",
			fakeCfg:     mcptest.SlowInitConfig(5 * time.Second),
			ctxTimeout:  200 * time.Millisecond,
			expectState: events.StateError,
			expectTools: -1,
			expectError: true,
		},
		// NOTE: tools_list_timeout test removed - the supervisor has a hardcoded 30s timeout
		// for tools/list, making it impractical to test without waiting 30 seconds or
		// making the timeout configurable. Using a context timeout instead causes the
		// subprocess to be killed, which tests cancellation not timeout behavior.
		{
			name:        "crash_on_init",
			fakeCfg:     mcptest.CrashOnInitConfig(1),
			expectState: events.StateError,
			expectTools: -1,
			expectError: true,
		},
		{
			name:           "malformed_response",
			fakeCfg:        mcptest.MalformedResponseConfig(),
			expectTools:    -1,
			expectError:    true,
			mustObserve:    events.StateError, // error must be observed, but final state may be stopped
			skipFinalCheck: true,              // final state is non-deterministic (error or stopped)
		},
		{
			name:          "notification_before_response",
			fakeCfg:       mcptest.NotificationBeforeResponseConfig(),
			expectState:   events.StateRunning,
			expectTools:   1,
			expectError:   false,
			stateSequence: []events.RuntimeState{events.StateStarting, events.StateRunning},
		},
		{
			name:          "mismatched_id_first",
			fakeCfg:       mcptest.MismatchedIDConfig(),
			expectState:   events.StateRunning,
			expectTools:   1,
			expectError:   false,
			stateSequence: []events.RuntimeState{events.StateStarting, events.StateRunning},
		},
		{
			name:          "empty_tools",
			fakeCfg:       mcptest.EmptyToolsConfig(),
			expectState:   events.StateRunning,
			expectTools:   0,
			expectError:   false,
			stateSequence: []events.RuntimeState{events.StateStarting, events.StateRunning},
		},
		{
			name:          "large_tool_list",
			fakeCfg:       mcptest.LargeToolListConfig(100),
			expectState:   events.StateRunning,
			expectTools:   100,
			expectError:   false,
			stateSequence: []events.RuntimeState{events.StateStarting, events.StateRunning},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create fresh bus and collector for each test
			bus := events.NewBus()
			defer bus.Close()

			collector := testutil.NewEventCollector()
			bus.Subscribe(collector.Handler)

			supervisor := process.NewSupervisor(bus)
			defer supervisor.StopAll()

			serverID := "test-" + tc.name
			srvCfg := fakeServerConfig(t, serverID, tc.fakeCfg)

			// Set up context with timeout if specified
			ctx := context.Background()
			if tc.ctxTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.ctxTimeout)
				defer cancel()
			}

			// Start the server
			handle, err := supervisor.Start(ctx, serverID, srvCfg)

			// Check error expectation
			if tc.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			// Allow time for events to propagate
			time.Sleep(100 * time.Millisecond)

			// Check final state (unless skipped)
			if !tc.skipFinalCheck {
				finalState := collector.LastStateFor(serverID)
				if finalState != tc.expectState {
					t.Errorf("expected state %v, got %v", tc.expectState, finalState)
					t.Logf("observed states: %v", collector.StatesFor(serverID))
				}
			}

			// Check that a specific state was observed (if specified)
			if tc.mustObserve != 0 {
				observed := collector.StatesFor(serverID)
				found := false
				for _, s := range observed {
					if s == tc.mustObserve {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected to observe state %v, but only saw: %v", tc.mustObserve, observed)
				}
			}

			// Check tools if expected
			if tc.expectTools >= 0 && handle != nil {
				tools := collector.ToolsFor(serverID)
				if len(tools) != tc.expectTools {
					t.Errorf("expected %d tools, got %d", tc.expectTools, len(tools))
				}
			}

			// Check state sequence if specified
			if len(tc.stateSequence) > 0 {
				observed := collector.StatesFor(serverID)
				if !testutil.StatesContainSequence(observed, tc.stateSequence) {
					t.Errorf("state sequence not found: expected %v in %v", tc.stateSequence, observed)
				}
			}

			// Cleanup: stop the server if running
			if handle != nil && handle.IsRunning() {
				if err := supervisor.Stop(serverID); err != nil {
					t.Logf("warning: stop failed: %v", err)
				}
			}
		})
	}
}

func TestSupervisor_CrashMidSession(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	defer bus.Close()

	collector := testutil.NewEventCollector()
	bus.Subscribe(collector.Handler)

	supervisor := process.NewSupervisor(bus)
	defer supervisor.StopAll()

	// Server crashes on 3rd request (after init and tools/list)
	// Since we call Initialize then ListTools, it crashes during or after ListTools
	serverID := "crash-mid"
	srvCfg := fakeServerConfig(t, serverID, mcptest.CrashOnNthRequestConfig(3, 1))

	_, err := supervisor.Start(context.Background(), serverID, srvCfg)
	// The crash happens during tools/list, so start may or may not return error
	// depending on timing
	if err != nil {
		t.Logf("start returned error (expected due to crash): %v", err)
	}

	// Wait for crash state
	time.Sleep(200 * time.Millisecond)

	// Check that we saw starting at minimum
	states := collector.StatesFor(serverID)
	if len(states) == 0 {
		t.Error("expected at least one state transition")
	}
	t.Logf("observed states: %v", states)
}

func TestSupervisor_ConcurrentStartStop(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	defer bus.Close()

	supervisor := process.NewSupervisor(bus)
	defer supervisor.StopAll()

	// Start multiple servers concurrently
	const numServers = 5
	var wg sync.WaitGroup
	errors := make(chan error, numServers*2)

	for i := 0; i < numServers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			serverID := "concurrent-" + string(rune('a'+idx))
			srvCfg := fakeServerConfig(t, serverID, mcptest.DefaultConfig())

			_, err := supervisor.Start(context.Background(), serverID, srvCfg)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("concurrent start error: %v", err)
	}

	// Verify all servers are running
	running := supervisor.RunningCount()
	if running != numServers {
		t.Errorf("expected %d running servers, got %d", numServers, running)
	}

	// Stop all concurrently
	supervisor.StopAll()

	// Allow cleanup
	time.Sleep(100 * time.Millisecond)

	// Verify all stopped
	running = supervisor.RunningCount()
	if running != 0 {
		t.Errorf("expected 0 running servers after StopAll, got %d", running)
	}
}

func TestSupervisor_ConcurrentStopAll_WithServers(t *testing.T) {
	testutil.SetupTestHome(t)

	bus := events.NewBus()
	defer bus.Close()

	supervisor := process.NewSupervisor(bus)

	// Start a few servers
	const numServers = 3
	for i := 0; i < numServers; i++ {
		serverID := "stopall-" + string(rune('a'+i))
		srvCfg := fakeServerConfig(t, serverID, mcptest.DefaultConfig())
		_, err := supervisor.Start(context.Background(), serverID, srvCfg)
		if err != nil {
			t.Fatalf("start server %s: %v", serverID, err)
		}
	}

	// Call StopAll from multiple goroutines concurrently
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervisor.StopAll()
		}()
	}
	wg.Wait()

	// Verify all stopped
	running := supervisor.RunningCount()
	if running != 0 {
		t.Errorf("expected 0 running servers, got %d", running)
	}
}

func TestEventBus_ConcurrentOperations(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()

	var wg sync.WaitGroup

	// Subscribe handlers concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unsub := bus.Subscribe(func(e events.Event) {
				// Just receive events
			})
			time.Sleep(10 * time.Millisecond)
			unsub()
		}()
	}

	// Publish events concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				bus.Publish(events.NewStatusChangedEvent(
					"test-server",
					events.StateIdle,
					events.StateRunning,
					events.ServerStatus{ID: "test-server"},
				))
			}
		}(i)
	}

	wg.Wait()
}

func TestEventCollector_WaitForState(t *testing.T) {
	collector := testutil.NewEventCollector()

	// Start a goroutine that will publish state after a delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		collector.Handler(events.NewStatusChangedEvent(
			"test-server",
			events.StateIdle,
			events.StateRunning,
			events.ServerStatus{ID: "test-server"},
		))
	}()

	// Wait for the state
	if !collector.WaitForState("test-server", events.StateRunning, 500*time.Millisecond) {
		t.Error("WaitForState timed out")
	}

	// Test timeout
	if collector.WaitForState("test-server", events.StateCrashed, 50*time.Millisecond) {
		t.Error("WaitForState should have timed out")
	}
}

func TestStatesContainSequence(t *testing.T) {
	tests := []struct {
		name     string
		observed []events.RuntimeState
		expected []events.RuntimeState
		want     bool
	}{
		{
			name:     "exact match",
			observed: []events.RuntimeState{events.StateStarting, events.StateRunning},
			expected: []events.RuntimeState{events.StateStarting, events.StateRunning},
			want:     true,
		},
		{
			name:     "sequence with extras",
			observed: []events.RuntimeState{events.StateIdle, events.StateStarting, events.StateRunning, events.StateStopping},
			expected: []events.RuntimeState{events.StateStarting, events.StateRunning},
			want:     true,
		},
		{
			name:     "missing element",
			observed: []events.RuntimeState{events.StateStarting, events.StateStopped},
			expected: []events.RuntimeState{events.StateStarting, events.StateRunning},
			want:     false,
		},
		{
			name:     "wrong order",
			observed: []events.RuntimeState{events.StateRunning, events.StateStarting},
			expected: []events.RuntimeState{events.StateStarting, events.StateRunning},
			want:     false,
		},
		{
			name:     "empty expected",
			observed: []events.RuntimeState{events.StateStarting},
			expected: []events.RuntimeState{},
			want:     true,
		},
		{
			name:     "empty observed",
			observed: []events.RuntimeState{},
			expected: []events.RuntimeState{events.StateStarting},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := testutil.StatesContainSequence(tt.observed, tt.expected)
			if got != tt.want {
				t.Errorf("StatesContainSequence() = %v, want %v", got, tt.want)
			}
		})
	}
}
