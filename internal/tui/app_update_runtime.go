package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
	delay := m.currentTitleTickerDelay()
	if delay <= 0 {
		m.stopTerminalTitleTicker()
		return nil
	}
	switch m.currentTitleMode() {
	case terminalTitleModeSpinner:
		m.setTerminalTitle(terminalTitleModeSpinner)
	case terminalTitleModeRequest:
		m.terminalTitleRequestBlinkOff = !m.terminalTitleRequestBlinkOff
		m.setTerminalTitle(terminalTitleModeRequest)
	default:
		m.stopTerminalTitleTicker()
		return nil
	}
	return terminalTitleTickCmd(m.terminalTitleTickGeneration, delay)
}

func (m *Model) handleAnimTick() tea.Cmd {
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
	if cadence.visualAnimDelay > 0 && m.hasActiveAnimation() {
		return tea.Batch(animTickCmd(cadence.visualAnimDelay), m.scheduleStreamFlush(0))
	}

	// Visual animation disabled or no active animation.
	m.animRunning = false
	// Stop or downgrade the terminal title ticker to the current title mode.
	_ = m.syncTerminalTitleState()

	// Housekeeping tick: schedule a slower tick for background modes
	// so chord timeout, streaming stale detection, and background idle sweep
	// state tracking continue.
	if m.displayState != stateForeground {
		if entered := m.tryEnterRenderFreeze("background-idle"); entered {
			return tea.Tick(5*time.Second, func(time.Time) tea.Msg {
				return animTickMsg(time.Now())
			})
		}
		if idleCmd := m.updateBackgroundIdleSweepState(); idleCmd != nil {
			return tea.Batch(idleCmd, tea.Tick(cadence.housekeepingDelay, func(time.Time) tea.Msg {
				return animTickMsg(time.Now())
			}))
		}
		return tea.Tick(cadence.housekeepingDelay, func(time.Time) tea.Msg {
			return animTickMsg(time.Now())
		})
	}
	return nil
}

func (m *Model) handleStreamFlushTick(msg streamFlushTickMsg) tea.Cmd {
	if !m.consumeStreamFlush(msg) {
		return nil
	}
	m.exitRenderFreeze()
	m.streamRenderForceView = true
	m.streamRenderDeferred = false
	m.streamRenderDeferNext = false
	// Do NOT issue ClearScreen during streaming. On cmux/libghostty,
	// rapid ClearScreen (~250ms) creates a race between "clear" and
	// "repaint" that leaves ghost cells (e.g. a second separator line).
	// Bubble Tea's incremental renderer already updates every changed
	// cell without clearing, which is both faster and ghost-free.
	// ClearScreen is still used for focus-settle and scroll-flush
	// (infrequent, user-triggered) where it works correctly.
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
	if m.viewport == nil || !m.viewport.HasUserLocalShellPending() {
		m.localShellStartedAt = time.Time{}
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
	logToastExpired(m.activeToast)
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
	m.recalcViewportSize()
	return nil
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
	m.sessionSelect.loading = false
	m.sessionSelect.loadErr = ""
	if msg.err != nil {
		m.sessionSelect.options = nil
		m.sessionSelect.filteredIdx = nil
		m.sessionSelect.searchCorpus = nil
		if m.sessionSelect.list != nil {
			m.sessionSelect.list.SetItems(nil)
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
		block := &Block{
			ID:      m.nextBlockID,
			Type:    BlockError,
			Content: fmt.Sprintf("Failed to switch model: %s", msg.err),
		}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		return nil
	}
	// SwitchModel emits its own toast/event; keep local activity clean.
	m.markAgentIdle("main")
	m.stopActiveAnimationIfIdle()
	return nil
}

func (m *Model) handleImageProtocolTick(msg imageProtocolTickMsg) tea.Cmd {
	if msg.generation != m.focusResizeGeneration {
		m.recordTUIDiagnostic("image-protocol-skip", "reason=%s generation=%d current_generation=%d", strings.TrimSpace(msg.reason), msg.generation, m.focusResizeGeneration)
		return nil
	}
	m.recordTUIDiagnostic("image-protocol-replay", "reason=%s generation=%d visible_inline=%t mode=%s", strings.TrimSpace(msg.reason), msg.generation, m.viewport != nil && m.viewport.HasVisibleInlineImage(), debugModeString(m.mode))
	return m.imageProtocolCmdWithReason(msg.reason)
}

func (m *Model) handleHostRedrawSettle(msg hostRedrawSettleMsg) tea.Cmd {
	m.recordTUIDiagnostic("host-redraw-settle", "reason=%s frozen=%t mode=%s", strings.TrimSpace(msg.reason), m.focusResizeFrozen, debugModeString(m.mode))
	if m.focusResizeFrozen {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=%s settle_while_frozen=true", strings.TrimSpace(msg.reason))
		return nil
	}
	if m.suppressPeriodicViewerHostRedraw(msg.reason) {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=%s settle_viewer_open=true periodic=true", strings.TrimSpace(msg.reason))
		return nil
	}
	return m.imageProtocolCmdWithReason("host-redraw:" + strings.TrimSpace(msg.reason))
}

func (m *Model) handlePostFocusSettleRedraw(msg postFocusSettleRedrawMsg) tea.Cmd {
	if msg.generation != m.focusResizeGeneration {
		kind := "post-focus-settle-redraw-skip"
		if msg.fallback {
			kind = "post-focus-settle-fallback-skip"
		}
		m.recordTUIDiagnostic(kind, "generation=%d current=%d", msg.generation, m.focusResizeGeneration)
		return nil
	}
	if msg.fallback {
		bypassMinInterval := false
		if !m.lastForegroundAt.IsZero() && m.lastHostRedrawAt.After(m.lastForegroundAt) {
			if hostRedrawSuppressesPostFocusFallback(m.lastHostRedrawReason) {
				m.recordTUIDiagnostic("post-focus-settle-fallback-skip", "generation=%d host_redraw_after_focus=%s reason=%s", msg.generation, m.lastHostRedrawAt.Format(time.RFC3339Nano), strings.TrimSpace(m.lastHostRedrawReason))
				return nil
			}
			bypassMinInterval = true
		}
		m.recordTUIDiagnostic("post-focus-settle-fallback", "generation=%d mode=%s", msg.generation, debugModeString(m.mode))
		return m.hostRedrawCmdWithOptions("post-focus-settle-fallback", bypassMinInterval)
	}
	m.recordTUIDiagnostic("post-focus-settle-redraw", "generation=%d mode=%s", msg.generation, debugModeString(m.mode))
	return m.hostRedrawCmdWithOptions("post-focus-settle-redraw", true)
}

func (m *Model) handlePostHostRedrawFallback(msg postHostRedrawFallbackMsg) tea.Cmd {
	reason := strings.TrimSpace(msg.reason)
	if msg.generation != m.hostRedrawGeneration {
		m.recordTUIDiagnostic("post-host-redraw-fallback-skip", "reason=%s generation=%d current=%d", reason, msg.generation, m.hostRedrawGeneration)
		return nil
	}
	if reason != "scroll-flush" {
		m.recordTUIDiagnostic("post-host-redraw-fallback-skip", "reason=%s generation=%d unsupported=true", reason, msg.generation)
		return nil
	}
	m.recordTUIDiagnostic("post-host-redraw-fallback", "reason=%s generation=%d mode=%s", reason, msg.generation, debugModeString(m.mode))
	return m.hostRedrawCmd("scroll-flush-fallback")
}
