package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/recovery"
)

func takeoverScreenRows(scr uv.ScreenBuffer, w, h int) []string {
	var b strings.Builder
	for y := range h {
		line := scr.Line(y)
		for x := 0; x < w && x < len(line); x++ {
			r := line[x].Content
			if r == "" {
				r = " "
			}
			b.WriteString(r)
		}
		b.WriteByte('\n')
	}
	lines := strings.Split(b.String(), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// A submitted /takeover must reach the screen. The status lane's cache key used
// to ignore takeover state entirely, so the wait text was generated correctly
// and then dropped by a cache hit.
func TestTakeoverWaitingReachesTheStatusBar(t *testing.T) {
	m, _ := newTakeoverTestModel()
	m.SetTakeoverHandler(func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error) {
		t.Error("handler ran before the command goroutine was driven")
		return recovery.SessionPauseStatus{}, nil
	})
	const w, h = 140, 30

	s0 := newScreenBuffer(w, h)
	m.Draw(s0, s0.Bounds())
	if rows := takeoverScreenRows(s0, w, h); strings.Contains(strings.Join(rows, "\n"), "Waiting for session transfer") {
		t.Fatal("waiting hint appeared before the command started")
	}

	cmd := m.startTakeoverCommand("/takeover other-session --timeout 30s")
	if cmd == nil || !m.takeover.waiting {
		t.Fatal("takeover did not enter the waiting state")
	}

	s1 := newScreenBuffer(w, h)
	m.Draw(s1, s1.Bounds())
	rows := strings.Join(takeoverScreenRows(s1, w, h), "\n")
	if !strings.Contains(rows, "Waiting for session transfer") {
		t.Fatalf("waiting hint never reached the screen; status row = %q", takeoverScreenRows(s1, w, h)[h-1])
	}
}

// The elapsed counter must advance once per second while waiting, otherwise the
// lane reads as a frozen "12s" instead of a live wait.
func TestTakeoverWaitingElapsedAdvances(t *testing.T) {
	m, _ := newTakeoverTestModel()
	m.SetTakeoverHandler(func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{}, nil
	})
	m.startTakeoverCommand("/takeover other-session --timeout 30s")
	if !m.takeover.waiting {
		t.Fatal("takeover did not enter the waiting state")
	}

	base := time.Now()
	m.takeover.startedAt = base.Add(-12 * time.Second)
	if got := m.takeoverStatusTextAt(base); got != "Waiting for session transfer · 12s" {
		t.Fatalf("statusText = %q", got)
	}
	if got := m.takeoverStatusTextAt(base.Add(9 * time.Second)); got != "Waiting for session transfer · 21s" {
		t.Fatalf("statusText after 9s = %q", got)
	}
	// Sub-second and negative anchors must not produce a negative duration.
	if got := m.takeoverStatusTextAt(base.Add(-30 * time.Second)); strings.Contains(got, "-") {
		t.Fatalf("statusText showed a negative elapsed: %q", got)
	}
}

// A wait with no observation budget still has to tick: takeover deliberately
// leaves the focused agent idle, so nothing else schedules a refresh.
func TestTakeoverWaitingSchedulesStatusBarTick(t *testing.T) {
	m, _ := newTakeoverTestModel()
	m.SetTakeoverHandler(func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{}, nil
	})
	now := time.Now()
	if delay := m.statusBarNextRefreshDelayAt(now); delay != 0 {
		t.Fatalf("idle delay = %v, want 0s", delay)
	}
	m.startTakeoverCommand("/takeover other-session")
	if !m.takeover.waiting {
		t.Fatal("takeover did not enter the waiting state")
	}
	if delay := m.statusBarNextRefreshDelayAt(now); delay <= 0 || delay > time.Second {
		t.Fatalf("waiting delay = %v, want (0, 1s]", delay)
	}

	// The observing state (budget expired, source still releasing) must keep
	// reporting, so it also has to keep ticking.
	m.takeover.waiting = false
	m.takeover.observing = true
	if delay := m.statusBarNextRefreshDelayAt(now); delay <= 0 || delay > time.Second {
		t.Fatalf("observing delay = %v, want (0, 1s]", delay)
	}
}

