package events

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testEvent is a simple event implementation for testing.
type testEvent struct {
	id        int
	serverID  string
	timestamp time.Time
}

func (e testEvent) Type() EventType      { return EventStatusChanged }
func (e testEvent) ServerID() string     { return e.serverID }
func (e testEvent) Timestamp() time.Time { return e.timestamp }

func newTestEvent(id int, serverID string) testEvent {
	return testEvent{id: id, serverID: serverID, timestamp: time.Now()}
}

func TestBus_BasicPublishSubscribe(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	received := make(chan Event, 1)
	bus.Subscribe(func(e Event) {
		received <- e
	})

	event := newTestEvent(1, "server-1")
	bus.Publish(event)

	select {
	case got := <-received:
		te := got.(testEvent)
		if te.id != 1 {
			t.Errorf("expected event id 1, got %d", te.id)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	var count atomic.Int32
	var wg sync.WaitGroup

	// Add 3 subscribers
	for range 3 {
		wg.Add(1)
		bus.Subscribe(func(e Event) {
			count.Add(1)
			wg.Done()
		})
	}

	bus.Publish(newTestEvent(1, "server-1"))

	// Wait for all subscribers to receive the event
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if count.Load() != 3 {
			t.Errorf("expected 3 handlers called, got %d", count.Load())
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout: only %d handlers called", count.Load())
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	var count atomic.Int32

	unsubscribe := bus.Subscribe(func(e Event) {
		count.Add(1)
	})

	// First event should be received
	bus.Publish(newTestEvent(1, "server-1"))
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Fatalf("expected count 1 before unsubscribe, got %d", count.Load())
	}

	// Unsubscribe
	unsubscribe()

	// Second event should not be received
	bus.Publish(newTestEvent(2, "server-1"))
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 1 {
		t.Errorf("expected count 1 after unsubscribe, got %d", count.Load())
	}
}

// blockingHandler returns a handler that blocks until release is closed, plus
// a channel that is closed once the handler has been entered.
func blockingHandler(release <-chan struct{}) (Handler, <-chan struct{}) {
	entered := make(chan struct{})
	var once sync.Once
	return func(Event) {
		once.Do(func() { close(entered) })
		<-release
	}, entered
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(original) })
	return &buf
}

func TestBus_QueueOverflow_DropsEventsForThatSubscriber(t *testing.T) {
	bus := newBusWithQueueSize(10)
	defer bus.Close()
	logBuf := captureLog(t)

	release := make(chan struct{})
	defer close(release)
	slow, entered := blockingHandler(release)
	bus.Subscribe(slow)

	// First event is consumed by the worker and blocks in the handler.
	bus.Publish(newTestEvent(0, "server-1"))
	<-entered

	// Next 10 fill the queue with no drops.
	for i := 1; i <= 10; i++ {
		bus.Publish(newTestEvent(i, "server-1"))
	}
	if strings.Contains(logBuf.String(), "dropping event") {
		t.Fatal("unexpected drop before the queue was full")
	}

	// One more overflows.
	bus.Publish(newTestEvent(11, "server-overflow"))
	out := logBuf.String()
	if !strings.Contains(out, "dropping event") {
		t.Error("expected 'dropping event' in log output")
	}
	if !strings.Contains(out, "type=") || !strings.Contains(out, "server-overflow") {
		t.Errorf("drop message should carry type and server id, got %q", out)
	}

	// Ten more overflow too: exactly 11 drops in total.
	for i := 12; i < 22; i++ {
		bus.Publish(newTestEvent(i, "server-1"))
	}
	if got := strings.Count(logBuf.String(), "dropping event"); got != 11 {
		t.Errorf("expected 11 dropped events, got %d", got)
	}
}

func TestBus_SlowSubscriberDoesNotStarveFastOne(t *testing.T) {
	bus := newBusWithQueueSize(50)
	defer bus.Close()
	captureLog(t)

	release := make(chan struct{})
	defer close(release)
	slow, entered := blockingHandler(release)
	bus.Subscribe(slow)

	var fastCount atomic.Int32
	done := make(chan struct{})
	bus.Subscribe(func(Event) {
		if fastCount.Add(1) == 50 {
			close(done)
		}
	})

	bus.Publish(newTestEvent(0, "server-1"))
	<-entered
	for i := 1; i < 50; i++ {
		bus.Publish(newTestEvent(i, "server-1"))
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("fast subscriber got %d of 50 events while slow one was blocked", fastCount.Load())
	}
}

func TestBus_NoDeliveryAfterUnsubscribeReturns(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	release := make(chan struct{})
	slow, entered := blockingHandler(release)
	var calls atomic.Int32
	unsub := bus.Subscribe(func(e Event) {
		calls.Add(1)
		slow(e)
	})

	// Handler is mid-call for event 0; events 1..4 sit in its queue.
	bus.Publish(newTestEvent(0, "server-1"))
	<-entered
	for i := 1; i < 5; i++ {
		bus.Publish(newTestEvent(i, "server-1"))
	}

	unsubReturned := make(chan struct{})
	go func() {
		unsub()
		close(unsubReturned)
	}()

	// Unsubscribe must wait for the in-flight call rather than return early.
	select {
	case <-unsubReturned:
		t.Fatal("unsubscribe returned while the handler was still running")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-unsubReturned:
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not return after the handler finished")
	}

	// Queued events are discarded, and later publishes are not delivered.
	bus.Publish(newTestEvent(5, "server-1"))
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Errorf("handler called %d times; want exactly the 1 in-flight call", got)
	}

	// Calling the unsubscribe function again is harmless.
	unsub()
}

