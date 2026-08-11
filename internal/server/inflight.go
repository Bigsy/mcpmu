package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// inflightCalls tracks the upstream work a single session has outstanding, so
// that a cancellation from that session's client can reach the upstream call it
// names — and only that call.
//
// The table is deliberately per-Session rather than per-Core: several sessions
// may share one upstream instance, and cancelling by request ID alone would let
// one agent abort another agent's work. Keying by session *and* JSON-RPC id is
// what keeps the shared-instance model honest.
type inflightCalls struct {
	mu    sync.Mutex
	calls map[string]*inflightCall
}

// inflightCall is addressed by pointer so a release can tell "my entry" from
// "a later request that reused the same id".
type inflightCall struct {
	cancel context.CancelCauseFunc
}

func newInflightCalls() *inflightCalls {
	return &inflightCalls{calls: make(map[string]*inflightCall)}
}

// track derives a cancellable context for a request and registers it under the
// request's JSON-RPC id. The returned release func unregisters the entry and
// releases the context; callers must defer it.
func (t *inflightCalls) track(ctx context.Context, id json.RawMessage) (context.Context, func()) {
	callCtx, cancel := context.WithCancelCause(ctx)
	key := requestKey(id)
	if key == "" {
		// A request without a usable id can never be named by a cancellation
		// notification. Still return a cancellable context so the caller's
		// shape is uniform.
		return callCtx, func() { cancel(context.Canceled) }
	}

	entry := &inflightCall{cancel: cancel}
	t.mu.Lock()
	t.calls[key] = entry
	t.mu.Unlock()

	return callCtx, func() {
		t.mu.Lock()
		if t.calls[key] == entry {
			delete(t.calls, key)
		}
		t.mu.Unlock()
		cancel(context.Canceled)
	}
}

// cancel aborts the call registered under id, reporting whether one was found.
func (t *inflightCalls) cancel(id json.RawMessage, cause error) bool {
	key := requestKey(id)
	if key == "" {
		return false
	}
	t.mu.Lock()
	entry, ok := t.calls[key]
	delete(t.calls, key)
	t.mu.Unlock()
	if !ok {
		return false
	}
	entry.cancel(cause)
	return true
}

// cancelAll aborts every call this session has outstanding. Sessions sharing
// the same upstream instance are untouched — they have their own table.
func (t *inflightCalls) cancelAll(cause error) {
	t.mu.Lock()
	calls := t.calls
	t.calls = make(map[string]*inflightCall)
	t.mu.Unlock()
	for _, entry := range calls {
		entry.cancel(cause)
	}
}

func (t *inflightCalls) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.calls)
}

// requestKey canonicalises a JSON-RPC id for map use. JSON-RPC ids may be
// strings or numbers, and `1` and `"1"` are distinct ids, so the raw encoding
// is the key — with whitespace normalised away by re-encoding.
func requestKey(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(id, &decoded); err != nil {
		return string(id)
	}
	if decoded == nil {
		return ""
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return string(id)
	}
	return string(canonical)
}

// errSessionClosed is the cancellation cause when a client disconnects with
// calls still outstanding.
var errSessionClosed = errors.New("client session closed")

// cancelledError carries the client's stated reason so it can be forwarded
// upstream on the cancellation notification.
type cancelledError struct{ reason string }

func (e *cancelledError) Error() string {
	if e.reason == "" {
		return "request cancelled by client"
	}
	return "request cancelled by client: " + e.reason
}

// progressRoutes maps the tokens mcpmu minted for upstream back to the tokens
// its own client chose.
//
// Progress correlation is by token, and the spec requires tokens to be unique
// across all active requests. Two sessions sharing one upstream instance are
// free to pick the same token — nothing stops both from sending
// `progressToken: 1` — so mcpmu substitutes a token of its own on the way up
// and reverses the substitution on the way down. That makes delivery exact
// rather than a guess: a notification whose token is not in this table did not
// originate from this session's request, and is dropped.
type progressRoutes struct {
	mu     sync.Mutex
	tokens map[string]progressRoute // upstream token → the client's token
	next   atomic.Uint64
	now    func() time.Time // overridable for tests
}

// progressRoute is one token substitution. expires is zero while the call is
// still running and is set when it finishes.
type progressRoute struct {
	clientToken json.RawMessage
	expires     time.Time
}

