package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
)

// TestControlActionBaselineGuardedBySubAgentWork verifies that a user control
// operation cannot swallow a running subagent's completion notification: call
// sites gate markControlAction behind controlActionCanSetBaseline, and the
// baseline must not be advanced while subagent work is in flight.
func TestControlActionBaselineGuardedBySubAgentWork(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.markControlAction()
	baseline := a.lastIdleWorkEpoch

	ctx, cancel := context.WithCancel(context.Background())
	sub := &SubAgent{
		instanceID: "sub-1",
		parent:     a,
		parentCtx:  ctx,
		cancel:     cancel,
		ctxMgr:     ctxmgr.NewManager(8192, 0),
	}
	a.subs.mu.Lock()
	a.subs.subAgents["sub-1"] = sub
	a.subs.mu.Unlock()
	sub.setState(SubAgentStateRunning, "working")

	a.markRealWorkStarted()
	// Call sites only mark a control action when the baseline may be set.
	if a.controlActionCanSetBaseline() {
		t.Fatal("expected baseline to be blocked while subagent is running")
	}
	if a.lastIdleWorkEpoch != baseline {
		t.Fatalf("control action advanced the baseline during subagent work: epoch=%d want %d", a.lastIdleWorkEpoch, baseline)
	}

	// Once the subagent stops, the same control action may set the baseline.
	sub.setState(SubAgentStateCompleted, "")
	if !a.controlActionCanSetBaseline() {
		t.Fatal("expected baseline to be allowed after subagent completed")
	}
	a.markControlAction()
	if a.lastIdleWorkEpoch != a.realWorkEpoch.Load() {
		t.Fatalf("control action did not advance the baseline after subagent completion: epoch=%d want %d", a.lastIdleWorkEpoch, a.realWorkEpoch.Load())
	}
}

// A preparing status only reports that a request waits behind a session gate.
// It must not count as real work: after a control action such as /new, the
// gate wait alone would otherwise turn the next idle into a completion
// notification.
func TestPreparingActivityIsNotRealWork(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.markControlAction()
	baseline := a.realWorkEpoch.Load()

	a.emitActivity("main", ActivityPreparing, "waiting for MCP servers")
	if got := a.realWorkEpoch.Load(); got != baseline {
		t.Fatalf("realWorkEpoch after preparing = %d, want %d", got, baseline)
	}
	a.emitActivity("main", ActivityStreaming, "")
	if got := a.realWorkEpoch.Load(); got == baseline {
		t.Fatal("streaming activity must still count as real work")
	}
}
