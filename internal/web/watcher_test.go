package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

func TestConfigBroadcaster(t *testing.T) {
	b := newConfigBroadcaster()

	ch1, unsub1 := b.Subscribe()
	ch2, unsub2 := b.Subscribe()
	defer unsub1()
	defer unsub2()

	b.Broadcast()

	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Fatal("subscriber 1 did not receive broadcast")
	}
	select {
	case <-ch2:
	case <-time.After(time.Second):
		t.Fatal("subscriber 2 did not receive broadcast")
	}

	// After unsubscribe, should not receive
	unsub1()
	b.Broadcast()

	select {
	case <-ch1:
		t.Fatal("unsubscribed channel should not receive")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestConfigBroadcasterNonBlocking(t *testing.T) {
	b := newConfigBroadcaster()

	ch, unsub := b.Subscribe()
	defer unsub()

	// Buffer is 1, so first broadcast fills it
	b.Broadcast()
	// Second broadcast should not block (drops silently)
	b.Broadcast()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("should have received at least one broadcast")
	}
}

func TestSSEConfigEndpoint(t *testing.T) {
	srv := newTestServer(t)

	// Use a real HTTP test server for SSE (httptest.NewRecorder doesn't support Flusher)
	ts := httptest.NewServer(srv.httpServer.Handler)
	defer ts.Close()

	// Connect to SSE endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE connect: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", ct)
	}

	// Read lines in a goroutine to avoid blocking
	lineCh := make(chan string, 32)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()

	// Read initial comment — confirms the SSE handler is subscribed and flushing.
	// SSE sends ": connected\n\n" which the scanner sees as two lines: the comment and a blank.
	for i := range 2 {
		select {
		case line := <-lineCh:
			if i == 0 && !strings.HasPrefix(line, ": connected") {
				t.Fatalf("expected connected comment, got %q", line)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for initial comment")
		}
	}

	// Small delay to ensure the SSE goroutine is back in its select loop
	time.Sleep(50 * time.Millisecond)

	// Broadcast a config change
	srv.configBcast.Broadcast()

	// Read the SSE event lines
	var lines []string
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("connection closed, got lines: %v", lines)
			}
			if line == "" {
				goto done // end of event (blank line delimiter)
			}
			lines = append(lines, line)
		case <-ctx.Done():
			t.Fatalf("timed out reading event, got lines: %v", lines)
		}
	}
done:

	if len(lines) < 2 {
		t.Fatalf("expected event+data lines, got %v", lines)
	}
	if lines[0] != "event: config-changed" {
		t.Fatalf("expected 'event: config-changed', got %q", lines[0])
	}
	if lines[1] != "data: {}" {
		t.Fatalf("expected 'data: {}', got %q", lines[1])
	}
}

