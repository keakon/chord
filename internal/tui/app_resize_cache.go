package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"
)

const resizeCacheSweepDelay = 30 * time.Second

type resizeCacheSweepTickMsg struct{ generation uint64 }

func (m *Model) scheduleResizeCacheSweep() tea.Cmd {
	if m.resizeCacheSweepScheduled {
		return nil
	}
	m.resizeCacheSweepScheduled = true
	m.resizeCacheSweepGeneration++
	generation := m.resizeCacheSweepGeneration
	return tickCmd(resizeCacheSweepDelay, func(time.Time) tea.Msg {
		return resizeCacheSweepTickMsg{generation: generation}
	})
}

func (m *Model) handleResizeCacheSweep(msg resizeCacheSweepTickMsg) {
	if !m.resizeCacheSweepScheduled || msg.generation != m.resizeCacheSweepGeneration {
		return
	}
	m.resizeCacheSweepScheduled = false
	// A resize belongs to the terminal, so a session/focus change during the
	// delay must clean the current viewport, not retain the previous one.
	if m.viewport != nil {
		for _, block := range m.viewport.blocks {
			if block.viewportCacheWidth != m.viewport.width && block.viewportCache != nil {
				block.viewportCache = nil
				block.viewportCacheWidth = 0
				block.hotBytesMemoValid = false
				m.viewport.markHotBudgetDirty()
			}
		}
		m.viewport.DropOffScreenCaches()
	}
	m.dropStartupDeferredTranscriptHiddenCaches()
	m.shrinkResizeScreenBuffer()
	if m.renderScratchW > m.width {
		m.renderScratch = uv.ScreenBuffer{}
		m.renderScratchW = 0
		m.renderScratchOK = false
	}
}

func (m *Model) shrinkResizeScreenBuffer() {
	if m.screenBuf.RenderBuffer == nil {
		return
	}
	lines := m.screenBuf.Lines
	oversized := cap(lines) > len(lines)
	for _, line := range lines {
		oversized = oversized || cap(line) > len(line)
	}
	if !oversized {
		return
	}
	// UV resizes by reslicing, leaving hidden rows/cells referenced. Copy the
	// logical canvas, including its cells, so a frozen frame remains intact.
	buffer := newScreenBuffer(m.screenBuf.Width(), m.screenBuf.Height())
	for y, line := range lines {
		copy(buffer.Lines[y], line)
	}
	m.screenBuf = buffer
}
