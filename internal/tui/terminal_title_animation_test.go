package tui

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestAgentActivityStartsTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "Update title animation"

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		Type:    agent.ActivityWaitingHeaders,
		AgentID: "main",
	}})

	if cmd == nil {
		t.Fatal("activity transition should schedule animation work")
	}
	if !m.animRunning {
		t.Fatal("activity transition should start visual animation")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("activity transition should start terminal title ticker")
	}
}

func TestRequestProgressStartsTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "Request progress animation"

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   128 * 1024,
		Events:  42,
	}})

	if cmd == nil {
		t.Fatal("request progress should schedule animation work")
	}
	if !m.animRunning {
		t.Fatal("request progress should start visual animation")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("request progress should start terminal title ticker")
	}
	if m.currentTitleMode() != terminalTitleModeSpinner {
		t.Fatalf("title mode = %v, want spinner", m.currentTitleMode())
	}
}

func TestBackgroundRequestProgressDoesNotRestartTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.terminalTitleBase = "Background request progress"

	first := m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   128 * 1024,
		Events:  42,
	}})
	if first == nil {
		t.Fatal("initial request progress should schedule animation work")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("initial request progress should start terminal title ticker")
	}
	if got := m.terminalTitleTickGeneration; got != 1 {
		t.Fatalf("ticker generation after start = %d, want 1", got)
	}

	second := m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "main",
		Bytes:   256 * 1024,
		Events:  84,
	}})
	if second == nil {
		t.Fatal("follow-up request progress should still be handled")
	}
	if got := m.terminalTitleTickGeneration; got != 1 {
		t.Fatalf("ticker generation after repeated progress = %d, want 1", got)
	}
	if got := m.terminalTitleTickerDelay; got != backgroundTitleSpinnerCadence {
		t.Fatalf("ticker delay = %s, want %s", got, backgroundTitleSpinnerCadence)
	}
}

func TestHandleBlurMsgKeepsBackgroundActiveTitleTickerForBusyAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.terminalTitleBase = "background busy"
	m.animRunning = true
	m.terminalTitleTickRunning = true
	m.terminalTitleTickGeneration = 3
	m.terminalTitleTickerMode = terminalTitleModeSpinner
	m.terminalTitleTickerDelay = titleSpinnerCadence

	if cmd := m.handleBlurMsg(); cmd != nil {
		_ = cmd()
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("background-active blur should keep title ticker running at low cadence")
	}
	if got := m.terminalTitleTickGeneration; got != 5 {
		t.Fatalf("ticker generation after blur = %d, want 5 after restart", got)
	}
	if got := m.terminalTitleTickerDelay; got != backgroundTitleSpinnerCadence {
		t.Fatalf("ticker delay after blur = %s, want %s", got, backgroundTitleSpinnerCadence)
	}
}

func TestHandleFocusMsgRestartsTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.terminalTitleBase = "Restore title animation"

	cmd := m.handleFocusMsg()

	if cmd == nil {
		t.Fatal("focus restore should schedule redraw and animation work")
	}
	if !m.animRunning {
		t.Fatal("focus restore should restart visual animation")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("focus restore should restart terminal title ticker")
	}
}

func TestConfirmRequestUsesStaticForegroundRequestTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.terminalTitleBase = "Need your input"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	cmd := m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: tools.NameEdit, RequestID: "req-1"}})
	if cmd == nil {
		t.Fatal("confirm request should schedule follow-up work")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("foreground confirm request should stop terminal title ticker")
	}
	if m.currentTitleMode() != terminalTitleModeRequest {
		t.Fatalf("title mode = %v, want request", m.currentTitleMode())
	}
}

func TestQuestionRequestStartsBlinkingBackgroundRequestTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "Question pending"

	cmd := m.handleQuestionRequest(questionRequestMsg{request: QuestionRequest{Questions: []tools.QuestionItem{{Header: "Name", Question: "Who?"}}}})
	if cmd == nil {
		t.Fatal("question request should schedule follow-up work")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("background question request should start terminal title ticker")
	}
	if m.currentTitleMode() != terminalTitleModeRequest {
		t.Fatalf("title mode = %v, want request", m.currentTitleMode())
	}
}

func TestHandleBlurMsgStartsBlinkingRequestTitleForPendingConfirm(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.terminalTitleBase = "await approval"
	m.confirm.request = &ConfirmRequest{RequestID: "req-1"}

	if cmd := m.handleBlurMsg(); cmd != nil {
		_ = cmd()
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("background confirm should blink terminal title")
	}
}

