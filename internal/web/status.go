package web

import (
	"maps"
	"sync"

	"github.com/Bigsy/mcpmu/internal/events"
)

// StatusTracker subscribes to the event bus and maintains last-known
// ServerStatus per server. This mirrors what the TUI does with its
// serverStatuses map — without it, status is lost when a handle is removed.
type StatusTracker struct {
	mu       sync.RWMutex
	statuses map[string]events.ServerStatus
	tools    map[string][]events.McpTool

	unsub func() // event bus unsubscribe
}

// NewStatusTracker creates a tracker and subscribes it to the bus.
func NewStatusTracker(bus *events.Bus) *StatusTracker {
	st := &StatusTracker{
		statuses: make(map[string]events.ServerStatus),
		tools:    make(map[string][]events.McpTool),
	}
	st.unsub = bus.Subscribe(st.handleEvent)
	return st
}

// Close unsubscribes from the event bus.
func (st *StatusTracker) Close() {
	if st.unsub != nil {
		st.unsub()
	}
}

// Get returns the last-known status for a server.
func (st *StatusTracker) Get(serverID string) (events.ServerStatus, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	s, ok := st.statuses[serverID]
	return s, ok
}

// All returns a snapshot of all last-known statuses.
func (st *StatusTracker) All() map[string]events.ServerStatus {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make(map[string]events.ServerStatus, len(st.statuses))
	maps.Copy(out, st.statuses)
	return out
}

// Tools returns the last-known tools for a server.
func (st *StatusTracker) Tools(serverID string) ([]events.McpTool, bool) {
	st.mu.RLock()
	defer st.mu.RUnlock()
	t, ok := st.tools[serverID]
	return t, ok
}

func (st *StatusTracker) handleEvent(e events.Event) {
	st.mu.Lock()
	defer st.mu.Unlock()

	switch evt := e.(type) {
	case events.StatusChangedEvent:
		st.statuses[evt.ServerID()] = evt.Status
	case events.ToolsUpdatedEvent:
		st.tools[evt.ServerID()] = evt.Tools
		// Keep ToolCount in sync so callers reading Status.ToolCount
		// see the live count even if no StatusChangedEvent followed.
		if s, ok := st.statuses[evt.ServerID()]; ok {
			s.ToolCount = len(evt.Tools)
			st.statuses[evt.ServerID()] = s
		}
	}
}
