package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) deferredStartupTranscriptDirectoryEntries() []DirectoryEntry {
	state := m.startupDeferredTranscript
	if state == nil || len(state.blockMeta) == 0 || m.viewport == nil {
		return nil
	}
	entries := make([]DirectoryEntry, 0, len(state.blockMeta))
	lineOffset := 0
	width := m.viewport.width
	if width <= 0 {
		width = 80
	}
	for i, meta := range state.blockMeta {
		entries = append(entries, DirectoryEntry{
			BlockIndex: i,
			BlockID:    meta.BlockID,
			LineOffset: lineOffset,
			Summary:    meta.Summary,
			Type:       meta.Type,
		})
		lineOffset += startupDeferredBlockLineCount(meta, width)
	}
	return entries
}

func (m *Model) deferredStartupTranscriptSearch(query string) []MatchPosition {
	state := m.startupDeferredTranscript
	if state == nil || len(state.blockMeta) == 0 || m.viewport == nil {
		return nil
	}
	return findMatchesInStartupDeferredBlockMeta(state.blockMeta, query, m.viewport.width)
}

func (m *Model) locateDeferredStartupTranscriptBlock(blockID int, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.blockMeta) == 0 || blockID < 0 || m.viewport == nil {
		return false
	}
	for i, meta := range state.blockMeta {
		if meta.BlockID == blockID {
			return m.maybeJumpDeferredStartupTranscriptOrdinal(i+1, trigger)
		}
	}
	return false
}

func (m *Model) startupDeferredBlockByID(blockID int) *Block {
	state := m.startupDeferredTranscript
	if state == nil || blockID < 0 {
		return nil
	}
	for _, block := range state.allBlocks {
		if block != nil && block.ID == blockID {
			return block
		}
	}
	return nil
}

func (m *Model) rehydrateStartupDeferredViewportBlock(blockID int) *Block {
	if m.viewport == nil || blockID < 0 {
		return nil
	}
	block := m.viewport.MaterializeBlockByID(blockID)
	source := m.startupDeferredBlockByID(blockID)
	if block == nil || source == nil {
		return block
	}
	inspect, temporary := source.inspectionBlock()
	if inspect != nil {
		source = inspect
		if temporary {
			defer source.InvalidateCache()
		}
	}
	preserveMutableBlockState(block, source)
	block.Content = source.Content
	block.ResultContent = source.ResultContent
	block.ResultStatus = source.ResultStatus
	block.ResultDone = source.ResultDone
	block.ToolExecutionState = source.ToolExecutionState
	block.DoneSummary = source.DoneSummary
	block.Diff = source.Diff
	block.ToolName = source.ToolName
	block.ToolID = source.ToolID
	block.IsError = source.IsError
	block.ReadContentExpanded = source.ReadContentExpanded
	block.ToolCallDetailExpanded = source.ToolCallDetailExpanded
	block.Collapsed = source.Collapsed
	block.InvalidateCache()
	return block
}

func (m *Model) openDeferredStartupTranscriptDirectory() tea.Cmd {
	m.clearActiveSearch()
	entries := m.deferredStartupTranscriptDirectoryEntries()
	m.dirEntries = entries
	m.dirList = NewOverlayList(directoryItems(entries), m.directoryMaxVisible())
	cmd := m.switchModeWithIME(ModeDirectory)
	m.recalcViewportSize()
	return cmd
}

func (m *Model) beginDeferredStartupTranscriptSearch() tea.Cmd {
	m.clearActiveSearch()
	m.search = NewSearchModel(m.mode)
	sr := m.search.Input
	sr.SetWidth(m.width - 4)
	m.search.Input = sr
	m.clearChordState()
	m.mode = ModeSearch
	m.recalcViewportSize()
	return textinput.Blink
}

func (m *Model) executeSearchAgainstCurrentTranscript(query string) {
	query = strings.TrimSpace(query)
	if m.hasDeferredStartupTranscript() {
		m.search.State.Query = query
		m.search.State.Active = true
		if query == "" {
			m.search.State.Matches = nil
			m.search.State.Current = -1
			return
		}
		m.search.State.Matches = m.deferredStartupTranscriptSearch(query)
		m.search.State.Current = initialSearchCurrentIndex(m.search.State.Matches, m.searchAnchorIndexInDeferredMeta())
		return
	}
	blocks := m.viewport.visibleBlocks()
	ExecuteSearch(&m.search.State, blocks, query, m.viewport.width, m.searchAnchorIndexInVisibleBlocks(blocks))
}