func TestWatchConfigDetectsExternalWrite(t *testing.T) {
	srv := newTestServer(t)

	ctx := t.Context()

	// Subscribe before starting watcher
	ch, unsub := srv.configBcast.Subscribe()
	defer unsub()

	// Start watcher
	go srv.WatchConfig(ctx)

	// Give watcher time to set up
	time.Sleep(100 * time.Millisecond)

	// Simulate an external config write
	newCfg := config.NewConfig()
	enabled := true
	_ = newCfg.AddServer("external-server", config.ServerConfig{
		Command: "echo",
		Args:    []string{"external"},
		Enabled: &enabled,
	})
	if err := config.SaveTo(newCfg, srv.configPath); err != nil {
		t.Fatalf("save external config: %v", err)
	}

	// Wait for the broadcast (debounce is 150ms)
	select {
	case <-ch:
		// Verify config was updated
		if _, ok := srv.cfg.GetServer("external-server"); !ok {
			t.Fatal("config should contain 'external-server' after reload")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for config change broadcast")
	}
}

func TestWatchConfigSuppressesSelfWrite(t *testing.T) {
	srv := newTestServer(t)

	ctx := t.Context()

	ch, unsub := srv.configBcast.Subscribe()
	defer unsub()

	go srv.WatchConfig(ctx)
	time.Sleep(100 * time.Millisecond)

	// Use mutateConfig (simulates a web UI write) — should NOT trigger SSE
	err := srv.mutateConfig(func(cfg *config.Config) error {
		enabled := true
		return cfg.AddServer("self-write-server", config.ServerConfig{
			Command: "echo",
			Args:    []string{"self"},
			Enabled: &enabled,
		})
	})
	if err != nil {
		t.Fatalf("mutateConfig: %v", err)
	}

	// Wait past the debounce — should NOT receive broadcast
	select {
	case <-ch:
		t.Fatal("self-write should not trigger config-changed broadcast")
	case <-time.After(500 * time.Millisecond):
		// expected — no broadcast for self-writes
	}
}

func TestWatchConfigExternalAfterSelfWrite(t *testing.T) {
	srv := newTestServer(t)

	ctx := t.Context()

	ch, unsub := srv.configBcast.Subscribe()
	defer unsub()

	go srv.WatchConfig(ctx)
	time.Sleep(100 * time.Millisecond)

	// Self-write (web UI save)
	err := srv.mutateConfig(func(cfg *config.Config) error {
		enabled := true
		return cfg.AddServer("web-server", config.ServerConfig{
			Command: "echo",
			Args:    []string{"web"},
			Enabled: &enabled,
		})
	})
	if err != nil {
		t.Fatalf("mutateConfig: %v", err)
	}

	// Wait for the self-write's debounce to complete and be consumed
	time.Sleep(500 * time.Millisecond)

	// Drain any spurious notification (there shouldn't be one)
	select {
	case <-ch:
	default:
	}

	// Now simulate an external CLI write — this should NOT be suppressed
	newCfg := config.NewConfig()
	enabled := true
	_ = newCfg.AddServer("cli-server", config.ServerConfig{
		Command: "echo",
		Args:    []string{"cli"},
		Enabled: &enabled,
	})
	if err := config.SaveTo(newCfg, srv.configPath); err != nil {
		t.Fatalf("save external config: %v", err)
	}

	select {
	case <-ch:
		if _, ok := srv.cfg.GetServer("cli-server"); !ok {
			t.Fatal("config should contain 'cli-server' after external reload")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("external write after self-write was incorrectly suppressed")
	}
}

func TestWatchConfigCoalescedSelfAndExternal(t *testing.T) {
	srv := newTestServer(t)

	ctx := t.Context()

	ch, unsub := srv.configBcast.Subscribe()
	defer unsub()

	go srv.WatchConfig(ctx)
	time.Sleep(100 * time.Millisecond)

	// Self-write adds "web-server"
	err := srv.mutateConfig(func(cfg *config.Config) error {
		enabled := true
		return cfg.AddServer("web-server", config.ServerConfig{
			Command: "echo",
			Args:    []string{"web"},
			Enabled: &enabled,
		})
	})
	if err != nil {
		t.Fatalf("mutateConfig: %v", err)
	}

	// Immediately write an external config change (within the debounce window).
	// This adds "cli-server" on top of what's already on disk (which includes "web-server").
	diskCfg, err := config.LoadFrom(srv.configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	enabled := true
	_ = diskCfg.AddServer("cli-server", config.ServerConfig{
		Command: "echo",
		Args:    []string{"cli"},
		Enabled: &enabled,
	})
	if err := config.SaveTo(diskCfg, srv.configPath); err != nil {
		t.Fatalf("save external config: %v", err)
	}

	// The debounce should detect that disk differs from memory (memory has
	// web-server but not cli-server) and broadcast.
	select {
	case <-ch:
		if _, ok := srv.cfg.GetServer("cli-server"); !ok {
			t.Fatal("config should contain 'cli-server' after coalesced reload")
		}
		if _, ok := srv.cfg.GetServer("web-server"); !ok {
			t.Fatal("config should still contain 'web-server' after coalesced reload")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("coalesced self+external write: external change was incorrectly suppressed")
	}
}
