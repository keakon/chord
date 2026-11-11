package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestWaitForAgentEventMicroBatchesStreamText(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax)
	ch <- agent.StreamTextEvent{Text: "a"}
	cmd := waitForAgentEvent(ch)

	go func() {
		time.Sleep(agentEventStreamBatchWindow / 2)
		ch <- agent.StreamTextEvent{Text: "b"}
	}()

	msg := cmd()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("batch length = %d, want 2", len(batch))
	}
}

func TestWaitForAgentEventReducesPacedStreamTextWakeups(t *testing.T) {
	// Widen the window instead of racing it. The original pacing sent the
	// deltas across most of a 16ms window, so scheduling jitter on a loaded
	// machine pushed the tail past the deadline and failed the test for a
	// timing reason rather than a real one. The guarantee is that paced
	// deltas merge into one wakeup, so give the pacing room and assert it
	// exactly.
	restore := agentEventStreamBatchWindow
	agentEventStreamBatchWindow = 500 * time.Millisecond
	t.Cleanup(func() { agentEventStreamBatchWindow = restore })

	const events = 5
	ch := make(chan agent.AgentEvent, events)
	ch <- agent.StreamTextEvent{Text: "0"}
	cmd := waitForAgentEvent(ch)

	go func() {
		for i := 1; i < events; i++ {
			time.Sleep(2 * time.Millisecond)
			ch <- agent.StreamTextEvent{Text: "x"}
		}
	}()

	msg := cmd()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != events {
		t.Fatalf("batch length = %d, want all %d paced stream events merged into one wakeup", len(batch), events)
	}
}

func TestWaitForAgentEventStopsStreamTextBatchAtMax(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax+1)
	for range agentEventBatchMax + 1 {
		ch <- agent.StreamTextEvent{Text: "x"}
	}
	msg := waitForAgentEvent(ch)()
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != agentEventBatchMax {
		t.Fatalf("batch length = %d, want %d", len(batch), agentEventBatchMax)
	}
}

func TestWaitForAgentEventDoesNotDelayNonStreamingEvent(t *testing.T) {
	ch := make(chan agent.AgentEvent, agentEventBatchMax)
	ch <- agent.IdleEvent{}
	cmd := waitForAgentEvent(ch)

	start := time.Now()
	msg := cmd()
	if elapsed := time.Since(start); elapsed >= agentEventStreamBatchWindow/2 {
		t.Fatalf("waitForAgentEvent delayed non-streaming event by %s", elapsed)
	}
	batch, ok := msg.(agentEventBatchMsg)
	if !ok {
		t.Fatalf("waitForAgentEvent() = %T, want agentEventBatchMsg", msg)
	}
	if len(batch) != 1 {
		t.Fatalf("batch length = %d, want 1", len(batch))
	}
}

func BenchmarkWaitForAgentEventStreamTextMicroBatch(b *testing.B) {
	for b.Loop() {
		ch := make(chan agent.AgentEvent, agentEventBatchMax)
		for range agentEventBatchMax {
			ch <- agent.StreamTextEvent{Text: "x"}
		}
		msg := waitForAgentEvent(ch)()
		batch := msg.(agentEventBatchMsg)
		if len(batch) != agentEventBatchMax {
			b.Fatalf("batch length = %d, want %d", len(batch), agentEventBatchMax)
		}
	}
}

func BenchmarkWaitForAgentEventPacedStreamTextMicroBatch(b *testing.B) {
	const events = 5
	var totalBatches int
	var totalEvents int
	for b.Loop() {
		completed := totalEvents
		ch := make(chan agent.AgentEvent, events)
		ch <- agent.StreamTextEvent{Text: "0"}
		go func() {
			for j := 1; j < events; j++ {
				time.Sleep(agentEventStreamBatchWindow / events)
				ch <- agent.StreamTextEvent{Text: "x"}
			}
		}()
		msg := waitForAgentEvent(ch)()
		batch := msg.(agentEventBatchMsg)
		totalBatches++
		totalEvents += len(batch)
		for totalEvents < completed+events {
			msg := waitForAgentEvent(ch)()
			batch := msg.(agentEventBatchMsg)
			totalBatches++
			totalEvents += len(batch)
		}
	}
	b.ReportMetric(float64(totalEvents)/float64(totalBatches), "events/batch")
	b.ReportMetric(float64(totalBatches)/float64(totalEvents), "batches/event")
}

func TestActivityAnchorHoldsAcrossRetryCycle(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	send := func(typ agent.ActivityType) {
		t.Helper()
		_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{AgentID: "main", Type: typ}})
	}
	send(agent.ActivityConnecting)
	start := m.activityStartTime["main"]
	if start.IsZero() {
		t.Fatal("connecting did not set the elapsed anchor")
	}
	// Alternation inside one connect/retry cycle holds the anchor even when
	// every flip lands outside the 100ms debounce window (a slow flip pair, or
	// a zero-backoff failure storm rendering at batch granularity).
	time.Sleep(150 * time.Millisecond)
	send(agent.ActivityRetrying)
	if got := m.activityStartTime["main"]; !got.Equal(start) {
		t.Fatalf("retrying moved the elapsed anchor: %v -> %v", start, got)
	}
	time.Sleep(150 * time.Millisecond)
	send(agent.ActivityRetryingKey)
	if got := m.activityStartTime["main"]; !got.Equal(start) {
		t.Fatalf("key retry moved the elapsed anchor: %v -> %v", start, got)
	}
	time.Sleep(150 * time.Millisecond)
	send(agent.ActivityConnecting)
	if got := m.activityStartTime["main"]; !got.Equal(start) {
		t.Fatalf("reconnecting moved the elapsed anchor: %v -> %v", start, got)
	}
	// Leaving the cycle starts a new phase anchor.
	time.Sleep(150 * time.Millisecond)
	send(agent.ActivityWaitingHeaders)
	if got := m.activityStartTime["main"]; !got.After(start) {
		t.Fatalf("leaving the retry cycle did not move the elapsed anchor: %v -> %v", start, got)
	}
}

