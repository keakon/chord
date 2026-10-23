package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestExternalSessionNavigationWaitsOffLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, "exec sleep 60\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	h, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Wait."})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(h.AgentID).(*externalWorker)
	owner := a.currentRecoveryOwnership()
	resumed := false
	start := time.Now()
	if !a.deferExternalSessionSwitch(func() { resumed = true }, nil) {
		t.Fatal("navigation was not deferred")
	}
	if time.Since(start) > time.Second || resumed || a.currentRecoveryOwnership() != owner {
		t.Fatal("navigation blocked the loop or replaced recovery too early")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for !resumed {
		event, err := a.nextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a.dispatch(event)
	}
	select {
	case <-w.done:
	default:
		t.Fatal("navigation resumed before worker stopped")
	}
	w.promptMu.Lock()
	ended := w.processEnded
	w.promptMu.Unlock()
	if !ended {
		t.Fatal("navigation resumed while process still exists")
	}
}
