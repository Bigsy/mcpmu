package httpserve

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
)

// hubBacklogCap bounds the frames queued while no GET stream is attached.
// After coalescing this is practically unreachable — hitting it means
// hundreds of distinct notification keys with no consumer.
const hubBacklogCap = 256

// sseFrame is one queued frame: a notification, or a server→client request
// queued by Deliver.
type sseFrame struct {
	key     string // coalescing key; "" = never coalesce
	data    []byte // one JSON-RPC frame, no trailing newline
	request bool   // a server→client request: never coalesced or evicted
}

// Why Deliver refused a request.
var (
	errHubClosed          = errors.New("session is closed")
	errNoStandaloneStream = errors.New("no standalone SSE stream is attached")
	errBacklogFull        = errors.New("SSE backlog is full of undelivered requests")
)

// sseHub is an HTTP session's writer. Every Session notification arrives as
// exactly one Write call per frame (send() writes payload+newline together);
// the hub queues frames and hands them to the currently attached standalone
// GET stream. Unlike the daemon's queuedWriter it never kills the session on
// overflow — responses do not flow through here, only notifications, and the
// load-bearing ones are idempotent "go re-fetch" signals that coalesce.
// Server→client requests that cannot ride a POST stream arrive through
// Deliver instead, which can refuse them (see postStream).
//
// Network writes happen on the GET handler's goroutine, never here: Write
// only appends to the queue under a short mutex and signals the current
// stream's drain channel.
type sseHub struct {
	mu       sync.Mutex
	closed   bool
	draining bool
	queue    []sseFrame
	// replaced is non-nil exactly while a GET stream holds the consumer slot,
	// and is closed when a newer stream takes over or the hub shuts down.
	replaced chan struct{}

	// drain belongs to the currently attached stream: attach mints a fresh
	// buffered channel per stream and Write signals only the current one.
	// A single channel shared across streams loses wakeups at replacement —
	// an evicted handler can consume the signal via its select race and then
	// exit, stranding a queued frame on a replacement sitting on an empty
	// channel. Per-stream channels make that impossible: whatever the evicted
	// handler does with its own channel, the replacement's was primed by
	// attach if any backlog existed and is signalled by every later Write.
	drain chan struct{}
}

func newSSEHub() *sseHub {
	return &sseHub{}
}

// Write implements io.Writer for server.Session. Safe to call after teardown
// (Session-internal notification goroutines may fire late): a closed hub
// swallows the frame.
func (h *sseHub) Write(p []byte) (int, error) {
	data := trimFrame(p)
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
		if len(h.queue) >= hubBacklogCap && !h.evictNotificationLocked() {
			// Every queued frame is a request awaiting its answer; this
			// notification is the one that goes.
			h.mu.Unlock()
			log.Printf("httpserve: SSE backlog full of requests, dropping notification")
			return len(p), nil
		}
		h.queue = append(h.queue, frame)
	}
	drain := h.drain
	h.mu.Unlock()

	signal(drain)
	return len(p), nil
}

// Deliver queues a server→client request for the attached GET stream. Unlike
// Write it can fail, because a request nobody sees would wait out its whole
// timeout: it errors when the hub is closed, when no stream is attached (a
// client need not open one, and a request queued for a stream that may never
// come is as good as lost), or when the backlog holds nothing but requests. A
// request is never coalesced, and the overflow logic evicts a notification to
// make room for it rather than another request.
func (h *sseHub) Deliver(p []byte) error {
	data := trimFrame(p)
	if len(data) == 0 {
		return errors.New("empty frame")
	}
	h.mu.Lock()
	switch {
	case h.closed:
		h.mu.Unlock()
		return errHubClosed
	case h.replaced == nil:
		h.mu.Unlock()
		return errNoStandaloneStream
	}
	if len(h.queue) >= hubBacklogCap && !h.evictNotificationLocked() {
		h.mu.Unlock()
		return errBacklogFull
	}
	h.queue = append(h.queue, sseFrame{data: data, request: true})
	drain := h.drain
	h.mu.Unlock()

	signal(drain)
	return nil
}

// evictNotificationLocked drops the oldest queued notification to make room,
// reporting false when every queued frame is a request.
func (h *sseHub) evictNotificationLocked() bool {
	for i, frame := range h.queue {
		if !frame.request {
			log.Printf("httpserve: SSE backlog full, dropping oldest notification")
			h.queue = append(h.queue[:i], h.queue[i+1:]...)
			return true
		}
	}
	return false
}

// trimFrame copies one written frame, stripping the NDJSON framing newline;
// SSE re-frames each message itself.
func trimFrame(p []byte) []byte {
	data := make([]byte, len(p))
	copy(data, p)
	for len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r') {
		data = data[:len(data)-1]
	}
	return data
}

// signal wakes a stream's drain loop without blocking.
func signal(drain chan struct{}) {
	if drain == nil {
		return
	}
	select {
	case drain <- struct{}{}:
	default:
	}
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
// conn is detected; refusing would strand them). It returns the stream's
// eviction signal — closed when a newer stream takes over or the hub closes —
// and its drain signal, primed up-front when a backlog already awaits.
// ok is false once the hub is closed.
func (h *sseHub) attach() (replaced, drain <-chan struct{}, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.draining {
		return nil, nil, false
	}
	if h.replaced != nil {
		close(h.replaced)
	}
	h.replaced = make(chan struct{})
	next := make(chan struct{}, 1)
	if len(h.queue) > 0 {
		next <- struct{}{}
	}
	h.drain = next
	return h.replaced, next, true
}

// detach releases the consumer slot, but only if own is still the current
// stream — a replacement GET owns the slot now and must not be detached by
// its predecessor's exit path.
func (h *sseHub) detach(own <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.replaced != nil && own == (<-chan struct{})(h.replaced) {
		h.replaced = nil
	}
}

// takeAll drains the queue for the stream identified by own. A stream that
// has been replaced gets nothing: an evicted handler can still be scheduled
// after its replacement attached — its select had both channels ready — and
// must not drain, or frames would be delivered to (or lost on) the abandoned
// connection.
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

// closeStreams ends the standalone GET stream: the attached handler's
// eviction signal fires (its select exits without detaching) and future
// attaches are refused until teardown. This is deliberately narrower than
// close — the session stays fully alive, so POST round trips in flight keep
// dispatching and their notifications still queue — while ending the one
// connection that never goes idle on its own and would otherwise pin
// http.Shutdown for its whole grace period.
func (h *sseHub) closeStreams() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.draining = true
	if h.replaced != nil {
		close(h.replaced)
	}
	h.replaced = nil
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
	h.drain = nil
	if h.replaced != nil {
		close(h.replaced)
		h.replaced = nil
	}
}
