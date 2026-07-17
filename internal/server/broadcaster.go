package server

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/Bigsy/mcpmu/internal/mcp"
)

// NotificationBroadcaster is the Core-owned entry point for notifications
// emitted by upstream MCP servers. Phase 1 deliberately supports one Session;
// the interface lets the daemon phases replace the implementation without
// coupling Supervisor to a particular downstream connection.
type NotificationBroadcaster interface {
	mcp.NotificationSink
	Subscribe(mcp.NotificationSink) (unsubscribe func(), err error)
}

var errNotificationSubscriberExists = errors.New("notification subscriber already registered")

type singleSubscriberBroadcaster struct {
	mu         sync.RWMutex
	subscriber mcp.NotificationSink
}

func (b *singleSubscriberBroadcaster) Subscribe(sink mcp.NotificationSink) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscriber != nil {
		return nil, errNotificationSubscriberExists
	}
	b.subscriber = sink

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.subscriber = nil
		})
	}, nil
}

func (b *singleSubscriberBroadcaster) OnUpstreamNotification(serverName, method string, params json.RawMessage) {
	b.mu.RLock()
	subscriber := b.subscriber
	b.mu.RUnlock()
	if subscriber != nil {
		subscriber.OnUpstreamNotification(serverName, method, params)
	}
}
