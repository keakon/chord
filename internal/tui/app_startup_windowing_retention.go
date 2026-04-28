package tui

import (
	"log/slog"
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
	slog.Debug("tui startup transcript retention",
		"blocks", blocks,
		"window_start", state.windowStart,
		"window_end", state.windowEnd,
		"max_hot_bytes", m.viewport.maxHotBytes,
	)
}

func (m *Model) logStartupDeferredTranscriptExit(state *startupDeferredTranscriptState, reason, trigger string) {
	if state == nil {
		return
	}
	slog.Debug("tui startup transcript deferred exit",
		"reason", reason,
		"trigger", trigger,
		"blocks", len(state.allBlocks),
		"window_switches", state.windowSwitchCount,
		"preheat_ticks", state.preheatTickCount,
		"preheat_blocks", state.preheatBlocksProcessed,
		"lifetime_ms", time.Since(state.startedAt).Milliseconds(),
	)
}
