package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchRecoveryAndBusyConsumer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := NewConfig()
	if err := SaveTo(cfg, path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan *Config)
	failures := make(chan error, 10)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, time.Millisecond, func(c *Config) {
			select {
			case updates <- c:
			case <-ctx.Done():
			}
		}, func(e error) { failures <- e })
	}()
	// Repeated saves synchronize with watcher setup without a readiness sleep.
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
ready:
	for {
		select {
		case <-updates:
			break ready
		case <-ticker.C:
			if err := SaveTo(cfg, path); err != nil {
				t.Fatal(err)
			}
		case <-deadline:
			t.Fatal("watcher did not start")
		}
	}
	if err := os.WriteFile(path, []byte(`{"secret":"private",`), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failures:
		if err.Error() != "config reload failed; using the previous valid configuration" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing failure")
	}
	cfg.DefaultNamespace = "final"
	// Direct writes and an atomic replacement converge even while delivery blocks.
	if err := SaveTo(cfg, path); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		if got.DefaultNamespace != "final" {
			t.Fatal("stale update")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing recovery")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not stop")
	}
	select {
	case <-updates:
		t.Fatal("delivery after return")
	default:
	}
}

func TestWatchSetupError(t *testing.T) {
	err := Watch(context.Background(), filepath.Join(t.TempDir(), "missing", "config.json"), 0, func(*Config) { t.Fatal("unexpected update") }, nil)
	if err == nil {
		t.Fatal("expected setup error")
	}
}

func TestWatchPathAndBusyDelivery(t *testing.T) {
	for _, mode := range []string{"relative", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "config.json")
			path := target
			cfg := NewConfig()
			if err := SaveTo(cfg, target); err != nil {
				t.Fatal(err)
			}
			if mode == "relative" {
				t.Chdir(dir)
				path = "config.json"
			} else {
				path = filepath.Join(dir, "link.json")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			updates := make(chan *Config)
			entered := make(chan struct{}, 10)
			done := make(chan error, 1)
			go func() {
				done <- Watch(ctx, path, time.Millisecond, func(c *Config) {
					entered <- struct{}{}
					select {
					case updates <- c:
					case <-ctx.Done():
					}
				}, nil)
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.After(3 * time.Second)
		ready:
			for {
				select {
				case <-updates:
					<-entered
					break ready
				case <-ticker.C:
					if err := SaveTo(cfg, path); err != nil {
						t.Fatal(err)
					}
				case <-deadline:
					t.Fatal("watcher setup timed out")
				}
			}
			// Queue a callback, then change the disk again while that callback has no
			// receiver. Once delivered, the loop must eventually observe the final save.
			cfg.DefaultNamespace = "older"
			if err := SaveTo(cfg, path); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("older callback did not enter")
			}
			cfg.DefaultNamespace = "latest"
			if err := SaveTo(cfg, path); err != nil {
				t.Fatal(err)
			}
			deadline = time.After(3 * time.Second)
		latest:
			for {
				select {
				case got := <-updates:
					if got.DefaultNamespace == "latest" {
						break latest
					}
				case <-deadline:
					t.Fatal("latest value was lost")
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "unrelated"), []byte("ignored"), 0600); err != nil {
				t.Fatal(err)
			}
			// Negative observation is deliberately bounded; no filesystem event for
			// this unrelated file may cause another delivery.
			select {
			case <-updates:
				t.Fatal("unrelated file triggered reload")
			case <-time.After(50 * time.Millisecond):
			}
			if err := SaveTo(cfg, path); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending debounce did not cancel")
			}
		})
	}
}
