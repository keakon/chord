package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// displayState represents whether the terminal window is considered
// "in the foreground" (focused) or "in the background" (blurred).
// This is a best-effort proxy: BlurMsg/FocusMsg are the only signals.
type displayState int

const (
	stateForeground displayState = iota
	stateBackground
)

// cadenceProfile holds the timing parameters for the TUI refresh loop.
// Different profiles are selected based on foreground/background/busy state.
type cadenceProfile struct {
	// contentFlushDelay controls how quickly streaming deltas are flushed to
	// the screen. Lower values = smoother streaming but more CPU.
	contentFlushDelay time.Duration

	// visualAnimDelay controls the visual animation tick (separator glow,
	// spinner frames). 0 means visual animation is disabled.
	visualAnimDelay time.Duration

	// titleTickerDelay controls the terminal title spinner cadence.
	// 0 means the title spinner is disabled.
	titleTickerDelay time.Duration

	// housekeepingDelay controls the low-frequency background anim tick used for
	// stale detection, chord timeout, and other maintenance.
	housekeepingDelay time.Duration

	// hostRedrawAllowed controls whether hostRedrawForStreamingCmd may
	// actually issue redraw requests. When false, streaming still advances
	// the internal state but no clear-screen cycle is triggered.
	hostRedrawAllowed bool

	// aggressiveHotBudget, when true, signals that idle sweep should
	// shrink the viewport hot budget.
	aggressiveHotBudget bool
}

// Cadence constants – tuned for text-first streaming and lower background cost.
const (
	titleSpinnerCadence = 500 * time.Millisecond // terminal title spinner tick (foreground + background)
)

var (
	foregroundCadence = cadenceProfile{
		contentFlushDelay:   200 * time.Millisecond,
		visualAnimDelay:     200 * time.Millisecond,
		titleTickerDelay:    titleSpinnerCadence,
		housekeepingDelay:   backgroundHousekeepingDelay,
		hostRedrawAllowed:   true,
		aggressiveHotBudget: false,
	}

	// Background-active: user switched focus away but agent is still busy.
	// Keep state moving, but substantially reduce terminal output.
	backgroundActiveCadence = cadenceProfile{
		contentFlushDelay:   1 * time.Second,
		visualAnimDelay:     1 * time.Second,
		titleTickerDelay:    titleSpinnerCadence,
		housekeepingDelay:   backgroundHousekeepingDelay,
		hostRedrawAllowed:   false,
		aggressiveHotBudget: false,
	}

	// Background-idle: user switched focus away and agent is idle.
	// Skip unnecessary visual work entirely and keep only housekeeping alive.
	backgroundIdleCadence = cadenceProfile{
		contentFlushDelay:   0,
		visualAnimDelay:     0,
		titleTickerDelay:    0,
		housekeepingDelay:   backgroundHousekeepingDelay,
		hostRedrawAllowed:   false,
		aggressiveHotBudget: true,
	}
)

// currentCadence returns the appropriate cadenceProfile based on the model's
// current display state and busy status.
func (m *Model) currentCadence() cadenceProfile {
	if m.displayState == stateForeground {
		return foregroundCadence
	}
	if m.focusedAgentBusyForIdleSweep() {
		return backgroundActiveCadence
	}
	return backgroundIdleCadence
}

func (m *Model) handleBlurMsg() tea.Cmd {
	now := time.Now()
	if m.displayState == stateForeground {
		m.displayState = stateBackground
		m.lastBackgroundAt = now
	}
	m.lastForegroundAt = time.Time{}
	m.exitRenderFreeze()
	m.streamRenderForceView = false
	m.streamRenderDeferred = false
	m.streamRenderDeferNext = false
	titleCmd := m.syncTerminalTitleState()
	idleCmd := m.updateBackgroundIdleSweepState()
	if titleCmd != nil || idleCmd != nil {
		return tea.Batch(titleCmd, idleCmd)
	}
	return nil
}

