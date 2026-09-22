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

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// ErrCodeURLElicitationRequired is URLElicitationRequiredError: the request
// needs URL-mode elicitations, listed in the error's data, completed first.
const ErrCodeURLElicitationRequired = -32042

// elicitationFallback is the answer an upstream gets when its elicitation
// cannot be relayed or was not answered: "cancel" is a normal result every
// server must handle, unlike an error.
var elicitationFallback = json.RawMessage(`{"action":"cancel"}`)

// Interaction outcomes recorded in metrics: the client's action, or why the
// fallback was sent instead.
const (
	outcomeAccept           = "accept"
	outcomeDecline          = "decline"
	outcomeCancel           = "cancel"
	outcomeClientError      = "client-error"
	fallbackUnroutable      = "fallback:unroutable"
	fallbackUnsupported     = "fallback:unsupported"
	fallbackBudgetSpent     = "fallback:budget-spent"
	fallbackTimeout         = "fallback:timeout"
	fallbackCallEnded       = "fallback:call-ended"
	fallbackUndeliverable   = "fallback:undeliverable"
	fallbackSessionClosed   = "fallback:session-closed"
	fallbackUpstreamGaveUp  = "withdrawn-by-server"
	fallbackMalformedParams = "fallback:malformed"
)

// Routing rules, in the order they are tried. Each carries a different level
// of evidence; see ARCHITECTURE.md "Server-to-client requests".
const (
	rulePrivate      = "private-instance" // certain for the session
	rulePostOrigin   = "post-origin"      // strong: the spec says such a message SHOULD relate to the POST's request
	ruleSingleCaller = "single-caller"    // heuristic: nothing tells it from a request left over from an earlier call
)

// routeServerRequest resolves which session a server-to-client request goes
// to, trying the rules in order of evidence. ok is false when none applies —
// several callers on a shared stdio instance, no caller at all — and the
// caller answers with the method's fallback. allowHeuristic admits the
// single-caller rule, and even then only for a target session that enabled it;
// sampling never passes it.
func (c *Core) routeServerRequest(req process.UpstreamRequest, allowHeuristic bool) (target interactionTarget, rule string, ok bool) {
	if !req.Instance.IsShared() {
		// A private instance has exactly one owning session.
		session := c.sessionForID(req.Instance.Session)
		if session == nil || session.closed.Load() {
			return interactionTarget{}, "", false
		}
		target = interactionTarget{session: session}
		if origin := callFromOwner(req.Origin); origin != nil && origin.session == session {
			target.exact = origin
			target.calls = []*upstreamCall{origin}
			return target, rulePrivate, true
		}
		// The exact call is unknown: attribute the request to every call
		// the session had awaiting a response on the instance when it was
		// read, and tie it to one only if there was exactly one.
		for _, owner := range req.InFlight {
			if call := callFromOwner(owner); call != nil && call.session == session {
				target.calls = append(target.calls, call)
			}
		}
		if len(target.calls) == 1 {
			target.exact = target.calls[0]
		}
		return target, rulePrivate, true
	}

	// A shared instance serves several sessions. The request arrived on the
	// response stream of one of mcpmu's POSTs to an HTTP upstream: route it
	// to the call that made that POST.
	if origin := callFromOwner(req.Origin); origin != nil && !origin.session.closed.Load() {
		return interactionTarget{session: origin.session, exact: origin, calls: []*upstreamCall{origin}}, rulePostOrigin, true
	}
	// Exactly one call awaiting a response on the instance when the request
	// was read. A request left over from an earlier call looks the same, so
	// this is used only where the target session enabled it.
	if allowHeuristic && len(req.InFlight) == 1 {
		if call := callFromOwner(req.InFlight[0]); call != nil && !call.session.closed.Load() && call.session.singleCallerHeuristic() {
			return interactionTarget{session: call.session, exact: call, calls: []*upstreamCall{call}}, ruleSingleCaller, true
		}
	}
	return interactionTarget{}, "", false
}

// singleCallerHeuristic reports whether this session accepts requests routed
// by the single-caller heuristic: its own override if set, else the config's
// switch for its transport.
func (s *Session) singleCallerHeuristic() bool {
	return s.opts.SingleCallerHeuristic.Resolve(s.currentConfig().SingleCallerHeuristicEnabled(s.opts.HTTP))
}

