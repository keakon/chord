package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestRenderStatusBarShowsLatestCumulativeProgressAfterMultipleAgentEvents(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 17_869, Events: 105}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 18_041, Events: 106}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 18 KB · 106 events · 0s") {
		t.Fatalf("status bar should show latest cumulative progress, got %q", plain)
	}
}

func TestRequestCycleStartedResetsProgressForNewRequest(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 300 * 1024, Events: 120}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestCycleStartedEvent{AgentID: "main", TurnID: 2}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 5 * 1024, Events: 2}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events") {
		t.Fatalf("new request cycle should reset progress state before next bytes arrive, got %q", plain)
	}
}

func TestPendingDraftConsumedResetsProgressBaselineForNewRequest(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 300 * 1024, VisibleEvents: 120}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.PendingDraftConsumedEvent{DraftID: "draft-1", Parts: []message.ContentPart{{Type: "text", Text: "continue"}}, AgentID: "main"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 305 * 1024, Events: 122}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events") {
		t.Fatalf("pending draft consumed should reset request progress baseline for the next request, got %q", plain)
	}
}

func TestRequestProgressStartsFromZeroOnFirstStreamTextAssistantCard(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 200 * 1024, VisibleEvents: 80}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{AgentID: "main", Text: "hi"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 205 * 1024, Events: 82}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events · 0s") {
		t.Fatalf("assistant stream should start from zero baseline on first text, got %q", plain)
	}
}

func TestRequestProgressStartsFromZeroForNewAssistantCard(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 200 * 1024, VisibleEvents: 80}
	m.markRequestProgressBaseline("main")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 205 * 1024, Events: 82}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 5.0 KB · 2 events · 0s") {
		t.Fatalf("new assistant card progress should start from zero baseline, got %q", plain)
	}
}

func TestRequestProgressResetsPerCardAcrossAssistantToolAssistant(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 200 * 1024, Events: 80}})
	m.markRequestProgressBaseline("main")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 220 * 1024, Events: 90}})
	plain1 := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain1, "↓ 20 KB · 10 events · 0s") {
		t.Fatalf("first assistant card progress = %q, want delta from card baseline", plain1)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{ID: "tool-1", Name: "shell", AgentID: "main", State: agent.ToolCallExecutionStateRunning}})
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}
	m.activityStartTime["main"] = time.Now().Add(-3 * time.Second)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 225 * 1024, Events: 93}})
	plain2 := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain2, "⚙ 3s") {
		t.Fatalf("status bar should show executing activity with elapsed, got %q", plain2)
	}

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.markRequestProgressBaseline("main")
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 230 * 1024, Events: 95}})
	plain3 := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain3, "↓ 5.0 KB · 2 events") {
		t.Fatalf("second assistant card progress should restart from zero, got %q", plain3)
	}
}

func TestRenderStatusBarShowsZeroByteDownloadForWaitingDownloadStates(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityWaitingHeaders, AgentID: "main"}
	m.activityStartTime["main"] = time.Now()
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 0 B · 0s") {
		t.Fatalf("waiting_headers should render zero-byte download state, got %q", plain)
	}

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "main"}
	plain = stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 0 B · 0s") {
		t.Fatalf("waiting_token should render zero-byte download state, got %q", plain)
	}
}

func TestRenderStatusBarKeepsMainPrefixedSubAgentProgressSeparate(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main-1", Bytes: 128 * 1024, Events: 42}})
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "↓ 128 KB · 42 events") {
		t.Fatalf("status bar should not map a main-prefixed subagent to the main progress lane, got %q", plain)
	}
}

func TestRenderStatusBarShowsRequestProgressAfterAgentEventInjection(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 128 KB · 42 events · 0s") {
		t.Fatalf("status bar should show request progress summary after progress injection, got %q", plain)
	}
}

func TestRenderStatusBarShowsRequestProgressInsteadOfStreamingLabel(t *testing.T) {
	m := NewModel(nil)
	m.width = 180
	m.workingDir = "/home/user/projects/myapp"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 128 * 1024, VisibleEvents: 42}

	got := stripANSI(m.renderStatusBar())
	if !strings.Contains(got, "↓ 128 KB · 42 events · 0s") {
		t.Fatalf("status bar should show request progress summary in new icon style; got %q", got)
	}
}

