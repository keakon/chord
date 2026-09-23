package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// newFallbackDownshiftTestAgent builds an agent whose context usage (900 of
// 1000 tokens) crosses the 0.8 threshold of the model it currently runs on.
func newFallbackDownshiftTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{
		Context: config.ContextConfig{
			Compaction: config.CompactionConfig{Threshold: 0.8},
		},
	}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 900})
	// The fallback boundary always runs inside a round that already passed the
	// pre-request gate, which applied the primary model's line. Emulate that
	// application so the boundary re-evaluates against a genuine model change
	// instead of the first-application case that arms nothing.
	a.applyModelCompactionConfig()
	a.newTurn()
	a.started.Store(true)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})
	return a
}

// askFallbackBoundary drives one fallback boundary decision and returns the
// result the requesting goroutine would read.
func askFallbackBoundary(a *MainAgent, payload *llmFallbackBoundaryPayload) llmFallbackBoundaryResult {
	reply := make(chan llmFallbackBoundaryResult, 1)
	payload.reply = reply
	a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: a.turn.ID, Payload: payload})
	return <-reply
}

// TestFallbackBoundaryCommitsSmallerWindowAndArmsCompaction covers the fallback
// downshift contract under usage-only triggering: a fallback that narrows the
// window commits its identity and budgets and the round goes out, but the stale
// size observation is invalidated so nothing arms until fresh usage on the new
// window crosses its own line. The compaction starts at a later pre-request gate,
// so the boundary must neither claim the compaction slot nor hold the reply.
func TestFallbackBoundaryCommitsSmallerWindowAndArmsCompaction(t *testing.T) {
	a := newFallbackDownshiftTestAgent(t)

	result := askFallbackBoundary(a, &llmFallbackBoundaryPayload{
		turnID:               a.turn.ID,
		messages:             a.ctxMgr.Snapshot(),
		fallbackModelRef:     "provider/smaller-model",
		fallbackContextLimit: 500,
		fallbackInputLimit:   500,
	})

	if result.err != nil {
		t.Fatalf("fallback boundary error = %v, want the round to proceed on the fallback", result.err)
	}
	if a.IsCompactionRunning() {
		t.Fatal("the boundary must not start a compaction ahead of the round")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("a fallback switch must not arm from stale observation: size is invalidated, fresh usage required")
	}
	if got := a.RunningModelRef(); got != "provider/smaller-model" {
		t.Fatalf("RunningModelRef = %q, want the committed fallback identity", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 500 {
		t.Fatalf("context window = %d, want the committed fallback window 500", got)
	}
}

// TestFallbackBoundaryCommitsSmallerInputBudgetOnly verifies that a fallback
// whose total context window matches the current model but whose effective
// input budget is smaller still commits the new budgets, but under usage-only
// triggering it must not arm from the stale observation.
func TestFallbackBoundaryCommitsSmallerInputBudgetOnly(t *testing.T) {
	a := newFallbackDownshiftTestAgent(t)

	result := askFallbackBoundary(a, &llmFallbackBoundaryPayload{
		turnID:               a.turn.ID,
		messages:             a.ctxMgr.Snapshot(),
		fallbackModelRef:     "provider/smaller-input-model",
		fallbackContextLimit: 1000, // same total window as the current model
		fallbackInputLimit:   500,
	})

	if result.err != nil {
		t.Fatalf("fallback boundary error = %v, want the round to proceed on the fallback", result.err)
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("an input-budget-only downshift must not arm from stale observation after invalidation")
	}
	if got := a.RunningModelRef(); got != "provider/smaller-input-model" {
		t.Fatalf("RunningModelRef = %q, want the committed fallback identity", got)
	}
	if got := a.ctxMgr.GetInputBudget(); got != 500 {
		t.Fatalf("input budget = %d, want the committed fallback input budget 500", got)
	}
}

// TestFallbackBoundaryCommitsWithoutArmingBelowTheNewLine pins the other half of
// the contract: narrowing the window is not by itself a crossing. The commit
// still happens (the sidebar and the compaction line must describe the window
// the request is admitted against), but nothing is armed until usage actually
// crosses the new line.
func TestFallbackBoundaryCommitsWithoutArmingBelowTheNewLine(t *testing.T) {
	a := newFallbackDownshiftTestAgent(t)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 300}) // below either line

	result := askFallbackBoundary(a, &llmFallbackBoundaryPayload{
		turnID:               a.turn.ID,
		messages:             a.ctxMgr.Snapshot(),
		fallbackModelRef:     "provider/smaller-model",
		fallbackContextLimit: 600,
		fallbackInputLimit:   600,
	})

	if result.err != nil {
		t.Fatalf("fallback boundary error = %v, want the round to proceed on the fallback", result.err)
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("usage below the new line must not arm the compaction")
	}
	if got := a.RunningModelRef(); got != "provider/smaller-model" {
		t.Fatalf("RunningModelRef = %q, want the committed fallback identity", got)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != 600 {
		t.Fatalf("context window = %d, want the committed fallback window 600", got)
	}
}

// TestFallbackBoundaryLetsLargerWindowFallbackThrough verifies that a fallback
// whose context and input budgets are both at least the current model's is
// dispatched immediately: no crossing was introduced, so compacting ahead of it
// would only slow down an otherwise healthy fallback. Nothing about the running
// model may change either — the round is admitted against a budget no smaller
// than the one it already passed.
func TestFallbackBoundaryLetsLargerWindowFallbackThrough(t *testing.T) {
	a := newFallbackDownshiftTestAgent(t)
	beforeRef := a.RunningModelRef()
	beforeWindow := a.ctxMgr.GetMaxTokens()

	result := askFallbackBoundary(a, &llmFallbackBoundaryPayload{
		turnID:               a.turn.ID,
		messages:             a.ctxMgr.Snapshot(),
		fallbackModelRef:     "provider/larger-model",
		fallbackContextLimit: 2000,
		fallbackInputLimit:   1500,
	})

	if result.err != nil {
		t.Fatalf("fallback boundary error = %v, want nil (a larger window needs no compaction)", result.err)
	}
	if a.IsCompactionRunning() {
		t.Fatal("a larger-window fallback must not start a compaction")
	}
	if got := a.RunningModelRef(); got != beforeRef {
		t.Fatalf("RunningModelRef = %q, want the unchanged %q", got, beforeRef)
	}
	if got := a.ctxMgr.GetMaxTokens(); got != beforeWindow {
		t.Fatalf("context window = %d, want the unchanged %d", got, beforeWindow)
	}
}
