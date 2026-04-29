package tui

import (
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleNormalKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	m.maybeClearSearchSessionForNormalKey(key)
	// Any key other than Quit clears the "press again to quit" hint.
	if !keyMatches(key, m.keyMap.Quit) {
		m.clearPendingQuit()
	}

	if key == "esc" {
		if m.search.State.Active {
			m.clearActiveSearch()
			m.recalcViewportSize()
			return nil
		}
		if m.chord.active() {
			m.clearChordState()
			return nil
		}
		if m.agent != nil && m.agent.CurrentLoopState() != "" {
			m.agent.DisableLoopMode()
			return m.enqueueToast("Loop disabled.", "info")
		}
		// First, try to cancel a busy turn
		if cmd := m.cancelBusyAgent(); cmd != nil {
			return cmd
		}
		// If no turn to cancel, try to cancel an in-flight compaction
		if m.agent != nil && m.agent.IsCompactionRunning() {
			if m.agent.CancelCompaction() {
				return m.enqueueToast("Cancelling context compaction...", "info")
			}
		}
		return nil
	}

	if digit, ok := normalCountDigit(key); ok {
		if m.chord.op != chordNone {
			m.clearChordState()
			return nil
		}
		if m.chord.count == 0 && digit == 0 {
			return nil
		}
		return m.appendChordCount(digit)
	}

	if m.chord.op != chordNone {
		count := m.chordCountOr(1)
		switch m.chord.op {
		case chordG:
			if keyMatches(key, m.keyMap.ScrollToTopSeq) {
				m.clearChordState()
				return m.jumpToVisibleBlockOrdinal(count)
			}
		case chordY:
			if key == "y" {
				m.clearChordState()
				if count > 1 {
					return m.copyFocusedBlocks(count)
				}
				return m.copyFocusedBlock()
			}
		case chordD:
			if key == "d" {
				m.clearChordState()
				return m.clearInputAndAttachments()
			}
		case chordE:
			if keyMatches(key, m.keyMap.ForkSession) {
				m.clearChordState()
				if m.agent == nil || m.focusedAgentID != "" || m.focusedBlockID < 0 {
					return nil
				}
				if m.isAgentBusy() {
					return m.enqueueToast("Wait until the agent is idle before forking", "warn")
				}
				if block := m.viewport.GetFocusedBlock(m.focusedBlockID); block != nil &&
					block.Type == BlockUser && !block.IsUserLocalShell() && block.MsgIndex >= 0 {
					m.beginSessionSwitch("fork", "")
					m.agent.ForkSession(block.MsgIndex)
				}
				return nil
			}
		}
		m.clearChordState()
		return nil
	}

	if m.chord.count > 0 {
		switch {
		case keyMatches(key, m.keyMap.ScrollDown):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.repeatNormalVertical(1, count)
		case keyMatches(key, m.keyMap.ScrollUp):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.repeatNormalVertical(-1, count)
		case keyMatches(key, m.keyMap.NextBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.repeatNormalBoundary(1, count)
		case keyMatches(key, m.keyMap.PrevBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.repeatNormalBoundary(-1, count)
		case keyMatches(key, m.keyMap.ScrollToBottom):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.jumpToVisibleBlockOrdinal(count)
		case keyMatches(key, m.keyMap.ScrollToTopSeq):
			return m.startChordOp(chordG)
		case key == "y":
			if m.focusedBlockID < 0 {
				m.setFocusedBlockFromViewport()
			}
			return m.startChordOp(chordY)
		case key == "d":
			return m.startChordOp(chordD)
		default:
			m.clearChordState()
			return nil
		}
	}

	if cmd := m.maybeExportDiagnosticsShortcut(key); cmd != nil {
		return cmd
	}

	switch {
	// -- mode switches ---------------------------------------------------
	case keyMatches(key, m.keyMap.EnterInsert):
		m.clearActiveSearch()
		m.switchModeWithIME(ModeInsert)
		m.recalcViewportSize()
		m.clearFocusedBlock()
		cmd := m.input.Focus()
		return cmd

	case keyMatches(key, m.keyMap.Quit):
		now := time.Now()
		// Exit only on second consecutive q (not q then Ctrl+C).
		if m.pendingQuitBy == "q" && !m.pendingQuitAt.IsZero() && now.Sub(m.pendingQuitAt) < pendingQuitWindow {
			m.clearPendingQuit()
			m.quitting = true
			return tea.Quit
		}
		// Other key (e.g. had pressed Ctrl+C first): cancel wait, do not start new one.
		if m.pendingQuitBy == "ctrl+c" {
			m.clearPendingQuit()
			return nil
		}
		m.pendingQuitAt = now
		m.pendingQuitBy = "q"
		m.pendingQuitGen++
		return clearPendingQuitTick(m.pendingQuitGen)

	case keyMatches(key, m.keyMap.HelpToggle):
		return m.openHelp()

	// -- basic scroll / block navigation ---------------------------------
	case keyMatches(key, m.keyMap.ScrollDown):
		return m.repeatNormalVertical(1, 1)
	case keyMatches(key, m.keyMap.ScrollUp):
		return m.repeatNormalVertical(-1, 1)

	case key == "y":
		if m.hasMouseSelection() {
			text := m.viewport.ExtractSelectionText(m.mouseSelectionRange())
			if text != "" {
				m.clearMouseSelection()
				return writeClipboardCmd(text, "Selection copied to clipboard")
			}
		}
		if m.focusedBlockID < 0 {
			m.setFocusedBlockFromViewport()
		}
		return m.startChordOp(chordY)

	// -- clear input ("dd") ------------------------------------------------
	case key == "d":
		return m.startChordOp(chordD)

	// -- half / full page ------------------------------------------------
	case keyMatches(key, m.keyMap.FullPageDown):
		prevOffset := m.viewport.offset
		if m.hasDeferredStartupTranscript() && m.viewport.atBottom() {
			m.maybePageStartupDeferredTranscriptWindow(1, "page_down")
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
		m.viewport.ScrollDown(m.viewport.height)
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	case keyMatches(key, m.keyMap.FullPageUp):
		prevOffset := m.viewport.offset
		if m.hasDeferredStartupTranscript() && m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
			m.maybePageStartupDeferredTranscriptWindow(-1, "page_up")
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
		m.viewport.ScrollUp(m.viewport.height)
		return m.refreshInlineImagesIfViewportMoved(prevOffset)

	// -- jump to top / bottom --------------------------------------------
	case keyMatches(key, m.keyMap.ScrollToBottom):
		prevOffset := m.viewport.offset
		if m.hasDeferredStartupTranscript() {
			m.maybeSwitchStartupDeferredTranscriptWindow(startupTranscriptWindowTail, "jump_bottom")
		}
		m.viewport.ScrollToBottom()
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	case keyMatches(key, m.keyMap.ScrollToTopSeq):
		return m.startChordOp(chordG)

	// -- block boundaries ------------------------------------------------
	case keyMatches(key, m.keyMap.NextBlock):
		return m.repeatNormalBoundary(1, 1)
	case keyMatches(key, m.keyMap.PrevBlock):
		return m.repeatNormalBoundary(-1, 1)

	// -- toggle collapse / open image viewer ------------------------------
	case keyMatches(key, m.keyMap.ToggleCollapse):
		toggleAtOffset := func() {
			m.viewport.ToggleBlockAtOffset()
		}
		if m.focusedBlockID >= 0 {
			if block := m.viewport.GetFocusedBlock(m.focusedBlockID); block != nil {
				m.recordTUIDiagnostic("toggle-block", "block=%d type=%s collapsed=%t read_expanded=%t detail_expanded=%t linked_agent=%q", block.ID, debugBlockTypeString(block.Type), block.Collapsed, block.ReadContentExpanded, block.ToolCallDetailExpanded, block.LinkedAgentID)
				// For linked Delegate blocks, Enter jumps to the worker view while space/o
				// keep the normal expand/collapse behavior.
				if key == "enter" && block.LinkedAgentID != "" {
					m.maybeSwitchToTaskAgent(block)
				} else if part, ok := block.firstImagePart(m.viewport.width); ok && m.imageCaps.SupportsFullscreen {
					m.openImageViewer(block.ID, part.Index)
					return m.imageProtocolCmd()
				} else {
					m.viewport.ToggleBlockByID(m.focusedBlockID)
				}
			} else {
				m.focusedBlockID = -1
				m.refreshBlockFocus()
				toggleAtOffset()
			}
		} else {
			toggleAtOffset()
		}

	// -- fork session (edit selected user block) -------------------------
	case keyMatches(key, m.keyMap.ForkSession):
		if m.agent == nil || m.focusedAgentID != "" || m.focusedBlockID < 0 {
			return nil
		}
		if m.isAgentBusy() {
			return m.enqueueToast("Wait until the agent is idle before forking", "warn")
		}
		if block := m.viewport.GetFocusedBlock(m.focusedBlockID); block != nil &&
			block.Type == BlockUser && !block.IsUserLocalShell() && block.MsgIndex >= 0 {
			return m.startChordOp(chordE)
		}
		return nil

	// -- message directory -----------------------------------------------
	case keyMatches(key, m.keyMap.Directory):
		if m.hasDeferredStartupTranscript() {
			return m.openDeferredStartupTranscriptDirectory()
		}
		m.dirEntries = m.viewport.MessageDirectory()
		m.dirList = NewOverlayList(directoryItems(m.dirEntries), m.directoryMaxVisible())
		cmd := m.switchModeWithIME(ModeDirectory)
		m.recalcViewportSize()
		return cmd

	// -- usage stats -----------------------------------------------------
	case keyMatches(key, m.keyMap.UsageStats):
		m.openUsageStats()
		return nil

	// -- search ----------------------------------------------------------
	case keyMatches(key, m.keyMap.SearchStart):
		if m.hasDeferredStartupTranscript() {
			return m.beginDeferredStartupTranscriptSearch()
		}
		m.search = NewSearchModel(m.mode)
		sr := m.search.Input
		sr.SetWidth(m.width - 4)
		m.search.Input = sr
		m.clearChordState()
		m.mode = ModeSearch
		m.recalcViewportSize()
		return textinput.Blink

	case keyMatches(key, m.keyMap.SearchNext):
		if m.search.State.Active && m.search.State.HasMatches() {
			if match, ok := NextMatch(&m.search.State); ok {
				prevOffset := m.viewport.offset
				if m.maybeScrollToSearchMatch(match, "search_next") {
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
			}
		}

	case keyMatches(key, m.keyMap.SearchPrev):
		if m.search.State.Active && m.search.State.HasMatches() {
			if match, ok := PrevMatch(&m.search.State); ok {
				prevOffset := m.viewport.offset
				if m.maybeScrollToSearchMatch(match, "search_prev") {
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
			}
		}

	// -- multi-agent switch (Shift+Tab: cycle view) ----------------------
	case keyMatches(key, m.keyMap.SwitchAgent):
		return m.handleSwitchAgent()

	// -- main agent role switch (Tab: only in main view) ------------------
	case keyMatches(key, m.keyMap.SwitchRole):
		if m.focusedAgentID == "" {
			m.handleSwitchRole()
		}

	// -- model selector ---------------------------------------------------
	case keyMatches(key, m.keyMap.SwitchModel):
		m.openModelSelect()
	}

	return nil
}

func (m *Model) repeatNormalVertical(dir, count int) tea.Cmd {
	if count < 1 {
		count = 1
	}
	prevOffset := m.viewport.offset
	if dir < 0 && m.hasDeferredStartupTranscript() && m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
		if m.maybeStepStartupDeferredTranscriptWindow(-1, "scroll_up") {
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
		m.maybeHydrateStartupDeferredTranscript("scroll_up")
	}
	if dir > 0 && m.hasDeferredStartupTranscript() && m.viewport.atBottom() {
		if m.maybeStepStartupDeferredTranscriptWindow(1, "scroll_down") {
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
	}
	if m.focusedBlockID >= 0 {
		for range count {
			m.navigateFocusedBlock(dir)
		}
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}
	if dir > 0 {
		m.viewport.ScrollDown(count)
	} else {
		m.viewport.ScrollUp(count)
	}
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

func (m *Model) repeatNormalBoundary(dir, count int) tea.Cmd {
	if count < 1 {
		count = 1
	}
	prevOffset := m.viewport.offset
	if dir < 0 && m.hasDeferredStartupTranscript() && m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
		if m.maybeStepStartupDeferredTranscriptWindow(-1, "prev_boundary") {
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
		m.maybeHydrateStartupDeferredTranscript("prev_boundary")
	}
	if dir > 0 && m.hasDeferredStartupTranscript() && m.viewport.atBottom() {
		if m.maybeStepStartupDeferredTranscriptWindow(1, "next_boundary") {
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
	}
	for range count {
		if dir > 0 {
			m.viewport.NextMessageBoundary()
		} else {
			m.viewport.PrevMessageBoundary()
		}
		m.setFocusedBlockFromViewport()
	}
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

func (m *Model) clearInputAndAttachments() tea.Cmd {
	m.input.SetDisplayValueAndPastes("", nil, 0)
	m.input.syncHeight()
	m.attachments = nil
	m.closeAtMention()
	m.recalcViewportSize()
	return nil
}
