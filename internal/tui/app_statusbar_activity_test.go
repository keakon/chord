package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func TestRenderActivitySummaryFallsBackToExistingLabels(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	got := m.renderActivityPrimaryText(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityWaitingHeaders})
	if got != "" {
		t.Fatalf("renderActivityPrimaryText = %q, want empty string for non-connecting non-progress state", got)
	}
}

func TestRenderExecutingSummaryShowsElapsed(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.activityStartTime["main"] = time.Now().Add(-12 * time.Second)
	got := m.renderExecutingSummary("main")
	if !strings.HasPrefix(got, "⚙ ") {
		t.Fatalf("renderExecutingSummary = %q, want activity glyph prefix", got)
	}
	if !strings.Contains(got, "12s") {
		t.Fatalf("renderExecutingSummary = %q, want elapsed seconds", got)
	}
}

func TestRenderExecutingSummaryStartsFromZero(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.activityStartTime["main"] = time.Now()
	got := m.renderExecutingSummary("main")
	if got != "⚙ · 0s" {
		t.Fatalf("renderExecutingSummary = %q, want activity glyph with zero elapsed", got)
	}
}

func TestRenderActivityExecutingUsesElapsedStyle(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-12 * time.Second)
	out := stripANSI(m.renderActivity(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityExecuting}, 200))
	if !strings.Contains(out, "⚙ 12s") {
		t.Fatalf("renderActivity(executing) = %q, want elapsed time", out)
	}
	if strings.Contains(out, "Loop:") {
		t.Fatalf("renderActivity(executing) should not include loop phase label; got %q", out)
	}
}

func TestRenderActivityExecutingWithoutStartShowsActivityGlyph(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	out := stripANSI(m.renderActivity(agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityExecuting}, 200))
	if out != "⚙" {
		t.Fatalf("renderActivity(executing without start) = %q, want activity glyph without elapsed", out)
	}
	if got := m.renderExecutingSummary("main"); got != "⚙" {
		t.Fatalf("renderExecutingSummary without start = %q, want activity glyph", got)
	}
}

func TestFormatStatusBarElapsed(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 45 * time.Second, want: " 45s"},
		{in: 59 * time.Second, want: " 59s"},
		{in: time.Minute, want: " 1m00s"},
		{in: 135 * time.Second, want: " 2m15s"},
		{in: 3720 * time.Second, want: " 1h02m00s"},
		{in: 400 * time.Millisecond, want: " 0s"},
	}
	for _, tt := range tests {
		if got := formatStatusBarElapsed(tt.in); got != tt.want {
			t.Fatalf("formatStatusBarElapsed(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTurnBusyKey(t *testing.T) {
	if got := turnBusyKey(""); got != "main" {
		t.Fatalf("empty -> %q, want main", got)
	}
	if got := turnBusyKey("main"); got != "main" {
		t.Fatalf("main -> %q, want main", got)
	}
	if got := turnBusyKey("worker-1"); got != "worker-1" {
		t.Fatalf("worker-1 -> %q", got)
	}
}

func TestRenderStatusBarShowsCompactingContextWhenActivityIsCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"}
	// Compaction is now in the background lane
	m.compactionBgStatus = compactionBackgroundStatus{Active: true, StartedAt: time.Now().Add(-10 * time.Second)}
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "■") && !strings.Contains(plain, "▪") {
		t.Fatalf("status bar should still show compacting icon in background lane, got %q", plain)
	}
}

func TestRenderStatusBarShowsCompactionProgressInBackgroundLane(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.mode = ModeNormal
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-12 * time.Second),
		Bytes:     8 * 1024,
		Events:    3,
	}
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "■") && !strings.Contains(plain, "▪") {
		t.Fatalf("status bar should show compaction icon, got %q", plain)
	}
	// The background compaction lane should show icon + elapsed + progress.
	if !strings.Contains(plain, "↓ 8.0 KB") {
		t.Fatalf("status bar should show compaction progress bytes, got %q", plain)
	}
	if !strings.Contains(plain, " · 3 events") {
		t.Fatalf("status bar should show compaction progress event count, got %q", plain)
	}
}

