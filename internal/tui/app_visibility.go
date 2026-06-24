package tui

import (
	"os"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
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

	// scrollFlushDelay controls how quickly accumulated wheel deltas are flushed.
	// Lower values = smoother scrolling but more terminal frames.
	scrollFlushDelay time.Duration

	// visualAnimDelay controls the visual animation tick (separator glow,
	// spinner frames). 0 means visual animation is disabled.
	visualAnimDelay time.Duration

	// titleTickerDelay controls the terminal title spinner cadence.
	// 0 means the title spinner is disabled.
	titleTickerDelay time.Duration

	// housekeepingDelay controls the low-frequency background anim tick used for
	// stale detection, chord timeout, and other maintenance.
	housekeepingDelay time.Duration

	// aggressiveHotBudget, when true, signals that idle sweep should
	// shrink the viewport hot budget.
	aggressiveHotBudget bool
}

// Cadence constants – tuned for text-first streaming and lower background cost.
const (
	foregroundContentFlushCadence       = 200 * time.Millisecond
	foregroundBoundaryFlushCadence      = foregroundContentFlushCadence
	foregroundScrollFlushCadence        = 33 * time.Millisecond
	backgroundActiveContentFlushCadence = 1 * time.Second
	visualSpinnerCadence                = 200 * time.Millisecond // running tool/local-shell spinner tick (foreground only)
	backgroundActiveVisualAnimCadence   = 0                      // terminal is blurred; keep housekeeping but skip invisible visual frames
	titleSpinnerCadence                 = 500 * time.Millisecond // terminal title spinner tick (foreground)
	backgroundTitleSpinnerCadence       = time.Second            // blurred tab title still animates, but at half the wakeup rate
	backgroundIdleAnimTickCadence       = 5 * time.Second
	lowCadenceContentFlushDelay         = 500 * time.Millisecond
	lowCadenceScrollFlushDelay          = 100 * time.Millisecond
	lowCadenceTitleTickerDelay          = 2 * time.Second
)

var (
	foregroundCadence = cadenceProfile{
		contentFlushDelay:   foregroundContentFlushCadence,
		scrollFlushDelay:    foregroundScrollFlushCadence,
		visualAnimDelay:     visualSpinnerCadence,
		titleTickerDelay:    titleSpinnerCadence,
		housekeepingDelay:   backgroundHousekeepingDelay,
		aggressiveHotBudget: false,
	}

	// Background-active: user switched focus away but agent is still busy.
	// Keep state moving, but substantially reduce terminal output.
	backgroundActiveCadence = cadenceProfile{
		contentFlushDelay: backgroundActiveContentFlushCadence,
		scrollFlushDelay:  foregroundScrollFlushCadence,
		// Background-active work still needs stale detection/progress housekeeping,
		// but visual spinner frames are invisible while the terminal is blurred.
		visualAnimDelay:     backgroundActiveVisualAnimCadence,
		titleTickerDelay:    backgroundTitleSpinnerCadence,
		housekeepingDelay:   backgroundHousekeepingDelay,
		aggressiveHotBudget: false,
	}

	// Background-idle: user switched focus away and agent is idle.
	// Skip unnecessary visual work entirely and keep only housekeeping alive.
	backgroundIdleCadence = cadenceProfile{
		contentFlushDelay:   0,
		scrollFlushDelay:    foregroundScrollFlushCadence,
		visualAnimDelay:     0,
		titleTickerDelay:    0,
		housekeepingDelay:   backgroundHousekeepingDelay,
		aggressiveHotBudget: true,
	}
)

type cadenceProfiles struct {
	foreground       cadenceProfile
	backgroundActive cadenceProfile
	backgroundIdle   cadenceProfile
}

func defaultCadenceProfiles() cadenceProfiles {
	return cadenceProfiles{
		foreground:       foregroundCadence,
		backgroundActive: backgroundActiveCadence,
		backgroundIdle:   backgroundIdleCadence,
	}
}

func lowCadenceProfiles() cadenceProfiles {
	p := defaultCadenceProfiles()
	p.foreground.contentFlushDelay = lowCadenceContentFlushDelay
	p.foreground.scrollFlushDelay = lowCadenceScrollFlushDelay
	p.foreground.visualAnimDelay = 0
	p.foreground.titleTickerDelay = lowCadenceTitleTickerDelay
	return p
}

