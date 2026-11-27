package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
)

// This file is the only place in package server that may start a goroutine.
// Every background task goes through Core.spawn, Session.spawn/runStdioRequest,
// or goSafe so that it (a) recovers from panics instead of taking the whole
// daemon (and every agent's session) down with it, (b) runs under a lifetime
// context that SIGTERM / Close cancels, and (c) where it is a Session task, is
// tracked by handlersWG so Run does not return with work still writing to the
// client. TestNoBareGoroutines enforces the rule.

// recoverPanic converts a panic in the calling goroutine into a log line with
// a stack trace. onPanic, if non-nil, runs after logging (used to reply
// -32603 to the request whose handler died). Must be deferred directly.
func recoverPanic(name string, onPanic func(recovered any)) {
	recovered := recover()
	if recovered == nil {
		return
	}
	log.Printf("PANIC in %s: %v\n%s", name, recovered, debug.Stack())
	if onPanic != nil {
		onPanic(recovered)
	}
}

// protect runs fn synchronously with panic recovery. A non-nil requestID
// means fn is answering a JSON-RPC request; on panic the client gets an
// internal error for that id instead of silence.
func (s *Session) protect(name string, requestID json.RawMessage, fn func(ctx context.Context) error) {
	defer recoverPanic(name, func(recovered any) {
		if requestID != nil {
			s.sendError(requestID, ErrInternalError(fmt.Sprintf("handler panicked: %v", recovered)))
		}
	})
	if err := fn(s.lifetime); err != nil {
		log.Printf("%s: %v", name, err)
	}
}

// spawn runs fn on a handlersWG-tracked goroutine under the session lifetime
// context. Run's shutdown waits for it; Run's ctx (SIGTERM, client hang-up)
// and Session.Close cancel it.
func (s *Session) spawn(name string, fn func(ctx context.Context) error) {
	s.handlersWG.Go(func() { s.protect(name, nil, fn) })
}

// runStdioRequest owns one admitted slot until the response, including a panic
// response, has finished writing. Register before spawning so a cancellation
// arriving on the very next frame can find its call.
func (s *Session) runStdioRequest(ctx context.Context, msg RPCMessage) {
	async := isUpstreamMethod(msg.Method)
	var release func()
	if async {
		ctx, release = s.TrackRequest(ctx, msg.ID)
	}
	run := func() {
		defer func() { <-s.requestSlots }()
		if release != nil {
			defer release()
		}
		s.protect("handler "+msg.Method, msg.ID, func(context.Context) error {
			resp, _ := s.Dispatch(ctx, msg)
			s.send(resp)
			return nil
		})
	}
	if async {
		s.handlersWG.Go(run)
	} else {
		run()
	}
}

// spawn runs fn on a Core-tracked goroutine under the Core lifetime context.
// Core.Close cancels the context and waits for fn to return.
func (c *Core) spawn(name string, fn func(ctx context.Context) error) {
	c.bgWG.Go(func() {
		defer recoverPanic(name, nil)
		if err := fn(c.lifetime); err != nil {
			log.Printf("%s: %v", name, err)
		}
	})
}

// goSafe starts an untracked goroutine with panic recovery. For scoped
// fan-outs whose completion the caller already waits on with its own
// WaitGroup or channel; fn must do that bookkeeping itself (deferred).
func goSafe(name string, fn func()) {
	go func() {
		defer recoverPanic(name, nil)
		fn()
	}()
}

// joinContext returns a child of parent that is additionally cancelled when
// other ends. Used where a caller-supplied ctx must also stop on Close.
func joinContext(parent, other context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(other, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