func TestRenderStatusBarDoesNotShowIdleSinceDuringCompaction(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-5 * time.Second),
	}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "previous", StartedAt: time.Now().Add(-2 * time.Minute)})

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Since ") {
		t.Fatalf("status bar should not show idle time during compaction; got %q", plain)
	}
}

func TestRenderStatusBarDoesNotShowIdleSinceDuringConcurrentRequest(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.compactionBgStatus = compactionBackgroundStatus{
		Active:    true,
		StartedAt: time.Now().Add(-5 * time.Second),
	}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "previous", StartedAt: time.Now().Add(-2 * time.Minute)})

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Since ") {
		t.Fatalf("status bar should not show idle time during a concurrent request; got %q", plain)
	}
}

func TestRenderStatusBarDoesNotShowIdleSinceDuringPendingToolWork(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "previous", StartedAt: time.Now().Add(-2 * time.Minute)})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockToolCall, ToolID: "call-1", ToolName: "shell", ResultDone: false})

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Since ") {
		t.Fatalf("status bar should not show idle time during pending tool work; got %q", plain)
	}
}

func TestRenderStatusBarShowsLastWhenIdleAndSettled(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	when := time.Date(2024, 6, 15, 15, 4, 12, 0, time.Local)
	b := &Block{ID: 1, Type: BlockAssistant, Content: "hi", AgentID: "", StartedAt: when}
	m.viewport.AppendBlock(b)

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Since ") || !strings.Contains(plain, "15:04") {
		t.Fatalf("status bar should show last time; got %q", plain)
	}
	if strings.Contains(plain, "15:04:12") {
		t.Fatalf("status bar should use minute precision for idle timestamp; got %q", plain)
	}
}

func TestRenderStatusBarUsesCompactIdleLabelInNarrowLayout(t *testing.T) {
	m := NewModelWithSize(nil, 90, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	when := time.Date(2024, 6, 15, 15, 4, 12, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", AgentID: "", StartedAt: when})

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Since update ") {
		t.Fatalf("narrow status bar should not use malformed idle label; got %q", plain)
	}
}

func TestRenderStatusBarShowsTerminalWhenPending(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	startedAt := time.Now().Add(-5 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, AgentID: "", UserLocalShellCmd: "echo hi", UserLocalShellPending: true, StartedAt: startedAt})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "Terminal") {
		t.Fatalf("status bar should show shell activity; got %q", plain)
	}
	if strings.Contains(plain, "Since") && !strings.Contains(plain, "Terminal") {
		t.Fatalf("status bar should not show idle label while shell is pending; got %q", plain)
	}
}

func TestRenderStatusBarSwitchesLastByFocusedAgentView(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}

	mainWhen := time.Date(2024, 6, 15, 15, 4, 0, 0, time.Local)
	subWhen := time.Date(2024, 6, 15, 15, 6, 0, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "main", AgentID: "", StartedAt: mainWhen})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "sub", AgentID: "agent-1", StartedAt: subWhen})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:04") {
		t.Fatalf("main view should show main settle time; got %q", plain)
	}
	if strings.Contains(plain, "15:06") {
		t.Fatalf("main view should not show sub-agent settle time; got %q", plain)
	}

	m.setFocusedAgent("agent-1")
	plain = stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:06") {
		t.Fatalf("sub-agent view should show sub-agent settle time; got %q", plain)
	}
	if strings.Contains(plain, "15:04") {
		t.Fatalf("sub-agent view should not show main settle time; got %q", plain)
	}
}

func TestRenderActivityStreamingUsesElapsedWhenNoProgress(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⣿") && !strings.Contains(out, "⣶") {
		t.Fatalf("expected streaming icon in %q", out)
	}
	if !strings.Contains(out, "1m30s") {
		t.Fatalf("streaming render should show canonical elapsed while in progress, got %q", out)
	}
}

func TestRenderActivityCompactingUsesUnifiedProgressStyle(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-8 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "■") && !strings.Contains(out, "▪") {
		t.Fatalf("compacting render should still show icon, got %q", out)
	}
	if !strings.Contains(out, " 8s") {
		t.Fatalf("compacting render should show current phase timer in parens, got %q", out)
	}
}

