package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
)

// RequestDeliverer delivers one server→client request frame to a session's
// client. Unlike notifications, delivery must be able to fail: a relayed
// request nobody can see would otherwise wait out its whole timeout, so a
// failure lets the relay answer its upstream with the fallback at once.
//
// related is the client's JSON-RPC id of the request this one is tied to — the
// tools/call whose upstream asked for it — or nil when that is not known
// exactly. The HTTP transport uses it to send the request on that POST's
// response stream.
type RequestDeliverer interface {
	DeliverRequest(frame []byte, related json.RawMessage) error
}

// SetRequestDeliverer installs how this session delivers server→client
// requests. Without one they are written to the session's writer like any
// other frame, which is right for stdio and the daemon, where the connection
// is a single ordered stream.
func (s *Session) SetRequestDeliverer(d RequestDeliverer) {
	s.delivererMu.Lock()
	s.deliverer = d
	s.delivererMu.Unlock()
}

// errNotDelivered wraps a delivery failure.
var errNotDelivered = errors.New("request could not be delivered to the client")

// outboundRequests tracks the requests mcpmu sent to a session's client and is
// waiting on, keyed by canonical id. It is separate from inflightCalls (the
// requests the client sent to mcpmu), and which table applies is decided by
// the kind of message, never the id: a response is looked up here, a
// notifications/cancelled only there.
type outboundRequests struct {
	mu      sync.Mutex
	next    atomic.Uint64
	pending map[string]*outboundRequest
	closed  error // set by failAll; later requests fail at once
}

// outboundRequest is addressed by pointer so a withdrawal can tell its own
// entry from a later one.
type outboundRequest struct {
	done chan outboundResult // buffered 1; written once, by resolve or failAll
}

type outboundResult struct {
	result json.RawMessage
	rpcErr *RPCError
	err    error
}

func newOutboundRequests() *outboundRequests {
	return &outboundRequests{pending: make(map[string]*outboundRequest)}
}

// register allocates an id and a pending entry for a request about to be sent.
func (o *outboundRequests) register() (json.RawMessage, string, *outboundRequest, error) {
	id, _ := json.Marshal(fmt.Sprintf("mcpmu-%d", o.next.Add(1)))
	key := requestKey(id)
	entry := &outboundRequest{done: make(chan outboundResult, 1)}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed != nil {
		return nil, "", nil, o.closed
	}
	o.pending[key] = entry
	return id, key, entry, nil
}

// withdraw removes an entry if it is still pending, reporting whether it was.
func (o *outboundRequests) withdraw(key string, entry *outboundRequest) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending[key] != entry {
		return false
	}
	delete(o.pending, key)
	return true
}

// resolve completes the entry for a client response, reporting whether one
// was pending. A response to a withdrawn or unknown id resolves nothing.
func (o *outboundRequests) resolve(id json.RawMessage, result outboundResult) bool {
	key := requestKey(id)
	if key == "" {
		return false
	}
	o.mu.Lock()
	entry, ok := o.pending[key]
	delete(o.pending, key)
	o.mu.Unlock()
	if !ok {
		return false
	}
	entry.done <- result
	return true
}

// failAll fails every pending request and every later register.
func (o *outboundRequests) failAll(err error) {
	o.mu.Lock()
	pending := o.pending
	o.pending = make(map[string]*outboundRequest)
	if o.closed == nil {
		o.closed = err
	}
	o.mu.Unlock()
	for _, entry := range pending {
		entry.done <- outboundResult{err: err}
	}
}

func (o *outboundRequests) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}

// Request sends a request to this session's client and waits for its answer.
// related names the client's request this one is tied to (see
// RequestDeliverer), or nil.
//
// The result is the client's result, or its JSON-RPC error; err is set instead
// when there is no answer — delivery failed, the session closed, or ctx ended.
// When ctx ends first, the request is withdrawn with notifications/cancelled
// (valid, because mcpmu issued it) and a late answer is ignored.
func (s *Session) Request(ctx context.Context, method string, params any, related json.RawMessage) (json.RawMessage, *RPCError, error) {
	if s.closed.Load() {
		return nil, nil, errSessionClosed
	}
	id, key, entry, err := s.outbound.register()
	if err != nil {
		return nil, nil, err
	}

	frame, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  any             `json:"params,omitempty"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		s.outbound.withdraw(key, entry)
		return nil, nil, fmt.Errorf("marshal %s: %w", method, err)
	}

	if err := s.deliverRequest(frame, related); err != nil {
		s.outbound.withdraw(key, entry)
		return nil, nil, fmt.Errorf("%w: %v", errNotDelivered, err)
	}

	select {
	case res := <-entry.done:
		return res.result, res.rpcErr, res.err
	case <-s.lifetime.Done():
		// The connection is going away (EOF, SIGTERM): nobody is left to
		// answer. Run waits for in-flight handlers before it closes the
		// session, so this must not wait for Close to fail the entry.
		s.outbound.withdraw(key, entry)
		return nil, nil, errSessionClosed
	case <-ctx.Done():
		cause := context.Cause(ctx)
		if s.outbound.withdraw(key, entry) && !s.closed.Load() {
			params := map[string]any{"requestId": id}
			if cause != nil {
				params["reason"] = cause.Error()
			}
			s.sendNotificationWithParams("notifications/cancelled", params)
		}
		return nil, nil, cause
	}
}

// deliverRequest hands a request frame to the installed deliverer, or writes
// it to the session's stream.
func (s *Session) deliverRequest(frame []byte, related json.RawMessage) error {
	s.delivererMu.RLock()
	d := s.deliverer
	s.delivererMu.RUnlock()
	if d != nil {
		return d.DeliverRequest(frame, related)
	}
	return s.writeFrame(frame)
}

// writeFrame writes one pre-encoded frame to the session writer, reporting a
// write failure (send only logs one).
func (s *Session) writeFrame(frame []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if DebugLogging {
		log.Printf("Send: %s", string(frame))
	}
	_, err := s.writer.Write(append(append([]byte(nil), frame...), '\n'))
	return err
}

// HandleClientResponse routes a client's response to the request mcpmu sent
// it, reporting whether one was waiting. It never replies: a response to a
// response is a protocol violation. A response to a request that was
// withdrawn, or that mcpmu never sent, is dropped.
func (s *Session) HandleClientResponse(msg RPCMessage) bool {
	res := outboundResult{result: msg.Result}
	if len(msg.Error) > 0 {
		var rpcErr RPCError
		if err := json.Unmarshal(msg.Error, &rpcErr); err != nil {
			rpcErr = RPCError{Code: ErrCodeInternalError, Message: "malformed error in client response"}
		}
		res = outboundResult{rpcErr: &rpcErr}
	}
	if s.outbound.resolve(msg.ID, res) {
		return true
	}
	log.Printf("Dropping client response to unknown or withdrawn request (id %s)", msg.ID)
	return false
}
