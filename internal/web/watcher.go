package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/fsnotify/fsnotify"
)

// configBroadcaster manages SSE subscribers for config-change notifications.
type configBroadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newConfigBroadcaster() *configBroadcaster {
	return &configBroadcaster{
		subs: make(map[chan struct{}]struct{}),
	}
}

// Subscribe returns a channel that receives a value when the config changes,
// and an unsubscribe function.
func (b *configBroadcaster) Subscribe() (ch chan struct{}, unsub func()) {
	ch = make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Broadcast sends a notification to all subscribers (non-blocking).
func (b *configBroadcaster) Broadcast() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
			// Subscriber already has a pending notification
		}
	}
}

// WatchConfig watches the config file for external changes and updates the
// in-memory config. Connected SSE clients are notified so the UI can refresh.
// It watches the parent directory (not the file) to handle atomic renames.
func (s *Server) WatchConfig(ctx context.Context) {
	if s.configPath == "" {
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed to create config watcher: %v", err)
		return
	}
	defer func() { _ = watcher.Close() }()

	// Expand ~ and resolve symlinks so we watch the real file's directory.
	// LoadFrom/SaveTo both expand ~ internally, and SaveTo resolves symlinks
	// before writing (atomic rename targets the real path), so the watcher
	// must watch the fully resolved path to see those events.
	watchPath := s.configPath
	if strings.HasPrefix(watchPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			watchPath = filepath.Join(home, watchPath[2:])
		}
	}
	if resolved, err := filepath.EvalSymlinks(watchPath); err == nil {
		watchPath = resolved
	}

	dir := filepath.Dir(watchPath)
	filename := filepath.Base(watchPath)

	if err := watcher.Add(dir); err != nil {
		log.Printf("Failed to watch config directory %s: %v", dir, err)
		return
	}

	log.Printf("Watching config file: %s (resolved: %s)", s.configPath, watchPath)

	const debounceDelay = 150 * time.Millisecond
	var debounceTimer *time.Timer
	var debounceMu sync.Mutex

	triggerReload := func() {
		debounceMu.Lock()
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceDelay, func() {
			newCfg, err := config.LoadFrom(s.configPath)
			if err != nil {
				log.Printf("Failed to load config after change: %v (keeping current config)", err)
				return
			}

			// Compare disk config with in-memory config. If they match,
			// this was a self-write (mutateConfig already updated s.cfg)
			// and no broadcast is needed. If they differ, something
			// external changed — update and notify SSE clients.
			s.cfgMu.Lock()
			changed := !configEqual(s.cfg, newCfg)
			if changed {
				s.cfg = newCfg
			}
			s.cfgMu.Unlock()

			if !changed {
				log.Printf("Config file changed but matches in-memory state, skipping broadcast")
				return
			}

			s.configBcast.Broadcast()
			log.Printf("Config reloaded: %d servers, %d namespaces",
				len(newCfg.Servers), len(newCfg.Namespaces))
		})
		debounceMu.Unlock()
	}

	for {
		select {
		case <-ctx.Done():
			debounceMu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceMu.Unlock()
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			// Filter for our target file
			if filepath.Base(event.Name) != filename {
				continue
			}

			// React to write, create, rename, or remove events.
			// Atomic writes show up as rename/create depending on OS/editor.
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				log.Printf("Config file event: %s (%s)", event.Name, event.Op)
				triggerReload()
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Config watcher error: %v", err)
		}
	}
}

// configEqual returns true if two configs serialize to the same JSON.
// Used by the watcher to detect whether a file change introduced new content
// or was a self-write that's already reflected in memory.
func configEqual(a, b *config.Config) bool {
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false // if we can't compare, assume different
	}
	return bytes.Equal(aj, bj)
}

// handleSSEConfig streams config-change notifications as SSE events.
// Browsers connect to this endpoint and reload the page when the config
// file is modified externally (e.g., by CLI commands).
func (s *Server) handleSSEConfig(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	ch, unsub := s.configBcast.Subscribe()
	defer unsub()

	// Send initial comment to confirm the connection is live
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			_, _ = fmt.Fprintf(w, "event: config-changed\ndata: {}\n\n")
			flusher.Flush()
		}
	}
}