func TestRenderActivityRetryingShowsDetailAndElapsed(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-17 * time.Second)

	retrying := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "round 6"}, 200))
	if !strings.Contains(retrying, "↺") {
		t.Fatalf("retrying render should still show icon, got %q", retrying)
	}
	if !strings.Contains(retrying, "round 6") || !strings.Contains(retrying, "17s") {
		t.Fatalf("retrying render should show the detail and the phase timer, got %q", retrying)
	}

	fallback := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "fallback: fallback-model (5xx)"}, 200))
	if !strings.Contains(fallback, "fallback: fallback-model (5xx)") || !strings.Contains(fallback, "17s") {
		t.Fatalf("fallback wait render should name the target, reason, and elapsed time, got %q", fallback)
	}
}

func TestRenderActivityRetryingCountsDownToNextAttempt(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	now := time.Unix(1_000_000, 0)
	m.activityStartTime["main"] = now.Add(-40 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityRetrying, AgentID: "main", Detail: "round 12", Deadline: now.Add(45 * time.Second)}

	out := stripANSI(m.renderActivityAt(a, 200, now))
	if !strings.Contains(out, "round 12") || !strings.Contains(out, "retry in 45s") {
		t.Fatalf("retrying render with a deadline should count down to the next attempt, got %q", out)
	}
	if strings.Contains(out, "40s") {
		t.Fatalf("retrying countdown should replace the elapsed phase time, got %q", out)
	}

	later := stripANSI(m.renderActivityAt(a, 200, now.Add(30*time.Second)))
	if !strings.Contains(later, "retry in 15s") {
		t.Fatalf("retrying countdown should shrink as time passes, got %q", later)
	}

	compact := stripANSI(m.renderActivityAt(a, 16, now))
	if !strings.Contains(compact, "retry in 45s") {
		t.Fatalf("narrow retrying render should keep the countdown instead of truncating, got %q", compact)
	}
	narrow := stripANSI(m.renderActivityAt(a, 12, now))
	if !strings.Contains(narrow, "45s") || strings.Contains(narrow, "round") {
		t.Fatalf("12-column retrying render should keep the countdown and drop the round, got %q", narrow)
	}
}

func TestRenderActivityCoolingShowsRemainingCountdown(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	now := time.Unix(1_000_000, 0)
	m.activityStartTime["main"] = now.Add(-12 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "45s", Deadline: now.Add(33 * time.Second)}

	out := stripANSI(m.renderActivityAt(a, 200, now))
	if !strings.Contains(out, "cooling down") || !strings.Contains(out, "33s left") {
		t.Fatalf("cooling render should name the cause and show the remaining wait, got %q", out)
	}

	later := stripANSI(m.renderActivityAt(a, 200, now.Add(20*time.Second)))
	if !strings.Contains(later, "13s left") {
		t.Fatalf("cooling countdown should shrink as time passes, got %q", later)
	}
}

func TestRenderActivityCoolingDegradesBeforeTruncating(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	now := time.Unix(1_000_000, 0)
	a := agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Deadline: now.Add(33 * time.Second)}

	compact := stripANSI(m.renderActivityAt(a, 20, now))
	if strings.Contains(compact, "cooling down") {
		t.Fatalf("tight cooling render should drop the cause to keep the wait, got %q", compact)
	}
	if !strings.Contains(compact, "33s left") {
		t.Fatalf("tight cooling render should keep the remaining wait, got %q", compact)
	}

	narrow := stripANSI(m.renderActivityAt(a, 8, now))
	if strings.Contains(narrow, "left") || strings.Contains(narrow, "…") {
		t.Fatalf("narrow cooling render should drop the label instead of truncating, got %q", narrow)
	}
	if !strings.Contains(narrow, "33s") {
		t.Fatalf("narrow cooling render should keep the remaining time, got %q", narrow)
	}
}

func TestRenderActivityCoolingFallsBackToElapsedWithoutDeadline(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "45s"}

	out := stripANSI(m.renderActivity(a, 200))
	if strings.Contains(out, "left") {
		t.Fatalf("cooling without a deadline should not claim a countdown, got %q", out)
	}
	if !strings.Contains(out, " 7s") {
		t.Fatalf("cooling without a deadline should fall back to elapsed, got %q", out)
	}
}