// relayElicitation answers elicitation/create from an upstream: it routes the
// request to the session it belongs to, checks that session's client can show
// it, and relays it. Anything that stops the relay — no route, a client
// without the mode, no answer in time, the call ending — answers the upstream
// with {"action":"cancel"} rather than an error.
func (c *Core) relayElicitation(ctx context.Context, req process.UpstreamRequest) (json.RawMessage, *mcp.RPCError) {
	server := req.Instance.Server
	record := func(namespace, outcome string) {
		c.recordInteraction(namespace, server, req.Method, outcome)
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil || params == nil {
		record("", fallbackMalformedParams)
		return elicitationFallback, nil
	}
	mode := modeForm
	if raw, ok := params["mode"]; ok {
		if err := json.Unmarshal(raw, &mode); err != nil {
			record("", fallbackMalformedParams)
			return elicitationFallback, nil
		}
	}

	target, rule, ok := c.routeServerRequest(req, true)
	if !ok {
		log.Printf("elicitation from %s (id %s): no route (%d calls in flight, origin hint: %t), answering cancel",
			req.Instance, req.ID, len(req.InFlight), req.Origin != nil)
		record("", fallbackUnroutable)
		return elicitationFallback, nil
	}
	session := target.session
	namespace := session.activeNamespace()
	if DebugLogging {
		log.Printf("elicitation from %s (id %s) routed to %s by rule %s", req.Instance, req.ID, session.id, rule)
	}
	if !session.clientSupports(featureElicitation, mode) {
		record(namespace, fallbackUnsupported)
		return elicitationFallback, nil
	}

	// Identify the requester: the client only knows it is talking to mcpmu,
	// and the spec wants the user to see who is asking.
	if raw, ok := params["message"]; ok {
		var message string
		if json.Unmarshal(raw, &message) == nil {
			params["message"], _ = json.Marshal(fmt.Sprintf("[%s] %s", server, message))
		}
	}
	// A URL-mode elicitation's id is rewritten into this session's space, so
	// two servers picking the same id cannot collide and a restarted
	// instance's completion cannot match an old incarnation's entry. The URL
	// belongs to the upstream and is left untouched.
	if raw, ok := params["elicitationId"]; ok {
		var upstreamID string
		if json.Unmarshal(raw, &upstreamID) == nil {
			downstreamID := session.elicitations.mint(session.id, req.Instance, req.Generation, upstreamID, c.elicitationRouteTTL(server))
			params["elicitationId"], _ = json.Marshal(downstreamID)
		}
	}

	cfg := c.currentConfig()
	srv, _ := cfg.GetServer(server)
	result, rpcErr, err := target.interact(ctx, req.Method, params, cfg.InteractionTimeout(srv))
	switch {
	case err != nil:
		reason := interactionFailureOutcome(ctx, err)
		if DebugLogging || reason != fallbackUpstreamGaveUp {
			log.Printf("elicitation from %s (id %s) not answered: %v", req.Instance, req.ID, err)
		}
		record(namespace, reason)
		return elicitationFallback, nil
	case rpcErr != nil:
		record(namespace, outcomeClientError)
		return nil, &mcp.RPCError{Code: rpcErr.Code, Message: rpcErr.Message, Data: rpcErr.Data}
	}
	record(namespace, elicitationAction(result))
	return result, nil
}

// interactionFailureOutcome classifies why a relayed interaction got no
// answer, for metrics and logs.
func interactionFailureOutcome(ctx context.Context, err error) string {
	switch {
	case ctx.Err() != nil:
		// The upstream withdrew its request or the instance stopped; nothing
		// is sent back either way.
		return fallbackUpstreamGaveUp
	case errors.Is(err, errInteractionBudgetSpent):
		return fallbackBudgetSpent
	case errors.Is(err, errInteractionTimeout):
		return fallbackTimeout
	case errors.Is(err, errOwningCallEnded):
		return fallbackCallEnded
	case errors.Is(err, errNotDelivered):
		return fallbackUndeliverable
	case errors.Is(err, errSessionClosed):
		return fallbackSessionClosed
	}
	return fallbackUndeliverable
}

// elicitationAction reads the action from a client's elicitation result.
func elicitationAction(result json.RawMessage) string {
	var parsed struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(result, &parsed)
	switch parsed.Action {
	case outcomeAccept, outcomeDecline, outcomeCancel:
		return parsed.Action
	}
	return "unknown-action"
}

// elicitationRouteTTL is how long a minted URL-mode elicitation id keeps
// resolving: a URL flow is completed by a human out of band, and its
// notifications/elicitation/complete can arrive long after the elicitation
// was shown — so the interaction timeout, plus a grace window like progress
// tokens get.
func (c *Core) elicitationRouteTTL(server string) time.Duration {
	cfg := c.currentConfig()
	srv, _ := cfg.GetServer(server)
	return cfg.InteractionTimeout(srv) + progressRouteGrace
}

// elicitationRoutes maps the URL-mode elicitation ids mcpmu minted for its
// client back to the upstream ids they stand for. Keys include the instance
// generation: a restarted instance is a different incarnation, and its
// completion must not match an entry the old one created.
type elicitationRoutes struct {
	mu           sync.Mutex
	next         atomic.Uint64
	byUpstream   map[elicitationKey]*elicitationRoute
	byDownstream map[string]*elicitationRoute
	now          func() time.Time // overridable for tests
}

type elicitationKey struct {
	instance   process.InstanceID
	generation uint64
	upstreamID string
}

type elicitationRoute struct {
	key          elicitationKey
	downstreamID string
	expires      time.Time
}

func newElicitationRoutes() *elicitationRoutes {
	return &elicitationRoutes{
		byUpstream:   make(map[elicitationKey]*elicitationRoute),
		byDownstream: make(map[string]*elicitationRoute),
		now:          time.Now,
	}
}

// mint returns the downstream id for an upstream elicitation id, reusing the
// existing mapping when the same elicitation is seen again (a server may
// report one elicitation in several -32042 errors) and extending its expiry.
func (e *elicitationRoutes) mint(sessionID string, instance process.InstanceID, generation uint64, upstreamID string, ttl time.Duration) string {
	key := elicitationKey{instance: instance, generation: generation, upstreamID: upstreamID}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sweepLocked()
	expires := e.now().Add(ttl)
	if route, ok := e.byUpstream[key]; ok {
		route.expires = expires
		return route.downstreamID
	}
	route := &elicitationRoute{
		key:          key,
		downstreamID: fmt.Sprintf("mcpmu/%s/e%d", sessionID, e.next.Add(1)),
		expires:      expires,
	}
	e.byUpstream[key] = route
	e.byDownstream[route.downstreamID] = route
	return route.downstreamID
}

// complete resolves a notifications/elicitation/complete from an instance to
// the downstream id, retiring the mapping.
func (e *elicitationRoutes) complete(instance process.InstanceID, generation uint64, upstreamID string) (string, bool) {
	key := elicitationKey{instance: instance, generation: generation, upstreamID: upstreamID}
	e.mu.Lock()
	defer e.mu.Unlock()
	route, ok := e.byUpstream[key]
	if !ok {
		return "", false
	}
	delete(e.byUpstream, key)
	delete(e.byDownstream, route.downstreamID)
	if e.now().After(route.expires) {
		return "", false
	}
	return route.downstreamID, true
}

func (e *elicitationRoutes) sweepLocked() {
	now := e.now()
	for key, route := range e.byUpstream {
		if now.After(route.expires) {
			delete(e.byUpstream, key)
			delete(e.byDownstream, route.downstreamID)
		}
	}
}

func (e *elicitationRoutes) clear() {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.byUpstream)
	clear(e.byDownstream)
}