func TestRequestProgressDoneImmediatelyStopsShowingPreviousRequestLength(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42, Done: true}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}})
	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "128 KB") || strings.Contains(plain, "42 events") {
		t.Fatalf("status bar should drop finished request length immediately when switching request, got %q", plain)
	}
	if !strings.Contains(plain, "■") && !strings.Contains(plain, "▪") {
		t.Fatalf("status bar should switch to compacting immediately after previous request done, got %q", plain)
	}
}

func TestCompactionKeepAliveRepeatKeepsOriginalSinceAnchor(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	started := time.Now().Add(-2 * time.Minute)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}})
	original, ok := m.workStartedAt["main"]
	if !ok || original.IsZero() {
		t.Fatal("compacting activity should record a work start anchor")
	}
	m.workStartedAt["main"] = started

	// The compaction keep-alive re-emits the same activity type periodically;
	// the repeat must not move the since anchor to the heartbeat time.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}})
	if got := m.workStartedAt["main"]; !got.Equal(started) {
		t.Fatalf("keep-alive repeat moved the since anchor: got %v, want %v", got, started)
	}

	// A real activity transition into compacting must refresh the anchor. The
	// agent only emits compacting after the foreground yields the slot via
	// idle, so the transition arrives as executing → idle → compacting.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}})
	if got := m.workStartedAt["main"]; !got.After(started) {
		t.Fatalf("transition into compacting should refresh the since anchor, got %v", got)
	}
}

func TestLeavingRequestActivityClearsPreviousRequestProgress(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 64 * 1024, Events: 10}})
	// Switching to ActivityExecuting should NOT clear request progress yet —
	// tool arg streaming may still be in flight and RequestProgressEvent{Done:true}
	// has not arrived. The status bar will show executing state (⚙) because
	// buildStatusBarActivityDisplay checks activity type first, but the progress
	// data is retained until explicitly done or a new cycle starts.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}})
	if _, ok := m.requestProgress["main"]; !ok {
		t.Fatal("request progress should be retained during ActivityExecuting until Done event arrives")
	}
	plain := stripANSI(m.renderStatusBar())
	// Status bar shows executing icon, not download progress, because activity type is Executing
	if !strings.Contains(plain, "⚙") {
		t.Fatalf("status bar should show executing state, got %q", plain)
	}
	// Now simulate RequestProgressEvent{Done:true} — this should clear the progress
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 64 * 1024, Events: 10, Done: true}})
	if _, ok := m.requestProgress["main"]; ok {
		t.Fatal("request progress should be cleared after Done event")
	}
}

func TestRequestDoneThenNextConnectingStartsAtZero(t *testing.T) {
	m := NewModelWithSize(nil, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{AgentID: "main", Bytes: 128 * 1024, Events: 42, Done: true}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityConnecting, AgentID: "main"}})
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "↓ 0 B · 0s") {
		t.Fatalf("next request connecting should start from zero immediately after previous request done, got %q", plain)
	}
	if strings.Contains(plain, "128 KB") || strings.Contains(plain, "42 events") {
		t.Fatalf("next request connecting should not inherit previous request length, got %q", plain)
	}
}

func TestRenderStatusBarShowsLoopStateImmediatelyAfterEnableEvent(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 120, 24)

	backend.loopState = agent.LoopStateExecuting
	backend.loopTarget = "finish current task"
	backend.loopIteration = 1
	backend.loopMaxIterations = 10
	_ = m.handleAgentEvent(agentEventMsg{event: agent.LoopStateChangedEvent{}})

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want immediate loop pill after enable event", plain)
	}
}

func TestRenderStatusBarShowsLoopStateAfterRuntimeEnable(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	backend.loopState = ""
	backend.loopTarget = "finish current task"
	backend.loopIteration = 1
	backend.loopMaxIterations = 10
	backend.loopState = agent.LoopStateExecuting
	m := NewModelWithSize(backend, 120, 24)
	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want loop pill after runtime enable", plain)
	}
}

func TestRenderStatusBarShowsLoopStateForMainAgent(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopTarget: "finish current task", loopIteration: 1, loopMaxIterations: 10}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want loop pill with iteration info", plain)
	}
	if strings.Contains(plain, "finish current task") {
		t.Fatalf("status bar = %q, should not expose loop target text", plain)
	}
}