// An expired observation budget is not a failure and not a terminal state: the
// source keeps releasing, so the UI must keep saying so instead of going idle.
func TestTakeoverExpiredObservationStaysVisible(t *testing.T) {
	m, backend := newTakeoverTestModel()
	m.SetTakeoverHandler(func(_ context.Context, sid string, _ *time.Duration) (recovery.SessionPauseStatus, error) {
		// Mirrors cmd/chord startTakeover: err is nil, WaitExpired is set, and
		// the source has not released yet.
		return recovery.SessionPauseStatus{
			SessionID: sid, TransferID: "transfer-1",
			State:       recovery.SessionPauseDraining,
			WaitExpired: true,
		}, nil
	})
	const w, h = 140, 30

	m.Update(m.startTakeoverCommand("/takeover other-session --timeout 1s")())
	if m.takeover.waiting {
		t.Fatal("still marked as waiting after the result applied")
	}
	if backend.resumeCount != 0 {
		t.Fatal("an unreleased source was activated after the budget expired")
	}
	text := m.takeoverStatusText()
	if !strings.Contains(text, "still handing off") {
		t.Fatalf("expired observation lost its status: %q", text)
	}

	s := newScreenBuffer(w, h)
	m.Draw(s, s.Bounds())
	if rows := strings.Join(takeoverScreenRows(s, w, h), "\n"); !strings.Contains(rows, "still handing off") {
		t.Fatalf("expired observation is not on screen; status row = %q", takeoverScreenRows(s, w, h)[h-1])
	}

	// A terminal source state retires the observation.
	m.handleAgentEvent(agentEventMsg{event: agent.SessionTransferEvent{Status: recovery.SessionPauseStatus{
		TransferIdentity: recovery.TransferIdentity{SessionID: "source-session", TransferID: "transfer-1"},
		State:            recovery.SessionPauseReleased,
	}}})
	if m.takeover.observing {
		t.Fatal("released source left the observation banner active")
	}
}

// A timeout is reported as information, not as a failure.
func TestTakeoverExpiredObservationIsNotWarned(t *testing.T) {
	m, _ := newTakeoverTestModel()
	m.SetTakeoverHandler(func(context.Context, string, *time.Duration) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{
			SessionID: "other-session", TransferID: "transfer-1",
			State:       recovery.SessionPauseDraining,
			WaitExpired: true,
		}, nil
	})
	m.Update(m.startTakeoverCommand("/takeover other-session --timeout 1s")())
	if m.activeToast == nil {
		t.Fatal("no guidance after the observation budget ended")
	}
	if level := strings.ToLower(m.activeToast.Level); level == "warn" || level == "error" {
		t.Fatalf("timeout reported as %q: %q", level, m.activeToast.Message)
	}
	if !strings.Contains(m.activeToast.Message, "chord takeover status") {
		t.Fatalf("guidance does not tell the user how to follow up: %q", m.activeToast.Message)
	}
}

// The idle path must stay byte-identical: no takeover state means no lane, so
// the fingerprint stays free of per-second churn.
func TestTakeoverIdleAddsNoStatusBarChurn(t *testing.T) {
	m, _ := newTakeoverTestModel()
	now := time.Now()
	base := m.statusBarFingerprint(now)
	for _, offset := range []time.Duration{200 * time.Millisecond, 500 * time.Millisecond} {
		if got := m.statusBarFingerprint(now.Add(offset)); got != base {
			t.Fatalf("idle fingerprint changed after %v", offset)
		}
	}
	if text := m.takeoverStatusText(); text != "" {
		t.Fatalf("idle takeover status = %q", text)
	}
}

// enterTakeoverObservation drives a /takeover whose observation budget expired
// while the source was still handing off, the state the status bar then has to
// confirm on its own.
func enterTakeoverObservation(t *testing.T, m Model) Model {
	t.Helper()
	m.SetTakeoverHandler(func(_ context.Context, sid string, _ *time.Duration) (recovery.SessionPauseStatus, error) {
		return recovery.SessionPauseStatus{
			SessionID: sid, TransferID: "transfer-1",
			State:       recovery.SessionPauseDraining,
			WaitExpired: true,
		}, nil
	})
	m.Update(m.startTakeoverCommand("/takeover other-session --timeout 1s")())
	if !m.takeover.observing {
		t.Fatal("expired budget did not start observing")
	}
	return m
}

func takeoverObserveIdentity(sid string) recovery.SessionPauseStatus {
	return recovery.SessionPauseStatus{SessionID: sid, TransferID: "transfer-1"}
}

