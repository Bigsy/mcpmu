package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch observes saves (including atomic replacement) of path. Its single loop
// owns loading, debouncing and synchronous delivery. Callbacks must return when
// ctx is cancelled; none can run after Watch returns. A busy callback delays
// loading but never drops the final filesystem state. No caller channel is closed.
// Errors deliberately omit parser details, which can contain config secrets.
func Watch(ctx context.Context, path string, delay time.Duration, update func(*Config), report func(error)) error {
	resolved, err := ResolvePath(path)
	if err != nil {
		return errors.New("resolve config watch path failed")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return errors.New("resolve absolute config watch path failed")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return errors.New("create config watcher failed")
	}
	defer func() { _ = watcher.Close() }()
	if err := watcher.Add(filepath.Dir(resolved)); err != nil {
		return errors.New("watch config directory failed")
	}
	if delay <= 0 {
		delay = 150 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	timer.Stop()
	defer timer.Stop()
	var tick <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("config watcher closed")
			}
			if event.Name != resolved || event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}
			timer.Reset(delay)
			tick = timer.C
		case _, ok := <-watcher.Errors:
			if !ok {
				return errors.New("config watcher closed")
			}
			if ctx.Err() == nil && report != nil {
				report(errors.New("config watcher event error"))
			}
			timer.Reset(delay)
			tick = timer.C
		case <-tick:
			tick = nil
			if ctx.Err() != nil {
				return nil
			}
			_, err := os.Stat(resolved)
			var cfg *Config
			if err == nil {
				cfg, err = LoadFrom(resolved)
			}
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				if report != nil {
					report(errors.New("config reload failed; using the previous valid configuration"))
				}
				continue
			}
			update(cfg)
		}
	}
}
