package server

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/Bigsy/mcpmu/internal/process"
)

func (s *Session) pendingServers(serverNames []string) []string {
	shared, private := s.splitServersBySharing(serverNames)
	pending := s.currentAggregator().PendingServers(shared)
	return append(pending, s.privateAggregatorSnapshot().PendingServers(private)...)
}

// sendNotification sends a JSON-RPC notification (no ID, no response expected).
func (s *Session) sendNotification(method string) {
	s.sendNotificationWithParams(method, nil)
}

// sendNotificationWithParams sends a JSON-RPC notification with optional
// params (pass nil to omit the field entirely).
func (s *Session) sendNotificationWithParams(method string, params any) {
	type notifMsg struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}
	s.send(notifMsg{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

// OnUpstreamNotification implements mcp.NotificationSink. It runs on the
// upstream client's reader goroutine — must not block on stdout writes, so
// any downstream emission happens in a goroutine.
func (s *Session) OnUpstreamNotification(notification process.UpstreamNotification) {
	if !s.ownsInstance(notification.Instance) {
		return
	}
	serverName := notification.Instance.Server
	switch notification.Method {
	case "notifications/resources/updated":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(notification.Params, &p); err != nil || p.URI == "" {
			if DebugLogging {
				log.Printf("resources/updated: malformed params from %s: %v", serverName, err)
			}
			return
		}
		s.subMu.Lock()
		owner, ok := s.subs[p.URI]
		s.subMu.Unlock()
		if !ok || owner != notification.Instance {
			if DebugLogging {
				log.Printf("resources/updated: dropping stray notification for %q from %s (owner=%q, subscribed=%t)",
					p.URI, serverName, owner, ok)
			}
			return
		}
		// Dispatch off the reader goroutine — writing to stdout blocks on
		// writeMu and the reader must stay responsive. spawn tracks it via
		// handlersWG so Run() doesn't return with a notification write
		// still in flight (otherwise callers reading the stdout buffer
		// after Run exits would race with the write).
		s.spawn("relay resources/updated", func(context.Context) error {
			s.sendNotificationWithParams("notifications/resources/updated", map[string]string{"uri": p.URI})
			return nil
		})
	case "notifications/progress":
		// Filtered at the sink rather than routed by the broadcaster: only the
		// session that minted the token has it, so a fan-out to every session
		// still lands in exactly one place, and the broadcaster's ordering
		// guarantees are preserved.
		params, ok := s.progressNotificationForSession(notification.Params)
		if !ok {
			return
		}
		s.spawn("relay progress", func(context.Context) error {
			s.sendNotificationWithParams("notifications/progress", params)
			return nil
		})
	default:
		if notification.Method == "notifications/tools/list_changed" ||
			(notification.Method == "notifications/resources/list_changed" && s.opts.ExposeResources) ||
			(notification.Method == "notifications/prompts/list_changed" && s.opts.ExposePrompts) {
			s.spawn("relay "+notification.Method, func(context.Context) error {
				s.sendNotification(notification.Method)
				return nil
			})
			return
		}
		if DebugLogging {
			log.Printf("OnUpstreamNotification: dropping %s from %s (relay not implemented)", notification.Method, serverName)
		}
	}
}

// discoverAndNotify continues tool discovery for straggling servers in the background.
// It discovers pending servers concurrently and sends a notifications/tools/list_changed
// each time a straggler succeeds, so the client can refresh promptly without missing
// later successes or waiting for broken servers to time out.
//
// pendingNames is the set of servers that were still pending when the grace
// period expired. ctx is the session lifetime: shutdown ends discovery
// promptly instead of letting it run out the full discovery timeout.
func (s *Session) discoverAndNotify(ctx context.Context, pendingNames []string) {
	defer s.bgDiscovering.Store(false)

	ctx, cancel := context.WithTimeout(ctx, DefaultToolDiscoveryTimeout)
	defer cancel()

	// Buffer one completion per pending server. A non-blocking, single-slot
	// signal loses later completions when several stragglers finish close
	// together, leaving clients unaware of tools discovered after the first
	// notification.
	completed := make(chan string, len(pendingNames))

	var wg sync.WaitGroup
	for _, name := range pendingNames {
		wg.Add(1)
		goSafe("background discovery "+name, func() {
			defer wg.Done()

			tools, err := s.aggregatorForServer(name).DiscoverServer(ctx, name)
			if err != nil {
				log.Printf("Background discovery failed for %s: %v", name, err)
				return
			}
			log.Printf("Background discovery succeeded for %s (%d tools)", name, len(tools))

			completed <- name
		})
	}

	// Close the completion stream after every discovery attempt has finished.
	goSafe("background discovery join", func() {
		wg.Wait()
		close(completed)
	})

	notified := 0
	for serverName := range completed {
		notified++
		s.sendNotification("notifications/tools/list_changed")
		log.Printf("Sent tools/list_changed notification (background discovery completed for %s)", serverName)
	}

	if notified == 0 {
		log.Printf("Background discovery made no progress (%d still pending), skipping notification",
			len(pendingNames))
	}
}