// rewriteURLElicitationError rewrites the elicitation ids in a
// URLElicitationRequiredError's data into this session's id space, exactly as
// for elicitation/create, so the notifications/elicitation/complete the client
// may be waiting on to retry is recognisable. Anything unparseable passes
// through unchanged.
func (s *Session) rewriteURLElicitationError(rpcErr *RPCError, instance process.InstanceID, generation uint64) *RPCError {
	if rpcErr == nil || rpcErr.Code != ErrCodeURLElicitationRequired || len(rpcErr.Data) == 0 {
		return rpcErr
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(rpcErr.Data, &data); err != nil {
		return rpcErr
	}
	var elicitations []map[string]json.RawMessage
	if err := json.Unmarshal(data["elicitations"], &elicitations); err != nil {
		return rpcErr
	}
	ttl := s.elicitationRouteTTL(instance.Server)
	for _, elicitation := range elicitations {
		var upstreamID string
		if json.Unmarshal(elicitation["elicitationId"], &upstreamID) != nil {
			continue
		}
		downstreamID := s.elicitations.mint(s.id, instance, generation, upstreamID, ttl)
		elicitation["elicitationId"], _ = json.Marshal(downstreamID)
	}
	encoded, err := json.Marshal(elicitations)
	if err != nil {
		return rpcErr
	}
	data["elicitations"] = encoded
	rewritten, err := json.Marshal(data)
	if err != nil {
		return rpcErr
	}
	out := *rpcErr
	out.Data = rewritten
	return &out
}

// elicitationCompleteForSession rewrites an upstream
// notifications/elicitation/complete into this session's id space, returning
// ok=false when the elicitation is not one this session was shown.
func (s *Session) elicitationCompleteForSession(notification process.UpstreamNotification) (json.RawMessage, bool) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		return nil, false
	}
	var upstreamID string
	if err := json.Unmarshal(params["elicitationId"], &upstreamID); err != nil {
		return nil, false
	}
	downstreamID, ok := s.elicitations.complete(notification.Instance, notification.Generation, upstreamID)
	if !ok {
		return nil, false
	}
	params["elicitationId"], _ = json.Marshal(downstreamID)
	rewritten, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	return rewritten, true
}

// activeNamespace returns the session's active namespace name.
func (s *Session) activeNamespace() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeNamespaceName
}
