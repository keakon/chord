package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	hostRedrawMinInterval = 250 * time.Millisecond
	hostRedrawSettleDelay = 40 * time.Millisecond
	// contentBoundaryHostRedrawMinInterval limits the non-periodic redraw used
	// when streaming/live appends move the sticky viewport. It intentionally
	// stays much slower than stream-flush so Ghostty/cmux do not see the old
	// "clear every frame" race, while still clearing stale bottom rows without
	// waiting for user scroll or Ctrl+G.
	contentBoundaryHostRedrawMinInterval = 2 * time.Second

	// postFocusSettleRedrawDelay is the delay after focus-settle before
	// issuing a secondary ClearScreen. cmux / libghostty terminals often
	// bounce window-size events (e.g. 182→180→182) for ~100-200ms after
	// a tab switch, which can leave stale cells even though focus-settle
	// already issued a ClearScreen. This secondary redraw fires after the
	// typical bounce window and clears any remaining artifacts.
	//
	// Empirically 250ms was too early for cmux: the host's surface
	// invalidation can persist beyond that, so the second ClearScreen
	// output gets corrupted by lingering jitter. 500ms is a compromise
	// between letting the host settle and avoiding a perceptible flash
	// long after focus restore.
	postFocusSettleRedrawDelay = 500 * time.Millisecond

	// postFocusSettleFallbackRedrawDelay is a best-effort late redraw for
	// Ghostty/cmux-style hosts that can still show stale cells well after the
	// first settle window. We only suppress it after a strong recovery redraw;
	// weak redraws such as content-boundary can still race with host surface
	// recovery while streaming.
	postFocusSettleFallbackRedrawDelay = 1500 * time.Millisecond

	// scrollFlushFallbackRedrawDelay is a follow-up redraw for scroll flushes that
	// happen shortly after a focus restore. In Ghostty/cmux the initial scroll
	// redraw can still race with the host's lingering surface recovery; a later
	// redraw after scrolling settles helps clear stale cells without turning every
	// ordinary scroll into a double-clear sequence.
	scrollFlushFallbackRedrawDelay = 900 * time.Millisecond

	// scrollFlushFallbackAfterFocusWindow limits the late scroll redraw to the
	// host-recovery window right after regaining focus, which keeps normal
	// in-session scrolling behavior unchanged.
	scrollFlushFallbackAfterFocusWindow = 5 * time.Second
)

type hostRedrawSettleMsg struct {
	reason string
}

func hostRedrawSettleCmd(reason string, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return hostRedrawSettleMsg{reason: reason}
	})
}

type postFocusSettleRedrawMsg struct {
	generation int
	fallback   bool
}

func postFocusSettleRedrawCmd(generation int) tea.Cmd {
	return tea.Tick(postFocusSettleRedrawDelay, func(time.Time) tea.Msg {
		return postFocusSettleRedrawMsg{generation: generation}
	})
}

func postFocusSettleFallbackRedrawCmd(generation int) tea.Cmd {
	return tea.Tick(postFocusSettleFallbackRedrawDelay, func(time.Time) tea.Msg {
		return postFocusSettleRedrawMsg{generation: generation, fallback: true}
	})
}

func postHostRedrawFallbackCmd(generation uint64, reason string, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return postHostRedrawFallbackMsg{generation: generation, reason: reason}
	})
}

func hostRedrawSuppressesPostFocusFallback(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "content-boundary", "live-append", "scroll-flush", "stream-flush":
		return false
	default:
		return true
	}
}

func (m *Model) suppressPeriodicViewerHostRedraw(reason string) bool {
	if m == nil || m.mode != ModeImageViewer || !m.imageViewer.Open {
		return false
	}
	switch strings.TrimSpace(reason) {
	case "stream-flush", "scroll-flush":
		return true
	default:
		return false
	}
}

func (m *Model) hostRedrawSequence(reason string) []tea.Cmd {
	cmds := []tea.Cmd{tea.ClearScreen, hostRedrawSettleCmd(reason, hostRedrawSettleDelay)}
	if reason != "stream-flush" && reason != "scroll-flush" && reason != "scroll-flush-fallback" && reason != "debug-dump" && reason != "content-boundary" && reason != "live-append" {
		cmds = append(cmds[:1], append([]tea.Cmd{tea.RequestWindowSize}, cmds[1:]...)...)
	}
	return cmds
}