func TestBus_CloseIsIdempotentAndWaitsForHandlers(t *testing.T) {
	bus := NewBus()

	release := make(chan struct{})
	slow, entered := blockingHandler(release)
	bus.Subscribe(slow)
	bus.Publish(newTestEvent(0, "server-1"))
	<-entered

	closed := make(chan struct{})
	go func() {
		bus.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a handler was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after the handler finished")
	}

	// Second Close must not panic; Publish/Subscribe after Close are no-ops.
	bus.Close()
	bus.Publish(newTestEvent(1, "server-1"))
	unsub := bus.Subscribe(func(Event) { t.Error("handler called on closed bus") })
	bus.Publish(newTestEvent(2, "server-1"))
	unsub()
}

// TestBus_ConcurrentSubscribeUnsubscribePublishClose is a -race stress test:
// subscribers churn while publishers hammer the bus, then Close lands under
// everyone. Nothing may panic, and no handler may run after its unsubscribe.
func TestBus_ConcurrentSubscribeUnsubscribePublishClose(t *testing.T) {
	bus := NewBus()
	captureLog(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				bus.Publish(newTestEvent(i, "server-1"))
				i++
			}
		}()
	}

	var afterUnsub atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				var gone atomic.Bool
				unsub := bus.Subscribe(func(Event) {
					if gone.Load() {
						afterUnsub.Add(1)
					}
				})
				time.Sleep(time.Millisecond)
				unsub()
				gone.Store(true)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	bus.Close()
	close(stop)
	wg.Wait()

	if n := afterUnsub.Load(); n != 0 {
		t.Errorf("%d handler calls landed after unsubscribe returned", n)
	}
}

func TestBus_EventOrdering(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	const numEvents = 50
	received := make([]int, 0, numEvents)
	var mu sync.Mutex
	done := make(chan struct{})

	bus.Subscribe(func(e Event) {
		te := e.(testEvent)
		mu.Lock()
		received = append(received, te.id)
		if len(received) == numEvents {
			close(done)
		}
		mu.Unlock()
	})

	// Publish events in order
	for i := range numEvents {
		bus.Publish(newTestEvent(i, "server-1"))
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		mu.Lock()
		t.Fatalf("timeout: only received %d of %d events", len(received), numEvents)
		mu.Unlock()
	}

	// Verify ordering
	mu.Lock()
	defer mu.Unlock()
	for i, id := range received {
		if id != i {
			t.Errorf("event %d out of order: expected id %d, got %d", i, i, id)
		}
	}
}

func TestBus_EventOrderingNearCapacity(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	// Publish close to buffer capacity (100 events)
	const numEvents = 95
	received := make([]int, 0, numEvents)
	var mu sync.Mutex
	done := make(chan struct{})

	bus.Subscribe(func(e Event) {
		te := e.(testEvent)
		mu.Lock()
		received = append(received, te.id)
		if len(received) == numEvents {
			close(done)
		}
		mu.Unlock()
	})

	// Publish events rapidly
	for i := range numEvents {
		bus.Publish(newTestEvent(i, "server-1"))
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		mu.Lock()
		t.Fatalf("timeout: only received %d of %d events", len(received), numEvents)
		mu.Unlock()
	}

	// Verify all events received in order
	mu.Lock()
	defer mu.Unlock()
	for i, id := range received {
		if id != i {
			t.Errorf("event %d out of order: expected id %d, got %d", i, i, id)
		}
	}
}

func TestBus_ConcurrentPublish(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	// Use fewer events than buffer capacity (100) to avoid drops
	// The point of this test is concurrent safety, not overflow behavior
	const numGoroutines = 5
	const eventsPerGoroutine = 10
	totalEvents := numGoroutines * eventsPerGoroutine

	var receivedCount atomic.Int32
	done := make(chan struct{})

	bus.Subscribe(func(e Event) {
		if receivedCount.Add(1) == int32(totalEvents) {
			close(done)
		}
	})

	// Publish from multiple goroutines concurrently
	var wg sync.WaitGroup
	for g := range numGoroutines {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := range eventsPerGoroutine {
				bus.Publish(newTestEvent(goroutineID*100+i, fmt.Sprintf("server-%d", goroutineID)))
			}
		}(g)
	}

	wg.Wait()

	select {
	case <-done:
		if receivedCount.Load() != int32(totalEvents) {
			t.Errorf("expected %d events, got %d", totalEvents, receivedCount.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout: only received %d of %d events", receivedCount.Load(), totalEvents)
	}
}

func TestBus_SlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	bus := NewBus()
	defer bus.Close()

	// Subscribe with a slow handler
	bus.Subscribe(func(e Event) {
		time.Sleep(100 * time.Millisecond)
	})

	// Publishing should not block (returns immediately due to buffered channel)
	start := time.Now()
	for i := range 10 {
		bus.Publish(newTestEvent(i, "server-1"))
	}
	elapsed := time.Since(start)

	// Publishing 10 events should be nearly instant (< 10ms), not 1 second
	if elapsed > 50*time.Millisecond {
		t.Errorf("publishing took too long (%v), suggests blocking", elapsed)
	}
}

func TestBus_Close(t *testing.T) {
	bus := NewBus()

	// Subscribe to verify bus was working
	received := make(chan Event, 1)
	bus.Subscribe(func(e Event) {
		received <- e
	})

	// Publish an event
	bus.Publish(newTestEvent(1, "server-1"))

	select {
	case <-received:
		// Good, event received
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event before close")
	}

	// Close the bus
	bus.Close()

	// Give time for goroutine to exit
	time.Sleep(50 * time.Millisecond)

	// Publish after close should not panic
	// (it will just put in channel which is never consumed, or drop if full)
	bus.Publish(newTestEvent(2, "server-1"))
}
