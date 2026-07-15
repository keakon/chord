package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func (m *Model) handleReconnected(msg reconnectedMsg) tea.Cmd {
	// Auto-reconnect succeeded: swap in the new agent and resume listening.
	if closer, ok := m.agent.(interface{ Close() }); ok {
		closer.Close()
	}
	m.agent = msg.agent
	m.syncWorkingDirFromAgent()
	m.agentHadEvent = false
	m.keyPoolTickGen++
	return tea.Batch(
		waitForAgentEvent(m.agent.Events()),
		m.enqueueToast("Reconnected", "info"),
		m.scheduleKeyPoolTick(),
	)
}

func (m *Model) handleReconnectFailed() tea.Cmd {
	// All reconnect attempts exhausted.
	return m.enqueueToast("Connection lost, please restart the client", "warn")
}

func (m *Model) handleTerminalTitleTick(msg terminalTitleTickMsg) tea.Cmd {
	if !m.terminalTitleTickRunning || msg.generation != m.terminalTitleTickGeneration {
		return nil
	}
	desired := m.deriveTerminalTitleState()
	if desired.tickerDelay <= 0 {
		m.stopTerminalTitleTicker()
		return nil
	}
	switch desired.mode {
	case terminalTitleModeSpinner:
		m.setTerminalTitle(terminalTitleModeSpinner)
	case terminalTitleModeRequest:
		m.terminalTitleRequestBlinkOff = !m.terminalTitleRequestBlinkOff
		m.setTerminalTitle(terminalTitleModeRequest)
	default:
		m.stopTerminalTitleTicker()
		return nil
	}
	return terminalTitleTickCmd(m.terminalTitleTickGeneration, desired.tickerDelay)
}

func (m *Model) handleAnimTick(msg animTickMsg) tea.Cmd {
	if msg.generation != m.animTickGeneration {
		return nil
	}
	// Housekeeping: chord timeout (must run even in background).
	if m.chord.active() && time.Since(m.chord.startAt) >= normalChordTimeout {
		m.clearChordState()
	}
	// Housekeeping: streaming stale detection (must run even in background).
	if m.streamingStale() {
		m.resetStreamingToIdle()
		return m.enqueueToast("Streaming timed out, connection may be lost, please reconnect", "warn")
	}

	cadence := m.currentCadence()
	m.flushVisibleRequestProgress(time.Now())

	// Visual animation: only continue if profile allows and there's active animation.
	if msg.source == animTickSourceVisual && cadence.visualAnimDelay > 0 && m.hasActiveAnimation() {
		if n := len(activeToolSpinnerSegments); n > 0 {
			m.activitySpinnerFrameIndex = (m.activitySpinnerFrameIndex + 1) % n
		}
		// Only schedule a stream flush when there's streaming content to sync.
		// During tool/shell execution (spinner-only, no streaming block), the
		// spinner frame is already advanced and will be rendered by this tick's
		// own View() call. Scheduling an extra forced flush just re-renders an
		// identical transcript frame — pure CPU waste on every terminal.
		var flushCmd tea.Cmd
		if m.hasActiveStreamBlock() {
			flushCmd = m.scheduleStreamFlush(0)
		}
		return tea.Batch(animTickCmd(m.animTickGeneration, animTickSourceVisual, cadence.visualAnimDelay), flushCmd)
	}

	// Visual animation disabled or no active animation.
	m.animRunning = false
	m.invalidateAnimTicks()
	m.activitySpinnerFrameIndex = 0
	// Stop or downgrade the terminal title ticker to the current title mode.
	_ = m.syncTerminalTitleState()

	// Housekeeping tick: schedule a slower tick for background modes
	// so chord timeout, streaming stale detection, and background idle sweep
	// state tracking continue.
	if m.displayState != stateForeground {
		if entered := m.tryEnterRenderFreeze("background-idle"); entered {
			return animTickCmd(m.animTickGeneration, animTickSourceHousekeeping, backgroundIdleAnimTickCadence)
		}
		if idleCmd := m.updateBackgroundIdleSweepState(); idleCmd != nil {
			return tea.Batch(idleCmd, animTickCmd(m.animTickGeneration, animTickSourceHousekeeping, cadence.housekeepingDelay))
		}
		return animTickCmd(m.animTickGeneration, animTickSourceHousekeeping, cadence.housekeepingDelay)
	}
	return nil
}

func (m *Model) handleStreamFlushTick(msg streamFlushTickMsg) tea.Cmd {
	if !m.consumeStreamFlush(msg) {
		return nil
	}
	if m.currentAssistantBlock != nil && m.flushStreamingBlock(m.currentAssistantBlock, m.assistantBlockAppended) && m.hasDeferredStartupTranscript() {
		m.syncStartupDeferredTranscriptBlock(m.currentAssistantBlock)
	}
	if m.currentThinkingBlock != nil && m.flushStreamingBlock(m.currentThinkingBlock, m.thinkingBlockAppended) && m.hasDeferredStartupTranscript() {
		m.syncStartupDeferredTranscriptBlock(m.currentThinkingBlock)
	}
	for agentID, state := range m.subAgentStreamStates {
		if state.assistant != nil && m.flushStreamingBlock(state.assistant, state.assistantAppended) && m.hasDeferredStartupTranscript() {
			m.syncStartupDeferredTranscriptBlock(state.assistant)
		}
		if state.thinking != nil && m.flushStreamingBlock(state.thinking, state.thinkingAppended) && m.hasDeferredStartupTranscript() {
			m.syncStartupDeferredTranscriptBlock(state.thinking)
		}
		m.subAgentStreamStates[agentID] = state
	}
	m.exitRenderFreeze()
	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
	// Do NOT issue ClearScreen during streaming. Bubble Tea's incremental renderer
	// already updates every changed cell, and terminal hard-scroll optimizations
	// are disabled at program construction to avoid stale rows.
	return nil
}

