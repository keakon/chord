package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	hostRedrawMinInterval = 250 * time.Millisecond
	hostRedrawSettleDelay = 40 * time.Millisecond
	// defaultContentBoundaryHostRedrawMinInterval limits the non-periodic redraw used
	// when streaming/live appends move the sticky viewport. It intentionally
	// stays much slower than stream-flush so terminals do not see the old
	// "clear every frame" race, while still clearing stale bottom rows without
	// waiting for user scroll or Ctrl+G.
	defaultContentBoundaryHostRedrawMinInterval = 2 * time.Second

	// riskyHostContentBoundaryHostRedrawMinInterval lowers the content-boundary
	// throttle for Ghostty/cmux-style terminals that can leave stale cells in
	// steady-state streaming updates near separators and panel boundaries.
	riskyHostContentBoundaryHostRedrawMinInterval = 900 * time.Millisecond

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

	// postFocusSettleFallbackDelay is a later follow-up redraw armed from
	// focus-settle so Ghostty/cmux still get one more strong recovery pass
	// if the earlier post-focus-settle redraw lands inside the host's longer
	// invalidation window. Combined with the initial 500ms redraw, this keeps
	// the final fallback around ~2s after focus-settle without delaying the
	// common-case recovery path for every focus change.
	postFocusSettleFallbackDelay = 1500 * time.Millisecond

	// scrollFlushFallbackRedrawDelay is a follow-up redraw for scroll flushes that
	// need a later recovery pass. It is used both for scrolls shortly after a
	// focus restore and for throttled scroll bursts on Ghostty/cmux-style hosts,
	// where the initial redraw can still race with lingering surface recovery or
	// be skipped by the min-interval gate.
	scrollFlushFallbackRedrawDelay = 900 * time.Millisecond

	// contentBoundaryFallbackRedrawDelay schedules a late redraw when a
	// content-boundary redraw was suppressed by the slower content-boundary
	// throttle. Near focus-restore it repairs host recovery races; in steady-state
	// it is only used for Ghostty/cmux content-boundary-class bursts
	// (content-boundary/live-append) so ordinary updates stay single-pass and
	// scroll recovery keeps its own fallback.
	contentBoundaryFallbackRedrawDelay = 900 * time.Millisecond

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

func postFocusSettleFallbackCmd(generation int) tea.Cmd {
	return tea.Tick(postFocusSettleFallbackDelay, func(time.Time) tea.Msg {
		return postFocusSettleRedrawMsg{generation: generation, fallback: true}
	})
}

func postHostRedrawFallbackCmd(generation uint64, reason string, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return postHostRedrawFallbackMsg{generation: generation, reason: reason}
	})
}

type hostRedrawReasonPolicy struct {
	requestWindowSize         bool
	suppressPostFocusFallback bool
	suppressPeriodicViewer    bool
	postHostFallbackReason    string
	postHostFallbackDelay     time.Duration
}

func hostRedrawPolicyForReason(reason string) hostRedrawReasonPolicy {
	policy := hostRedrawReasonPolicy{
		// Unknown reasons are treated as strong enough to suppress the late
		// post-focus fallback. This preserves the previous conservative default:
		// only known weak/incremental redraws allow the late fallback to run.
		suppressPostFocusFallback: true,
	}

	switch strings.TrimSpace(reason) {
	case "focus-restore", "post-focus-settle-fallback":
		policy.requestWindowSize = true
	case "post-focus-settle-redraw":
		policy.requestWindowSize = true
		policy.suppressPostFocusFallback = false
	case "stream-flush":
		policy.suppressPostFocusFallback = false
		policy.suppressPeriodicViewer = true
	case "scroll-flush":
		policy.suppressPostFocusFallback = false
		policy.suppressPeriodicViewer = true
		policy.postHostFallbackReason = "scroll-flush-fallback"
		policy.postHostFallbackDelay = scrollFlushFallbackRedrawDelay
	case "content-boundary", "live-append", "toast-boundary":
		policy.suppressPostFocusFallback = false
		policy.postHostFallbackReason = "content-boundary-fallback"
		policy.postHostFallbackDelay = contentBoundaryFallbackRedrawDelay
	case "background-dirty-focus":
		policy.suppressPostFocusFallback = false
		policy.postHostFallbackReason = "background-dirty-focus-fallback"
		policy.postHostFallbackDelay = contentBoundaryFallbackRedrawDelay
	}

	return policy
}

func hostRedrawSuppressesPostFocusFallback(reason string) bool {
	return hostRedrawPolicyForReason(reason).suppressPostFocusFallback
}

func (m *Model) suppressPeriodicViewerHostRedraw(reason string) bool {
	if m == nil || m.mode != ModeImageViewer || !m.imageViewer.Open {
		return false
	}
	return hostRedrawPolicyForReason(reason).suppressPeriodicViewer
}

func hostRedrawRequestsWindowSize(reason string) bool {
	return hostRedrawPolicyForReason(reason).requestWindowSize
}

func (m *Model) hostRedrawSequence(reason string) []tea.Cmd {
	cmds := []tea.Cmd{tea.ClearScreen, hostRedrawSettleCmd(reason, hostRedrawSettleDelay)}
	if hostRedrawRequestsWindowSize(reason) {
		cmds = append(cmds[:1], append([]tea.Cmd{tea.RequestWindowSize}, cmds[1:]...)...)
	}
	return cmds
}

