package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

func (m *Model) handleBlurUpdate() tea.Cmd {
	m.recordTUIDiagnostic("blur", "focused=%t current=%dx%d", m.terminalAppFocused, m.width, m.height)
	m.terminalAppFocused = false
	return m.handleBlurMsg()
}

func (m *Model) handleFocusUpdate() tea.Cmd {
	m.recordTUIDiagnostic("focus", "focused=%t mode=%s current=%dx%d", m.terminalAppFocused, debugModeString(m.mode), m.width, m.height)
	m.terminalAppFocused = true
	if m.mode == ModeImageViewer && m.imageCaps.Backend == ImageBackendKitty {
		m.imageViewer.NeedsRetransmit = true
		if m.imageViewer.ImageID > 0 {
			delete(m.kittyImageCache, m.imageViewer.ImageID)
			delete(m.kittyPlacementCache, m.imageViewer.ImageID)
		}
	}
	focusCmd := m.handleFocusMsg()
	m.reapplyIMEForCurrentModeOnFocus()
	return focusCmd
}

func (m *Model) handleWindowSizeUpdate(msg tea.WindowSizeMsg) tea.Cmd {
	m.recordTUIDiagnostic("window-size", "incoming=%dx%d current=%dx%d pending=%dx%d", msg.Width, msg.Height, m.width, m.height, m.pendingResizeW, m.pendingResizeH)
	prevW, prevH := m.width, m.height
	if msg.Width <= 0 || msg.Height <= 0 {
		return nil
	}
	if msg.Width == prevW && msg.Height == prevH {
		m.pendingResizeW = msg.Width
		m.pendingResizeH = msg.Height
		if m.mode == ModeImageViewer && m.imageViewer.Open && m.imageCaps.Backend == ImageBackendKitty {
			m.refreshKittyTerminalMetrics()
		}
		return nil
	}
	m.pendingResizeW = msg.Width
	m.pendingResizeH = msg.Height
	grewW := msg.Width > prevW
	grewH := msg.Height > prevH
	shrankW := msg.Width < prevW
	shrankH := msg.Height < prevH
	largeShrinkW := shrankW && (prevW-msg.Width) >= immediateWidthShrinkCols
	largeShrinkH := shrankH && (prevH-msg.Height) >= immediateHeightShrinkRows
	applyNowW := grewW || largeShrinkW
	applyNowH := grewH || largeShrinkH
	if applyNowW || applyNowH {
		m.resizeVersion++
		nextW, nextH := prevW, prevH
		if applyNowW {
			nextW = msg.Width
		}
		if applyNowH {
			nextH = msg.Height
		}
		m.applyTerminalSize(nextW, nextH, true)
		if (shrankW && !largeShrinkW) || (shrankH && !largeShrinkH) {
			version := m.resizeVersion
			return tea.Batch(
				m.imageProtocolCmd(),
				tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg {
					return applyResizeMsg{version: version}
				}),
			)
		}
		return m.imageProtocolCmd()
	}
	m.resizeVersion++
	version := m.resizeVersion
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg {
		return applyResizeMsg{version: version}
	})
}

func (m *Model) handleApplyResize(msg applyResizeMsg) tea.Cmd {
	m.recordTUIDiagnostic("apply-resize", "version=%d current_version=%d pending=%dx%d current=%dx%d", msg.version, m.resizeVersion, m.pendingResizeW, m.pendingResizeH, m.width, m.height)
	if msg.version != m.resizeVersion {
		return nil
	}
	if m.pendingResizeW == m.width && m.pendingResizeH == m.height {
		return nil
	}
	m.applyTerminalSize(m.pendingResizeW, m.pendingResizeH, false)
	return m.imageProtocolCmd()
}
