package tui

import (
	"github.com/keakon/golog/log"
	"time"
)

const startupDeferredTranscriptAggressiveHotBytes int64 = 1 << 20 // 1 MiB

func (m *Model) configureStartupDeferredTranscriptRetention(state *startupDeferredTranscriptState) {
	if state == nil || m.viewport == nil {
		return
	}
	if state.originalViewportBudget == 0 {
		state.originalViewportBudget = m.viewport.maxHotBytes
	}
	if state.originalViewportBudget <= 0 {
		state.originalViewportBudget = m.viewport.baseHotBytes
	}
	if state.originalViewportBudget <= 0 {
		state.originalViewportBudget = defaultViewportHotBytes
	}
	if m.viewport.baseHotBytes <= 0 {
		m.viewport.baseHotBytes = state.originalViewportBudget
	}
	if m.viewport.maxHotBytes > startupDeferredTranscriptAggressiveHotBytes {
		m.viewport.maxHotBytes = startupDeferredTranscriptAggressiveHotBytes
		m.viewport.markHotBudgetNeedsEnforcement()
	}
}

func (m *Model) restoreStartupDeferredTranscriptRetention(state *startupDeferredTranscriptState) {
	if state == nil || m.viewport == nil {
		return
	}
	budget := state.originalViewportBudget
	if budget <= 0 {
		budget = m.viewport.baseHotBytes
	}
	if budget <= 0 {
		budget = defaultViewportHotBytes
	}
	if m.viewport.maxHotBytes != budget {
		m.viewport.maxHotBytes = budget
		m.viewport.markHotBudgetNeedsEnforcement()
	}
}

func (m *Model) maybeEnforceStartupDeferredTranscriptRetention() {
	if m.viewport == nil {
		return
	}
	if state := m.startupDeferredTranscript; state != nil {
		m.configureStartupDeferredTranscriptRetention(state)
		m.viewport.enforceHotBudget()
		return
	}
	m.viewport.enforceHotBudget()
}

func (m *Model) logStartupDeferredTranscriptRetention(state *startupDeferredTranscriptState, blocks int) {
	if state == nil || m.viewport == nil {
		return
	}
	log.Debugf("tui startup transcript retention blocks=%v window_start=%v window_end=%v max_hot_bytes=%v", blocks, state.windowStart, state.windowEnd, m.viewport.maxHotBytes)
}

func (m *Model) logStartupDeferredTranscriptExit(state *startupDeferredTranscriptState, reason, trigger string) {
	if state == nil {
		return
	}
	log.Debugf("tui startup transcript deferred exit reason=%v trigger=%v blocks=%v window_switches=%v preheat_ticks=%v preheat_blocks=%v lifetime_ms=%v", reason, trigger, len(state.allBlocks), state.windowSwitchCount, state.preheatTickCount, state.preheatBlocksProcessed, time.Since(state.startedAt).Milliseconds())
}
