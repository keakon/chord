package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestStatusBarProgressSamplesSurviveDrawInvalidation(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	now := time.Unix(100, 0)
	a := agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	m.activities["main"] = a
	m.activityStartTime["main"] = now
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 1024, VisibleEvents: 2}
	first := m.renderActivityAt(a, 80, now)
	fingerprint := m.statusBarFingerprint(now)
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 128 * 1024, VisibleEvents: 42}
	m.invalidateDrawCaches()
	if got := m.renderActivityAt(a, 80, now.Add(200*time.Millisecond)); got != first {
		t.Fatalf("numeric display changed inside the second: %q -> %q", first, got)
	}
	if got := m.statusBarFingerprint(now.Add(200 * time.Millisecond)); got != fingerprint {
		t.Fatal("raw progress invalidated the status fingerprint")
	}
	got := stripANSI(m.renderActivityAt(a, 80, now.Add(time.Second)))
	if !strings.Contains(got, "↓ 128 KB · 42 events · 1s") {
		t.Fatalf("next second did not show latest progress: %q", got)
	}
	// A different card in the same request immediately gets its own counter.
	progress := m.requestProgress["main"]
	progress.BaseBytes = 64 * 1024
	progress.BaseEvents = 10
	m.requestProgress["main"] = progress
	got = stripANSI(m.renderActivityAt(a, 80, now.Add(1100*time.Millisecond)))
	if !strings.Contains(got, "64 KB · 32 events") {
		t.Fatalf("new card retained previous card's counter: %q", got)
	}
	// A new request of the same type must not inherit the sampled display.
	m.activityStartTime["main"] = now.Add(1200 * time.Millisecond)
	m.requestProgress["main"] = requestProgressState{}
	got = stripANSI(m.renderActivityAt(a, 80, now.Add(1200*time.Millisecond)))
	if got != "↓ 0s" {
		t.Fatalf("new request retained previous sample: %q", got)
	}
	a.Type = agent.ActivityCooling
	a.Deadline = now.Add(5 * time.Second)
	got = stripANSI(m.renderActivityAt(a, 80, now.Add(1300*time.Millisecond)))
	if got != "⏸ 4s" {
		t.Fatalf("phase transition did not immediately update: %q", got)
	}
}

func TestStatusBarCompactionSamplesProgressButPublishesTerminalImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	now := time.Unix(100, 0)
	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: now, Bytes: 1024}
	first := m.renderCompactionBackgroundPill(now)
	m.compactionBgStatus.Bytes = 128 * 1024
	if got := m.renderCompactionBackgroundPill(now.Add(200 * time.Millisecond)); got != first {
		t.Fatalf("compaction progress changed inside the second: %q -> %q", first, got)
	}
	if got := stripANSI(m.renderCompactionBackgroundPill(now.Add(time.Second))); !strings.Contains(got, "128 KB") {
		t.Fatalf("compaction did not publish latest progress: %q", got)
	}
	m.compactionBgStatus.Active = false
	m.compactionBgStatus.Terminal = agent.CompactionStatusSucceeded
	m.compactionBgStatus.TerminalAt = now.Add(1100 * time.Millisecond)
	m.compactionBgStatus.Bytes = 256 * 1024
	if got := stripANSI(m.renderCompactionBackgroundPill(now.Add(1100 * time.Millisecond))); !strings.Contains(got, "✓") || !strings.Contains(got, "256 KB") {
		t.Fatalf("terminal compaction was delayed or lost final progress: %q", got)
	}
}

func TestStatusBarConnectingCadenceDoesNotChangeContentCadence(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.cadenceProfiles = defaultCadenceProfiles()
	now := time.Unix(100, 0)
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
	m.animRunning = true
	key := m.statusBarFingerprint(now)
	if later := m.statusBarFingerprint(now.Add(200 * time.Millisecond)); later != key {
		t.Fatal("connection footer followed the tool spinner tick")
	}
	if later := m.statusBarFingerprint(now.Add(500 * time.Millisecond)); later == key {
		t.Fatal("connection footer missed its half-second frame")
	}
	if got := m.statusBarNextRefreshDelayAt(now.Add(200 * time.Millisecond)); got != 300*time.Millisecond {
		t.Fatalf("connecting refresh delay = %v, want 300ms", got)
	}
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	if got := m.statusBarNextRefreshDelayAt(now.Add(200 * time.Millisecond)); got != 800*time.Millisecond {
		t.Fatalf("streaming refresh delay = %v, want 800ms", got)
	}
	if got := m.currentCadence().contentFlushDelay; got != 200*time.Millisecond {
		t.Fatalf("content cadence changed: %v", got)
	}
	m.cadenceProfiles = lowCadenceProfiles()
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
	if got := m.statusBarNextRefreshDelayAt(now.Add(200 * time.Millisecond)); got != 800*time.Millisecond {
		t.Fatalf("low-cadence footer scheduled visual frames: %v", got)
	}
}

func TestStatusBarProgressReusesPendingTickAndTransitionsAdvanceIt(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	now := time.Unix(100, 0)
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	if m.restartStatusBarTickAt(now) == nil {
		t.Fatal("streaming did not schedule its numeric refresh")
	}
	generation := m.statusBarTickGeneration
	for i := 1; i <= 9; i++ {
		m.requestProgress["main"] = requestProgressState{VisibleBytes: int64(i * 1024)}
		if m.restartStatusBarTickAt(now.Add(time.Duration(i)*50*time.Millisecond)) != nil {
			t.Fatal("progress created another timer before the pending refresh")
		}
	}
	if m.statusBarTickGeneration != generation || !m.statusBarTickAt.Equal(now.Add(time.Second)) {
		t.Fatal("progress postponed or replaced the pending refresh")
	}
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityConnecting}
	if m.restartStatusBarTickAt(now.Add(460*time.Millisecond)) == nil || !m.statusBarTickAt.Equal(now.Add(500*time.Millisecond)) {
		t.Fatal("connecting did not advance the refresh to its next frame")
	}
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	if m.restartStatusBarTickAt(now.Add(470*time.Millisecond)) != nil {
		t.Fatal("slower activity discarded an earlier pending refresh")
	}
}
