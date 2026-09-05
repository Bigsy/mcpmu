package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/Bigsy/mcpmu/internal/config"
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
	report := func(err error) {
		if s.setReloadFailed(true) {
			log.Printf("%v", err)
			s.configBcast.Broadcast()
		}
	}
	err := config.Watch(ctx, s.configPath, 0, func(newCfg *config.Config) {
		s.cfgMu.Lock()
		changed := !configEqual(s.cfg, newCfg)
		if changed {
			s.cfg = newCfg
		}
		s.cfgMu.Unlock()
		recovered := s.setReloadFailed(false)
		if changed || recovered {
			s.configBcast.Broadcast()
		}
	}, report)
	if err != nil {
		report(err)
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

// Reload warnings have their own lock: page rendering may already hold cfgMu.
func (s *Server) setReloadFailed(failed bool) bool {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	changed := s.reloadFailed != failed
	s.reloadFailed = failed
	return changed
}
func (s *Server) configWarning() string {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if s.reloadFailed {
		return "Config reload failed; using the previous valid configuration"
	}
	return ""
}
