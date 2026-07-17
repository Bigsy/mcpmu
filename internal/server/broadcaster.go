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
}

func newNotificationBroadcaster(processNotification func(process.UpstreamNotification) bool) *notificationBroadcaster {
	b := &notificationBroadcaster{
		subscribers: make(map[uint64]*notificationSubscriber),
		wake:        make(chan struct{}, 1),
		closed:      make(chan struct{}),
		process:     processNotification,
	}
	go b.run()
	return b
}

func (b *notificationBroadcaster) Subscribe(sink NotificationSink) (func(), error) {
	sub := &notificationSubscriber{sink: sink, ch: make(chan process.UpstreamNotification, notificationSubscriberBuffer)}
	b.mu.Lock()
	if b.isClosed {
		b.mu.Unlock()
		return nil, errNotificationBroadcasterClosed
	}
	b.nextID++
	id := b.nextID
	b.subscribers[id] = sub
	b.mu.Unlock()
	go func() {
		for notification := range sub.ch {
			sub.sink.OnUpstreamNotification(notification)
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if current := b.subscribers[id]; current != nil {
				delete(b.subscribers, id)
				close(current.ch)
			}
			b.mu.Unlock()
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

				if b.process != nil && !b.process(notification) {
					continue
				}
				b.fanout(notification)
			}
		case <-b.closed:
			return
		}
	}
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