func TestHandleFocusMsgStopsBlinkingButKeepsRequestTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "await approval"
	m.confirm.request = &ConfirmRequest{RequestID: "req-1"}
	m.terminalTitleTickRunning = true
	m.terminalTitleTickGeneration = 7
	m.terminalTitleRequestBlinkOff = true

	m.handleFocusMsg()
	if m.terminalTitleTickRunning {
		t.Fatal("focus should stop request-title blinking")
	}
	if !m.terminalTitleRequestSeen {
		t.Fatal("focus should mark the pending request as seen")
	}
	if m.currentTitleMode() != terminalTitleModeRequest {
		t.Fatalf("title mode = %v, want request", m.currentTitleMode())
	}
	if delay := m.currentTitleTickerDelay(); delay != 0 {
		t.Fatalf("title ticker delay after focus = %s, want 0", delay)
	}

	if cmd := m.handleBlurMsg(); cmd != nil {
		_ = cmd()
	}
	if m.terminalTitleTickRunning {
		t.Fatal("seen pending request should stay solid instead of blinking again after blur")
	}
	if m.currentTitleMode() != terminalTitleModeRequest {
		t.Fatalf("title mode after blur = %v, want request", m.currentTitleMode())
	}
}

func TestResolveConfirmRestoresSpinnerWhenBusyWorkRemains(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.terminalTitleBase = "keep working"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.confirm = confirmState{
		request:  &ConfirmRequest{ToolName: tools.NameEdit, ArgsJSON: `{}`},
		prevMode: ModeNormal,
	}

	cmd := m.resolveConfirm(ConfirmResult{Action: ConfirmAllow})
	if cmd == nil {
		t.Fatal("resolveConfirm should resubscribe and resync title")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("resolveConfirm should resume spinner while busy work remains")
	}
	if m.currentTitleMode() != terminalTitleModeSpinner {
		t.Fatalf("title mode = %v, want spinner", m.currentTitleMode())
	}
}

func TestQuestionTickTogglesBackgroundRequestBlinkState(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "Question pending"
	m.question.request = &QuestionRequest{Questions: []tools.QuestionItem{{Header: "Name", Question: "Who?"}}}
	m.terminalTitleTickRunning = true
	m.terminalTitleTickGeneration = 7

	if m.terminalTitleRequestBlinkOff {
		t.Fatal("blink state should start visible")
	}
	cmd := m.handleTerminalTitleTick(terminalTitleTickMsg{generation: 7})
	if cmd == nil {
		t.Fatal("background request title should schedule another tick")
	}
	if !m.terminalTitleRequestBlinkOff {
		t.Fatal("request blink tick should toggle to spacer frame")
	}
}

func TestCompactingActivityDoesNotStartTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		Type:    agent.ActivityCompacting,
		AgentID: "main",
		Detail:  "context",
	}})

	if cmd != nil {
		t.Fatal("compacting activity should not schedule animation work")
	}
	if m.animRunning {
		t.Fatal("compacting activity should not leave visual animation running")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("compacting activity should not start terminal title ticker")
	}
}

func TestIdleEventStopsTerminalTitleTickerImmediately(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "Idle cleanup"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})

	if got := m.activities["main"].Type; got != agent.ActivityIdle {
		t.Fatalf("main activity = %q, want idle", got)
	}
	if m.animRunning {
		t.Fatal("idle event should stop visual animation immediately")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("idle event should stop terminal title ticker immediately")
	}
}

func TestAgentDoneEventStopsTerminalTitleTickerForLastBusySubAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "worker done"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", Summary: "done"}})

	if got := m.activities["agent-1"].Type; got != agent.ActivityIdle {
		t.Fatalf("agent-1 activity = %q, want idle after done fallback", got)
	}
	if m.animRunning {
		t.Fatal("done fallback should stop visual animation when no busy agent remains")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("done fallback should stop terminal title ticker when no busy agent remains")
	}
}

func TestAgentDoneEventKeepsTerminalTitleTickerWhenMainStillBusy(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "main busy"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", Summary: "done"}})

	if got := m.activities["agent-1"].Type; got != agent.ActivityIdle {
		t.Fatalf("agent-1 activity = %q, want idle after done fallback", got)
	}
	if !m.animRunning {
		t.Fatal("done fallback should not stop visual animation while main is still busy")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("done fallback should not stop terminal title ticker while main is still busy")
	}
}

