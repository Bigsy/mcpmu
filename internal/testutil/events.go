// Package testutil provides common test utilities.
package testutil

import (
	"sync"
	"time"

	"github.com/hedworth/mcp-studio-go/internal/events"
)

// EventCollector is a thread-safe event collector for test assertions.
// Subscribe it to an event bus and then query collected events.
type EventCollector struct {
	mu     sync.Mutex
	events []events.Event
	states map[string][]events.RuntimeState
	tools  map[string][]events.McpTool
	cond   *sync.Cond
}

// NewEventCollector creates a new EventCollector.
func NewEventCollector() *EventCollector {
	ec := &EventCollector{
		events: make([]events.Event, 0),
		states: make(map[string][]events.RuntimeState),
		tools:  make(map[string][]events.McpTool),
	}
	ec.cond = sync.NewCond(&ec.mu)
	return ec
}

// Handler returns a function suitable for bus.Subscribe().
func (c *EventCollector) Handler(e events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.events = append(c.events, e)

	switch evt := e.(type) {
	case events.StatusChangedEvent:
		c.states[evt.ServerID()] = append(c.states[evt.ServerID()], evt.NewState)
	case events.ToolsUpdatedEvent:
		c.tools[evt.ServerID()] = evt.Tools
	}

	// Signal any waiters
	c.cond.Broadcast()
}

// Events returns all collected events.
func (c *EventCollector) Events() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]events.Event, len(c.events))
	copy(result, c.events)
	return result
}

// StatesFor returns all states observed for a server ID.
func (c *EventCollector) StatesFor(serverID string) []events.RuntimeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]events.RuntimeState, len(c.states[serverID]))
	copy(result, c.states[serverID])
	return result
}

// LastStateFor returns the most recent state for a server ID.
// Returns StateIdle if no states have been observed.
func (c *EventCollector) LastStateFor(serverID string) events.RuntimeState {
	c.mu.Lock()
	defer c.mu.Unlock()
	states := c.states[serverID]
	if len(states) == 0 {
		return events.StateIdle
	}
	return states[len(states)-1]
}

// ToolsFor returns the most recent tools for a server ID.
// Returns nil if no tools have been observed.
func (c *EventCollector) ToolsFor(serverID string) []events.McpTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tools := c.tools[serverID]
	if tools == nil {
		return nil
	}
	result := make([]events.McpTool, len(tools))
	copy(result, tools)
	return result
}

// WaitForState blocks until the specified state is observed or timeout expires.
// Returns true if the state was observed, false on timeout.
func (c *EventCollector) WaitForState(serverID string, state events.RuntimeState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		// Check if state already observed
		for _, s := range c.states[serverID] {
			if s == state {
				return true
			}
		}

		// Check timeout
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}

		// Wait with timeout using a goroutine
		done := make(chan struct{})
		go func() {
			time.Sleep(remaining)
			c.cond.Broadcast()
			close(done)
		}()

		c.cond.Wait()

		// Check if timeout goroutine finished
		select {
		case <-done:
			// Timeout expired, check one more time then return
			for _, s := range c.states[serverID] {
				if s == state {
					return true
				}
			}
			return false
		default:
			// Continue waiting
		}
	}
}

// WaitForAnyState blocks until any of the specified states is observed or timeout expires.
// Returns the observed state and true, or StateIdle and false on timeout.
func (c *EventCollector) WaitForAnyState(serverID string, states []events.RuntimeState, timeout time.Duration) (events.RuntimeState, bool) {
	deadline := time.Now().Add(timeout)

	c.mu.Lock()
	defer c.mu.Unlock()

	stateSet := make(map[events.RuntimeState]bool)
	for _, s := range states {
		stateSet[s] = true
	}

	for {
		// Check if any target state already observed
		for _, s := range c.states[serverID] {
			if stateSet[s] {
				return s, true
			}
		}

		// Check timeout
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return events.StateIdle, false
		}

		// Wait with timeout
		done := make(chan struct{})
		go func() {
			time.Sleep(remaining)
			c.cond.Broadcast()
			close(done)
		}()

		c.cond.Wait()

		select {
		case <-done:
			// Timeout expired
			for _, s := range c.states[serverID] {
				if stateSet[s] {
					return s, true
				}
			}
			return events.StateIdle, false
		default:
		}
	}
}

// Clear resets the collector's state.
func (c *EventCollector) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = make([]events.Event, 0)
	c.states = make(map[string][]events.RuntimeState)
	c.tools = make(map[string][]events.McpTool)
}

// StatesContainSequence checks if the observed states contain the expected sequence in order.
// The expected sequence doesn't need to be contiguous - there can be other states in between.
func StatesContainSequence(observed, expected []events.RuntimeState) bool {
	if len(expected) == 0 {
		return true
	}
	if len(observed) == 0 {
		return false
	}

	expectedIdx := 0
	for _, state := range observed {
		if state == expected[expectedIdx] {
			expectedIdx++
			if expectedIdx == len(expected) {
				return true
			}
		}
	}
	return false
}
