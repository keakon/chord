package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestCompactionStatusBarRightCacheAdvancesWithTimeFrame(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Unix(1_700_000_000, 0)
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: now,
	}

	first, _, _ := m.renderStatusBarRightSide(now, 120, 0, 0, "", "")
	firstKey := m.cachedStatusBarRightKey
	second, _, _ := m.renderStatusBarRightSide(now.Add(2*time.Second), 120, 0, 0, "", "")

	if firstKey == m.cachedStatusBarRightKey {
		t.Fatal("compaction right-side cache key did not advance with time")
	}
	if first == second {
		t.Fatalf("compaction right-side rendering did not refresh: first=%q second=%q", first, second)
	}
	if !strings.Contains(stripANSI(second), "2s") {
		t.Fatalf("refreshed compaction pill = %q, want elapsed 2s", stripANSI(second))
	}
}

func TestCompactionPillShowsBytesAndEvents(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	now := time.Unix(1_700_000_000, 0)

	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: now.Add(-5 * time.Second), Bytes: 1024, Events: 7}
	if got := stripANSI(m.renderCompactionBackgroundPill(now)); !strings.Contains(got, "↓ 1.0 KB · 7 events") {
		t.Fatalf("compaction pill = %q, want bytes and events suffix", got)
	}

	// Header-only progress carries bytes without events; either counter alone
	// must surface the suffix.
	m.compactionBgStatus.Bytes = 0
	m.compactionBgStatus.Events = 3
	if got := stripANSI(m.renderCompactionBackgroundPill(now)); !strings.Contains(got, "3 events") {
		t.Fatalf("compaction pill = %q, want events-only progress", got)
	}
}

func TestCompactionStatusProgressEventUpdatesPill(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	now := time.Now()

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted}})
	if !m.compactionBgStatus.Active {
		t.Fatal("compaction pill not armed by started status")
	}
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusProgress, Bytes: 2048, Events: 5}})
	got := stripANSI(m.renderCompactionBackgroundPill(now))
	if !strings.Contains(got, "2.0 KB · 5 events") {
		t.Fatalf("compaction pill after progress event = %q, want byte and event counts", got)
	}
}

func TestCompactionSkippedStatusClearsBackgroundPill(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-time.Second),
		Bytes:     128,
		Events:    2,
	}

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusSkipped}})

	if m.compactionBgStatus != (compactionBackgroundStatus{}) {
		t.Fatalf("compaction status after skip = %+v, want zero state", m.compactionBgStatus)
	}
	if got := m.renderCompactionBackgroundPill(time.Now()); got != "" {
		t.Fatalf("compaction pill after skip = %q, want empty", stripANSI(got))
	}
}

func TestStatusBarFingerprintTracksCompactionProgressAndTimeFrame(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Unix(1_700_000_000, 0)
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: now,
		Bytes:     8,
		Events:    1,
	}

	initial := m.statusBarFingerprint(now)
	m.compactionBgStatus.Bytes = 16
	if updated := m.statusBarFingerprint(now); updated == initial {
		t.Fatal("status bar fingerprint did not change with compaction progress")
	}
	if later := m.statusBarFingerprint(now.Add(compactionPillBreathPhase)); later == m.statusBarFingerprint(now) {
		t.Fatal("status bar fingerprint did not change with compaction animation frame")
	}
}

func TestIdleSinceHiddenWhileRequestProgressIsPending(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 1, VisibleEvents: 1}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "previous", StartedAt: time.Now().Add(-time.Minute)})

	if m.focusedAgentCanShowIdleSince() {
		t.Fatal("idle Since should be hidden while request progress is pending")
	}
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Since ") {
		t.Fatalf("status bar showed idle time with pending request progress: %q", plain)
	}
}

func TestCompactionIndicatorsUseSameAnimationPhase(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.UnixMilli(1_700_000_000_200)
	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: now.Add(-time.Second)}
	activity := agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}

	wantIcon := compactionPillIconAt(now)
	if got := m.buildStatusBarActivityDisplayAt(activity, now).Icon; got != wantIcon {
		t.Fatalf("foreground compaction icon = %q, want %q", got, wantIcon)
	}
	if got := stripANSI(m.renderCompactionBackgroundPill(now)); !strings.HasPrefix(got, wantIcon+" ") {
		t.Fatalf("background compaction pill = %q, want icon %q", got, wantIcon)
	}
}

func TestCompactionAnimationUsesCalmCadence(t *testing.T) {
	if compactionPillBreathPhase < time.Second {
		t.Fatalf("compaction animation cadence = %v, want at least 1s", compactionPillBreathPhase)
	}
}

func TestCompactionTerminalStatusRendersAndExpires(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Unix(1_700_000_000, 0)
	m.compactionBgStatus = compactionBackgroundStatus{
		StartedAt:  now.Add(-3 * time.Second),
		Terminal:   agent.CompactionStatusSucceeded,
		TerminalAt: now,
	}

	if got := stripANSI(m.renderCompactionBackgroundPill(now)); !strings.Contains(got, "✓ 3s") {
		t.Fatalf("terminal compaction pill = %q, want success status", got)
	}
	if delay := m.statusBarNextRefreshDelayAt(now); delay <= 0 {
		t.Fatalf("terminal refresh delay = %v, want positive", delay)
	}
	if got := m.renderCompactionBackgroundPill(now.Add(compactionStatusTerminalDuration)); got != "" {
		t.Fatalf("expired terminal compaction pill = %q, want empty", stripANSI(got))
	}
	if delay := m.statusBarNextRefreshDelayAt(now.Add(compactionStatusTerminalDuration)); delay != 0 {
		t.Fatalf("expired terminal refresh delay = %v, want 0", delay)
	}
}

// A finished compaction keeps Terminal set until the next one starts. Once the
// pill has expired the status bar must fall back to the normal idle cadence:
// treating the leftover terminal state as "nothing left to refresh" froze the
// idle timer for the rest of the session.
func TestExpiredCompactionTerminalStatusKeepsIdleRefresh(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Unix(1_700_000_000, 0)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "previous", StartedAt: now.Add(-time.Minute)})
	m.compactionBgStatus = compactionBackgroundStatus{
		StartedAt:  now.Add(-3 * time.Second),
		Terminal:   agent.CompactionStatusSucceeded,
		TerminalAt: now,
	}

	if !m.focusedAgentCanShowIdleSince() {
		t.Fatal("focusedAgentCanShowIdleSince() = false, want an idle agent with a prior block to show it")
	}
	delay := m.statusBarNextRefreshDelayAt(now.Add(compactionStatusTerminalDuration))
	if delay <= 0 || delay > time.Minute {
		t.Fatalf("expired terminal refresh delay = %v, want the idle minute cadence", delay)
	}
}