func TestAgentStatusEventStopsTerminalTitleTickerForTerminalSubAgentState(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "worker error"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "agent-1"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStatusEvent{AgentID: "agent-1", Status: "error"}})

	if got := m.activities["agent-1"].Type; got != agent.ActivityIdle {
		t.Fatalf("agent-1 activity = %q, want idle after terminal status fallback", got)
	}
	if m.animRunning {
		t.Fatal("terminal subagent status should stop visual animation when no busy agent remains")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("terminal subagent status should stop terminal title ticker when no busy agent remains")
	}
}

func TestStoppedSubAgentDoesNotShowLateActivityInStatusBar(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.setFocusedAgent("agent-1")
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "agent-1"}
	m.requestProgress["agent-1"] = requestProgressState{VisibleBytes: 128, VisibleEvents: 2}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStatusEvent{
		AgentID: "agent-1",
		Status:  string(agent.SubAgentStateCancelled),
	}})

	if _, ok := m.requestProgress["agent-1"]; ok {
		t.Fatal("terminal subagent status should clear request progress")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		AgentID: "agent-1",
		Type:    agent.ActivityExecuting,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.RequestProgressEvent{
		AgentID: "agent-1",
		Bytes:   256,
		Events:  3,
	}})

	if m.isFocusedAgentBusy() {
		t.Fatal("late activity should not make a stopped focused subagent appear busy")
	}
	if m.hasActiveAgentActivity() {
		t.Fatal("late activity should not keep global agent activity active")
	}
	if m.hasActiveAnimation() {
		t.Fatal("late activity should not keep status or terminal-title animation active")
	}
	if delay := m.statusBarNextRefreshDelayAt(time.Now()); delay != 0 {
		t.Fatalf("stopped subagent status bar refresh delay = %v, want 0", delay)
	}
	if got := m.renderRequestProgressSummary("agent-1"); got != "" {
		t.Fatalf("late request progress summary = %q, want empty", got)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentStatusEvent{
		AgentID: "agent-1",
		Status:  string(agent.SubAgentStateRunning),
	}})
	if !m.isFocusedAgentBusy() {
		t.Fatal("activity should become visible again after the subagent resumes running")
	}
}

func TestResetStreamingToIdleStopsTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "stream stale"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	m.resetStreamingToIdle()

	if m.animRunning {
		t.Fatal("resetStreamingToIdle should stop visual animation immediately")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("resetStreamingToIdle should stop terminal title ticker immediately")
	}
}

func TestResetTimingStateForSessionRestoreStopsTerminalTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "restore reset"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.terminalTitleTickRunning = true

	m.resetTimingStateForSessionRestore()

	if m.animRunning {
		t.Fatal("session restore timing reset should stop visual animation immediately")
	}
	if m.terminalTitleTickRunning {
		t.Fatal("session restore timing reset should stop terminal title ticker immediately")
	}
}

// TestTerminalTitleTickerSurvivesActivityRoundTrip reproduces the regression
// where the title spinner stops after activity transitions Streaming →
// Compacting → Streaming. With the syncTerminalTitleTickerWithCadence fix the
// ticker should be running again at the end of the round trip.
func TestTerminalTitleTickerSurvivesActivityRoundTrip(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.terminalTitleBase = "round trip"

	// Streaming → ticker on
	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		Type:    agent.ActivityStreaming,
		AgentID: "main",
	}}); cmd == nil {
		t.Fatal("Streaming activity should schedule animation work")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("title ticker should be running after Streaming activity")
	}
	genAfterStreaming := m.terminalTitleTickGeneration

	// Compacting → ticker off (Compacting is treated as no-animation)
	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		Type:    agent.ActivityCompacting,
		AgentID: "main",
	}}); cmd != nil {
		_ = cmd()
	}
	if m.terminalTitleTickRunning {
		t.Fatal("title ticker should be stopped during Compacting")
	}

	// Streaming again → ticker should be running again
	if cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		Type:    agent.ActivityStreaming,
		AgentID: "main",
	}}); cmd == nil {
		t.Fatal("returning to Streaming should schedule animation work")
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("title ticker should resume after activity returns to Streaming")
	}
	if m.terminalTitleTickGeneration <= genAfterStreaming {
		t.Fatalf("ticker generation should advance on resume; got %d, prev %d",
			m.terminalTitleTickGeneration, genAfterStreaming)
	}
}

