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
	for i := 0; i < 3; i++ {
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

func TestBus_ChannelOverflow_DropsEvents(t *testing.T) {
	// Create a bus but don't process events (don't subscribe or block the consumer)
	bus := &Bus{
		handlers: make([]Handler, 0),
		ch:       make(chan Event, 100), // Same buffer size as production
		done:     make(chan struct{}),
	}
	// Note: We intentionally don't start the run() goroutine to simulate a blocked consumer

	// Capture log output
	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(originalOutput)

	// Fill the buffer completely
	for i := 0; i < 100; i++ {
		bus.Publish(newTestEvent(i, "server-1"))
	}

	// Verify no drops yet
	if strings.Contains(logBuf.String(), "dropping event") {
		t.Error("unexpected drop before buffer full")
	}

	// This one should be dropped
	bus.Publish(newTestEvent(100, "server-overflow"))

	// Check that drop was logged
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "dropping event") {
		t.Error("expected 'dropping event' in log output")
	}
	if !strings.Contains(logOutput, "server-overflow") {
		t.Error("expected server ID in log output")
	}

	bus.Close()
}

func TestBus_ChannelOverflow_CountsDrops(t *testing.T) {
	// Create a bus with a small buffer to make testing easier
	bus := &Bus{
		handlers: make([]Handler, 0),
		ch:       make(chan Event, 10),
		done:     make(chan struct{}),
	}

	// Capture log output
	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(originalOutput)

	// Publish more events than the buffer can hold
	for i := 0; i < 20; i++ {
		bus.Publish(newTestEvent(i, fmt.Sprintf("server-%d", i)))
	}

	// Count how many "dropping event" messages were logged
	logOutput := logBuf.String()
	dropCount := strings.Count(logOutput, "dropping event")

	// Should have dropped 10 events (20 published - 10 buffer capacity)
	if dropCount != 10 {
		t.Errorf("expected 10 dropped events, got %d", dropCount)
	}

	bus.Close()
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
	for i := 0; i < numEvents; i++ {
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
	for i := 0; i < numEvents; i++ {
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
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
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
	for i := 0; i < 10; i++ {
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

func TestBus_DropMessageIncludesEventType(t *testing.T) {
	bus := &Bus{
		handlers: make([]Handler, 0),
		ch:       make(chan Event, 1),
		done:     make(chan struct{}),
	}

	var logBuf bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(originalOutput)

	// Fill buffer
	bus.Publish(newTestEvent(1, "server-1"))
	// This should be dropped
	bus.Publish(NewErrorEvent("error-server", nil, "test error"))

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "type=") {
		t.Error("expected event type in drop message")
	}
	if !strings.Contains(logOutput, "error-server") {
		t.Error("expected server ID in drop message")
	}

	bus.Close()
}
