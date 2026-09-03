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

func TestCompactionSkippedStatusShowsTerminalReason(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-time.Second),
		Bytes:     128,
		Events:    2,
	}

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{
		Status:  agent.CompactionStatusSkipped,
		Trigger: "model_driven",
		Reason:  "projected savings 500 tokens is below the low-gain gate",
	}})

	if m.compactionBgStatus.Active {
		t.Fatal("compaction status after skip = active, want terminal flush")
	}
	if m.compactionBgStatus.Terminal != agent.CompactionStatusSkipped {
		t.Fatalf("compaction status after skip = %q, want skipped terminal", m.compactionBgStatus.Terminal)
	}
	if m.compactionBgStatus.Trigger != "model_driven" {
		t.Fatalf("compaction trigger after skip = %q, want model_driven", m.compactionBgStatus.Trigger)
	}
	got := stripANSI(m.renderCompactionBackgroundPill(time.Now()))
	if !strings.Contains(got, "skipped") && !strings.Contains(got, "low-gain") {
		t.Fatalf("compaction pill after skip = %q, want terminal reason", got)
	}
	if m.compactionBgStatus.TerminalAt.IsZero() {
		t.Fatal("compaction status after skip has no terminal timestamp")
	}
}

func TestCompactionCancelledStatusShowsTerminalReason(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-time.Second),
		Bytes:     128,
		Events:    2,
	}

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{
		Status:  agent.CompactionStatusCancelled,
		Trigger: "model_driven",
		Reason:  "the requesting turn is no longer active",
	}})

	if m.compactionBgStatus.Active {
		t.Fatal("compaction status after cancel = active, want terminal flush")
	}
	if m.compactionBgStatus.Terminal != agent.CompactionStatusCancelled {
		t.Fatalf("compaction status after cancel = %q, want cancelled terminal", m.compactionBgStatus.Terminal)
	}
	if m.compactionBgStatus.Trigger != "model_driven" {
		t.Fatalf("compaction trigger after cancel = %q, want model_driven", m.compactionBgStatus.Trigger)
	}
	if m.compactionBgStatus.TerminalAt.IsZero() {
		t.Fatal("compaction status after cancel has no terminal timestamp")
	}
	got := stripANSI(m.renderCompactionBackgroundPill(time.Now()))
	if !strings.Contains(got, "✕") || !strings.Contains(got, "no longer active") {
		t.Fatalf("compaction pill after cancel = %q, want void icon and terminal reason", got)
	}
	// The terminal flush expires like every other terminal outcome.
	if pill := m.renderCompactionBackgroundPill(time.Now().Add(compactionStatusTerminalDuration)); pill != "" {
		t.Fatalf("expired cancelled pill = %q, want empty", stripANSI(pill))
	}
}

func TestCompactionModelDrivenStartedShowsLabel(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Now()

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted, Trigger: "model_driven"}})
	if !m.compactionBgStatus.Active {
		t.Fatal("compaction pill not armed by started status")
	}
	if m.compactionBgStatus.Trigger != "model_driven" {
		t.Fatalf("compaction trigger = %q, want model_driven", m.compactionBgStatus.Trigger)
	}
	got := stripANSI(m.renderCompactionBackgroundPill(now))
	if !strings.Contains(got, "model checkpoint") {
		t.Fatalf("compaction pill for model-driven = %q, want model checkpoint label", got)
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

// A live compaction indicator must survive beside a busy foreground request.
// The right-aligned group is placed with its left edge against the centered
// activity lane; if path/session are budgeted without reserving the pill, the
// placed line slices the right block's left edge and the whole compaction
// indicator disappears on narrower terminals for the full compaction duration.
func TestCompactionPillSurvivesBusyActivityLane(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Unix(1_700_000_000, 0)
	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: now.Add(-4 * time.Second)}

	const (
		effectiveWidth = 100
		leftWidth      = 24
		activityWidth  = 22
	)
	rightSide, rightStart, _ := m.renderStatusBarRightSide(now, effectiveWidth, leftWidth, activityWidth, "/Users/keakon/Workspace/chord", "20260902024540990")

	centerStart := max((effectiveWidth-activityWidth)/2, leftWidth+2)
	centerEnd := centerStart + activityWidth
	if rightStart < centerEnd {
		t.Fatalf("compaction pill collides with the busy activity lane: rightStart=%d centerEnd=%d", rightStart, centerEnd)
	}
	line := renderStatusBarPlacedLine("", 0, rightStart, rightSide, "", activityWidth, effectiveWidth)
	if plain := stripANSI(line); !strings.Contains(plain, "▪") && !strings.Contains(plain, "■") {
		t.Fatalf("status bar lost the compaction indicator beside a busy activity lane: %q", plain)
	}
}