func TestFormatStatusBarCountdown(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{200 * time.Millisecond, "1s"},
		{33 * time.Second, "33s"},
		{33*time.Second + time.Millisecond, "34s"},
		{time.Minute, "1m"},
		{70 * time.Second, "1m10s"},
		// The trailing field is zero-padded so a per-second repaint cannot
		// shift the digit position when the value crosses a power of ten.
		{65 * time.Second, "1m05s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		// A provider quota reset can be hours out; seconds are dropped at this
		// range so the lane does not repaint every second.
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{3 * time.Hour, "3h"},
		{2*time.Hour + 5*time.Minute + 30*time.Second, "2h05m"},
	}
	for _, tc := range cases {
		if got := formatStatusBarCountdown(tc.in); got != tc.want {
			t.Fatalf("formatStatusBarCountdown(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRenderActivityWaitingUsesExplicitElapsedLabel(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, " 7s") {
		t.Fatalf("waiting render should keep phase timer in parens, got %q", out)
	}
}

func TestRenderActivityWaitingTokenUsesDistinctLabel(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, " 7s") {
		t.Fatalf("waiting_token render should keep phase timer in parens, got %q", out)
	}
}

func TestRenderActivityUsesCompactParenStyleWhenWidthIsTight(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-7 * time.Second)

	waiting := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}, 32))
	if !strings.Contains(waiting, "↺ 7s") {
		t.Fatalf("narrow waiting render should use icon+elapsed style, got %q", waiting)
	}

	waitingToken := stripANSI(m.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "main"}, 64))
	if !strings.Contains(waitingToken, "↺ 7s") {
		t.Fatalf("waiting_token render should use icon+elapsed style, got %q", waitingToken)
	}
}

func TestRenderActivityShowsTimeFromZeroSeconds(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	m.activityStartTime["main"] = time.Now().Add(-4 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, " 4s") {
		t.Fatalf("phase timer should start from 0s, got %q", out)
	}

	m2 := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-4 * time.Second)
	m2.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	out = stripANSI(m2.renderActivity(agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}, 200))
	if !strings.Contains(out, " 4s") && !strings.Contains(out, " 0s") {
		t.Fatalf("streaming elapsed should be shown immediately, got %q", out)
	}
}

func TestRenderActivityTruncatesToCoreWhenWidthIsTight(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-2 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	wide := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(wide, "⣿") && !strings.Contains(wide, "⣶") {
		t.Fatalf("wide render should include streaming icon; got %q", wide)
	}
	if !strings.Contains(wide, " 2s") {
		t.Fatalf("wide render should show phase timer from 0s; got %q", wide)
	}

	narrow := stripANSI(m.renderActivity(a, 24))
	if strings.Contains(narrow, statusBarTotalLabel()) {
		t.Fatalf("narrow render should drop total label first; got %q", narrow)
	}
	if !strings.Contains(narrow, " 2s") {
		t.Fatalf("narrow render should keep phase timer from 0s; got %q", narrow)
	}
}

func TestRenderActivityUsesCompactLastLabelInCompactExtras(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-2 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	compact := stripANSI(m.renderActivity(a, 46))
	if !strings.Contains(compact, "⣿") && !strings.Contains(compact, "⣶") {
		t.Fatalf("compact render should keep streaming icon; got %q", compact)
	}
}

func TestRenderActivityOverflowDropsElapsedThenSinceThenPhaseTimer(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-3 * time.Minute)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-20 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	full := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(full, "⣿") && !strings.Contains(full, "⣶") {
		t.Fatalf("full render should keep streaming icon, got %q", full)
	}
	if !strings.Contains(full, " 20s") {
		t.Fatalf("full render should keep phase elapsed, got %q", full)
	}

	noElapsed := stripANSI(m.renderActivity(a, 40))
	if strings.Contains(noElapsed, statusBarTotalLabel()) {
		t.Fatalf("medium render should hide anchor elapsed first, got %q", noElapsed)
	}
	if !strings.Contains(noElapsed, " 20s") {
		t.Fatalf("medium render should retain phase timer, got %q", noElapsed)
	}

	noSince := stripANSI(m.renderActivity(a, 28))
	if strings.Contains(noSince, statusBarTotalLabel()) {
		t.Fatalf("narrower render should not restore total label, got %q", noSince)
	}
	if !strings.Contains(noSince, " 20s") {
		t.Fatalf("narrower render should still retain phase timer, got %q", noSince)
	}
}

