package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/mcptest"
	"github.com/Bigsy/mcpmu/internal/mcptest/fakeserver"
)

// stalledListSession starts an mcpmu session over pipes whose single fake
// upstream parks every resources/list for holdFor. It initializes the session,
// issues resources/list, and waits until the upstream has received the request
// — RequestLogPath records methods on arrival, before the fake server applies
// its per-method delay — so the caller knows the handler is parked inside
// upstream I/O, which is exactly where the old global resourceStateMu.RLock
// used to block hot reload and Core.Close daemon-wide.
func stalledListSession(t *testing.T, holdFor time.Duration) *Session {
	t.Helper()

	requestLog := filepath.Join(t.TempDir(), "upstream-methods.log")
	fakeCfg, err := json.Marshal(mcptest.FakeServerConfig{
		Resources:      []fakeserver.Resource{{URI: "file:///held.txt", Name: "held"}},
		Delays:         map[string]time.Duration{"resources/list": holdFor},
		RequestLogPath: requestLog,
	})
	if err != nil {
		t.Fatalf("marshal fake server config: %v", err)
	}

	enabled := true
	cfg := &config.Config{
		SchemaVersion: 1,
		Servers: map[string]config.ServerConfig{
			"res-srv": {
				Enabled: &enabled,
				Command: os.Args[0],
				Args:    []string{"-test.run=TestHelperProcess", "--"},
				Env: map[string]string{
					"GO_WANT_HELPER_PROCESS": "1",
					"FAKE_MCP_CFG":           string(fakeCfg),
				},
			},
		},
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var stdout bytes.Buffer

	srv, err := New(Options{
		SessionOptions: SessionOptions{
			ExposeResources: true,
		},
		Config:        cfg,
		PIDTrackerDir: t.TempDir(),
		Stdin:         stdinR,
		Stdout:        &stdout,
		ServerName:    "mcpmu-test",
		ServerVersion: "1.0.0",
		LogLevel:      "error",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = srv.Run(ctx)
	}()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}

	if _, err := stdinW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"test","version":"1.0"}}}` + "\n")); err != nil {
		t.Fatalf("write initialize: %v", err)
	}
	waitFor("session initialized", func() bool {
		srv.mu.RLock()
		defer srv.mu.RUnlock()
		return srv.initialized
	})

	if _, err := stdinW.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"resources/list"}` + "\n")); err != nil {
		t.Fatalf("write resources/list: %v", err)
	}
	waitFor("upstream to receive resources/list", func() bool {
		data, err := os.ReadFile(requestLog)
		return err == nil && strings.Contains(string(data), "resources/list")
	})

	t.Cleanup(func() {
		// Kill any surviving upstream first so the stalled handler errors out
		// and Run's handlersWG drain does not wait out the full hold.
		srv.Core.Close()
		_ = stdinR.Close()
		_ = stdinW.Close()
		cancel()
		select {
		case <-runDone:
		case <-time.After(20 * time.Second):
			t.Error("Run did not exit after teardown")
		}
	})
	return srv
}

// TestReloadCompletesWhileResourcesListStalled pins finding 9: a hot reload
// must complete while a resources/* handler is stalled in upstream I/O. With
// the old global read lock held across the whole handler, applyReload waited
// behind the held resources/list for its entire upstream timeout; now nothing
// the handler does blocks the reloader.
func TestReloadCompletesWhileResourcesListStalled(t *testing.T) {
	t.Parallel()
	srv := stalledListSession(t, 30*time.Second)

	newCfg := &config.Config{
		SchemaVersion: 1,
		Servers:       map[string]config.ServerConfig{},
	}

	reloadDone := make(chan struct{})
	go func() {
		defer close(reloadDone)
		srv.applyReload(context.Background(), newCfg)
	}()
	select {
	case <-reloadDone:
	case <-time.After(3 * time.Second):
		t.Fatal("applyReload blocked behind a stalled resources/list — global lock regression")
	}
}

// TestCoreCloseCompletesWhileResourcesListStalled is the Core.Close sibling of
// the reload property: shutdown tears down while a handler is parked in
// upstream I/O instead of stalling for the length of the stuck RPC.
func TestCoreCloseCompletesWhileResourcesListStalled(t *testing.T) {
	t.Parallel()
	srv := stalledListSession(t, 30*time.Second)

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		srv.Core.Close()
	}()
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Core.Close blocked behind a stalled resources/list — global lock regression")
	}
}