// Regression: the synchronous interval/cooldown skip of a model-driven
// request emits a synthetic started + skipped pair that never occupies the
// compaction slot. While a compaction is running, the pair must not overwrite
// the pill — the running compaction keeps showing until its own terminal
// arrives.
func TestCompactionSyntheticSkipDoesNotOverwriteRunningSlot(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)

	// A usage-driven compaction owns the slot.
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted, Trigger: "usage_driven", PlanID: "11"}})
	if !m.compactionBgStatus.Active || m.compactionBgStatus.PlanID != "11" {
		t.Fatalf("usage-driven started must arm the slot, got %+v", m.compactionBgStatus)
	}

	// Sync-skip pair for a model-driven request rejected on interval/cooldown:
	// synthetic started is ignored and its skipped terminal does not match the
	// running plan.
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted, Trigger: "model_driven", PlanID: "12", Synthetic: true}})
	if !m.compactionBgStatus.Active || m.compactionBgStatus.PlanID != "11" || m.compactionBgStatus.Trigger != "usage_driven" || m.compactionBgStatus.Terminal != "" {
		t.Fatalf("synthetic started must not touch the running slot, got %+v", m.compactionBgStatus)
	}
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusSkipped, Trigger: "model_driven", PlanID: "12", Reason: "minimum 3-request-batch interval"}})
	if !m.compactionBgStatus.Active || m.compactionBgStatus.Terminal != "" {
		t.Fatalf("foreign skipped terminal must not resolve the running slot, got %+v", m.compactionBgStatus)
	}

	// The running compaction's own progress still updates the pill...
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusProgress, Bytes: 2048, Events: 5}})
	if !m.compactionBgStatus.Active || m.compactionBgStatus.Bytes != 2048 {
		t.Fatalf("running compaction progress must keep updating the pill, got %+v", m.compactionBgStatus)
	}
	// ...and its own terminal resolves it.
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusSucceeded, PlanID: "11"}})
	if m.compactionBgStatus.Active || m.compactionBgStatus.Terminal != agent.CompactionStatusSucceeded {
		t.Fatalf("owning terminal must resolve the running slot, got %+v", m.compactionBgStatus)
	}
}

// A lone synthetic skip on an idle slot still surfaces: the synthetic started
// does not arm the pill, and the skipped terminal applies because no
// compaction is running.
func TestCompactionSyntheticSkipShowsOnIdleSlot(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted, Trigger: "model_driven", PlanID: "5", Synthetic: true}})
	if m.compactionBgStatus.Active {
		t.Fatalf("synthetic started must not arm an idle pill, got %+v", m.compactionBgStatus)
	}
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusSkipped, Trigger: "model_driven", PlanID: "5", Reason: "minimum 3-request-batch interval"}})
	if m.compactionBgStatus.Terminal != agent.CompactionStatusSkipped {
		t.Fatalf("idle-slot skip terminal must be shown, got %+v", m.compactionBgStatus)
	}
	got := stripANSI(m.renderCompactionBackgroundPill(time.Now()))
	if !strings.Contains(got, "minimum 3-request-batch interval") {
		t.Fatalf("idle-slot synthetic skip pill = %q, want the skip reason", got)
	}
}

// A real (non-synthetic) started replaces the running slot: a model-driven
// checkpoint that proceeded past the policy verdict has discarded the running
// automatic draft, so its started takes over and the superseded plan's
// late-arriving terminal is ignored.
func TestCompactionRealStartedReplacesRunningSlot(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)

	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted, Trigger: "usage_driven", PlanID: "1"}})
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusStarted, Trigger: "model_driven", PlanID: "2"}})
	if !m.compactionBgStatus.Active || m.compactionBgStatus.PlanID != "2" || m.compactionBgStatus.Trigger != "model_driven" {
		t.Fatalf("real started must take over the slot, got %+v", m.compactionBgStatus)
	}
	// The superseded plan's terminal (should be stale-guarded agent-side, but
	// a straggler must not resolve the slot it no longer owns).
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusSucceeded, PlanID: "1"}})
	if !m.compactionBgStatus.Active || m.compactionBgStatus.Terminal != "" {
		t.Fatalf("superseded terminal must be ignored, got %+v", m.compactionBgStatus)
	}
	m.handleAgentEvent(agentEventMsg{event: agent.CompactionStatusEvent{Status: agent.CompactionStatusSucceeded, PlanID: "2"}})
	if m.compactionBgStatus.Active || m.compactionBgStatus.Terminal != agent.CompactionStatusSucceeded {
		t.Fatalf("owning terminal must resolve the slot, got %+v", m.compactionBgStatus)
	}
}

func TestCompactionLivePillShowsElapsed(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	now := time.Unix(1_700_000_000, 0)
	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: now.Add(-3 * time.Second)}

	got := stripANSI(m.renderCompactionBackgroundPill(now))
	if !strings.Contains(got, "3s") {
		t.Fatalf("live compaction pill = %q, want elapsed 3s", got)
	}
	if strings.Contains(got, "compacting") {
		t.Fatalf("live compaction pill = %q, should not render the literal \"compacting\" word", got)
	}
}
