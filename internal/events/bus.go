package events

import (
	"sync"
)

// Handler is a function that handles events.
type Handler func(Event)

// Bus is a goroutine-safe event bus for dispatching events.
type Bus struct {
	mu       sync.RWMutex
	handlers []Handler
	ch       chan Event
	done     chan struct{}
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	b := &Bus{
		handlers: make([]Handler, 0),
		ch:       make(chan Event, 100), // Buffer to prevent blocking publishers
		done:     make(chan struct{}),
	}
	go b.run()
	return b
}

// run processes events from the channel.
func (b *Bus) run() {
	for {
		select {
		case event := <-b.ch:
			b.dispatch(event)
		case <-b.done:
			return
		}
	}
}

// dispatch sends an event to all registered handlers.
func (b *Bus) dispatch(event Event) {
	b.mu.RLock()
	handlers := make([]Handler, len(b.handlers))
	copy(handlers, b.handlers)
	b.mu.RUnlock()

	for _, h := range handlers {
		if h != nil {
			h(event)
		}
	}
}

// Subscribe registers a handler to receive events.
// Returns an unsubscribe function.
func (b *Bus) Subscribe(h Handler) func() {
	b.mu.Lock()
	b.handlers = append(b.handlers, h)
	idx := len(b.handlers) - 1
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		// Mark as nil rather than removing to preserve indices
		if idx < len(b.handlers) {
			b.handlers[idx] = nil
		}
	}
}

// Publish sends an event to all subscribers.
// This is non-blocking due to the buffered channel.
func (b *Bus) Publish(event Event) {
	select {
	case b.ch <- event:
	default:
		// Channel full, drop event (should be rare with buffer)
	}
}

// Close shuts down the event bus.
func (b *Bus) Close() {
	close(b.done)
}

// Channel returns a channel that receives all events.
// Useful for Bubble Tea integration.
func (b *Bus) Channel() <-chan Event {
	return b.ch
}
