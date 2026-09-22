package httpserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/server"
)

// errPostFinished: the POST a request was tied to has already been answered.
var errPostFinished = errors.New("the request's POST has already been answered")

// postStream lets a server→client request ride the response stream of the
// POST it belongs to. The spec lets a server answer a POSTed request with an
// SSE stream carrying related requests before the final response, and a client
// need not open the standalone GET stream at all — so for a request tied to a
// call (an elicitation the tool needs answered to finish), the POST stream is
// the one channel certain to reach the client.
//
// The POST handler owns the http.ResponseWriter and does every write on its
// own goroutine: deliver hands it a frame and waits for the write's outcome.
// The response starts "undecided" — plain JSON unless a request arrives before
// the final response, in which case it switches to text/event-stream, sends
// the request as an event, and later sends the final response on the same
// stream.
type postStream struct {
	frames chan postFrame // unbuffered: a send completes only when the handler takes it
	done   chan struct{}  // closed once the handler stops taking frames
}

type postFrame struct {
	data []byte
	ack  chan error // buffered 1; the handler reports the write's outcome
}

func newPostStream() *postStream {
	return &postStream{frames: make(chan postFrame), done: make(chan struct{})}
}

// deliver sends one frame on the POST's stream, failing if the POST has
// already been answered or the write fails.
func (ps *postStream) deliver(frame []byte) error {
	item := postFrame{data: frame, ack: make(chan error, 1)}
	select {
	case ps.frames <- item:
	case <-ps.done:
		return errPostFinished
	}
	return <-item.ack
}

// sseResponse writes an upgraded POST response. started is set by the first
// event, which also sends the headers.
type sseResponse struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	started bool
}

func (r *sseResponse) event(data []byte) error {
	if !r.started {
		r.started = true
		r.w.Header().Set("Content-Type", "text/event-stream")
		r.w.Header().Set("Cache-Control", "no-cache")
		r.w.WriteHeader(http.StatusOK)
	}
	_ = r.rc.SetWriteDeadline(time.Now().Add(writeDeadline))
	if _, err := r.w.Write(formatSSE(data)); err != nil {
		return err
	}
	return r.rc.Flush()
}

// formatSSE frames one JSON-RPC message as an SSE "message" event.
func formatSSE(data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("event: message\n")
	for line := range bytes.Lines(bytes.TrimRight(data, "\n")) {
		buf.WriteString("data: ")
		buf.Write(bytes.TrimRight(line, "\n"))
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// postStreams are an HTTP session's POSTs currently being handled, keyed by
// the canonical JSON-RPC id of the request each carries.
type postStreams struct {
	mu      sync.Mutex
	streams map[string]*postStream
}

func (p *postStreams) add(key string, ps *postStream) {
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streams == nil {
		p.streams = make(map[string]*postStream)
	}
	p.streams[key] = ps
}

func (p *postStreams) remove(key string, ps *postStream) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.streams[key] == ps {
		delete(p.streams, key)
	}
}

func (p *postStreams) get(key string) *postStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streams[key]
}

// DeliverRequest implements server.RequestDeliverer. A request tied to a POST
// still being handled goes on that POST's stream. Anything else — no tie, or a
// tie to a request whose POST is already answered — goes on the standalone GET
// stream if one is attached, and fails otherwise: guessing some other POST
// could hand one call's question to another.
func (hs *httpSession) DeliverRequest(frame []byte, related json.RawMessage) error {
	if key := server.CanonicalRequestID(related); key != "" {
		if ps := hs.posts.get(key); ps != nil {
			return ps.deliver(frame)
		}
	}
	return hs.hub.Deliver(frame)
}
