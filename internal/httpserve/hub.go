package httpserve

import (
	"encoding/json"
	"log"
	"sync"
)

// hubBacklogCap bounds the frames queued while no GET stream is attached.
// After coalescing this is practically unreachable — hitting it means
// hundreds of distinct notification keys with no consumer.
const hubBacklogCap = 256

// sseFrame is one queued notification.
type sseFrame struct {
	key  string // coalescing key; "" = never coalesce
	data []byte // one JSON-RPC frame, no trailing newline
}

// sseHub is an HTTP session's writer. Every Session notification arrives as
// exactly one Write call per frame (send() writes payload+newline together);
// the hub queues frames and hands them to the currently attached standalone
// GET stream. Unlike the daemon's queuedWriter it never kills the session on
// overflow — responses do not flow through here, only notifications, and the
// load-bearing ones are idempotent "go re-fetch" signals that coalesce.
//
// Network writes happen on the GET handler's goroutine, never here: Write
// only appends to the queue under a short mutex and signals the wake channel.
type sseHub struct {
	mu       sync.Mutex
	closed   bool
	queue    []sseFrame
	attached bool
	replaced chan struct{} // closed when a newer GET stream takes over

	// wake is shared by consecutive streams (only one is attached at a time).
	// Buffered so a producer never blocks and a signal is never lost between
	// takeAll and the consumer's next select.
	wake chan struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{wake: make(chan struct{}, 1)}
}

// Write implements io.Writer for server.Session. Safe to call after teardown
// (Session-internal notification goroutines may fire late): a closed hub
// swallows the frame.
func (h *sseHub) Write(p []byte) (int, error) {
	data := make([]byte, len(p))
	copy(data, p)
	// Strip the NDJSON framing newline; SSE re-frames each message itself.
	for len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r') {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return len(p), nil
	}
	frame := sseFrame{key: coalesceKey(data), data: data}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return len(p), nil
	}
	replaced := false
	if frame.key != "" {
		for i := range h.queue {
			if h.queue[i].key == frame.key {
				// One pending "go re-fetch" signal is as good as ten; keep
				// its position, carry the newest payload.
				h.queue[i].data = frame.data
				replaced = true
				break
			}
		}
	}
	if !replaced {
		if len(h.queue) >= hubBacklogCap {
			log.Printf("httpserve: SSE backlog full, dropping oldest frame")
			h.queue = h.queue[1:]
		}
		h.queue = append(h.queue, frame)
	}
	h.mu.Unlock()

	select {
	case h.wake <- struct{}{}:
	default:
	}
	return len(p), nil
}

// coalesceKey derives the dedupe key for a queued frame. Notifications that
// mean "go re-fetch" coalesce per method; resources/updated is per-URI and
// progress is per-token (dropping the only notification for URI A while
// keeping ten for URI B would lose information). Anything else — including a
// frame carrying a response id, which should never reach the hub — never
// coalesces.
func coalesceKey(data []byte) string {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			URI           string          `json:"uri"`
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.ID != nil || msg.Method == "" {
		return ""
	}
	switch msg.Method {
	case "notifications/resources/updated":
		return msg.Method + "|" + msg.Params.URI
	case "notifications/progress":
		return msg.Method + "|" + string(msg.Params.ProgressToken)
	case "notifications/tools/list_changed",
		"notifications/resources/list_changed",
		"notifications/prompts/list_changed":
		return msg.Method
	}
	return ""
}

// attach registers a new GET stream as the hub's consumer, evicting any
// previous one (clients reconnect after network blips faster than a dead
// conn is detected; refusing would strand them). The returned channel is
// closed when a newer stream takes over. ok is false once the hub is closed.
func (h *sseHub) attach() (replaced <-chan struct{}, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	if h.attached && h.replaced != nil {
		close(h.replaced)
	}
	h.attached = true
	h.replaced = make(chan struct{})
	return h.replaced, true
}

// detach releases the consumer slot, but only if own is still the current
// stream — a replacement GET owns the slot now and must not be detached by
// its predecessor's exit path.
func (h *sseHub) detach(own <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attached && h.replaced != nil && own == (<-chan struct{})(h.replaced) {
		h.attached = false
		h.replaced = nil
	}
}

// takeAll drains the queue for the stream identified by own. A stream that
// has been replaced gets nothing: the shared wake channel means an evicted
// handler can win the race against its own replaced-channel case and reach
// here after a newer GET owns the slot — draining then would deliver (or
// lose) the frames on the abandoned connection.
func (h *sseHub) takeAll(own <-chan struct{}) []sseFrame {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.replaced == nil || own != (<-chan struct{})(h.replaced) {
		return nil
	}
	frames := h.queue
	h.queue = nil
	return frames
}

// close makes all future Write calls a no-op and evicts any attached stream.
func (h *sseHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	h.queue = nil
	if h.attached && h.replaced != nil {
		close(h.replaced)
		h.attached = false
		h.replaced = nil
	}
}
