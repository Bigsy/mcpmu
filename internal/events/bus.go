package events

import (
	"log"
	"sync"
)

// Handler is a function that handles events.
type Handler func(Event)

// defaultQueueSize is the per-subscriber queue depth. A subscriber that falls
// this far behind has its newest events dropped (and logged) rather than
// stalling publishers or other subscribers.
const defaultQueueSize = 100

// Bus is a goroutine-safe event bus. Each subscriber has its own bounded queue
// drained by its own goroutine, so a slow handler delays only itself, events
// are delivered to each subscriber in publish order, and Publish never blocks.
//
// Guarantees:
//   - Publish never blocks; on a full subscriber queue the event is dropped for
//     that subscriber only and a warning is logged.
//   - After the unsubscribe function returns, the handler is not called again
//     (an in-flight call is allowed to finish first).
//   - Close is idempotent, stops every subscriber, and waits for in-flight
//     handler calls to finish. Publish and Subscribe after Close are no-ops.
//
// The unsubscribe function must not be called from inside the handler it
// unsubscribes: it waits for that handler's goroutine to exit.
type Bus struct {
	mu        sync.Mutex
	subs      map[uint64]*subscriber
	nextID    uint64
	closed    bool
	closeOnce sync.Once
	queueSize int
}

// subscriber is one registered handler with its queue and worker goroutine.
type subscriber struct {
	handler  Handler
	queue    chan Event
	stop     chan struct{} // closed by Unsubscribe/Close to end the worker
	finished chan struct{} // closed by the worker when it has exited
	stopOnce sync.Once
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return newBusWithQueueSize(defaultQueueSize)
}

func newBusWithQueueSize(n int) *Bus {
	return &Bus{subs: make(map[uint64]*subscriber), queueSize: n}
}

// Subscribe registers a handler to receive events and returns a function that
// removes it. The returned function is safe to call more than once.
func (b *Bus) Subscribe(h Handler) func() {
	sub := &subscriber{
		handler:  h,
		queue:    make(chan Event, b.queueSize),
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(sub.finished)
		return func() {}
	}
	id := b.nextID
	b.nextID++
	b.subs[id] = sub
	b.mu.Unlock()

	go sub.run()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if b.subs[id] == sub {
				delete(b.subs, id)
			}
			b.mu.Unlock()
			sub.shutdown()
		})
	}
}

// run drains the queue into the handler until stopped. Events still queued
// when stop closes are discarded: the subscriber asked to stop hearing them.
func (s *subscriber) run() {
	defer close(s.finished)
	for {
		select {
		case <-s.stop:
			return
		case e := <-s.queue:
			// Re-check stop so an event that raced with Unsubscribe is not
			// delivered after the caller was told delivery had ended.
			select {
			case <-s.stop:
				return
			default:
			}
			s.handler(e)
		}
	}
}

// shutdown stops the worker and waits for it to exit. Unsubscribe and Close
// may both reach here for the same subscriber.
func (s *subscriber) shutdown() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.finished
}

// Publish delivers an event to every subscriber. It never blocks: a subscriber
// whose queue is full loses this event (logged) while the others still get it.
func (b *Bus) Publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, sub := range b.subs {
		select {
		case sub.queue <- event:
		default:
			log.Printf("Warning: event bus subscriber queue full, dropping event type=%s server=%s", event.Type(), event.ServerID())
		}
	}
}

// Close shuts down the event bus. It is safe to call more than once and
// returns only after every subscriber's worker has exited.
func (b *Bus) Close() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		subs := b.subs
		b.subs = make(map[uint64]*subscriber)
		b.mu.Unlock()

		for _, sub := range subs {
			sub.shutdown()
		}
	})
}
