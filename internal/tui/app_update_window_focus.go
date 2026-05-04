package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleBlurUpdate() tea.Cmd {
	m.recordTUIDiagnostic("blur", "focused=%t freeze=%t stable=%dx%d current=%dx%d", m.terminalAppFocused, m.useFocusResizeFreeze, m.stableWidth, m.stableHeight, m.width, m.height)
	m.terminalAppFocused = false
	idleCmd := m.handleBlurMsg()
	if !m.useFocusResizeFreeze {
		return idleCmd
	}
	m.focusResizeFrozen = true
	m.focusResizeGeneration++
	m.restoreStableTerminalSize()
	return idleCmd
}

func (m *Model) handleFocusUpdate() tea.Cmd {
	m.recordTUIDiagnostic("focus", "focused=%t freeze=%t mode=%s stable=%dx%d current=%dx%d", m.terminalAppFocused, m.useFocusResizeFreeze, debugModeString(m.mode), m.stableWidth, m.stableHeight, m.width, m.height)
	m.terminalAppFocused = true
	if m.mode == ModeImageViewer && m.imageCaps.Backend == ImageBackendKitty {
		m.imageViewer.NeedsRetransmit = true
		if m.imageViewer.ImageID > 0 {
			delete(m.kittyImageCache, m.imageViewer.ImageID)
			delete(m.kittyPlacementCache, m.imageViewer.ImageID)
		}
	}
	restoreCmd := m.handleFocusMsg()
	m.reapplyIMEForCurrentModeOnFocus()
	if !m.useFocusResizeFreeze {
		if m.mode == ModeImageViewer {
			m.focusResizeGeneration++
			gen := m.focusResizeGeneration
			m.refreshKittyTerminalMetrics()
			viewerReplayCmd := tea.Sequence(
				tea.RequestWindowSize,
				imageProtocolReplayCmd(gen, "focus-restore:image-viewer", 50*time.Millisecond),
			)
			if restoreCmd != nil {
				return tea.Batch(restoreCmd, viewerReplayCmd)
			}
			return viewerReplayCmd
		}
		return restoreCmd
	}
	m.focusResizeFrozen = true
	m.focusResizeGeneration++
	m.restoreStableTerminalSize()
	return tea.Batch(restoreCmd, focusResizeSettleCmd(m.focusResizeGeneration, 100*time.Millisecond))
}

func (m *Model) handleFocusResizeSettle(msg focusResizeSettleMsg) tea.Cmd {
	m.recordTUIDiagnostic("focus-settle", "generation=%d current_generation=%d mode=%s", msg.generation, m.focusResizeGeneration, debugModeString(m.mode))
	if !m.useFocusResizeFreeze {
		return nil
	}
	if msg.generation != m.focusResizeGeneration {
		return nil
	}
	m.focusResizeFrozen = false
	gen := m.focusResizeGeneration
	// focus-settle already performs the strong recovery redraw for this focus cycle
	// (ClearScreen + RequestWindowSize). If background activity dirtied the view
	// while blurred, consume that state here but fold it into the in-flight
	// focus-settle redraw instead of stacking a second concurrent host redraw.
	backgroundDirtyRedrawCmd := m.consumeBackgroundDirtyFocusRedrawWithOptions("focus-settle", time.Now(), false)
	postRedrawCmd := postFocusSettleRedrawCmd(gen)
	postFallbackCmd := postFocusSettleFallbackCmd(gen)
	m.recordTUIDiagnostic("post-focus-settle-fallback-arm", "generation=%d delay=%s mode=%s", gen, postFocusSettleFallbackDelay, debugModeString(m.mode))
	if m.mode == ModeImageViewer {
		return tea.Batch(
			tea.Sequence(
				tea.ClearScreen,
				tea.RequestWindowSize,
				imageProtocolReplayCmd(gen, "focus-settle:image-viewer", 50*time.Millisecond),
			),
			backgroundDirtyRedrawCmd,
			postRedrawCmd,
			postFallbackCmd,
		)
	}
	return tea.Batch(
		tea.Sequence(
			tea.ClearScreen,
			tea.RequestWindowSize,
			imageProtocolReplayCmd(gen, "focus-settle:inline-replay", 50*time.Millisecond),
		),
		backgroundDirtyRedrawCmd,
		postRedrawCmd,
		postFallbackCmd,
	)
}

func (m *Model) handleWindowSizeUpdate(msg tea.WindowSizeMsg) tea.Cmd {
	m.recordTUIDiagnostic("window-size", "incoming=%dx%d current=%dx%d frozen=%t pending=%dx%d", msg.Width, msg.Height, m.width, m.height, m.focusResizeFrozen, m.pendingResizeW, m.pendingResizeH)
	if m.focusResizeFrozen {
		m.pendingResizeW = msg.Width
		m.pendingResizeH = msg.Height
		return nil
	}
	prevW, prevH := m.width, m.height
	if msg.Width == prevW && msg.Height == prevH {
		m.pendingResizeW = msg.Width
		m.pendingResizeH = msg.Height
		if m.mode == ModeImageViewer && m.imageViewer.Open && m.imageCaps.Backend == ImageBackendKitty {
			m.refreshKittyTerminalMetrics()
		}
		return nil
	}
	// Terminals often emit a burst like final→smaller→final during tab switches.
	// Debouncing small shrink avoids the right panel jumping left, but debouncing growth
	// would leave a temporary blank strip on the right. Apply growth immediately
	// per-dimension (width/height independently). For shrink, apply large deltas
	// immediately so deliberate window drags do not feel laggy, and debounce only
	// smaller shrink values that are more likely to be transient tab-switch jitter.
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
			v := m.resizeVersion
			return tea.Batch(
				m.imageProtocolCmd(),
				tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg {
					return applyResizeMsg{version: v}
				}),
			)
		}
		return m.imageProtocolCmd()
	}
	m.resizeVersion++
	v := m.resizeVersion
	return tea.Tick(40*time.Millisecond, func(time.Time) tea.Msg {
		return applyResizeMsg{version: v}
	})
}

func (m *Model) handleApplyResize(msg applyResizeMsg) tea.Cmd {
	m.recordTUIDiagnostic("apply-resize", "version=%d current_version=%d pending=%dx%d current=%dx%d", msg.version, m.resizeVersion, m.pendingResizeW, m.pendingResizeH, m.width, m.height)
	if msg.version != m.resizeVersion {
		return nil // superseded by a newer resize event
	}
	if m.pendingResizeW == m.width && m.pendingResizeH == m.height {
		return nil
	}
	m.applyTerminalSize(m.pendingResizeW, m.pendingResizeH, false)
	return m.imageProtocolCmd()
}
