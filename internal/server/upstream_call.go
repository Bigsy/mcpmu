package server

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// upstreamCall is one request a session dispatched to an upstream instance on
// its client's behalf (tools/call, resources/read, prompts/get). It is the
// mcp.CallOwner State for that request, so a server-to-client request routed
// back to it can pause its budget and end with it.
type upstreamCall struct {
	session   *Session
	requestID json.RawMessage // the client's JSON-RPC id; nil if unknown
	instance  process.InstanceID
	owner     *mcp.CallOwner
	// ctx is the call's context: done when the call returns, is cancelled by
	// the client, or runs out of budget.
	ctx    context.Context
	budget *callBudget
}

// callFromOwner returns the upstreamCall behind an owner, or nil.
func callFromOwner(owner *mcp.CallOwner) *upstreamCall {
	if owner == nil {
		return nil
	}
	call, _ := owner.State.(*upstreamCall)
	return call
}

// activeCalls is a session's registry of upstream calls in flight, by
// instance. It answers "which of this session's calls could a request from
// this instance belong to" for private instances, where the session is
// certain but the exact call may not be.
type activeCalls struct {
	mu         sync.Mutex
	byInstance map[process.InstanceID]map[*upstreamCall]struct{}
}

func newActiveCalls() *activeCalls {
	return &activeCalls{byInstance: make(map[process.InstanceID]map[*upstreamCall]struct{})}
}

func (a *activeCalls) add(call *upstreamCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.byInstance[call.instance]
	if calls == nil {
		calls = make(map[*upstreamCall]struct{})
		a.byInstance[call.instance] = calls
	}
	calls[call] = struct{}{}
}

func (a *activeCalls) remove(call *upstreamCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := a.byInstance[call.instance]
	delete(calls, call)
	if len(calls) == 0 {
		delete(a.byInstance, call.instance)
	}
}

// forInstance returns the calls in flight on one instance.
func (a *activeCalls) forInstance(id process.InstanceID) []*upstreamCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	calls := make([]*upstreamCall, 0, len(a.byInstance[id]))
	for call := range a.byInstance[id] {
		calls = append(calls, call)
	}
	return calls
}

type downstreamRequestIDKey struct{}

// withDownstreamRequestID records the client's JSON-RPC id for the request a
// handler is serving, so the upstream calls it makes can be attributed to it.
func withDownstreamRequestID(ctx context.Context, id json.RawMessage) context.Context {
	return context.WithValue(ctx, downstreamRequestIDKey{}, id)
}

func downstreamRequestID(ctx context.Context) json.RawMessage {
	id, _ := ctx.Value(downstreamRequestIDKey{}).(json.RawMessage)
	return id
}

// beginUpstreamCall registers a call about to be dispatched to instance and
// returns its context: parent bounded by the execution budget (the tool
// timeout, paused while the call waits on relayed interactions), carrying the
// call's mcp.CallOwner so the upstream request id is registered against it.
// end must be called when the call returns; the first call reports the time
// the call spent waiting on relayed interactions, later calls report 0.
func (s *Session) beginUpstreamCall(parent context.Context, instance process.InstanceID, timeout time.Duration) (context.Context, *upstreamCall, func() time.Duration) {
	cfg := s.currentConfig()
	srv, _ := cfg.GetServer(instance.Server)
	ctx, budget := newCallBudget(parent, timeout, cfg.InteractionBudget(srv))

	call := &upstreamCall{
		session:   s,
		requestID: downstreamRequestID(parent),
		instance:  instance,
		budget:    budget,
	}
	call.owner = &mcp.CallOwner{Session: s.id, RequestID: call.requestID, State: call}
	ctx = mcp.WithCallOwner(ctx, call.owner)
	call.ctx = ctx
	s.calls.add(call)

	var once sync.Once
	return ctx, call, func() time.Duration {
		var waited time.Duration
		once.Do(func() {
			s.calls.remove(call)
			waited = budget.stop()
			budget.cancel(context.Canceled)
		})
		return waited
	}
}
