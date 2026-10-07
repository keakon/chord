package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

// The preparing activity marks the focused agent busy, so an empty-input
// continue is gated in the TUI while the first request waits for the session
// readiness gates. The compact footer keeps only the icon and elapsed time.
func TestPreparingActivityBlocksContinueAndShowsInStatusBar(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Now()

	if m.continueBlocked() {
		t.Fatal("continueBlocked = true on an idle model, want false")
	}

	m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		AgentID: "main",
		Type:    agent.ActivityPreparing,
		Detail:  "waiting for MCP servers",
	}})
	if !m.focusedAgentHasRuntimeActivity() {
		t.Fatal("focusedAgentHasRuntimeActivity = false during preparing, want true")
	}
	if !m.continueBlocked() {
		t.Fatal("continueBlocked = false during preparing, want true")
	}

	m.activityStartTime["main"] = now.Add(-7 * time.Second)
	display := m.buildStatusBarActivityDisplayAt(m.activities["main"], now)
	if display.Icon != "✶" {
		t.Fatalf("preparing icon = %q, want ✶", display.Icon)
	}
	if got := display.Text; !strings.Contains(got, "7s") || strings.Contains(got, "waiting for MCP servers") {
		t.Fatalf("preparing lane text = %q, want elapsed without detail", got)
	}
	if display.CompactText != "" {
		t.Fatalf("preparing compact text = %q, want empty", display.CompactText)
	}
	if display.NarrowText != "" {
		t.Fatalf("preparing narrow text = %q, want empty", display.NarrowText)
	}
}

// A preparing event without a gate detail still renders the elapsed time.
func TestPreparingActivityWithoutDetailRendersElapsed(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Now()
	activity := agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityPreparing}

	m.activityStartTime["main"] = now.Add(-3 * time.Second)
	display := m.buildStatusBarActivityDisplayAt(activity, now)
	if display.Icon != "✶" {
		t.Fatalf("preparing icon = %q, want ✶", display.Icon)
	}
	if display.Text != "3s" {
		t.Fatalf("preparing lane text = %q, want elapsed only", display.Text)
	}
}

// Preparing precedes the request phases and carries no transport progress, so
// it must not claim the request-progress display state.
func TestPreparingActivityIsNotRequestState(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Now()
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityPreparing, Detail: "waiting for skills"}

	if summary := m.renderRequestProgressSummary("main"); summary != "" {
		t.Fatalf("preparing request progress summary = %q, want empty", summary)
	}
	if _, ok := statusBarWaitRemaining(m.activities["main"], now); ok {
		t.Fatal("preparing wait has a deadline, want none")
	}
}