func detectCadenceProfilesFromEnv() cadenceProfiles {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return detectCadenceProfilesFromMap(env)
}

func detectCadenceProfilesFromMap(env map[string]string) cadenceProfiles {
	if strings.TrimSpace(env["CMUX_SOCKET_PATH"]) != "" || strings.TrimSpace(env["CMUX_SOCKET"]) != "" {
		return lowCadenceProfiles()
	}
	return defaultCadenceProfiles()
}

func (p cadenceProfiles) withDefaults() cadenceProfiles {
	d := defaultCadenceProfiles()
	if p.foreground == (cadenceProfile{}) {
		p.foreground = d.foreground
	}
	if p.backgroundActive == (cadenceProfile{}) {
		p.backgroundActive = d.backgroundActive
	}
	if p.backgroundIdle == (cadenceProfile{}) {
		p.backgroundIdle = d.backgroundIdle
	}
	return p
}

// currentCadence returns the appropriate cadenceProfile based on the model's
// current display state and busy status.
func (m *Model) currentCadence() cadenceProfile {
	profiles := m.cadenceProfiles.withDefaults()
	if m.displayState == stateForeground {
		return profiles.foreground
	}
	if m.focusedAgentBusyForIdleSweep() {
		return profiles.backgroundActive
	}
	return profiles.backgroundIdle
}

func (m *Model) scrollFlushDelay() time.Duration {
	if m == nil {
		return foregroundScrollFlushCadence
	}
	if delay := m.currentCadence().scrollFlushDelay; delay > 0 {
		return delay
	}
	return foregroundScrollFlushCadence
}

func (m *Model) ApplyCadenceProfileFromEnv() {
	if m == nil {
		return
	}
	m.cadenceProfiles = detectCadenceProfilesFromEnv()
}

func (m *Model) handleBlurMsg() tea.Cmd {
	now := time.Now()
	if m.displayState == stateForeground {
		m.displayState = stateBackground
		m.lastBackgroundAt = now
	}
	m.deferredResumeTailOnFocus = m.startupDeferredTranscriptPinnedToTail()
	m.lastForegroundAt = time.Time{}
	m.exitRenderFreeze()
	m.setStreamRenderInvalidation(streamRenderInvalidateClear)
	titleCmd := m.syncTerminalTitleState()
	idleCmd := m.updateBackgroundIdleSweepState()
	gitCmd := m.switchGitStatusToBackgroundRefresh()
	if titleCmd != nil || idleCmd != nil || gitCmd != nil {
		return tea.Batch(titleCmd, idleCmd, gitCmd)
	}
	return nil
}

// handleFocusMsg records a terminal focus event and transitions the model back
// to foreground state. It schedules a redraw to restore the UI promptly.
func (m *Model) handleFocusMsg() tea.Cmd {
	now := time.Now()
	m.displayState = stateForeground
	m.lastForegroundAt = now
	m.backgroundIdleSince = time.Time{}
	m.idleSweepScheduled = false
	if m.terminalTitleNeedsUserResponse() {
		m.terminalTitleRequestSeen = true
	}
	m.exitRenderFreeze()
	m.setStreamRenderInvalidation(streamRenderInvalidateForce)
	m.terminalTitleBackgroundCompletedAgentID = ""
	if m.deferredResumeTailOnFocus {
		m.maybeSwitchStartupDeferredTranscriptWindow(startupTranscriptWindowTail, "focus_restore_tail")
		m.deferredResumeTailOnFocus = false
	}

	var cmds []tea.Cmd

	// Restart visual animation if there's active animation.
	if cmd := m.startActiveAnimation(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.syncTerminalTitleState(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.switchGitStatusToForegroundRefresh(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Invalidate any previously scheduled idle sweep tick generation.
	m.idleSweepGeneration++

	if m.viewport != nil {
		m.viewport.RestoreHotBudget()
	}

	cmds = append(cmds, m.imageProtocolCmdWithReason("focus-restore"))

	return tea.Batch(cmds...)
}
