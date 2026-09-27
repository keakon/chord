package agent

import (
	"context"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
)

// waitForPreparingActivity drains output events until a preparing activity
// arrives, so interleaved unrelated events (persist loop, usage updates) do
// not break the gate-order assertions.
func waitForPreparingActivity(t *testing.T, events <-chan AgentEvent) AgentActivityEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if act, ok := ev.(AgentActivityEvent); ok && act.Type == ActivityPreparing {
				return act
			}
		case <-deadline:
			t.Fatal("timed out waiting for preparing activity")
			return AgentActivityEvent{}
		}
	}
}

// The first request after startup waits for the async readiness gates. While a
// gate is pending the turn is already active but no request phase has started,
// so the agent must surface a preparing activity naming the pending gate —
// otherwise the status bar looks idle while continues are silently dropped.
func TestEnsureSessionBuiltEmitsPreparingForPendingGates(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	done := make(chan error, 1)
	go func() {
		done <- a.ensureSessionBuiltWithoutPreparation(context.Background())
	}()

	first := waitForPreparingActivity(t, a.outputCh)
	if first.AgentID != identity.MainAgentID {
		t.Fatalf("preparing agent id = %q, want main", first.AgentID)
	}
	if first.Detail != "waiting for AGENTS.md" {
		t.Fatalf("first preparing detail = %q, want the AGENTS.md gate", first.Detail)
	}
	a.markAgentsMDReady()

	second := waitForPreparingActivity(t, a.outputCh)
	if second.Detail != "waiting for skills" {
		t.Fatalf("second preparing detail = %q, want the skills gate", second.Detail)
	}
	a.MarkSkillsReady()

	third := waitForPreparingActivity(t, a.outputCh)
	if third.Detail != "waiting for MCP servers" {
		t.Fatalf("third preparing detail = %q, want the MCP gate", third.Detail)
	}
	a.markMCPReady()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ensureSessionBuiltWithoutPreparation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSessionBuiltWithoutPreparation did not return after gates closed")
	}
}

// With every gate already ready the request must not flash a preparing
// activity: the emission is reserved for the actually-pending wait.
func TestEnsureSessionBuiltSkipsPreparingWhenGatesReady(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	if err := a.ensureSessionBuiltWithoutPreparation(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuiltWithoutPreparation: %v", err)
	}
	for _, evt := range drainAgentEvents(a.outputCh) {
		if act, ok := evt.(AgentActivityEvent); ok && act.Type == ActivityPreparing {
			t.Fatalf("unexpected preparing activity with detail %q", act.Detail)
		}
	}
}