func (m *Model) contentBoundaryHostRedrawMinInterval() time.Duration {
	if m == nil {
		return defaultContentBoundaryHostRedrawMinInterval
	}
	if m.useFocusResizeFreeze {
		return riskyHostContentBoundaryHostRedrawMinInterval
	}
	return defaultContentBoundaryHostRedrawMinInterval
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
		now := time.Now()
		since := now.Sub(m.lastHostRedrawAt)
		if since < m.contentBoundaryHostRedrawMinInterval() {
			m.recordTUIDiagnostic("host-redraw-skip", "reason=%s content_boundary_throttled=true since_last=%s last_reason=%s", reason, since.Truncate(time.Millisecond), m.lastHostRedrawReason)
			if fallback := m.maybePostHostRedrawFallbackCmd(reason, m.hostRedrawGeneration, now); fallback != nil {
				return fallback
			}
			if fallback := m.maybePostThrottledContentBoundaryRedrawFallbackCmd(reason, m.hostRedrawGeneration); fallback != nil {
				return fallback
			}
			return nil
		}
	}
	return m.hostRedrawCmd(reason)
}

func (m *Model) postHostRedrawFallbackAlreadyPending(reason string, generation uint64) bool {
	if m == nil || generation == 0 {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return false
	}
	if m.pendingPostHostRedrawFallback == nil {
		m.pendingPostHostRedrawFallback = make(map[string]uint64)
	}
	if m.pendingPostHostRedrawFallback[reason] == generation {
		m.recordTUIDiagnostic("post-host-redraw-fallback-skip", "reason=%s generation=%d duplicate_pending=true", reason, generation)
		return true
	}
	m.pendingPostHostRedrawFallback[reason] = generation
	return false
}

func (m *Model) clearPostHostRedrawFallback(reason string, generation uint64) {
	if m == nil || len(m.pendingPostHostRedrawFallback) == 0 {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	if m.pendingPostHostRedrawFallback[reason] == generation {
		delete(m.pendingPostHostRedrawFallback, reason)
	}
}

func (m *Model) clearAllPostHostRedrawFallbacks() {
	if m == nil || len(m.pendingPostHostRedrawFallback) == 0 {
		return
	}
	clear(m.pendingPostHostRedrawFallback)
}

func (m *Model) maybePostHostRedrawFallbackCmd(reason string, generation uint64, now time.Time) tea.Cmd {
	if m == nil || m.lastForegroundAt.IsZero() {
		return nil
	}
	reason = strings.TrimSpace(reason)
	sinceFocus := now.Sub(m.lastForegroundAt)
	if sinceFocus < 0 || sinceFocus > scrollFlushFallbackAfterFocusWindow {
		return nil
	}
	policy := hostRedrawPolicyForReason(reason)
	if policy.postHostFallbackReason == "" || policy.postHostFallbackDelay <= 0 {
		return nil
	}
	if m.postHostRedrawFallbackAlreadyPending(reason, generation) {
		return nil
	}
	m.recordTUIDiagnostic("post-host-redraw-fallback-arm", "reason=%s generation=%d since_focus=%s", reason, generation, sinceFocus.Truncate(time.Millisecond))
	return postHostRedrawFallbackCmd(generation, reason, policy.postHostFallbackDelay)
}

func isContentBoundaryClassRedrawReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "content-boundary", "live-append", "toast-boundary":
		return true
	default:
		return false
	}
}

func (m *Model) maybePostThrottledContentBoundaryRedrawFallbackCmd(reason string, generation uint64) tea.Cmd {
	reason = strings.TrimSpace(reason)
	if m == nil || !m.useFocusResizeFreeze || !isContentBoundaryClassRedrawReason(reason) || !isContentBoundaryClassRedrawReason(m.lastHostRedrawReason) {
		return nil
	}
	policy := hostRedrawPolicyForReason(reason)
	if policy.postHostFallbackReason == "" || policy.postHostFallbackDelay <= 0 {
		return nil
	}
	if m.postHostRedrawFallbackAlreadyPending(reason, generation) {
		return nil
	}
	m.recordTUIDiagnostic("post-host-redraw-fallback-arm", "reason=%s generation=%d content_boundary_throttled=true risky_host=%t last_reason=%s", reason, generation, m.useFocusResizeFreeze, strings.TrimSpace(m.lastHostRedrawReason))
	return postHostRedrawFallbackCmd(generation, reason, policy.postHostFallbackDelay)
}

func (m *Model) maybePostThrottledScrollRedrawFallbackCmd(reason string, generation uint64) tea.Cmd {
	if m == nil || !m.useFocusResizeFreeze || strings.TrimSpace(reason) != "scroll-flush" {
		return nil
	}
	policy := hostRedrawPolicyForReason(reason)
	if policy.postHostFallbackReason == "" || policy.postHostFallbackDelay <= 0 {
		return nil
	}
	if m.postHostRedrawFallbackAlreadyPending(reason, generation) {
		return nil
	}
	m.recordTUIDiagnostic("post-host-redraw-fallback-arm", "reason=%s generation=%d throttled=true risky_host=%t", strings.TrimSpace(reason), generation, m.useFocusResizeFreeze)
	return postHostRedrawFallbackCmd(generation, reason, policy.postHostFallbackDelay)
}

// markHostRedrawReplay advances the durable no-op replay suffix generation.
// View() must not consume this marker: Bubble Tea batches View updates and only
// flushes on its renderer ticker, so a one-shot marker can disappear before the
// ClearScreen repair frame actually reaches the terminal.
func (m *Model) markHostRedrawReplay() {
	if m == nil {
		return
	}
	m.hostRedrawFrameNonce++
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
			if fallback := m.maybePostThrottledScrollRedrawFallbackCmd(reason, m.hostRedrawGeneration); fallback != nil {
				return fallback
			}
			return nil
		}
	}
	m.hostRedrawGeneration++
	m.clearAllPostHostRedrawFallbacks()
	m.markHostRedrawReplay()
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