func TestStatusBarRetryCycleMergesConnectingFlips(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	send := func(typ agent.ActivityType, deadline time.Time) {
		t.Helper()
		_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{AgentID: "main", Type: typ, Deadline: deadline}})
	}
	now := time.Now()
	send(agent.ActivityRetrying, time.Time{})
	send(agent.ActivityConnecting, time.Time{})
	// The connecting flip renders as the cycle's retry state, not the
	// connecting spinner, so a fast failure loop cannot flap the glyph.
	got := terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}, 80, now))
	if !strings.Contains(got, "↻") {
		t.Fatalf("connecting flip inside the cycle rendered %q, want the retry glyph", got)
	}
	if strings.Contains(got, string([]rune(statusBarConnectingFrames[0]))) {
		t.Fatalf("connecting flip inside the cycle rendered the connecting spinner: %q", got)
	}
	// A retry state with a promised wait counts down through the flips.
	send(agent.ActivityRetrying, now.Add(30*time.Second))
	send(agent.ActivityConnecting, time.Time{})
	got = terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}, 80, now))
	if !strings.Contains(got, "↻") || !strings.Contains(got, "30s") {
		t.Fatalf("retry countdown lost across the connecting flip: %q", got)
	}
	// A response arriving leaves the cycle: the real state shows immediately.
	send(agent.ActivityWaitingHeaders, time.Time{})
	got = terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityWaitingHeaders}, 80, now))
	if !strings.Contains(got, "◷") {
		t.Fatalf("waiting-for-headers did not take over the lane: %q", got)
	}
	// A fresh request's first connecting is not masked by a stale cycle.
	send(agent.ActivityExecuting, time.Time{})
	send(agent.ActivityConnecting, time.Time{})
	got = terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}, 80, now))
	if strings.Contains(got, "↻") {
		t.Fatalf("first connecting of a new request rendered the stale retry state: %q", got)
	}
}

// TestRetryCycleDisplayClearedOnIdlePaths pins the reliable terminal paths of a
// request: whatever ends the agent's activity (turn idle, global idle, or any
// other markAgentIdle caller — agent done, status suspend, connection close)
// must drop the retry-cycle display substitution. A survivor would mask the
// next request's first connecting with the previous cycle's countdown.
func TestRetryCycleDisplayClearedOnIdlePaths(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	send := func(typ agent.ActivityType) {
		t.Helper()
		_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{AgentID: "main", Type: typ}})
	}
	assertCleared := func(stage string) {
		t.Helper()
		if _, ok := m.retryCycleDisplay["main"]; ok {
			t.Fatalf("%s left a stale retry-cycle display", stage)
		}
		send(agent.ActivityConnecting)
		got := terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}, 80, time.Now()))
		if strings.Contains(got, "↻") {
			t.Fatalf("%s: the next request's connecting rendered the stale retry state: %q", stage, got)
		}
	}

	send(agent.ActivityRetrying)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})
	assertCleared("IdleEvent")

	send(agent.ActivityRetrying)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.GlobalIdleEvent{}})
	assertCleared("GlobalIdleEvent")

	send(agent.ActivityRetrying)
	m.markAgentIdle("")
	assertCleared("markAgentIdle")
}

// TestRetryCycleDisplayMovesWithAgentRename covers the agent-identity handoff:
// the substitution is keyed by agent ID, so it must follow the renamed agent
// while its cycle is live and must not leak across the handoff otherwise.
func TestRetryCycleDisplayMovesWithAgentRename(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	send := func(agentID string, typ agent.ActivityType, deadline time.Time) {
		t.Helper()
		_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{AgentID: agentID, Type: typ, Deadline: deadline}})
	}
	rename := func(previous, next string) {
		t.Helper()
		_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStartedEvent{AgentID: next, PreviousAgentID: previous}})
	}

	send("sub-1", agent.ActivityRetrying, time.Now().Add(30*time.Second))
	rename("sub-1", "sub-2")
	if _, ok := m.retryCycleDisplay["sub-1"]; ok {
		t.Fatal("rename left the substitution on the old agent ID")
	}
	got := terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "sub-2", Type: agent.ActivityConnecting}, 80, time.Now()))
	if !strings.Contains(got, "↻") || !strings.Contains(got, "30s") {
		t.Fatalf("renamed agent lost the live cycle countdown: %q", got)
	}

	// An activity outside the cycle ends it: the handoff drops both sides.
	send("sub-2", agent.ActivityExecuting, time.Time{})
	rename("sub-2", "sub-3")
	if _, ok := m.retryCycleDisplay["sub-2"]; ok {
		t.Fatal("rename left the substitution on the ended cycle's agent ID")
	}
	send("sub-3", agent.ActivityRetrying, time.Now().Add(30*time.Second))
	rename("sub-3", "sub-4")
	if _, ok := m.retryCycleDisplay["sub-3"]; ok {
		t.Fatal("rename left a live retry substitution on the old agent ID")
	}
	got = terminaltext.StripANSI(m.renderActivityAt(agent.AgentActivityEvent{AgentID: "sub-4", Type: agent.ActivityConnecting}, 80, time.Now()))
	if !strings.Contains(got, "↻") {
		t.Fatalf("retry activity carried across the rename lost the substitution: %q", got)
	}
}