func (m *Model) markBackgroundDirty(reason string) {
	if m == nil || m.displayState != stateBackground {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unspecified"
	}
	m.backgroundDirty = true
	m.backgroundDirtyReason = reason
	m.backgroundDirtyAt = time.Now()
	m.backgroundDirtyCount++
	m.recordTUIDiagnostic("background-dirty", "reason=%s count=%d layout_main=%dx%d input_h=%d viewport=%dx%d", reason, m.backgroundDirtyCount, m.layout.main.Dx(), m.layout.main.Dy(), m.inputAreaHeight(), debugViewportWidth(m.viewport), debugViewportHeight(m.viewport))
}

func (m *Model) consumeBackgroundDirtyFocusRedraw(stage string, now time.Time) tea.Cmd {
	return m.consumeBackgroundDirtyFocusRedrawWithOptions(stage, now, true)
}

func (m *Model) consumeBackgroundDirtyFocusRedrawWithOptions(stage string, now time.Time, issueHostRedraw bool) tea.Cmd {
	if m == nil || !m.backgroundDirty {
		return nil
	}
	dirtyReason := m.backgroundDirtyReason
	dirtyCount := m.backgroundDirtyCount
	dirtyAt := m.backgroundDirtyAt
	sinceDirty := time.Duration(0)
	if !dirtyAt.IsZero() {
		sinceDirty = now.Sub(dirtyAt)
	}
	m.recordTUIDiagnostic("background-dirty-focus-redraw", "stage=%s reason=%s count=%d since_dirty=%s freeze=%t issue_host_redraw=%t", stage, dirtyReason, dirtyCount, sinceDirty.Truncate(time.Millisecond), m.focusResizeFrozen, issueHostRedraw)
	m.backgroundDirty = false
	m.backgroundDirtyReason = ""
	m.backgroundDirtyAt = time.Time{}
	m.backgroundDirtyCount = 0
	if !issueHostRedraw {
		return nil
	}
	return m.hostRedrawCmd("background-dirty-focus")
}

// handleFocusMsg records a terminal focus event and transitions the model back
// to foreground state. It schedules a redraw to restore the UI promptly.
func (m *Model) handleFocusMsg() tea.Cmd {
	now := time.Now()
	m.displayState = stateForeground
	m.lastForegroundAt = now
	m.backgroundIdleSince = time.Time{}
	m.idleSweepScheduled = false
	m.exitRenderFreeze()
	m.streamRenderForceView = true
	m.streamRenderDeferred = false
	m.streamRenderDeferNext = false

	var cmds []tea.Cmd

	// Restart visual animation if there's active animation.
	if cmd := m.startActiveAnimation(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.syncTerminalTitleState(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Invalidate any previously scheduled idle sweep tick generation.
	m.idleSweepGeneration++

	if m.viewport != nil {
		m.viewport.RestoreHotBudget()
	}

	// During focus recovery with freeze enabled, defer the strong host redraw to
	// focus-settle so cmux/libghostty tab-restore jitter doesn't stack an extra
	// ClearScreen+RequestWindowSize cycle on top of the later settle redraw.
	if !m.useFocusResizeFreeze {
		cmds = append(cmds, m.hostRedrawForStreamingCmd("focus-restore"))
	}
	if !m.useFocusResizeFreeze && !(m.mode == ModeImageViewer && m.imageViewer.Open) {
		cmds = append(cmds, m.imageProtocolCmdWithReason("focus-restore"))
	}
	if m.backgroundDirty && !m.focusResizeFrozen {
		cmds = append(cmds, m.consumeBackgroundDirtyFocusRedraw("focus", now))
	} else if m.backgroundDirty {
		m.recordTUIDiagnostic("background-dirty-focus-defer", "reason=%s count=%d frozen=%t", m.backgroundDirtyReason, m.backgroundDirtyCount, m.focusResizeFrozen)
	}

	return tea.Batch(cmds...)
}