func (m *Model) hostRedrawForContentBoundaryCmd(reason string) tea.Cmd {
	if m == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "content-boundary"
	}
	if m.displayState != stateForeground {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=%s display_state=%d", reason, m.displayState)
		return nil
	}
	if !m.lastHostRedrawAt.IsZero() {
		since := time.Since(m.lastHostRedrawAt)
		if since < contentBoundaryHostRedrawMinInterval {
			m.recordTUIDiagnostic("host-redraw-skip", "reason=%s content_boundary_throttled=true since_last=%s last_reason=%s", reason, since.Truncate(time.Millisecond), m.lastHostRedrawReason)
			return nil
		}
	}
	return m.hostRedrawCmd(reason)
}

func (m *Model) maybePostHostRedrawFallbackCmd(reason string, generation uint64, now time.Time) tea.Cmd {
	if m == nil || reason != "scroll-flush" || m.lastForegroundAt.IsZero() {
		return nil
	}
	sinceFocus := now.Sub(m.lastForegroundAt)
	if sinceFocus < 0 || sinceFocus > scrollFlushFallbackAfterFocusWindow {
		return nil
	}
	m.recordTUIDiagnostic("post-host-redraw-fallback-arm", "reason=%s generation=%d since_focus=%s", reason, generation, sinceFocus.Truncate(time.Millisecond))
	return postHostRedrawFallbackCmd(generation, reason, scrollFlushFallbackRedrawDelay)
}

func (m *Model) hostRedrawCmd(reason string) tea.Cmd {
	return m.hostRedrawCmdWithOptions(reason, false)
}

func (m *Model) hostRedrawCmdWithOptions(reason string, bypassMinInterval bool) tea.Cmd {
	if m == nil || !m.useFocusResizeFreeze || m.focusResizeFrozen {
		if m != nil {
			m.recordTUIDiagnostic("host-redraw-skip", "reason=%s enabled=%t frozen=%t", strings.TrimSpace(reason), m.useFocusResizeFreeze, m.focusResizeFrozen)
		}
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unspecified"
	}
	if m.suppressPeriodicViewerHostRedraw(reason) {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=%s viewer_open=true periodic=true", reason)
		return nil
	}
	now := time.Now()
	if !bypassMinInterval && !m.lastHostRedrawAt.IsZero() {
		since := now.Sub(m.lastHostRedrawAt)
		if since < hostRedrawMinInterval {
			m.recordTUIDiagnostic("host-redraw-skip", "reason=%s throttled=true since_last=%s last_reason=%s", reason, since.Truncate(time.Millisecond), m.lastHostRedrawReason)
			return nil
		}
	}
	m.hostRedrawGeneration++
	m.lastHostRedrawAt = now
	m.lastHostRedrawReason = reason
	m.recordTUIDiagnostic("host-redraw", "reason=%s mode=%s offset=%d layout_main=%dx%d viewport=%dx%d", reason, debugModeString(m.mode), debugViewportOffset(m.viewport), m.layout.main.Dx(), m.layout.main.Dy(), debugViewportWidth(m.viewport), debugViewportHeight(m.viewport))
	cmds := m.hostRedrawSequence(reason)
	if fallback := m.maybePostHostRedrawFallbackCmd(reason, m.hostRedrawGeneration, now); fallback != nil {
		cmds = append(cmds, fallback)
	}
	return tea.Sequence(cmds...)
}

func (m *Model) hostRedrawForStreamingCmd(reason string) tea.Cmd {
	if m == nil {
		return nil
	}
	// In background mode, skip host redraw unless it's necessary.
	cadence := m.currentCadence()
	if !cadence.hostRedrawAllowed {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=%s host_redraw_disallowed=true display_state=%d", strings.TrimSpace(reason), m.displayState)
		return nil
	}
	if m.currentAssistantBlock == nil && m.currentThinkingBlock == nil && !m.hasActiveAnimation() {
		m.recordTUIDiagnostic("host-redraw-skip", "reason=%s streaming=false anim=false", strings.TrimSpace(reason))
		return nil
	}
	return m.hostRedrawCmd(reason)
}

func debugViewportOffset(v *Viewport) int {
	if v == nil {
		return -1
	}
	return v.offset
}

func debugViewportWidth(v *Viewport) int {
	if v == nil {
		return -1
	}
	return v.width
}

func debugViewportHeight(v *Viewport) int {
	if v == nil {
		return -1
	}
	return v.height
}

func (m *Model) hostRedrawSummary() string {
	if m == nil || m.lastHostRedrawAt.IsZero() {
		return "none"
	}
	reason := strings.TrimSpace(m.lastHostRedrawReason)
	if reason == "" {
		reason = "unspecified"
	}
	return fmt.Sprintf("%s (%s)", reason, m.lastHostRedrawAt.Format(time.RFC3339Nano))
}