func TestBackgroundBusyToIdleShowsOneShotCompletionTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "background complete"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.terminalTitleTickRunning = true

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}})

	if m.terminalTitleBackgroundCompletedAgentID == "" {
		t.Fatal("background busy→idle should set one-shot completion marker")
	}
	if got := m.currentTitleMode(); got != terminalTitleModeCompletion {
		t.Fatalf("title mode = %v, want completion", got)
	}
	if m.terminalTitleTickRunning {
		t.Fatal("completion title should stop the spinner ticker")
	}
}

func TestForegroundBusyToIdleDoesNotShowCompletionTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.terminalTitleBase = "foreground complete"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}})

	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatal("foreground busy→idle should not set completion marker")
	}
	if got := m.currentTitleMode(); got == terminalTitleModeCompletion {
		t.Fatalf("title mode = %v, should not be completion", got)
	}
}

func TestIdleToIdleDoesNotShowCompletionTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "already idle"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}})

	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatal("idle→idle should not set completion marker")
	}
}

func TestFocusClearsCompletionTitleAndBlurDoesNotReadd(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "focus clears"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.terminalTitleBackgroundCompletedAgentID = "main"
	m.setTerminalTitle(terminalTitleModeCompletion)

	_ = m.handleFocusMsg()

	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatal("focus should clear completion marker")
	}
	if got := m.currentTitleMode(); got == terminalTitleModeCompletion {
		t.Fatalf("title mode = %v, completion should be cleared", got)
	}

	_ = m.handleBlurMsg()
	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatal("blur/focus switching should not re-add completion marker without a new busy→idle transition")
	}
}

func TestRequestTitleTakesPriorityOverCompletionTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "needs input"
	m.terminalTitleBackgroundCompletedAgentID = "main"
	m.question.request = &QuestionRequest{Questions: []tools.QuestionItem{{Header: "Name", Question: "Who?"}}}

	if got := m.currentTitleMode(); got != terminalTitleModeRequest {
		t.Fatalf("title mode = %v, want request", got)
	}
}

func TestBackgroundAgentDoneShowsCompletionForFocusedSubagent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.focusedAgentID = "agent-1"
	m.terminalTitleBase = "subagent complete"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "agent-1"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", Summary: "done"}})

	if m.terminalTitleBackgroundCompletedAgentID == "" {
		t.Fatal("focused background subagent completion should set completion marker")
	}
	if got := m.currentTitleMode(); got != terminalTitleModeCompletion {
		t.Fatalf("title mode = %v, want completion", got)
	}
}

func TestBackgroundAgentDoneDoesNotShowCompletionForUnfocusedSubagent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.focusedAgentID = "agent-2"
	m.terminalTitleBase = "other subagent complete"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "agent-1"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentDoneEvent{AgentID: "agent-1", Summary: "done"}})

	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatal("unfocused subagent completion should not set completion marker")
	}
}

func TestNewBusyActivityClearsPreviousCompletionMarkerOnlyForSameAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.focusedAgentID = "main"
	m.terminalTitleBase = "new work"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.terminalTitleBackgroundCompletedAgentID = "main"

	// Other agent starts work: should not clear main completion marker.
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "agent-1"}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}})
	if m.terminalTitleBackgroundCompletedAgentID != "main" {
		t.Fatalf("other agent activity should not clear completion marker; got %q", m.terminalTitleBackgroundCompletedAgentID)
	}

	// Same agent starts work: should clear.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}})
	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatal("new busy activity should clear stale completion marker for same agent")
	}
	if got := m.currentTitleMode(); got != terminalTitleModeSpinner {
		t.Fatalf("title mode = %v, want spinner for new busy work", got)
	}
}

func TestMainLoopKeepsBusyDoesNotShowCompletionTitle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.terminalTitleBase = "loop idle event"
	m.agent = loopBusyAgentStub{}
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})

	if m.terminalTitleBackgroundCompletedAgentID != "" {
		t.Fatalf("completion marker should not set while loop keeps main busy; got %q", m.terminalTitleBackgroundCompletedAgentID)
	}
	if got := m.currentTitleMode(); got == terminalTitleModeCompletion {
		t.Fatalf("title mode should not be completion while loop busy; got %v", got)
	}
}
