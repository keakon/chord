package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestHandleFocusMsgSkipsImmediateHostRedrawWhenFreezeEnabled(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.useFocusResizeFreeze = true
	m.displayState = stateBackground

	if cmd := m.handleFocusMsg(); cmd != nil {
		_ = cmd()
	}

	if !m.lastHostRedrawAt.IsZero() {
		t.Fatal("focus restore should not issue immediate host redraw when freeze is enabled")
	}
}

func TestCurrentCadenceReturnsForegroundWhenFocused(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground

	c := m.currentCadence()
	if c.contentFlushDelay != 200*time.Millisecond {
		t.Fatalf("foreground contentFlushDelay = %v, want 200ms", c.contentFlushDelay)
	}
	if c.visualAnimDelay != 200*time.Millisecond {
		t.Fatalf("foreground visualAnimDelay = %v, want 200ms", c.visualAnimDelay)
	}
	if c.titleTickerDelay != titleSpinnerCadence {
		t.Fatalf("foreground titleTickerDelay = %v, want %v", c.titleTickerDelay, titleSpinnerCadence)
	}
	if !c.hostRedrawAllowed {
		t.Fatal("foreground should allow host redraw")
	}
	if c.aggressiveHotBudget {
		t.Fatal("foreground should not use aggressive hot budget")
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenBusy(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.backgroundIdleSince = time.Now().Add(-time.Minute)

	c := m.currentCadence()
	if c.contentFlushDelay != time.Second {
		t.Fatalf("background-active contentFlushDelay = %v, want 1s", c.contentFlushDelay)
	}
	if c.visualAnimDelay != time.Second {
		t.Fatalf("background-active visualAnimDelay = %v, want 1s", c.visualAnimDelay)
	}
	if c.titleTickerDelay != titleSpinnerCadence {
		t.Fatalf("background-active titleTickerDelay = %v, want %v", c.titleTickerDelay, titleSpinnerCadence)
	}
	if c.hostRedrawAllowed {
		t.Fatal("background-active should not allow host redraw")
	}
	if c.aggressiveHotBudget {
		t.Fatal("background-active should not use aggressive hot budget")
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenPendingToolWork(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockToolCall, ToolName: "Read", ResultDone: false})

	c := m.currentCadence()
	if c != backgroundActiveCadence {
		t.Fatalf("cadence = %#v, want backgroundActiveCadence", c)
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenPendingLocalShell(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo hi", UserLocalShellPending: true})

	c := m.currentCadence()
	if c != backgroundActiveCadence {
		t.Fatalf("cadence = %#v, want backgroundActiveCadence", c)
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenCooling(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main"}

	c := m.currentCadence()
	if c != backgroundActiveCadence {
		t.Fatalf("cadence = %#v, want backgroundActiveCadence", c)
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenCompacting(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}

	c := m.currentCadence()
	if c != backgroundActiveCadence {
		t.Fatalf("cadence = %#v, want backgroundActiveCadence", c)
	}
}

func TestHasActiveAnimationIgnoresCompactingOnlyActivity(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}

	if m.hasActiveAnimation() {
		t.Fatal("compacting-only activity should not keep the visual animation loop active")
	}
}

func TestCompactingActivityDoesNotStartAnimTick(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{
		Type:    agent.ActivityCompacting,
		AgentID: "main",
		Detail:  "context",
	}})

	if cmd != nil {
		t.Fatal("compacting activity should not schedule a visual animation tick")
	}
	if m.animRunning {
		t.Fatal("compacting activity should not leave animRunning enabled")
	}
}

func TestAnimTickStopsWhenCompactingIsOnlyActivity(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCompacting, AgentID: "main"}
	m.animRunning = true

	updated, cmd := m.Update(animTickMsg(time.Now()))
	model := updated.(*Model)

	if cmd != nil {
		t.Fatal("compacting-only anim tick should not schedule another animation batch")
	}
	if model.animRunning {
		t.Fatal("compacting-only anim tick should stop the visual animation loop")
	}
}

func TestCurrentCadenceReturnsBackgroundIdleWhenNotBusy(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.backgroundIdleSince = time.Now().Add(-time.Minute)

	c := m.currentCadence()
	if c.contentFlushDelay != 0 {
		t.Fatalf("background-idle contentFlushDelay = %v, want 0", c.contentFlushDelay)
	}
	if c.visualAnimDelay != 0 {
		t.Fatalf("background-idle visualAnimDelay = %v, want 0 (disabled)", c.visualAnimDelay)
	}
	if c.titleTickerDelay != 0 {
		t.Fatalf("background-idle titleTickerDelay = %v, want 0", c.titleTickerDelay)
	}
	if c.hostRedrawAllowed {
		t.Fatal("background-idle should not allow host redraw")
	}
	if !c.aggressiveHotBudget {
		t.Fatal("background-idle should use aggressive hot budget")
	}
}

func TestHandleBlurMsgTransitionsToBackground(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground

	cmd := m.handleBlurMsg()

	if cmd == nil {
		t.Fatal("handleBlurMsg should start idle sweep tracking when entering background")
	}
	if m.displayState != stateBackground {
		t.Fatalf("displayState = %v, want stateBackground", m.displayState)
	}
	if m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should be set when background becomes idle")
	}
	if m.lastBackgroundAt.IsZero() {
		t.Fatal("lastBackgroundAt should be set")
	}
	if !m.idleSweepScheduled {
		t.Fatal("handleBlurMsg should schedule idle sweep when entering background idle")
	}
}

func TestHandleFocusMsgTransitionsToForeground(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.idleSweepScheduled = true
	m.idleSweepGeneration = 7

	cmd := m.handleFocusMsg()

	if m.displayState != stateForeground {
		t.Fatalf("displayState = %v, want stateForeground", m.displayState)
	}
	if m.lastForegroundAt.IsZero() {
		t.Fatal("lastForegroundAt should be set")
	}
	if m.idleSweepScheduled {
		t.Fatal("idleSweepScheduled should be cleared on focus")
	}
	if m.idleSweepGeneration != 8 {
		t.Fatalf("idleSweepGeneration = %d, want 8", m.idleSweepGeneration)
	}
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should be cleared on focus")
	}
	_ = cmd
}

func TestScheduleStreamFlushUsesCadenceDelay(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	// Foreground: should use 200ms default.
	m.displayState = stateForeground
	_ = m.scheduleStreamFlush(0)

	// Background-idle: should not schedule automatic stream flush.
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.streamFlushScheduled = false

	if cmd := m.scheduleStreamFlush(0); cmd != nil {
		t.Fatal("background-idle should not schedule stream flush when no explicit delay is requested")
	}
}

func TestHostRedrawForStreamingSkipsInBackground(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)

	// Foreground: should allow redraw.
	m.displayState = stateForeground
	m.currentAssistantBlock = &Block{ID: 1, Type: BlockAssistant, Streaming: true}
	cmd := m.hostRedrawForStreamingCmd("test")
	if cmd == nil {
		t.Fatal("foreground streaming should allow host redraw")
	}

	// Background-idle: should skip redraw.
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	cmd = m.hostRedrawForStreamingCmd("test")
	if cmd != nil {
		t.Fatal("background-idle should skip host redraw")
	}

	// Background-active: should also skip.
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	cmd = m.hostRedrawForStreamingCmd("test")
	if cmd != nil {
		t.Fatal("background-active should skip host redraw")
	}
}