func TestRenderStatusBarShowsLoopIterationWithoutLimitWhenUnlimited(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopIteration: 1, loopMaxSet: true, loopMaxIterations: 0}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1") {
		t.Fatalf("status bar = %q, want loop iteration without limit", plain)
	}
	if strings.Contains(plain, "LOOP 1/") {
		t.Fatalf("status bar = %q, should not show max iteration when unlimited", plain)
	}
}

func TestRenderStatusBarDoesNotShowLoopWhenDisabled(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder"}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if strings.Contains(plain, "Loop") {
		t.Fatalf("status bar = %q, should not show loop text when loop is disabled", plain)
	}
}

func TestRenderStatusBarShowsLoopEscHintWithoutSqueezingLoopPill(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopIteration: 1, loopMaxIterations: 10}
	m := NewModelWithSize(backend, 120, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	plain := stripANSI(m.renderStatusBar())
	if !strings.Contains(plain, "LOOP 1/10") {
		t.Fatalf("status bar = %q, want LOOP pill visible", plain)
	}
	if !strings.Contains(plain, "esc ⇢ exit loop") {
		t.Fatalf("status bar = %q, want loop esc hint", plain)
	}
}

func TestRenderStatusBarKeepsLoopTextWhileCompacting(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", loopState: agent.LoopStateExecuting, loopTarget: "finish current task"}
	m := NewModelWithSize(backend, 180, 24)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}

	rendered := m.renderStatusBar()
	plain := stripANSI(rendered)
	if !strings.Contains(plain, "LOOP") {
		t.Fatalf("status bar = %q, want loop pill while compacting", plain)
	}
	if strings.Contains(plain, "finish current task") {
		t.Fatalf("status bar = %q, should not expose loop target text while compacting", plain)
	}
	if strings.Contains(plain, "compacting") {
		t.Fatalf("status bar = %q, should not duplicate compacting in loop text", plain)
	}
}

func TestFlushVisibleRequestProgressPromotesRawValues(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.requestProgress["main"] = requestProgressState{RawBytes: 2048, RawEvents: 3}
	m.flushVisibleRequestProgress(time.Now())
	prog := m.requestProgress["main"]
	if prog.VisibleBytes != 2048 || prog.VisibleEvents != 3 {
		t.Fatalf("visible progress = (%d,%d), want (2048,3)", prog.VisibleBytes, prog.VisibleEvents)
	}
}

func TestRequestProgressCountsAsActiveAnimation(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityExecuting}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 1024, VisibleEvents: 2}

	if !m.hasActiveAnimation() {
		t.Fatal("request progress should count as active animation")
	}
}

func TestStatusBarDynamicCacheKeyIncludesRequestProgress(t *testing.T) {
	m := NewModelWithSize(nil, 80, 12)
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityCompacting}
	m.requestProgress["main"] = requestProgressState{VisibleBytes: 1024, VisibleEvents: 2}
	got := m.statusBarDynamicCacheKeyAt(time.UnixMilli(1000))
	if !strings.Contains(got, "frame:") || !strings.Contains(got, "↓ 1.0 KB · 2 events") {
		t.Fatalf("statusBarDynamicCacheKeyAt = %q, want frame-based progress summary", got)
	}
}

func TestInsertModeEscReturnsToNormalWithoutDisablingLoop(t *testing.T) {
	backend := &sessionControlAgent{loopState: agent.LoopStateExecuting}
	m := NewModel(backend)
	m.mode = ModeInsert

	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v, want %v", m.mode, ModeNormal)
	}
	if backend.loopDisableCalls != 0 {
		t.Fatalf("DisableLoopMode calls = %d, want 0", backend.loopDisableCalls)
	}
}

func TestNormalModeEscDisablesLoopBeforeCancellingBusyTurn(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true, loopState: agent.LoopStateExecuting}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.inflightDraft = &queuedDraft{ID: "draft-1", Content: "queued"}

	cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd == nil {
		t.Fatal("first esc should return loop-disable toast command")
	}
	if backend.loopDisableCalls != 1 {
		t.Fatalf("DisableLoopMode calls = %d, want 1", backend.loopDisableCalls)
	}
	if backend.cancelCalls != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 0 on first esc", backend.cancelCalls)
	}
	backend.loopState = ""
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1 on second esc", backend.cancelCalls)
	}
}
