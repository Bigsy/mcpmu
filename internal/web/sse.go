package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Bigsy/mcpmu/internal/events"
)

// handleSSELogs streams server logs and status changes as SSE events.
// On connect, it flushes historical logs from the handle's buffer,
// then streams live events from the bus.
func (s *Server) handleSSELogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Verify server exists in config
	if _, ok := s.cfg.GetServer(name); !ok {
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	// Send initial status
	if st, ok := s.status.Get(name); ok {
		writeSSEStatus(w, st)
		flusher.Flush()
	}

	// Flush historical logs from the handle's buffer
	if handle := s.supervisor.Get(name); handle != nil {
		for _, line := range handle.Logs() {
			writeSSELog(w, name, line, time.Time{})
		}
		flusher.Flush()
	}

	// Subscribe to live events with a per-subscriber buffered channel
	ch := make(chan events.Event, 64)
	unsub := s.bus.Subscribe(func(e events.Event) {
		if e.ServerID() != name {
			return
		}
		select {
		case ch <- e:
		default:
			// Slow subscriber — drop event rather than blocking the bus
		}
	})
	defer unsub()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-ch:
			switch e := evt.(type) {
			case events.LogReceivedEvent:
				writeSSELog(w, name, e.Line, e.Timestamp())
			case events.StatusChangedEvent:
				writeSSEStatus(w, e.Status)
			}
			flusher.Flush()
		}
	}
}

func writeSSELog(w http.ResponseWriter, serverID, line string, ts time.Time) {
	tsStr := ""
	if !ts.IsZero() {
		tsStr = ts.Format("15:04:05")
	}
	data := map[string]string{
		"line":      line,
		"server":    serverID,
		"timestamp": tsStr,
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
}

func writeSSEStatus(w http.ResponseWriter, st events.ServerStatus) {
	b, err := json.Marshal(st)
	if err != nil {
		log.Printf("sse: marshal status: %v", err)
		return
	}
	_, _ = fmt.Fprintf(w, "event: status\ndata: %s\n\n", b)
}
