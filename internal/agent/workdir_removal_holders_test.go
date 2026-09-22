package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

// A worker reclaiming the checkout it created must not delete the directory the
// session's main agent is working in.
func TestWorktreeRemovalRefusesWhileMainAgentIsBound(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-owner")
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateRunning, "working")

	res, err := sub.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-main-holder"})
	if err != nil {
		t.Fatalf("sub WorktreeEnter: %v", err)
	}
	if _, err := sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-main-holder"}); err != nil {
		t.Fatalf("sub WorktreeExit keep: %v", err)
	}
	if _, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-main-holder"}); err != nil {
		t.Fatalf("main WorktreeEnter: %v", err)
	}
	if got := a.effectiveToolBaseDir(); got != res.Path {
		t.Fatalf("main base dir = %q, want %q", got, res.Path)
	}

	_, err = sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-main-holder", Remove: true, DiscardChanges: true})
	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("err = %v, want a refusal naming the main agent as the holder", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("worktree should survive a refused removal: %v", err)
	}

	// Once the main agent leaves, the worker can reclaim its own checkout.
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-main-holder"}); err != nil {
		t.Fatalf("main WorktreeExit keep: %v", err)
	}
	if _, err := sub.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-main-holder", Remove: true}); err != nil {
		t.Fatalf("sub WorktreeExit remove after the main agent left: %v", err)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Errorf("worktree still present after removal: %v", err)
	}
}

// Leaving a checkout with action "keep" clears the binding without moving the
// agent out of the directory, and a worker delegated while the session worked
// there inherits the directory without ever recording a binding. Removal must
// judge holders by where an agent actually works: matching the recorded binding
// alone deletes the directory a live worker resolves its tools against.
func TestWorktreeRemovalRefusesWhileWorkerKeepsItsDirectory(t *testing.T) {
	ctx := context.Background()
	a, _ := newWorktreeTestAgent(t, "session-owner")

	entered, err := a.WorktreeEnter(ctx, tools.WorktreeEnterRequest{Name: "feat-keep-holder"})
	if err != nil {
		t.Fatalf("WorktreeEnter: %v", err)
	}
	// Delegated while the session works in the checkout, so that directory is
	// the one the worker started in.
	worker := newControllableTestSubAgent(t, a, "task-keep")
	worker.workDir = entered.Path
	worker.setState(SubAgentStateRunning, "working")

	// The main agent leaves without removing the checkout; the worker stays
	// behind with no binding of its own.
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-keep-holder"}); err != nil {
		t.Fatalf("WorktreeExit keep: %v", err)
	}
	if got := worker.effectiveToolBaseDir(); got != entered.Path {
		t.Fatalf("worker base dir = %q, want %q", got, entered.Path)
	}
	if _, err := worker.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-keep-holder"}); err != nil {
		t.Fatalf("worker WorktreeExit keep: %v", err)
	}
	if binding := worker.workDirState.load(); binding.WorktreeID != "" {
		t.Fatalf("worker binding = %#v, want no binding after keep", binding)
	}

	_, err = a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-keep-holder", Remove: true, DiscardChanges: true})
	if err == nil || !strings.Contains(err.Error(), "still in use") || !strings.Contains(err.Error(), worker.instanceID) {
		t.Fatalf("err = %v, want a refusal naming %s as the holder", err, worker.instanceID)
	}
	if _, err := os.Stat(entered.Path); err != nil {
		t.Fatalf("worktree should survive a refused removal: %v", err)
	}

	// A terminal worker no longer works there, so the checkout can go.
	worker.setState(SubAgentStateCompleted, "done")
	if _, err := a.WorktreeExit(ctx, tools.WorktreeExitRequest{Name: "feat-keep-holder", Remove: true}); err != nil {
		t.Fatalf("WorktreeExit after the worker finished: %v", err)
	}
	assertNoFile(t, entered.Path)
}