func (m *Model) maybeRevealSearchMatchBlock(match MatchPosition) {
	var block *Block
	if m.viewport != nil && match.BlockID > 0 {
		block = m.rehydrateStartupDeferredViewportBlock(match.BlockID)
		if block == nil {
			block = m.viewport.MaterializeBlockByID(match.BlockID)
		}
	}
	if block == nil {
		return
	}
	if revealSearchMatchedBlock(block) && m.viewport != nil {
		m.viewport.noteBlockLineCountMayChange()
		m.updateViewportBlock(block)
	}
	if isSelectableBlockType(block.Type) {
		m.focusedBlockID = block.ID
	} else {
		m.focusedBlockID = -1
	}
	m.refreshBlockFocus()
}

func (m *Model) maybeScrollToSearchMatch(match MatchPosition, trigger string) bool {
	applyMatchOffset := func(base int) {
		offset := base + match.InnerOffset
		if match.InnerOffset > 0 && m.viewport != nil {
			offset -= min(match.InnerOffset, max(0, m.viewport.height/3))
		}
		if offset < 0 {
			offset = 0
		}
		m.viewport.offset = offset
		m.viewport.clampOffset()
		m.viewport.sticky = m.viewport.atBottom()
	}
	if m.hasDeferredStartupTranscript() && match.BlockID > 0 {
		if !m.locateDeferredStartupTranscriptBlock(match.BlockID, trigger) {
			return false
		}
		if lineOffset, ok := m.viewport.LineOffsetForBlockID(match.BlockID); ok {
			applyMatchOffset(lineOffset)
			m.maybeRevealSearchMatchBlock(match)
			block := m.rehydrateStartupDeferredViewportBlock(match.BlockID)
			if block == nil {
				block = m.viewport.MaterializeBlockByID(match.BlockID)
			}
			if block != nil {
				m.viewport.noteBlockLineCountMayChange()
				m.updateViewportBlock(block)
			}
			if block != nil && !blockVisibleForSearch(block, m.viewport.width) {
				m.recordTUIDiagnostic("search-match-skip", "trigger=%s deferred=true block=%d invisible=true", strings.TrimSpace(trigger), match.BlockID)
				return false
			}
			if lineOffset, ok = m.viewport.LineOffsetForBlockID(match.BlockID); ok {
				applyMatchOffset(lineOffset)
				m.recordTUIDiagnostic("search-match-scroll", "trigger=%s deferred=true block=%d line_offset=%d inner_offset=%d viewport_offset=%d", strings.TrimSpace(trigger), match.BlockID, lineOffset, match.InnerOffset, m.viewport.offset)
			}
			return true
		}
		return false
	}
	applyMatchOffset(match.LineOffset)
	m.maybeRevealSearchMatchBlock(match)
	if match.BlockID > 0 {
		block := m.rehydrateStartupDeferredViewportBlock(match.BlockID)
		if block == nil {
			block = m.viewport.MaterializeBlockByID(match.BlockID)
		}
		if block != nil {
			m.viewport.noteBlockLineCountMayChange()
			m.updateViewportBlock(block)
		}
		if block != nil && !blockVisibleForSearch(block, m.viewport.width) {
			m.recordTUIDiagnostic("search-match-skip", "trigger=%s deferred=false block=%d invisible=true", strings.TrimSpace(trigger), match.BlockID)
			return false
		}
		if lineOffset, ok := m.viewport.LineOffsetForBlockID(match.BlockID); ok {
			applyMatchOffset(lineOffset)
			m.recordTUIDiagnostic("search-match-scroll", "trigger=%s deferred=false block=%d line_offset=%d inner_offset=%d viewport_offset=%d", strings.TrimSpace(trigger), match.BlockID, lineOffset, match.InnerOffset, m.viewport.offset)
		}
	}
	return true
}

func (m *Model) maybeScrollToDirectoryEntry(entry DirectoryEntry, trigger string) bool {
	if m.hasDeferredStartupTranscript() && entry.BlockID > 0 {
		if !m.locateDeferredStartupTranscriptBlock(entry.BlockID, trigger) {
			return false
		}
		if lineOffset, ok := m.viewport.LineOffsetForBlockID(entry.BlockID); ok {
			m.viewport.offset = lineOffset
			m.viewport.clampOffset()
			m.viewport.sticky = m.viewport.atBottom()
			return true
		}
		return false
	}
	m.viewport.offset = entry.LineOffset
	m.viewport.clampOffset()
	m.viewport.sticky = m.viewport.atBottom()
	return true
}
