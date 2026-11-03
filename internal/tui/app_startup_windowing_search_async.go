package tui

import (
	"maps"

	tea "github.com/keakon/bubbletea/v2"
)

type startupDeferredSearchResultMsg struct {
	generation uint64
	query      string
	prevOffset int
	matches    []MatchPosition
}

// snapshotStartupDeferredSearchBlocks clones the search inputs so the command
// can read them without touching live viewport or archive state. Hot blocks are
// shallow clones (strings and slices are immutable once published); cold blocks
// keep their spill reference and are loaded by the command on demand.
func (m *Model) snapshotStartupDeferredSearchBlocks() ([]startupDeferredBlockMeta, int) {
	state := m.startupDeferredTranscript
	if state == nil || m.viewport == nil {
		return nil, 0
	}
	meta := make([]startupDeferredBlockMeta, len(state.blockMeta))
	for i, src := range state.blockMeta {
		dst := src
		if src.block != nil {
			dst.block = cloneBlockForDeferredSearch(src.block)
		}
		if src.LineCounts != nil {
			dst.LineCounts = maps.Clone(src.LineCounts)
		}
		meta[i] = dst
	}
	return meta, m.viewport.width
}

func cloneBlockForDeferredSearch(src *Block) *Block {
	clone := cloneBlockForDeferredSource(src)
	clone.spillCold = src.spillCold
	clone.spillRef = src.spillRef
	clone.spillStore = src.spillStore
	clone.spillSummary = src.spillSummary
	if src.spillLineCounts != nil {
		clone.spillLineCounts = maps.Clone(src.spillLineCounts)
	}
	return clone
}

func (m *Model) startupDeferredHasColdBlocks() bool {
	state := m.startupDeferredTranscript
	if state == nil {
		return false
	}
	for _, meta := range state.blockMeta {
		if meta.block != nil && meta.block.spillCold {
			return true
		}
	}
	return false
}

func (m *Model) handleStartupDeferredSearchResult(msg startupDeferredSearchResultMsg) tea.Cmd {
	if m == nil || msg.generation != m.startupDeferredSearchGeneration || !m.search.State.Active || m.search.State.Query != msg.query {
		return nil
	}
	m.search.State.Matches = msg.matches
	m.search.State.Current = initialSearchCurrentIndex(msg.matches, m.searchAnchorIndexInDeferredMeta())
	if match, ok := m.search.State.CurrentMatch(); ok {
		m.maybeScrollToSearchMatch(match, "search_enter")
	}
	return m.refreshInlineImagesIfViewportMoved(msg.prevOffset, nil)
}
