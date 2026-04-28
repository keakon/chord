package tui

import (
	"testing"

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

func TestHandleBlurMsgKeepsBackgroundActiveTitleTickerForBusyAgent(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.terminalTitleBase = "background busy"
	m.animRunning = true
	m.terminalTitleTickRunning = true

	if cmd := m.handleBlurMsg(); cmd != nil {
		_ = cmd()
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("background-active blur should keep title ticker running at low cadence")
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

	cmd := m.handleConfirmRequest(confirmRequestMsg{request: ConfirmRequest{ToolName: "Edit", RequestID: "req-1"}})
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

func TestResolveConfirmRestoresSpinnerWhenBusyWorkRemains(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.terminalTitleBase = "keep working"
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.confirm = confirmState{
		request:  &ConfirmRequest{ToolName: "Edit", ArgsJSON: `{}`},
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
