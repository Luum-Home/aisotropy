package taskagent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// The property this pins: a deadline must stay RECOGNISABLE as a deadline after
// it has passed through exec.
//
// When a per-task deadline kills a subprocess, exec reports "signal: killed".
// Nothing in that chain satisfies errors.Is(err, context.DeadlineExceeded), so
// a caller switching on "ran out of time" versus "genuinely broke" silently gets
// the wrong branch — and only under load, because only under load does the
// subprocess get far enough to be killed rather than failing outright.
//
// TestRunTasks_PerTaskDeadline_TearsDownRunawaySibling is the integration-level
// consumer of this property. It went red during a full parallel sweep while
// passing in isolation, reporting a worktree error where it wanted
// DeadlineExceeded. The teardown had worked; only the classification was lost.
// This test states the rule directly, without needing load to reproduce it.
func TestWorktreeError_KeepsDeadlineClassification(t *testing.T) {
	execFailure := errors.New("sandbox: worktree add: signal: killed")

	deadlineCtx, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	<-deadlineCtx.Done()

	wrapped := fmt.Errorf("taskagent: worktree add: %w: %w", deadlineCtx.Err(), execFailure)

	if !errors.Is(wrapped, context.DeadlineExceeded) {
		t.Errorf("a subprocess killed by the deadline must still classify as DeadlineExceeded, got %v", wrapped)
	}
	// And the cause must survive: a classification that erases the diagnosis
	// trades one debugging problem for another.
	if !errors.Is(wrapped, execFailure) {
		t.Errorf("the underlying exec failure must remain in the chain, got %v", wrapped)
	}

	// A live context must NOT be given a deadline classification it did not earn.
	live := fmt.Errorf("taskagent: worktree add: %w", execFailure)
	if errors.Is(live, context.DeadlineExceeded) {
		t.Error("a worktree failure with a live context must not report a deadline")
	}
}
