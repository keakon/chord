package tui

import (
	"time"

	"github.com/keakon/bubbles/v2/textinput"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
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
		if m.expectedAgentClose {
			m.expectedAgentClose = false
			m.markAgentIdle(m.focusedAgentIDOrMain())
			m.inflightDraft = nil
			m.pauseQueuedDraftDrainOnce = true
			m.stopActiveAnimationIfIdle()
			m.clearPendingQuit()
			return nil
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
				m.clearMouseSelection()
				m.clearChordState()
				if m.focusedBlockID < 0 {
					m.setCopyFocusedBlockFromViewport()
				}
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
		case keyMatches(key, m.keyMap.NextUserBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.jumpToMatchingBlock(1, count, userTurnBlockMatch)
		case keyMatches(key, m.keyMap.PrevUserBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.jumpToMatchingBlock(-1, count, userTurnBlockMatch)
		case keyMatches(key, m.keyMap.NextAssistantBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.jumpToMatchingBlock(1, count, blockTypeMatch(BlockAssistant))
		case keyMatches(key, m.keyMap.PrevAssistantBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.jumpToMatchingBlock(-1, count, blockTypeMatch(BlockAssistant))
		case keyMatches(key, m.keyMap.NextSameTypeBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			if match, ok := m.sameTypeJumpMatch(); ok {
				return m.jumpToMatchingBlock(1, count, match)
			}
			return nil
		case keyMatches(key, m.keyMap.PrevSameTypeBlock):
			count := m.chordCountOr(1)
			m.clearChordState()
			if match, ok := m.sameTypeJumpMatch(); ok {
				return m.jumpToMatchingBlock(-1, count, match)
			}
			return nil
		case keyMatches(key, m.keyMap.ScrollToBottom):
			count := m.chordCountOr(1)
			m.clearChordState()
			return m.jumpToVisibleBlockOrdinal(count)
		case keyMatches(key, m.keyMap.ScrollToTopSeq):
			return m.startChordOp(chordG)
		case key == "y":
			if m.focusedBlockID < 0 {
				m.setCopyFocusedBlockFromViewport()
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
	if m.maybeServiceTierShortcut(key) {
		return nil
	}
	if m.maybeYoloShortcut(key) {
		return nil
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
		if m.quit.by == "q" && !m.quit.at.IsZero() && now.Sub(m.quit.at) < pendingQuitWindow {
			m.clearPendingQuit()
			m.quitting = true
			return tea.Quit
		}
		// Other key (e.g. had pressed Ctrl+C first): cancel wait, do not start new one.
		if m.quit.by == "ctrl+c" {
			m.clearPendingQuit()
			return nil
		}
		m.quit.at = now
		m.quit.by = "q"
		m.quit.gen++
		return clearPendingQuitTick(m.quit.gen)

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
			m.setCopyFocusedBlockFromViewport()
		}
		return m.startChordOp(chordY)

	// -- clear input ("dd") ------------------------------------------------
	case key == "d":
		return m.startChordOp(chordD)

	// -- half / full page ------------------------------------------------
	case keyMatches(key, m.keyMap.FullPageDown):
		return m.pageTranscript(1)
	case keyMatches(key, m.keyMap.FullPageUp):
		return m.pageTranscript(-1)

	// -- jump to top / bottom --------------------------------------------
	case keyMatches(key, m.keyMap.ScrollToBottom):
		return m.jumpToLastVisibleBlock()
	case keyMatches(key, m.keyMap.ScrollToTopSeq):
		return m.startChordOp(chordG)

	// -- block boundaries ------------------------------------------------
	case keyMatches(key, m.keyMap.NextBlock):
		return m.repeatNormalBoundary(1, 1)
	case keyMatches(key, m.keyMap.PrevBlock):
		return m.repeatNormalBoundary(-1, 1)

	// -- structural card jumps -------------------------------------------
	case keyMatches(key, m.keyMap.NextUserBlock):
		return m.jumpToMatchingBlock(1, 1, userTurnBlockMatch)
	case keyMatches(key, m.keyMap.PrevUserBlock):
		return m.jumpToMatchingBlock(-1, 1, userTurnBlockMatch)
	case keyMatches(key, m.keyMap.NextAssistantBlock):
		return m.jumpToMatchingBlock(1, 1, blockTypeMatch(BlockAssistant))
	case keyMatches(key, m.keyMap.PrevAssistantBlock):
		return m.jumpToMatchingBlock(-1, 1, blockTypeMatch(BlockAssistant))
	case keyMatches(key, m.keyMap.NextSameTypeBlock):
		if match, ok := m.sameTypeJumpMatch(); ok {
			return m.jumpToMatchingBlock(1, 1, match)
		}
		return nil
	case keyMatches(key, m.keyMap.PrevSameTypeBlock):
		if match, ok := m.sameTypeJumpMatch(); ok {
			return m.jumpToMatchingBlock(-1, 1, match)
		}
		return nil

	// -- toggle collapse / open image viewer ------------------------------
	case keyMatches(key, m.keyMap.ToggleCollapse):
		toggleAtOffset := func() {
			m.viewport.ToggleBlockAtOffset()
		}
		if m.focusedBlockID >= 0 {
			if block := m.viewport.GetFocusedBlock(m.focusedBlockID); block != nil && m.viewport.FocusedBlockIsVisible(m.focusedBlockID) {
				m.recordTUIDiagnostic("toggle-block", "block=%d type=%s collapsed=%t detail_expanded=%t linked_agent=%q", block.ID, debugBlockTypeString(block.Type), block.Collapsed, block.ToolCallDetailExpanded, block.LinkedAgentID)
				// A linked Delegate card is always expanded and has no expand/collapse
				// toggle: space/enter open its worker view. Click only selects the
				// card (see mouse handling), so the view switch lives on the keyboard
				// activation path.
				if block.ToolName == tools.NameDelegate && block.LinkedAgentID != "" {
					m.maybeSwitchToTaskAgent(block)
				} else if part, ok := block.firstImagePart(m.viewport.width); ok && m.imageCaps.SupportsFullscreen {
					m.openImageViewer(block.ID, part.Index)
					return m.imageProtocolCmd()
				} else if block.ToolName == tools.NameDelegate {
					// Always-expanded: nothing to toggle.
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
		m.dirList.SetCursor(directoryCursorForBlockID(m.dirEntries, m.currentDirectoryAnchorBlockID()))
		cmd := m.switchModeWithIME(ModeDirectory)
		m.recalcViewportSize()
		return cmd

	// -- usage stats -----------------------------------------------------
	case keyMatches(key, m.keyMap.UsageStats):
		m.openUsageStats()
		return nil

	// -- background jobs ------------------------------------------------
	// The jobs list is also reachable by clicking the status-bar pill, but a
	// terminal without mouse reporting (plain SSH, tmux with mouse off) has no
	// other way to inspect or stop a background job, so it needs a key too.
	case keyMatches(key, m.keyMap.BackgroundJobs):
		return m.openJobsOverlay()

	// -- error panel -----------------------------------------------------
	case keyMatches(key, m.keyMap.ErrorPanel):
		m.openErrorPanel()
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
	// Normal mode cycles the view, not the role: a view change is normally
	// followed by scrolling and reading, which is what this mode is for.
	// Role switching lives in Insert mode, where typing follows it.
	case keyMatches(key, m.keyMap.SwitchAgent):
		return m.handleSwitchAgent()

	// -- MCP selector --------------------------------------------------------
	case keyMatches(key, m.keyMap.MCP):
		m.openMCPSelect()
		return nil

	// -- model pool selector ---------------------------------------------------
	case keyMatches(key, m.keyMap.SwitchModel):
		m.openModelSelect()
	}

	return nil
}

// pageTranscript scrolls the conversation viewport one full page in the given
// direction (+1 down, -1 up), stepping the deferred startup transcript window
// when its edge is reached. Shared by Normal mode and Insert mode paging keys.
func (m *Model) pageTranscript(dir int) tea.Cmd {
	prevOffset := m.viewport.offset
	if dir > 0 {
		if m.hasDeferredStartupTranscript() && m.viewport.atBottom() {
			m.maybePageStartupDeferredTranscriptWindow(1, "page_down")
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
		m.viewport.ScrollDown(m.viewport.height)
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}
	if m.hasDeferredStartupTranscript() && m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
		m.maybePageStartupDeferredTranscriptWindow(-1, "page_up")
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}
	m.viewport.ScrollUp(m.viewport.height)
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

func (m *Model) repeatNormalVertical(dir, count int) tea.Cmd {
	if count < 1 {
		count = 1
	}
	prevOffset := m.viewport.offset
	if m.hasDeferredStartupTranscript() {
		if count == 1 {
			if dir < 0 && m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
				if m.maybeStepStartupDeferredTranscriptWindow(-1, "scroll_up") {
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
				m.maybeHydrateStartupDeferredTranscript("scroll_up")
			}
			if dir > 0 && m.viewport.atBottom() {
				if m.startupDeferredTranscriptAtTail() {
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
				if m.maybeStepStartupDeferredTranscriptWindow(1, "scroll_down") {
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
			}
		} else {
			trigger := "scroll_up"
			if dir > 0 {
				trigger = "scroll_down"
			}
			for range count {
				m.deferredScrollOneLine(dir, trigger)
			}
			return m.refreshInlineImagesIfViewportMoved(prevOffset)
		}
	}
	if m.focusedBlockID >= 0 && !m.hasDeferredStartupTranscript() {
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
	if m.hasDeferredStartupTranscript() {
		if count == 1 && m.focusedBlockID < 0 {
			if dir < 0 && m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
				if m.maybeStepStartupDeferredTranscriptWindow(-1, "prev_boundary") {
					m.setFocusedBlockFromViewport()
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
				if m.maybeHydrateStartupDeferredTranscript("prev_boundary") {
					m.setFocusedBlockFromViewport()
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
			}
			if dir > 0 && m.viewport.atBottom() {
				if !m.startupDeferredTranscriptAtTail() && m.maybeStepStartupDeferredTranscriptWindow(1, "next_boundary") {
					m.setFocusedBlockFromViewport()
					return m.refreshInlineImagesIfViewportMoved(prevOffset)
				}
			}
		}
		for range count {
			if !m.deferredMoveFocusedBlock(dir) {
				break
			}
		}
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}
	if m.focusedBlockID >= 0 {
		for range count {
			before := m.focusedBlockID
			m.navigateFocusedBlock(dir)
			if m.focusedBlockID < 0 {
				m.focusedBlockID = before
				m.refreshBlockFocus()
				break
			}
		}
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}
	for range count {
		before := m.viewport.offset
		if dir > 0 {
			m.viewport.NextMessageBoundary()
		} else {
			m.viewport.PrevMessageBoundary()
		}
		if m.viewport.offset != before {
			m.setFocusedBlockFromViewport()
			continue
		}
		blocks := m.viewport.visibleBlocks()
		if len(blocks) == 0 {
			m.focusedBlockID = -1
			m.refreshBlockFocus()
			continue
		}
		current := -1
		if dir > 0 {
			current = blocks[0].ID
		} else {
			current = blocks[len(blocks)-1].ID
		}
		nextID := focusNextSelectableBlockID(blocks, current, dir)
		if nextID < 0 {
			nextID = current
		}
		m.focusedBlockID = nextID
		m.refreshBlockFocus()
	}
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

func (m *Model) clearInputAndAttachments() tea.Cmd {
	m.cancelClipboardAttachmentPaste()
	m.input.SetDisplayValueAndPastes("", nil, 0)
	m.input.syncHeight()
	m.attachments = nil
	m.closeAtMention()
	m.recalcViewportSize()
	return nil
}
