package tui

import (
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestCurrentCadenceReturnsForegroundWhenFocused(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateForeground

	c := m.currentCadence()
	if c.contentFlushDelay != foregroundContentFlushCadence {
		t.Fatalf("foreground contentFlushDelay = %v, want %v", c.contentFlushDelay, foregroundContentFlushCadence)
	}
	if c.visualAnimDelay != visualSpinnerCadence {
		t.Fatalf("foreground visualAnimDelay = %v, want %v", c.visualAnimDelay, visualSpinnerCadence)
	}
	if c.titleTickerDelay != titleSpinnerCadence {
		t.Fatalf("foreground titleTickerDelay = %v, want %v", c.titleTickerDelay, titleSpinnerCadence)
	}
	if c.scrollFlushDelay != foregroundScrollFlushCadence {
		t.Fatalf("foreground scrollFlushDelay = %v, want %v", c.scrollFlushDelay, foregroundScrollFlushCadence)
	}
	if c.aggressiveHotBudget {
		t.Fatal("foreground should not use aggressive hot budget")
	}
}

func TestCadenceProfilesDetectLowForCmux(t *testing.T) {
	profiles := detectCadenceProfilesFromMap(map[string]string{"CMUX_SOCKET": "/tmp/cmux.sock"})
	c := profiles.foreground
	if c.contentFlushDelay != lowCadenceContentFlushDelay {
		t.Fatalf("low contentFlushDelay = %v, want %v", c.contentFlushDelay, lowCadenceContentFlushDelay)
	}
	if c.scrollFlushDelay != lowCadenceScrollFlushDelay {
		t.Fatalf("low scrollFlushDelay = %v, want %v", c.scrollFlushDelay, lowCadenceScrollFlushDelay)
	}
	if c.visualAnimDelay != 0 {
		t.Fatalf("low visualAnimDelay = %v, want 0", c.visualAnimDelay)
	}
	if c.titleTickerDelay != lowCadenceTitleTickerDelay {
		t.Fatalf("low titleTickerDelay = %v, want %v", c.titleTickerDelay, lowCadenceTitleTickerDelay)
	}
}

func TestEnvMsgAppliesCmuxCadenceProfile(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	updated, _ := m.Update(tea.EnvMsg{"CMUX_SOCKET=/tmp/cmux.sock"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	model.displayState = stateForeground
	c := model.currentCadence()
	if c.contentFlushDelay != lowCadenceContentFlushDelay {
		t.Fatalf("EnvMsg cmux contentFlushDelay = %v, want %v", c.contentFlushDelay, lowCadenceContentFlushDelay)
	}
	if c.scrollFlushDelay != lowCadenceScrollFlushDelay {
		t.Fatalf("EnvMsg cmux scrollFlushDelay = %v, want %v", c.scrollFlushDelay, lowCadenceScrollFlushDelay)
	}
	if c.visualAnimDelay != 0 {
		t.Fatalf("EnvMsg cmux visualAnimDelay = %v, want 0", c.visualAnimDelay)
	}
}

func TestMouseWheelUsesCadenceScrollFlushDelay(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.cadenceProfiles = lowCadenceProfiles()
	m.displayState = stateForeground

	cmd := m.handleMouseWheel(tea.Mouse{Button: tea.MouseWheelDown})
	if cmd == nil {
		t.Fatal("mouse wheel should schedule a scroll flush")
	}
	if m.pendingScrollDelta != mouseWheelScrollStep {
		t.Fatalf("pendingScrollDelta = %d, want %d", m.pendingScrollDelta, mouseWheelScrollStep)
	}
	if got, want := m.scrollFlushDelay(), lowCadenceScrollFlushDelay; got != want {
		t.Fatalf("scrollFlushDelay = %v, want %v", got, want)
	}
	if second := m.handleMouseWheel(tea.Mouse{Button: tea.MouseWheelDown}); second != nil {
		t.Fatal("second wheel event before flush should be coalesced")
	}
	if m.pendingScrollDelta != 2*mouseWheelScrollStep {
		t.Fatalf("pendingScrollDelta after coalescing = %d, want %d", m.pendingScrollDelta, 2*mouseWheelScrollStep)
	}
}

func TestCmuxCadenceStopsVisualAnimationButKeepsTitleTicker(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.cadenceProfiles = lowCadenceProfiles()
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}

	cmd := m.startActiveAnimation()
	if cmd == nil {
		t.Fatal("startActiveAnimation should still return title/housekeeping command")
	}
	if m.animRunning {
		t.Fatal("cmux cadence should not run visual animation ticks")
	}
	if m.activitySpinnerFrameIndex != 0 {
		t.Fatalf("activitySpinnerFrameIndex = %d, want 0", m.activitySpinnerFrameIndex)
	}
	if !m.terminalTitleTickRunning {
		t.Fatal("cmux cadence should keep terminal title ticker active")
	}
	if m.terminalTitleTickerDelay != lowCadenceTitleTickerDelay {
		t.Fatalf("terminalTitleTickerDelay = %v, want %v", m.terminalTitleTickerDelay, lowCadenceTitleTickerDelay)
	}
}

func TestCmuxCadenceThrottlesToolArgRenderingByContentCadence(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.cadenceProfiles = lowCadenceProfiles()
	now := time.Now()
	m.recordToolArgRender("call-1", `{"cmd":"echo"}`, now)

	if m.shouldRefreshToolArgRender("call-1", `{"cmd":"echo hi"}`, now.Add(499*time.Millisecond)) {
		t.Fatal("cmux cadence should not render tool args before content cadence elapses")
	}
	if !m.shouldRefreshToolArgRender("call-1", `{"cmd":"echo hi"}`, now.Add(500*time.Millisecond)) {
		t.Fatal("cmux cadence should render tool args after content cadence elapses")
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenBusy(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.currentAssistantBlock = &Block{ID: 1, Type: BlockAssistant, Streaming: true}
	m.backgroundIdleSince = time.Now().Add(-time.Minute)

	c := m.currentCadence()
	if c.contentFlushDelay != backgroundActiveContentFlushCadence {
		t.Fatalf("background-active contentFlushDelay = %v, want %v", c.contentFlushDelay, backgroundActiveContentFlushCadence)
	}
	if c.visualAnimDelay != backgroundActiveVisualAnimCadence {
		t.Fatalf("background-active visualAnimDelay = %v, want %v", c.visualAnimDelay, backgroundActiveVisualAnimCadence)
	}
	if c.titleTickerDelay != backgroundTitleSpinnerCadence {
		t.Fatalf("background-active titleTickerDelay = %v, want %v", c.titleTickerDelay, backgroundTitleSpinnerCadence)
	}
	if c.aggressiveHotBudget {
		t.Fatal("background-active should not use aggressive hot budget")
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenPendingToolWork(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockToolCall, ToolName: "read", ResultDone: false})

	c := m.currentCadence()
	if c != backgroundActiveCadence {
		t.Fatalf("cadence = %#v, want backgroundActiveCadence", c)
	}
}

func TestCurrentCadenceReturnsBackgroundActiveWhenPendingTerminal(t *testing.T) {
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
	m.animTickGeneration = 1

	updated, cmd := m.Update(animTickMsg{generation: 1, source: animTickSourceVisual})
	model := updated.(*Model)

	if cmd != nil {
		t.Fatal("compacting-only anim tick should not schedule another animation batch")
	}
	if model.animRunning {
		t.Fatal("compacting-only anim tick should stop the visual animation loop")
	}
	if model.animTickGeneration != 2 {
		t.Fatalf("animTickGeneration = %d, want 2 after stopping animation", model.animTickGeneration)
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
	m.animTickGeneration = 1
	m.backgroundIdleSince = time.Now().Add(-11 * time.Second)
	m.cachedFullView = tea.View{Content: "frozen"}
	m.cachedFullViewValid = true

	cmd := m.handleAnimTick(animTickMsg{generation: 1, source: animTickSourceHousekeeping})
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

func TestStaleVisualAnimTickDoesNotReviveAnimation(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.animTickGeneration = 3
	m.activitySpinnerFrameIndex = 1

	cmd := m.handleAnimTick(animTickMsg{generation: 2, source: animTickSourceVisual})
	if cmd != nil {
		t.Fatal("stale visual anim tick should be ignored")
	}
	if !m.animRunning {
		t.Fatal("stale visual anim tick should not stop active animation")
	}
	if m.activitySpinnerFrameIndex != 1 {
		t.Fatalf("activitySpinnerFrameIndex = %d, want unchanged 1", m.activitySpinnerFrameIndex)
	}
	if m.animTickGeneration != 3 {
		t.Fatalf("animTickGeneration = %d, want unchanged 3", m.animTickGeneration)
	}
}

func TestHousekeepingTickDoesNotReviveVisualAnimationAfterFocusRestart(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.animTickGeneration = 5

	staleCmd := m.handleAnimTick(animTickMsg{generation: 4, source: animTickSourceHousekeeping})
	if staleCmd != nil {
		t.Fatal("stale housekeeping tick should be ignored")
	}

	m.displayState = stateForeground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	startCmd := m.startActiveAnimation()
	if startCmd == nil {
		t.Fatal("startActiveAnimation should schedule a fresh visual tick")
	}
	if !m.animRunning {
		t.Fatal("visual animation should be running after restart")
	}
	if m.animTickGeneration != 6 {
		t.Fatalf("animTickGeneration = %d, want 6 after restart", m.animTickGeneration)
	}

	cmd := m.handleAnimTick(animTickMsg{generation: 5, source: animTickSourceHousekeeping})
	if cmd != nil {
		t.Fatal("stale housekeeping tick from prior generation should not reschedule animation")
	}
	if !m.animRunning {
		t.Fatal("stale housekeeping tick should not stop the fresh visual animation")
	}
	if m.activitySpinnerFrameIndex != 0 {
		t.Fatalf("activitySpinnerFrameIndex = %d, want unchanged 0", m.activitySpinnerFrameIndex)
	}
}

func TestBackgroundIdleTransitionSchedulesFreshHousekeepingAfterStoppingAnimation(t *testing.T) {
	oldIdleCadence := backgroundIdleCadence
	backgroundIdleCadence.housekeepingDelay = time.Nanosecond
	t.Cleanup(func() { backgroundIdleCadence = oldIdleCadence })

	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.animTickGeneration = 7
	m.idleSweepScheduled = true // keep the assertion focused on housekeeping, not idle-sweep scheduling.
	m.backgroundIdleSince = time.Now()

	cmd := m.handleAgentEvent(agentEventMsg{event: agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}})
	if cmd == nil {
		t.Fatal("background active→idle transition should schedule housekeeping after stopping animation")
	}
	if m.animRunning {
		t.Fatal("idle transition should stop visual animation")
	}
	if m.animTickGeneration != 8 {
		t.Fatalf("animTickGeneration = %d, want 8 after stopping animation", m.animTickGeneration)
	}

	messages := collectCommandMessages(cmd)
	var foundHousekeeping bool
	for _, msg := range messages {
		tick, ok := msg.(animTickMsg)
		if !ok {
			continue
		}
		if tick.source == animTickSourceVisual {
			t.Fatalf("idle transition scheduled visual tick: %+v", tick)
		}
		if tick.source == animTickSourceHousekeeping {
			foundHousekeeping = true
			if tick.generation != m.animTickGeneration {
				t.Fatalf("housekeeping tick generation = %d, want current generation %d", tick.generation, m.animTickGeneration)
			}
		}
	}
	if !foundHousekeeping {
		t.Fatalf("background active→idle transition did not schedule housekeeping tick; messages=%#v", messages)
	}

	if staleCmd := m.handleAnimTick(animTickMsg{generation: 7, source: animTickSourceVisual}); staleCmd != nil {
		t.Fatal("stale visual tick from stopped generation should be ignored")
	}
}

func TestBackgroundActiveStartAnimationSchedulesHousekeepingNotVisualTick(t *testing.T) {
	oldCadence := backgroundActiveCadence
	backgroundActiveCadence.housekeepingDelay = time.Nanosecond
	t.Cleanup(func() { backgroundActiveCadence = oldCadence })

	m := NewModelWithSize(nil, 80, 24)
	m.displayState = stateBackground
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.currentAssistantBlock = &Block{ID: 1, Type: BlockAssistant, Streaming: true}

	cmd := m.startActiveAnimation()
	if cmd == nil {
		t.Fatal("background-active animation start should keep housekeeping scheduled")
	}
	if m.animRunning {
		t.Fatal("background-active should not run invisible visual animation")
	}
	messages := collectCommandMessages(cmd)
	var foundHousekeeping bool
	for _, msg := range messages {
		tick, ok := msg.(animTickMsg)
		if !ok {
			continue
		}
		if tick.source == animTickSourceVisual {
			t.Fatalf("background-active scheduled visual tick: %+v", tick)
		}
		if tick.source == animTickSourceHousekeeping {
			foundHousekeeping = true
		}
	}
	if !foundHousekeeping {
		t.Fatalf("background-active start did not schedule housekeeping; messages=%#v", messages)
	}
}

func collectCommandMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch msg := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, sub := range msg {
			out = append(out, collectCommandMessages(sub)...)
		}
		return out
	default:
		return []tea.Msg{msg}
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