func TestRenderStatusBarUsesQueuedDraftStartWhenIdle(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.workingDir = "/tmp"
	m.updateRightPanelVisible()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	queuedAt := time.Date(2024, 6, 15, 15, 7, 0, 0, time.Local)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued", DisplayContent: "queued", QueuedAt: queuedAt}}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:07") {
		t.Fatalf("status bar should show queued draft start time; got %q", plain)
	}
}

func TestStatusBarDynamicCacheKeyBusyUsesAnimationFrameBucket(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "45s"}
	t0 := time.Unix(100, 0)
	t1 := t0.Add(199 * time.Millisecond)
	t2 := t0.Add(200 * time.Millisecond)
	if got := m.statusBarDynamicCacheKeyAt(t0); got != "frame:500" {
		t.Fatalf("busy cache key at t0 = %q, want frame:500", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t1); got != "frame:500" {
		t.Fatalf("busy cache key at t1 = %q, want frame:500", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t2); got != "frame:501" {
		t.Fatalf("busy cache key at t2 = %q, want frame:501", got)
	}
}

func TestStatusBarDynamicCacheKeyIdleUsesMinuteBucket(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	when := time.Date(2024, 6, 15, 15, 4, 12, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: when})
	t0 := time.Unix(120, 0)
	t1 := t0.Add(30 * time.Second)
	t2 := t0.Add(time.Minute)
	if got := m.statusBarDynamicCacheKeyAt(t0); got != "min:2" {
		t.Fatalf("idle cache key at t0 = %q, want min:2", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t1); got != "min:2" {
		t.Fatalf("idle cache key at t1 = %q, want min:2", got)
	}
	if got := m.statusBarDynamicCacheKeyAt(t2); got != "min:3" {
		t.Fatalf("idle cache key at t2 = %q, want min:3", got)
	}
}

func TestStatusBarDynamicCacheKeyCompactingUsesAnimationFrames(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"}
	t1 := time.UnixMilli(1000)
	t2 := t1.Add(compactionPillBreathPhase)
	if got1, got2 := m.statusBarDynamicCacheKeyAt(t1), m.statusBarDynamicCacheKeyAt(t2); got1 == got2 {
		t.Fatalf("compacting cache key should follow animation frame cadence, got identical keys %q", got1)
	}
}

func TestStatusBarDynamicCacheKeyCompactingUsesSecondBucket(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}
	t0 := time.UnixMilli(120000)
	t1 := t0.Add(compactionPillBreathPhase / 2)
	t2 := t0.Add(compactionPillBreathPhase)
	if got0, got1, got2 := m.statusBarDynamicCacheKeyAt(t0), m.statusBarDynamicCacheKeyAt(t1), m.statusBarDynamicCacheKeyAt(t2); got0 == got1 && got1 == got2 {
		t.Fatalf("compacting cache keys should change with animation frames, got %q %q %q", got0, got1, got2)
	}
}

func TestAgentEventBatchSchedulesStatusBarTickForCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	updated, cmd := m.Update(agentEventBatchMsg{{
		event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main", Detail: "context"},
	}})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("compacting activity batch should schedule follow-up commands")
	}
	if !model.statusBarTickScheduled {
		t.Fatal("compacting activity should schedule a status-bar timing tick")
	}
	if model.animRunning {
		t.Fatal("compacting activity should not restart the visual animation loop")
	}
}

func TestRenderActivityUsesQueuedDraftStartForTotal(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	queuedAt := time.Now().Add(-90 * time.Second)
	m.queuedDrafts = []queuedDraft{{ID: "draft-1", Content: "queued", DisplayContent: "queued", QueuedAt: queuedAt}}
	a := agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⣿") && !strings.Contains(out, "⣶") {
		t.Fatalf("expected streaming icon in %q", out)
	}
}