// The wait handler stops watching when the budget expires, but the transfer
// does not stop: the status bar must confirm the release itself and run the
// same activation the wait path would have run.
func TestTakeoverObservationCompletesReleaseAfterBudget(t *testing.T) {
	m, backend := newTakeoverTestModel()
	queries := 0
	m.SetTakeoverStatusHandler(func(_ context.Context, sid, transferID string) (recovery.SessionPauseStatus, error) {
		queries++
		if sid != "other-session" || transferID != "transfer-1" {
			t.Fatalf("status query identity = %q/%q", sid, transferID)
		}
		if queries == 1 {
			status := takeoverObserveIdentity(sid)
			status.State = recovery.SessionPauseSaving
			return status, nil
		}
		status := takeoverObserveIdentity(sid)
		status.State = recovery.SessionPauseReleased
		return status, nil
	})
	m = enterTakeoverObservation(t, m)

	now := time.Now()
	cmd := m.pollTakeoverObservation(now)
	if cmd == nil {
		t.Fatal("no status query scheduled while observing")
	}
	if again := m.pollTakeoverObservation(now); again != nil {
		t.Fatal("status query ignored its interval")
	}
	m.Update(cmd())
	if !m.takeover.observing {
		t.Fatal("a still-saving source retired the banner")
	}

	cmd = m.pollTakeoverObservation(now.Add(takeoverObserveInterval + time.Millisecond))
	if cmd == nil {
		t.Fatal("status query never rescheduled")
	}
	msg := cmd().(takeoverObserveMsg)
	if msg.err != nil || msg.status.State != recovery.SessionPauseReleased {
		t.Fatalf("status query = %+v", msg)
	}
	_, activation := m.Update(msg)
	if activation == nil {
		t.Fatal("released source did not run the activation")
	}
	m.Update(activation())
	if m.takeover.observing {
		t.Fatal("released source left the observation banner active")
	}
	if backend.resumeCount != 1 {
		t.Fatalf("resume count = %d, want the confirmed activation", backend.resumeCount)
	}
	if backend.resumeIdentity != (recovery.TransferIdentity{SessionID: "other-session", TransferID: "transfer-1"}) {
		t.Fatalf("resume identity = %+v", backend.resumeIdentity)
	}
	if queries != 2 {
		t.Fatalf("status queries = %d, want 2", queries)
	}
}

// A save failure keeps the source alive and retryable, so the banner must
// survive it and still complete a later release.
func TestTakeoverObservationSurvivesSaveFailureAndCompletesRetry(t *testing.T) {
	m, backend := newTakeoverTestModel()
	queries := 0
	m.SetTakeoverStatusHandler(func(_ context.Context, sid, _ string) (recovery.SessionPauseStatus, error) {
		queries++
		status := takeoverObserveIdentity(sid)
		switch queries {
		case 1:
			status.State = recovery.SessionPauseSaveFailed
			status.RecoverableAction = recovery.SessionPauseActionRetrySave
		default:
			status.State = recovery.SessionPauseReleased
		}
		return status, nil
	})
	m = enterTakeoverObservation(t, m)

	now := time.Now()
	m.Update(m.pollTakeoverObservation(now)())
	if !m.takeover.observing {
		t.Fatal("save failure retired the banner")
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "retry-save") {
		t.Fatalf("save failure toast = %+v", m.activeToast)
	}
	if strings.ToLower(m.activeToast.Level) != "warn" {
		t.Fatalf("save failure level = %q", m.activeToast.Level)
	}

	cmd := m.pollTakeoverObservation(now.Add(takeoverObserveInterval + time.Millisecond))
	msg := cmd().(takeoverObserveMsg)
	_, activation := m.Update(msg)
	m.Update(activation())
	if m.takeover.observing || backend.resumeCount != 1 {
		t.Fatal("release after a save failure did not complete")
	}
}

// A source that stops answering must not leave the banner ticking forever:
// after a bounded number of unanswered queries the banner retires and keeps
// the CLI query as the way out.
func TestTakeoverObservationRetiresWhenSourceStopsAnswering(t *testing.T) {
	m, backend := newTakeoverTestModel()
	queries := 0
	m.SetTakeoverStatusHandler(func(_ context.Context, _, _ string) (recovery.SessionPauseStatus, error) {
		queries++
		return recovery.SessionPauseStatus{}, fmt.Errorf("status_unknown: source status is unknown")
	})
	m = enterTakeoverObservation(t, m)

	now := time.Now()
	for i := range takeoverObserveFailureLimit {
		now = now.Add(takeoverObserveInterval + time.Millisecond)
		cmd := m.pollTakeoverObservation(now)
		if cmd == nil {
			t.Fatal("no status query scheduled while observing")
		}
		m.Update(cmd())
		if i < takeoverObserveFailureLimit-1 && !m.takeover.observing {
			t.Fatal("banner retired before the failure limit")
		}
	}
	if m.takeover.observing {
		t.Fatal("banner never retired")
	}
	if queries != takeoverObserveFailureLimit {
		t.Fatalf("status queries = %d, want %d", queries, takeoverObserveFailureLimit)
	}
	if m.activeToast == nil || !strings.Contains(m.activeToast.Message, "chord takeover status") {
		t.Fatalf("retirement toast = %+v", m.activeToast)
	}
	if strings.ToLower(m.activeToast.Level) != "warn" {
		t.Fatalf("retirement level = %q", m.activeToast.Level)
	}
	if backend.resumeCount != 0 {
		t.Fatal("unconfirmed transfer activated the session")
	}
}

