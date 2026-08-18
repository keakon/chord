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
	if later := m.statusBarFingerprint(now.Add(visualSpinnerCadence)); later == m.statusBarFingerprint(now) {
		t.Fatal("status bar fingerprint did not change with compaction animation frame")
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