func TestRenderActivityPrefersNewerToolStartOverEarlierSettledBlock(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	older := time.Now().Add(-3 * time.Minute)
	newer := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "done", SettledAt: older})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockToolCall, ToolName: "shell", StartedAt: newer})
	a := agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⚙ 1m30s") {
		t.Fatalf("expected newer tool start to anchor executing elapsed; got %q", out)
	}
}

func TestRenderActivityShowsUnifiedBusyElapsedStyle(t *testing.T) {
	m := NewModelWithSize(nil, 200, 24)
	started := time.Now().Add(-90 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "hi", StartedAt: started})
	m.activityStartTime["main"] = time.Now().Add(-20 * time.Second)
	a := agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}
	out := stripANSI(m.renderActivity(a, 200))
	if !strings.Contains(out, "⇋ 20s") {
		t.Fatalf("expected unified busy elapsed style in %q", out)
	}
}

func TestStatusBarIgnoresMainTerminalStartWhenFocusedOnSubAgentWithoutPendingShell(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
	}
	m := NewModelWithSize(backend, 140, 24)
	startedAt := time.Now().Add(-5 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, AgentID: "", UserLocalShellCmd: "echo hi", UserLocalShellPending: true, StartedAt: startedAt})
	m.setFocusedAgent("agent-1")

	if m.viewport.HasUserLocalShellPending() {
		t.Fatal("sub-agent viewport should not report pending terminal from main view")
	}
	if _, ok := m.latestStatusStartWall("agent-1"); ok {
		t.Fatal("latestStatusStartWall should ignore main-view terminal when focused on sub-agent without pending shell")
	}

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, statusBarStartedLabel()) {
		t.Fatalf("status bar should not inherit main-view terminal start time after focus switch, got %q", plain)
	}
}

func TestRenderStatusBarTerminalDropsStartedWhenWidthIsTight(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	startedAt := time.Now().Add(-5 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo hi", UserLocalShellPending: true, StartedAt: startedAt})

	wide := stripANSI(m.renderStatusBarLocalShell(200))
	if !strings.Contains(wide, statusBarStartedLabel()) {
		t.Fatalf("wide terminal render should include %q; got %q", statusBarStartedLabel(), wide)
	}

	narrow := stripANSI(m.renderStatusBarLocalShell(22))
	if strings.Contains(narrow, statusBarStartedLabel()) {
		t.Fatalf("narrow terminal render should drop started labels; got %q", narrow)
	}
	if !strings.Contains(narrow, "Terminal") {
		t.Fatalf("narrow shell render should keep core text; got %q", narrow)
	}
}

func TestRenderStatusBarTerminalUsesCompactStartedLabelInCompactLayout(t *testing.T) {
	m := NewModelWithSize(nil, 140, 24)
	startedAt := time.Now().Add(-5 * time.Second)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo hi", UserLocalShellPending: true, StartedAt: startedAt})

	compact := stripANSI(m.renderStatusBarLocalShell(36))
	if !strings.Contains(compact, statusBarStartedLabel()) {
		t.Fatalf("compact terminal render should use started label; got %q", compact)
	}
}

func TestStatusBarUsesVisiblePendingTerminalStartByFocusedAgentView(t *testing.T) {
	backend := &sessionControlAgent{
		events: make(chan agent.AgentEvent, 1),
		subAgents: []agent.SubAgentInfo{{
			InstanceID:   "agent-1",
			AgentDefName: "reviewer",
			TaskDesc:     "check code",
		}},
	}
	m := NewModelWithSize(backend, 140, 24)
	mainStarted := time.Date(2024, 6, 15, 15, 4, 0, 0, time.Local)
	subStarted := time.Date(2024, 6, 15, 15, 6, 0, 0, time.Local)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, AgentID: "", UserLocalShellCmd: "echo main", UserLocalShellPending: true, StartedAt: mainStarted})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, AgentID: "agent-1", UserLocalShellCmd: "echo sub", UserLocalShellPending: true, StartedAt: subStarted})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:04") || strings.Contains(plain, "15:06") {
		t.Fatalf("main view should use main pending shell start time, got %q", plain)
	}

	m.setFocusedAgent("agent-1")
	plain = stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "15:06") || strings.Contains(plain, "15:04") {
		t.Fatalf("sub-agent view should use sub-agent pending shell start time, got %q", plain)
	}
}