// An answered query clears the failure count, so a source that answers late
// still completes instead of being retired by earlier unanswered queries.
func TestTakeoverObservationAnswerResetsFailureCount(t *testing.T) {
	m, backend := newTakeoverTestModel()
	queries := 0
	m.SetTakeoverStatusHandler(func(_ context.Context, sid, _ string) (recovery.SessionPauseStatus, error) {
		queries++
		if queries == 1 {
			return recovery.SessionPauseStatus{}, fmt.Errorf("status_unknown: source status is unknown")
		}
		status := takeoverObserveIdentity(sid)
		status.State = recovery.SessionPauseDraining
		return status, nil
	})
	m = enterTakeoverObservation(t, m)

	now := time.Now()
	m.Update(m.pollTakeoverObservation(now)())
	m.Update(m.pollTakeoverObservation(now.Add(takeoverObserveInterval + time.Millisecond))())
	if !m.takeover.observing {
		t.Fatal("answered source retired the banner")
	}
	// The answered query reset the count, so two further failures must not
	// reach the limit.
	for range takeoverObserveFailureLimit - 1 {
		now = now.Add(takeoverObserveInterval + time.Millisecond)
		m.Update(m.pollTakeoverObservation(now)())
	}
	if !m.takeover.observing {
		t.Fatal("banner retired without consecutive failures")
	}
	if backend.resumeCount != 0 {
		t.Fatal("draining source activated the session")
	}
}

// The observation poll must stay inert outside the observing state, and a
// result from a superseded takeover must not touch the live one.
func TestTakeoverObservationPollIsScopedToTheObservingTransfer(t *testing.T) {
	m, _ := newTakeoverTestModel()
	now := time.Now()
	if m.pollTakeoverObservation(now) != nil {
		t.Fatal("poll scheduled while idle")
	}
	m.takeover.observing = true
	if m.pollTakeoverObservation(now) != nil {
		t.Fatal("poll scheduled without a status handler")
	}
	m.SetTakeoverStatusHandler(func(context.Context, string, string) (recovery.SessionPauseStatus, error) {
		t.Error("status handler ran outside the observing state")
		return recovery.SessionPauseStatus{}, nil
	})
	m.takeover.observing = false
	m.startTakeoverCommand("/takeover other-session --timeout 1s")
	if m.pollTakeoverObservation(now) != nil {
		t.Fatal("poll scheduled for the in-budget wait")
	}

	m.takeover.observing = true
	stale := takeoverObserveMsg{sequence: m.takeover.sequence + 1, status: takeoverObserveIdentity("other-session")}
	stale.status.State = recovery.SessionPauseReleased
	if cmd := m.applyTakeoverObservation(stale); cmd != nil {
		t.Fatal("stale observation produced a command")
	}
	if !m.takeover.observing {
		t.Fatal("stale observation changed the live banner")
	}
}

// The poll rides on the status bar tick, so the wiring between them is what
// makes a real session confirm its release: without it the banner would still
// only ever be retired by hand.
func TestTakeoverObservationRunsOnStatusBarTick(t *testing.T) {
	m, backend := newTakeoverTestModel()
	queried := make(chan struct{}, 1)
	m.SetTakeoverStatusHandler(func(_ context.Context, sid, transferID string) (recovery.SessionPauseStatus, error) {
		if sid != "other-session" || transferID != "transfer-1" {
			t.Errorf("status query identity = %q/%q", sid, transferID)
		}
		status := takeoverObserveIdentity(sid)
		status.State = recovery.SessionPauseSaving
		select {
		case queried <- struct{}{}:
		default:
		}
		return status, nil
	})
	m = enterTakeoverObservation(t, m)

	_, cmd := m.Update(statusBarTickMsg{generation: m.statusBarTickGeneration})
	if cmd == nil {
		t.Fatal("status bar tick produced no command while observing")
	}
	for _, msg := range runCmdTree(cmd) {
		m.Update(msg)
	}
	select {
	case <-queried:
	default:
		t.Fatal("status bar tick did not query the observed transfer")
	}
	if !m.takeover.observing || backend.resumeCount != 0 {
		t.Fatal("a still-saving source changed the observation state")
	}
}
