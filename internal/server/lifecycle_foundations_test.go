package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/process"
)

func phase2AServerConfig(t *testing.T, fake mcptest.FakeServerConfig) config.ServerConfig {
	t.Helper()
	encoded, err := json.Marshal(fake)
	if err != nil {
		t.Fatalf("marshal fake server config: %v", err)
	}
	return config.ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"FAKE_MCP_CFG":           string(encoded),
		},
	}
}

func TestCoreConcurrentAcquireCollapsesToOneInstance(t *testing.T) {
	t.Parallel()
	srv := phase2AServerConfig(t, mcptest.DefaultConfig())
	core, err := NewCore(Options{
		Config:        &config.Config{Servers: map[string]config.ServerConfig{"shared": srv}},
		PIDTrackerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	const callers = 12
	start := make(chan struct{})
	handles := make(chan *process.Handle, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			handle, _, acquireErr := core.getOrStartHandle(context.Background(), "shared")
			handles <- handle
			errs <- acquireErr
		})
	}
	close(start)
	wg.Wait()
	close(handles)
	close(errs)

	for acquireErr := range errs {
		if acquireErr != nil {
			t.Fatalf("concurrent acquire: %v", acquireErr)
		}
	}
	var first *process.Handle
	for handle := range handles {
		if first == nil {
			first = handle
			continue
		}
		if handle != first {
			t.Fatal("concurrent acquire returned more than one process handle")
		}
	}
	if got := core.supervisor.RunningCount(); got != 1 {
		t.Fatalf("running instances = %d, want 1", got)
	}
}

func TestCoreAcquireHonorsConfiguredStartupTimeout(t *testing.T) {
	t.Parallel()
	srv := phase2AServerConfig(t, mcptest.SlowInitConfig(5*time.Second))
	srv.StartupTimeoutSec = 1
	core, err := NewCore(Options{
		Config:        &config.Config{Servers: map[string]config.ServerConfig{"slow": srv}},
		PIDTrackerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	started := time.Now()
	_, _, err = core.getOrStartHandle(context.Background(), "slow")
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire error = %v, want deadline exceeded", err)
	}
	if elapsed < 800*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatalf("configured one-second startup timeout took %v", elapsed)
	}
}

func TestCoreAcquireRejectsConfigChangedWhileWaitingForLifecycle(t *testing.T) {
	t.Parallel()
	oldServer := phase2AServerConfig(t, mcptest.DefaultConfig())
	core, err := NewCore(Options{
		Config:        &config.Config{Servers: map[string]config.ServerConfig{"changing": oldServer}},
		PIDTrackerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)

	locked := make(chan struct{})
	release := make(chan struct{})
	blockerDone := make(chan struct{})
	go func() {
		defer close(blockerDone)
		_, _ = core.supervisor.StartInstance(
			context.Background(),
			process.SharedInstanceID("changing"),
			oldServer,
			func() error {
				close(locked)
				<-release
				return errors.New("test lifecycle blocker")
			},
		)
	}()
	<-locked

	result := make(chan error, 1)
	go func() {
		_, _, acquireErr := core.getOrStartHandle(context.Background(), "changing")
		result <- acquireErr
	}()
	// The helper snapshots config before waiting on the held lifecycle lock.
	time.Sleep(50 * time.Millisecond)
	changed := oldServer
	changed.Args = append([]string(nil), oldServer.Args...)
	changed.Args = append(changed.Args, "changed")
	core.replaceConfig(&config.Config{Servers: map[string]config.ServerConfig{"changing": changed}})
	close(release)
	<-blockerDone

	err = <-result
	if err == nil || !strings.Contains(err.Error(), "config changed during reload") {
		t.Fatalf("acquire error = %v, want config-generation rejection", err)
	}
	if handle := core.supervisor.Get("changing"); handle != nil {
		t.Fatalf("stale config unexpectedly started handle %p", handle)
	}
}

func TestCoreRestartRejectsConfigChangedWhileWaitingForLifecycle(t *testing.T) {
	t.Parallel()
	oldServer := phase2AServerConfig(t, mcptest.DefaultConfig())
	core, err := NewCore(Options{
		Config:        &config.Config{Servers: map[string]config.ServerConfig{"changing": oldServer}},
		PIDTrackerDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewCore: %v", err)
	}
	t.Cleanup(core.Close)
	id := process.SharedInstanceID("changing")

	locked := make(chan struct{})
	release := make(chan struct{})
	blockerDone := make(chan struct{})
	go func() {
		defer close(blockerDone)
		_, _ = core.supervisor.StartInstance(context.Background(), id, oldServer, func() error {
			close(locked)
			<-release
			return errors.New("test lifecycle blocker")
		})
	}()
	<-locked

	result := make(chan error, 1)
	go func() {
		_, restartErr := core.restartInstance(context.Background(), id, "changing")
		result <- restartErr
	}()
	time.Sleep(50 * time.Millisecond)
	changed := oldServer
	changed.Args = append(append([]string(nil), oldServer.Args...), "changed")
	core.replaceConfig(&config.Config{Servers: map[string]config.ServerConfig{"changing": changed}})
	close(release)
	<-blockerDone

	err = <-result
	if err == nil || !strings.Contains(err.Error(), "config changed during reload") {
		t.Fatalf("restart error = %v, want config-generation rejection", err)
	}
	if handle := core.supervisor.Get("changing"); handle != nil {
		t.Fatalf("stale restart unexpectedly started handle %p", handle)
	}
}
