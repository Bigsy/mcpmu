package server

import (
	"context"
	"errors"
	"sync"
	"time"
)

// callTimeoutError is the cancellation cause when a call runs out of time. It
// is a distinct type so the router can tell "timed out" (OutcomeTimeout,
// ErrToolCallTimeout) from the client cancelling, whatever the context says.
type callTimeoutError struct{ reason string }

func (e *callTimeoutError) Error() string { return e.reason }

var (
	// errExecutionBudgetExpired: the call used up its tool timeout while not
	// waiting on a relayed interaction.
	errExecutionBudgetExpired error = &callTimeoutError{reason: "tool call timed out"}
	// errCallLifetimeExpired: the call hit its hard wall-clock cap (tool
	// timeout plus interaction budget), however its time was spent.
	errCallLifetimeExpired error = &callTimeoutError{reason: "tool call exceeded its maximum lifetime"}
)

// isCallTimeout reports whether a call context's cause is one of the budget
// timeouts above.
func isCallTimeout(cause error) bool {
	_, ok := errors.AsType[*callTimeoutError](cause)
	return ok
}

// callBudget is a call's execution-budget timer. A plain context deadline
// cannot be paused, and a call waiting on a human (an elicitation relayed to
// the client) must not burn its tool timeout meanwhile; so the tool timeout is
// a timer that stops while one or more relayed interactions attributed to the
// call are pending, and resumes with the remaining budget once they settle.
//
// Pausing is itself bounded, so every call has a hard upper bound whatever the
// upstream does:
//   - the cumulative interaction budget caps the total paused time. Once it is
//     spent the timer stops pausing (it resumes even with an interaction still
//     pending), and pause refuses new interactions for the call.
//   - the hard lifetime caps wall-clock time from dispatch.
//
// Parent cancellation always wins: the budget only ever adds causes to a
// context derived from the caller's.
type callBudget struct {
	cancel context.CancelCauseFunc

	mu   sync.Mutex
	done bool
	// remaining is the execution budget not yet used, as of resumedAt.
	remaining time.Duration
	resumedAt time.Time
	execTimer *time.Timer // nil while paused
	// pauses counts the interactions currently holding the timer paused.
	pauses   int
	pausedAt time.Time
	// interactionLeft is the cumulative interaction budget not yet charged.
	interactionLeft  time.Duration
	interactionTimer *time.Timer // runs while paused; fires when interactionLeft is spent
	exhausted        bool
	waited           time.Duration // total paused time charged, for metrics
	hardTimer        *time.Timer
}

// newCallBudget derives the call context from parent and starts the budget:
// execution is the tool timeout, interaction the cumulative interaction
// budget. The hard lifetime is their sum. stop must be called when the call
// returns.
func newCallBudget(parent context.Context, execution, interaction time.Duration) (context.Context, *callBudget) {
	ctx, cancel := context.WithCancelCause(parent)
	b := &callBudget{
		cancel:          cancel,
		remaining:       execution,
		resumedAt:       time.Now(),
		interactionLeft: interaction,
	}
	b.mu.Lock()
	b.startExecTimerLocked()
	b.hardTimer = time.AfterFunc(execution+interaction, func() { b.expire(errCallLifetimeExpired) })
	b.mu.Unlock()
	return ctx, b
}

// startExecTimerLocked (re)arms the execution timer for the remaining budget.
// The callback checks it is still the current timer, so a stale timer that
// fired concurrently with a pause is ignored.
func (b *callBudget) startExecTimerLocked() {
	var timer *time.Timer
	timer = time.AfterFunc(max(b.remaining, 0), func() {
		b.mu.Lock()
		current := b.execTimer == timer
		b.mu.Unlock()
		if current {
			b.expire(errExecutionBudgetExpired)
		}
	})
	b.execTimer = timer
	b.resumedAt = time.Now()
}

func (b *callBudget) expire(cause error) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.cancel(cause)
}

// pause stops the execution timer for one relayed interaction. ok is false
// when the cumulative interaction budget is already spent (or the call is
// over): the interaction must then fail at once rather than extend the call.
// release resumes the timer once no interaction holds it; it is idempotent.
func (b *callBudget) pause() (release func(), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done || b.exhausted || b.interactionLeft <= 0 {
		return func() {}, false
	}
	b.pauses++
	if b.pauses == 1 {
		now := time.Now()
		b.execTimer.Stop()
		b.execTimer = nil
		b.remaining -= now.Sub(b.resumedAt)
		b.pausedAt = now
		var timer *time.Timer
		timer = time.AfterFunc(b.interactionLeft, func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.interactionTimer == timer {
				b.exhaustLocked()
			}
		})
		b.interactionTimer = timer
	}
	var once sync.Once
	return func() { once.Do(b.release) }, true
}

func (b *callBudget) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pauses--
	if b.pauses > 0 || b.done || b.exhausted {
		return
	}
	b.chargePauseLocked()
	b.startExecTimerLocked()
}

// exhaustLocked runs when the cumulative interaction budget runs out
// mid-pause: the timer resumes even though an interaction is still pending, so
// the call's remaining execution budget bounds it from here on.
func (b *callBudget) exhaustLocked() {
	if b.done || b.exhausted || b.pauses == 0 {
		return
	}
	b.chargePauseLocked()
	b.interactionLeft = 0
	b.exhausted = true
	b.startExecTimerLocked()
}

// chargePauseLocked ends the current pause, charging its length against the
// interaction budget.
func (b *callBudget) chargePauseLocked() {
	elapsed := time.Since(b.pausedAt)
	b.interactionLeft -= elapsed
	b.waited += elapsed
	if b.interactionTimer != nil {
		b.interactionTimer.Stop()
		b.interactionTimer = nil
	}
}

// stop ends the budget when the call returns and reports the total time the
// call spent paused on relayed interactions. The call context is left for the
// caller to cancel.
func (b *callBudget) stop() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return b.waited
	}
	if b.pauses > 0 && !b.exhausted {
		b.chargePauseLocked()
	}
	b.done = true
	if b.execTimer != nil {
		b.execTimer.Stop()
	}
	if b.interactionTimer != nil {
		b.interactionTimer.Stop()
	}
	b.hardTimer.Stop()
	return b.waited
}
