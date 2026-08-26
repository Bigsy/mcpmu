package server

import (
	"errors"
	"log"
	"sync"

	"github.com/Bigsy/mcpmu/internal/process"
)

const notificationSubscriberBuffer = 64

var errNotificationBroadcasterClosed = errors.New("notification broadcaster is closed")

// NotificationSink receives generation-tagged upstream notifications after
// Core has performed any required catalog refresh.
type NotificationSink interface {
	OnUpstreamNotification(process.UpstreamNotification)
}

// NotificationBroadcaster is the non-blocking Core-owned handoff between MCP
// client reader goroutines and downstream sessions.
type NotificationBroadcaster interface {
	OnUpstreamNotification(process.UpstreamNotification)
	Publish(process.UpstreamNotification)
	Subscribe(NotificationSink) (unsubscribe func(), err error)
	Close()
}

type notificationBroadcaster struct {
	mu          sync.Mutex
	subscribers map[uint64]*notificationSubscriber
	nextID      uint64
	queue       []process.UpstreamNotification
	wake        chan struct{}
	closed      chan struct{}
	closeOnce   sync.Once
	isClosed    bool
	process     func(process.UpstreamNotification) bool
}

type notificationSubscriber struct {
	sink NotificationSink
	ch   chan process.UpstreamNotification
	done chan struct{} // closed when the delivery loop has exited
}

func newNotificationBroadcaster(processNotification func(process.UpstreamNotification) bool) *notificationBroadcaster {
	b := &notificationBroadcaster{
		subscribers: make(map[uint64]*notificationSubscriber),
		wake:        make(chan struct{}, 1),
		closed:      make(chan struct{}),
		process:     processNotification,
	}
	goSafe("notification broadcaster", b.run)
	return b
}

func (b *notificationBroadcaster) Subscribe(sink NotificationSink) (func(), error) {
	sub := &notificationSubscriber{
		sink: sink,
		ch:   make(chan process.UpstreamNotification, notificationSubscriberBuffer),
		done: make(chan struct{}),
	}
	b.mu.Lock()
	if b.isClosed {
		b.mu.Unlock()
		return nil, errNotificationBroadcasterClosed
	}
	b.nextID++
	id := b.nextID
	b.subscribers[id] = sub
	b.mu.Unlock()
	goSafe("notification subscriber", func() {
		defer close(sub.done)
		for notification := range sub.ch {
			sub.deliver(notification)
		}
	})

	// Unsubscribe guarantees no delivery to sink after it returns: it closes
	// the queue and then waits for the delivery loop to finish whatever it
	// was mid-way through. That lets Session.Run unsubscribe and then wait on
	// handlersWG without a late OnUpstreamNotification racing an Add against
	// the Wait.
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if current := b.subscribers[id]; current != nil {
				delete(b.subscribers, id)
				close(current.ch)
			}
			b.mu.Unlock()
			<-sub.done
		})
	}, nil
}

// OnUpstreamNotification only queues; it never performs an upstream request
// on the MCP client's single response-reader goroutine.
func (b *notificationBroadcaster) OnUpstreamNotification(notification process.UpstreamNotification) {
	b.enqueue(notification)
}

// Publish queues a Core-originated notification through the same ordered
// worker path used for upstream notifications.
func (b *notificationBroadcaster) Publish(notification process.UpstreamNotification) {
	b.enqueue(notification)
}

func (b *notificationBroadcaster) enqueue(notification process.UpstreamNotification) {
	b.mu.Lock()
	if b.isClosed {
		b.mu.Unlock()
		return
	}
	b.queue = append(b.queue, notification.Clone())
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *notificationBroadcaster) run() {
	for {
		select {
		case <-b.wake:
			for {
				b.mu.Lock()
				if len(b.queue) == 0 {
					b.mu.Unlock()
					break
				}
				notification := b.queue[0]
				b.queue[0] = process.UpstreamNotification{}
				b.queue = b.queue[1:]
				b.mu.Unlock()

				b.dispatch(notification)
			}
		case <-b.closed:
			return
		}
	}
}

// dispatch runs Core's pre-processing and the fan-out for one notification
// with panic recovery, so one bad notification cannot end the worker loop.
func (b *notificationBroadcaster) dispatch(notification process.UpstreamNotification) {
	defer recoverPanic("notification broadcaster: "+notification.Method, nil)
	if b.process != nil && !b.process(notification) {
		return
	}
	b.fanout(notification)
}

// deliver hands one notification to the sink with panic recovery so a
// misbehaving sink does not end its own delivery loop.
func (s *notificationSubscriber) deliver(notification process.UpstreamNotification) {
	defer recoverPanic("notification sink: "+notification.Method, nil)
	s.sink.OnUpstreamNotification(notification)
}

func (b *notificationBroadcaster) fanout(notification process.UpstreamNotification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, subscriber := range b.subscribers {
		select {
		case subscriber.ch <- notification.Clone():
		default:
			delete(b.subscribers, id)
			close(subscriber.ch)
			log.Printf("Disconnecting notification subscriber %d: outbound queue is full", id)
		}
	}
}

func (b *notificationBroadcaster) Close() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.isClosed = true
		close(b.closed)
		for id, subscriber := range b.subscribers {
			delete(b.subscribers, id)
			close(subscriber.ch)
		}
		b.queue = nil
		b.mu.Unlock()
	})
}