func (m *Model) handleShellBangResult(msg shellBangResultMsg) tea.Cmd {
	resultText := msg.output
	if msg.err != nil {
		if resultText != "" {
			resultText = resultText + "\n" + msg.err.Error()
		} else {
			resultText = msg.err.Error()
		}
	}
	if blk := m.viewport.GetFocusedBlock(msg.blockID); blk != nil && blk.IsUserLocalShell() {
		blk.UserLocalShellPending = false
		blk.UserLocalShellResult = resultText
		blk.UserLocalShellFailed = msg.err != nil
		blk.Collapsed = true
		blk.InvalidateCache()
		m.updateViewportBlock(blk)
		m.markBlockSettled(blk)
	}
	m.recalcViewportSize()
	if m.agent != nil {
		m.agent.AppendContextMessage(localShellContextMessage(msg.userLine, msg.cmd, msg.output, msg.err))
	}
	return nil
}

func (m *Model) handleToastTick() tea.Cmd {
	if m.activeToast == nil {
		return nil
	}
	if m.renderFreezeActive && shouldBreakFreezeForToastLevel(m.activeToast.Level) {
		m.exitRenderFreeze()
	}
	m.activeToast = nil
	if len(m.toastQueue) > 0 {
		next := m.toastQueue[0]
		m.toastQueue = m.toastQueue[1:]
		m.activeToast = &next
		m.toastGeneration++
		m.recalcViewportSize()
		if m.renderFreezeActive && shouldBreakFreezeForToastLevel(next.Level) {
			m.exitRenderFreeze()
		}
		return toastTickCmdForLevel(next.Level, m.toastGeneration)
	}
	return m.recalcViewportSizeForToastBoundary()
}

func (m *Model) handleClipboardWriteResult(msg clipboardWriteResultMsg) tea.Cmd {
	if m.renderFreezeActive {
		m.exitRenderFreeze()
	}
	if strings.TrimSpace(msg.success) != "" {
		return m.enqueueToast(msg.success, "info")
	}
	if msg.err != nil {
		return m.enqueueToast("Failed to copy to clipboard", "warn")
	}
	return nil
}

func (m *Model) handleSessionSummariesLoaded(msg sessionSummariesLoadedMsg) tea.Cmd {
	if m.mode != ModeSessionSelect || msg.seq != m.sessionSelect.loadSeq {
		return nil
	}
	m.sessionSelect.loading = false
	m.sessionSelect.loadErr = ""
	if msg.err != nil {
		m.sessionSelect.options = nil
		m.sessionSelect.filteredIdx = nil
		m.sessionSelect.searchCorpus = nil
		if m.sessionSelect.selector.list != nil {
			m.sessionSelect.selector.list.SetItems(nil)
		}
		m.invalidateSessionSelectDialogCache()
		m.sessionSelect.loadErr = msg.err.Error()
		m.recalcViewportSize()
		return nil
	}
	m.sessionSelect.options = msg.options
	m.sessionSelect.searchCorpus = buildSessionSearchCorpus(msg.options)
	m.rebuildSessionSelectFilteredView(false)
	m.recalcViewportSize()
	return loadSessionSummaryDetailsCmd(m.agent, msg.options, msg.seq)
}

func (m *Model) handleSessionSummaryDetailsLoaded(msg sessionSummaryDetailsLoadedMsg) tea.Cmd {
	if m.mode != ModeSessionSelect || msg.seq != m.sessionSelect.loadSeq || len(msg.options) == 0 {
		return nil
	}
	byID := make(map[string]agent.SessionSummary, len(msg.options))
	for _, option := range msg.options {
		if strings.TrimSpace(option.ID) != "" {
			byID[option.ID] = option
		}
	}
	for i, option := range m.sessionSelect.options {
		if updated, ok := byID[option.ID]; ok {
			m.sessionSelect.options[i] = updated
		}
	}
	m.sessionSelect.searchCorpus = buildSessionSearchCorpus(m.sessionSelect.options)
	m.rebuildSessionSelectFilteredView(false)
	m.recalcViewportSize()
	return nil
}

func (m *Model) handleProjectUsageLoaded(msg projectUsageLoadedMsg) tea.Cmd {
	if m.usageStats.rangeFilter != msg.rangeFilter {
		return nil
	}
	m.usageStats.projectLoading = false
	if msg.err != nil {
		m.usageStats.projectReport = nil
		m.usageStats.projectLoadErr = msg.err.Error()
		m.invalidateUsageStatsCache()
		return nil
	}
	m.usageStats.projectReport = msg.report
	m.usageStats.projectLoadErr = ""
	m.invalidateUsageStatsCache()
	return nil
}

func (m *Model) handleModelSwitchResult(msg modelSwitchResultMsg) tea.Cmd {
	if msg.err != nil {
		m.pendingPoolSwitch = pendingPoolSwitchState{}
		block := &Block{
			ID:      m.nextBlockID,
			Type:    BlockError,
			Content: fmt.Sprintf("Failed to switch model pool: %s", msg.err),
		}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
	}
	return nil
}