// progressRouteGrace is how long a finished call's token keeps resolving.
//
// A server emits progress *before* the result, but the two travel different
// paths on the way down: progress goes through the notification broadcaster's
// worker while the result returns directly to the request handler. Deleting the
// mapping the instant the handler returns therefore drops progress frames that
// were already in flight — the notification loses a race it never should have
// been in. Holding the mapping briefly past completion closes that window; a
// stray late frame merely gets forwarded, which is harmless, and the entry is
// swept on the next mint.
const progressRouteGrace = 30 * time.Second

func newProgressRoutes() *progressRoutes {
	return &progressRoutes{tokens: make(map[string]progressRoute), now: time.Now}
}

// mint allocates an upstream token for a client token and records the mapping.
// The returned release func retires it; callers must defer it so a finished
// call eventually stops matching notifications.
func (p *progressRoutes) mint(sessionID string, clientToken json.RawMessage) (string, func()) {
	upstream := fmt.Sprintf("mcpmu/%s/%d", sessionID, p.next.Add(1))
	p.mu.Lock()
	p.sweepLocked()
	p.tokens[upstream] = progressRoute{clientToken: append(json.RawMessage(nil), clientToken...)}
	p.mu.Unlock()
	return upstream, func() {
		p.mu.Lock()
		if route, ok := p.tokens[upstream]; ok && route.expires.IsZero() {
			route.expires = p.now().Add(progressRouteGrace)
			p.tokens[upstream] = route
		}
		p.mu.Unlock()
	}
}

// lookup returns the client token an upstream token maps back to.
func (p *progressRoutes) lookup(upstreamToken string) (json.RawMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	route, ok := p.tokens[upstreamToken]
	if !ok {
		return nil, false
	}
	if !route.expires.IsZero() && p.now().After(route.expires) {
		delete(p.tokens, upstreamToken)
		return nil, false
	}
	return route.clientToken, true
}

// sweepLocked drops retired mappings whose grace period has passed. Called
// from mint so the table is bounded by concurrent calls plus one grace window,
// with no timer per call.
func (p *progressRoutes) sweepLocked() {
	now := p.now()
	for token, route := range p.tokens {
		if !route.expires.IsZero() && now.After(route.expires) {
			delete(p.tokens, token)
		}
	}
}

func (p *progressRoutes) clear() {
	p.mu.Lock()
	clear(p.tokens)
	p.mu.Unlock()
}

// rewriteRequestMeta returns the `_meta` object to send upstream for a call.
//
// When the client asked for progress, its token is replaced with a
// process-unique one and the mapping recorded; every other member of `_meta`
// is forwarded untouched. When it did not, `_meta` passes through verbatim.
func (s *Session) rewriteRequestMeta(meta json.RawMessage) (json.RawMessage, func()) {
	noop := func() {}
	if len(meta) == 0 {
		return nil, noop
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(meta, &members); err != nil {
		// Malformed `_meta` is the server's problem to report, not ours to
		// swallow: forward it and let the upstream reject it.
		if DebugLogging {
			log.Printf("tools/call: forwarding unparseable _meta verbatim: %v", err)
		}
		return meta, noop
	}

	clientToken, ok := members["progressToken"]
	if !ok || len(clientToken) == 0 || string(clientToken) == "null" {
		return meta, noop
	}

	upstreamToken, release := s.progress.mint(s.id, clientToken)
	encoded, err := json.Marshal(upstreamToken)
	if err != nil {
		release()
		return meta, noop
	}
	members["progressToken"] = encoded

	rewritten, err := json.Marshal(members)
	if err != nil {
		release()
		return meta, noop
	}
	return rewritten, release
}

// progressNotificationForSession rewrites an upstream progress notification
// back into this session's token space, returning ok=false when the
// notification belongs to some other session (or to no request at all).
func (s *Session) progressNotificationForSession(params json.RawMessage) (json.RawMessage, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(params, &members); err != nil {
		return nil, false
	}
	raw, ok := members["progressToken"]
	if !ok {
		return nil, false
	}
	var upstreamToken string
	if err := json.Unmarshal(raw, &upstreamToken); err != nil {
		// Only mcpmu-minted string tokens are ever routable; a numeric token
		// means the upstream invented one, which correlates to nothing.
		return nil, false
	}
	clientToken, ok := s.progress.lookup(upstreamToken)
	if !ok {
		return nil, false
	}
	members["progressToken"] = clientToken
	rewritten, err := json.Marshal(members)
	if err != nil {
		return nil, false
	}
	return rewritten, true
}
