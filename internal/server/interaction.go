package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"
)

// Why a relayed interaction ended without an answer from the client. The
// relay answers its upstream with the method's fallback in every case.
var (
	errInteractionTimeout     = errors.New("relayed interaction timed out")
	errOwningCallEnded        = errors.New("the call that asked for it has ended")
	errInteractionBudgetSpent = errors.New("the call's interaction budget is spent")
)

// interactionTarget is where a routed server-to-client request goes: the
// session whose client will answer it, plus the calls it is attributed to.
type interactionTarget struct {
	session *Session
	// exact is the call the request belongs to, when that is known exactly
	// (an HTTP origin hint, or a private instance with one call in flight).
	// It ties delivery to that call's request (HTTP POST stream) and ends the
	// interaction when that call ends.
	exact *upstreamCall
	// calls are the calls whose execution budgets pause while the
	// interaction is pending: exact alone when known, otherwise every call
	// the session has in flight on the instance (a private instance whose
	// exact call is unknown). Empty for a request that arrived with no call
	// in flight at all.
	calls []*upstreamCall
}

// interact relays one request to the target session's client and waits for
// the answer, bounded by:
//   - timeout, the per-interaction limit;
//   - the attributed calls: with an exact call, its end (returned, cancelled,
//     out of budget) ends the interaction; with several candidates, the end of
//     the last one does;
//   - ctx, which the upstream cancels when it withdraws its request.
//
// While the interaction is pending, the attributed calls' execution budgets
// are paused, each charged against its own cumulative interaction budget. A
// call whose budget is already spent cannot be paused; when no attributed call
// can be, the interaction fails at once with errInteractionBudgetSpent.
func (t interactionTarget) interact(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, *RPCError, error) {
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, call := range t.calls {
		if release, ok := call.budget.pause(); ok {
			releases = append(releases, release)
		}
	}
	if len(t.calls) > 0 && len(releases) == 0 {
		return nil, nil, errInteractionBudgetSpent
	}

	ictx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	ictx, cancelTimeout := context.WithTimeoutCause(ictx, timeout, errInteractionTimeout)
	defer cancelTimeout()

	var related json.RawMessage
	switch {
	case t.exact != nil:
		related = t.exact.requestID
		stop := context.AfterFunc(t.exact.ctx, func() { cancel(errOwningCallEnded) })
		defer stop()
	case len(t.calls) > 0:
		var remaining atomic.Int64
		remaining.Store(int64(len(t.calls)))
		for _, call := range t.calls {
			stop := context.AfterFunc(call.ctx, func() {
				if remaining.Add(-1) == 0 {
					cancel(errOwningCallEnded)
				}
			})
			defer stop()
		}
	}

	return t.session.Request(ictx, method, params, related)
}
