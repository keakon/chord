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