func TestHostRedrawForStreamingSkipsPeriodicViewerRedraw(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.SetFocusResizeFreezeEnabled(true)
	m.displayState = stateForeground
	m.mode = ModeImageViewer
	m.imageViewer = imageViewerState{Open: true}
	m.currentAssistantBlock = &Block{ID: 1, Type: BlockAssistant, Streaming: true}

	if cmd := m.hostRedrawForStreamingCmd("stream-flush"); cmd != nil {
		t.Fatal("image viewer should suppress periodic stream-flush redraws")
	}
	if cmd := m.hostRedrawForStreamingCmd("scroll-flush"); cmd != nil {
		t.Fatal("image viewer should suppress scroll-flush redraws")
	}
}

func TestBackgroundIdleSweepStartsOnBlurWhileBusyAndStopsOnIdle(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	if cmd := m.handleBlurMsg(); cmd != nil {
		_ = cmd()
	}
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should remain zero while still busy")
	}
	if m.idleSweepScheduled {
		t.Fatal("idle sweep should not schedule while still busy")
	}

	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	if cmd := m.updateBackgroundIdleSweepState(); cmd == nil {
		t.Fatal("expected idle sweep scheduling once background becomes idle")
	}
	if m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should be set once background becomes idle")
	}
	if !m.idleSweepScheduled {
		t.Fatal("idle sweep should be scheduled once background becomes idle")
	}
}

