package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

// waitDone waits for ctx to end within limit and returns its cause, failing
// the test if it does not.
func waitDone(t *testing.T, ctx context.Context, limit time.Duration) error {
	t.Helper()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-time.After(limit):
		t.Fatalf("call context still live after %v", limit)
		return nil
	}
}

// assertLive fails the test if ctx ends within d.
func assertLive(t *testing.T, ctx context.Context, d time.Duration) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("call context ended early: %v", context.Cause(ctx))
	case <-time.After(d):
	}
}

// TestCallBudget_ExpiresWithoutInteractions: an unpaused budget behaves like
// the old deadline, with a timeout cause.
func TestCallBudget_ExpiresWithoutInteractions(t *testing.T) {
	t.Parallel()
	ctx, budget := newCallBudget(context.Background(), 50*time.Millisecond, time.Second)
	defer budget.stop()
	if cause := waitDone(t, ctx, time.Second); cause != errExecutionBudgetExpired {
		t.Fatalf("cause = %v, want %v", cause, errExecutionBudgetExpired)
	}
	if !isCallTimeout(context.Cause(ctx)) {
		t.Error("isCallTimeout = false for an expired budget")
	}
}

// TestCallBudget_PauseStopsTheClock: time spent paused on an interaction is
// not charged to the execution budget, which resumes with what was left.
func TestCallBudget_PauseStopsTheClock(t *testing.T) {
	t.Parallel()
	ctx, budget := newCallBudget(context.Background(), 150*time.Millisecond, 5*time.Second)
	defer budget.stop()

	time.Sleep(50 * time.Millisecond)
	release, ok := budget.pause()
	if !ok {
		t.Fatal("pause refused on a fresh budget")
	}
	// Paused well past the whole execution budget.
	assertLive(t, ctx, 300*time.Millisecond)
	release()
	release() // idempotent

	// ~100ms of execution budget remains.
	assertLive(t, ctx, 40*time.Millisecond)
	if cause := waitDone(t, ctx, time.Second); cause != errExecutionBudgetExpired {
		t.Fatalf("cause = %v, want %v", cause, errExecutionBudgetExpired)
	}
	if waited := budget.stop(); waited < 250*time.Millisecond {
		t.Errorf("waited = %v, want at least the 300ms pause", waited)
	}
}

// TestCallBudget_OverlappingPausesResumeOnce: the clock stays stopped until
// the last overlapping interaction settles.
func TestCallBudget_OverlappingPausesResumeOnce(t *testing.T) {
	t.Parallel()
	ctx, budget := newCallBudget(context.Background(), 60*time.Millisecond, 5*time.Second)
	defer budget.stop()
	first, _ := budget.pause()
	second, _ := budget.pause()
	first()
	assertLive(t, ctx, 150*time.Millisecond)
	second()
	if cause := waitDone(t, ctx, time.Second); cause != errExecutionBudgetExpired {
		t.Fatalf("cause = %v, want %v", cause, errExecutionBudgetExpired)
	}
}

// TestCallBudget_CumulativeBudgetStopsPausing: once the cumulative
// interaction budget is spent the clock resumes even with an interaction
// still pending, and new interactions are refused.
func TestCallBudget_CumulativeBudgetStopsPausing(t *testing.T) {
	t.Parallel()
	ctx, budget := newCallBudget(context.Background(), 100*time.Millisecond, 100*time.Millisecond)
	defer budget.stop()

	release, ok := budget.pause()
	if !ok {
		t.Fatal("pause refused on a fresh budget")
	}
	defer release()
	// 100ms of pause, then 100ms of execution: the call ends while the
	// interaction is still "pending".
	if cause := waitDone(t, ctx, time.Second); !isCallTimeout(cause) {
		t.Fatalf("cause = %v, want a call timeout", cause)
	}
	if _, ok := budget.pause(); ok {
		t.Error("pause accepted after the cumulative interaction budget was spent")
	}
}

// TestCallBudget_BackToBackInteractionsAreBounded: an upstream that starts
// interaction after interaction cannot keep a call alive past its budgets:
// pauses are refused once the cumulative budget is spent, and the call ends
// with a timeout no later than its hard lifetime.
func TestCallBudget_BackToBackInteractionsAreBounded(t *testing.T) {
	t.Parallel()
	const execution, interaction = 150 * time.Millisecond, 120 * time.Millisecond
	start := time.Now()
	ctx, budget := newCallBudget(context.Background(), execution, interaction)
	defer budget.stop()

	refused := false
	for ctx.Err() == nil {
		release, ok := budget.pause()
		if !ok {
			refused = true
			break
		}
		time.Sleep(50 * time.Millisecond)
		release()
	}
	if !refused {
		t.Fatal("interactions were never refused")
	}
	cause := waitDone(t, ctx, time.Second)
	if !isCallTimeout(cause) {
		t.Fatalf("cause = %v, want a call timeout", cause)
	}
	if elapsed := time.Since(start); elapsed > execution+interaction+150*time.Millisecond {
		t.Errorf("call lived %v, past its hard lifetime of %v", elapsed, execution+interaction)
	}
}

// TestCallBudget_HardLifetime: the wall-clock cap holds even if the
// execution timer never gets to fire (here: the execution budget is far
// larger than the cap would allow once paused time is included).
func TestCallBudget_HardLifetime(t *testing.T) {
	t.Parallel()
	ctx, budget := newCallBudget(context.Background(), 80*time.Millisecond, 80*time.Millisecond)
	defer budget.stop()
	// Pause at once; the interaction budget runs out at 80ms, the execution
	// budget would then run out at 160ms, and the hard lifetime is 160ms too.
	release, _ := budget.pause()
	defer release()
	if cause := waitDone(t, ctx, time.Second); !isCallTimeout(cause) {
		t.Fatalf("cause = %v, want a call timeout", cause)
	}
}

// TestCallBudget_ParentCancellationWins: cancelling the caller ends the call
// mid-pause with the caller's cause, not a timeout.
func TestCallBudget_ParentCancellationWins(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancelCause(context.Background())
	ctx, budget := newCallBudget(parent, time.Minute, time.Minute)
	defer budget.stop()
	release, _ := budget.pause()
	defer release()

	stop := errors.New("client cancelled")
	cancel(stop)
	cause := waitDone(t, ctx, time.Second)
	if cause != stop {
		t.Fatalf("cause = %v, want the parent's", cause)
	}
	if isCallTimeout(cause) {
		t.Error("parent cancellation classified as a timeout")
	}
}

// TestCallBudget_StopDisarms: a stopped budget never cancels the context.
func TestCallBudget_StopDisarms(t *testing.T) {
	t.Parallel()
	ctx, budget := newCallBudget(context.Background(), 30*time.Millisecond, 30*time.Millisecond)
	budget.stop()
	assertLive(t, ctx, 150*time.Millisecond)
	if _, ok := budget.pause(); ok {
		t.Error("pause accepted on a stopped budget")
	}
}