func TestBackgroundIdleSweepMaintainsBusyBackground(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.idleSweepScheduled = true
	m.backgroundIdleSince = time.Now().Add(-time.Minute)
	if cmd := m.updateBackgroundIdleSweepState(); cmd != nil {
		t.Fatal("busy background should not schedule idle sweep")
	}
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should be cleared while busy in background")
	}
	if m.idleSweepScheduled {
		t.Fatal("idleSweepScheduled should be cleared while busy in background")
	}
}

func TestBackgroundIdleEntersRenderFreezeAfterQuietPeriod(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.backgroundIdleSince = time.Now().Add(-11 * time.Second)
	m.cachedFullView = tea.View{Content: "frozen"}
	m.cachedFullViewValid = true

	cmd := m.handleAnimTick()
	if cmd == nil {
		t.Fatal("background idle anim tick should continue housekeeping")
	}
	if !m.renderFreezeActive {
		t.Fatal("background idle should enter render freeze after quiet period")
	}
	if !m.cachedFrozenViewValid || m.cachedFrozenView.Content != "frozen" {
		t.Fatal("render freeze should capture cached frozen view")
	}
}

func TestFocusExitsRenderFreeze(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.renderFreezeActive = true
	m.cachedFrozenViewValid = true

	_ = m.handleFocusMsg()

	if m.renderFreezeActive {
		t.Fatal("focus should exit render freeze")
	}
	if !m.streamRenderForceView {
		t.Fatal("focus should force next live render")
	}
}

func TestViewReturnsFrozenViewDuringRenderFreeze(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.renderFreezeActive = true
	m.cachedFrozenView = tea.View{Content: "frozen-view"}
	m.cachedFrozenViewValid = true

	view := m.View()
	if view.Content != "frozen-view" {
		t.Fatalf("View content = %q, want frozen-view", view.Content)
	}
}

func TestPriorityBoundaryFlushExitsRenderFreeze(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.renderFreezeActive = true
	m.cachedFrozenView = tea.View{Content: "frozen-view"}
	m.cachedFrozenViewValid = true

	cmd := m.requestStreamBoundaryFlush()
	if cmd == nil {
		t.Fatal("priority boundary flush should schedule a flush command")
	}
	if m.renderFreezeActive {
		t.Fatal("priority boundary flush should exit render freeze")
	}
	if !m.streamRenderForceView {
		t.Fatal("priority boundary flush should force next live render")
	}
}

func TestBackgroundFreezeSkipsBatchTailStreamFlush(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.renderFreezeActive = true
	m.agent = nil

	updated, cmd := m.Update(agentEventBatchMsg{})
	model := updated.(*Model)
	if !model.renderFreezeActive {
		t.Fatal("empty agent batch should keep render freeze active")
	}
	if cmd != nil {
		_ = cmd()
	}
	if model.streamFlushScheduled {
		t.Fatal("render freeze should skip batch-tail stream flush scheduling")
	}
}

func TestBackgroundIdleSweepRecognizesPendingConfirmQuestionAsBusy(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.confirm.request = &ConfirmRequest{RequestID: "req-1"}
	if cmd := m.updateBackgroundIdleSweepState(); cmd != nil {
		t.Fatal("pending confirm should keep background from becoming idle")
	}
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should stay zero while confirm is pending")
	}
	if m.idleSweepScheduled {
		t.Fatal("idle sweep should not schedule while confirm is pending")
	}

	m.confirm.request = nil
	m.question.request = &QuestionRequest{Questions: []tools.QuestionItem{{Question: "continue?"}}}
	if cmd := m.updateBackgroundIdleSweepState(); cmd != nil {
		t.Fatal("pending question should keep background from becoming idle")
	}
	if !m.backgroundIdleSince.IsZero() {
		t.Fatal("backgroundIdleSince should stay zero while question is pending")
	}
	if m.idleSweepScheduled {
		t.Fatal("idle sweep should not schedule while question is pending")
	}
}
