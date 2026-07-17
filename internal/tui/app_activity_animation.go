package tui

import (
	"time"

	"github.com/keakon/golog/log"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func activityNeedsVisualAnimation(activity agent.ActivityType) bool {
	switch activity {
	case "", agent.ActivityIdle, agent.ActivityCompacting:
		return false
	default:
		return true
	}
}

func (m Model) hasActiveAgentActivity() bool {
	for agentID := range m.activities {
		act := m.activityForAgent(agentID)
		if act.Type != "" && act.Type != agent.ActivityIdle {
			return true
		}
	}
	return false
}

func (m Model) hasActiveAnimation() bool {
	if m.viewport != nil && m.viewport.HasUserLocalShellPending() {
		return true
	}
	if summary := m.renderRequestProgressSummary(m.focusedAgentIDOrMain()); summary != "" {
		return true
	}
	for agentID := range m.activities {
		act := m.activityForAgent(agentID)
		if activityNeedsVisualAnimation(act.Type) {
			return true
		}
	}
	return false
}

// startActiveAnimation routes activity-driven animation restarts through the
// shared guarded entry point so viewport animation and terminal-title spinner
// remain in sync.
//
// It always synchronises the terminal-title ticker first — previously this was
// skipped whenever m.animRunning was still true from a previous activity, which
// allowed the terminal title spinner to get stuck when activity transitioned
// into and back out of a non-animated state (e.g. Streaming → Compacting →
// Streaming). Syncing unconditionally is cheap and keeps the title ticker's
// lifecycle strictly aligned with hasActiveAnimation().
func (m *Model) startActiveAnimation() tea.Cmd {
	titleCmd := m.syncTerminalTitleState()
	if !m.hasActiveAnimation() {
		return titleCmd
	}
	if m.animRunning {
		return titleCmd
	}
	tickCmd := m.startAnimTick()
	if tickCmd == nil {
		return titleCmd
	}
	return tea.Batch(titleCmd, tickCmd)
}

// stopActiveAnimationIfIdle immediately tears down both the visual animation
// loop and the terminal-title spinner when no animation source remains. Use
// this from reliable idle/reset paths instead of waiting for a later animTick,
// because the title ticker runs independently and can otherwise leak.
func (m *Model) stopActiveAnimationIfIdle() {
	if m == nil || m.hasActiveAnimation() {
		if m != nil {
			_ = m.syncTerminalTitleState()
		}
		return
	}
	m.animRunning = false
	m.invalidateAnimTicks()
	m.activitySpinnerFrameIndex = 0
	_ = m.syncTerminalTitleState()
}

func (m *Model) markAgentIdle(agentID string) {
	if m == nil {
		return
	}
	if agentID == "" {
		agentID = "main"
	}
	m.activities[agentID] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: agentID}
	delete(m.requestProgress, agentID)
	tbk := turnBusyKey(agentID)
	delete(m.workStartedAt, tbk)
	delete(m.turnBusyStartedAt, tbk)
	delete(m.streamLastDeltaAt, agentID)
}

func animTickCmd(generation uint64, source animTickSource, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = foregroundCadence.visualAnimDelay
		if delay <= 0 {
			delay = visualSpinnerCadence
		}
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return animTickMsg{generation: generation, source: source}
	})
}

func (m *Model) invalidateAnimTicks() {
	if m == nil {
		return
	}
	m.animTickGeneration++
}

func (m *Model) scheduleBackgroundHousekeeping() tea.Cmd {
	if m == nil || m.displayState != stateBackground {
		return nil
	}
	delay := m.currentCadence().housekeepingDelay
	if delay <= 0 {
		delay = backgroundHousekeepingDelay
	}
	return animTickCmd(m.animTickGeneration, animTickSourceHousekeeping, delay)
}

// startAnimTick starts the animation ticker only if it is not already running.
// Calling animTickCmd() from multiple code paths without this guard creates N
// independent chains, multiplying render frequency by N and pushing CPU to
// 100%. Always use startAnimTick instead of raw animTickCmd().
func (m *Model) startAnimTick() tea.Cmd {
	if m.animRunning {
		return m.syncTerminalTitleState()
	}
	cadence := m.currentCadence()
	if cadence.visualAnimDelay <= 0 {
		m.animRunning = false
		m.activitySpinnerFrameIndex = 0
		m.invalidateAnimTicks()
		return tea.Batch(m.syncTerminalTitleState(), m.scheduleBackgroundHousekeeping())
	}
	m.invalidateAnimTicks()
	m.animRunning = true
	m.activitySpinnerFrameIndex = 0
	titleCmd := m.syncTerminalTitleState()
	return tea.Batch(animTickCmd(m.animTickGeneration, animTickSourceVisual, cadence.visualAnimDelay), titleCmd)
}

// activityFrame returns a non-empty string when animation is active,
// used by the viewport as a flag to trigger tool-call block animation.
func (m *Model) activityFrame() string {
	if m.animRunning && len(activeToolSpinnerSegments) > 0 {
		return activeToolSpinnerSegments[m.activitySpinnerFrameIndex%len(activeToolSpinnerSegments)]
	}
	return ""
}

// turnBusyKey normalizes agent IDs for per-agent busy-span bookkeeping.
func turnBusyKey(agentID string) string {
	if agentID == "" || agentID == "main" {
		return "main"
	}
	return agentID
}

func clearBlocksStartedAt(blocks []*Block) {
	for _, b := range blocks {
		if b != nil {
			b.StartedAt = time.Time{}
		}
	}
}

func clearBlocksTiming(blocks []*Block) {
	clearBlocksStartedAt(blocks)
	clearBlocksSettledAt(blocks)
}

func (m *Model) markBlockSettled(b *Block) {
	if m == nil || b == nil {
		return
	}
	// SettledAt can affect rendering (notably elapsed footers). Ensure caches and
	// viewport line spans reflect the final, settled state.
	b.SettledAt = time.Now()
	b.InvalidateCache()
	m.updateViewportBlock(b)
}

func clearBlocksSettledAt(blocks []*Block) {
	for _, b := range blocks {
		if b != nil {
			b.SettledAt = time.Time{}
		}
	}
}

func (m *Model) resetTimingStateForSessionRestore(preserveRequestActivity bool) {
	var (
		mainActivity        agent.AgentActivityEvent
		mainActivityStart   time.Time
		mainActivityChanged time.Time
		mainProgress        requestProgressState
		mainWorkStarted     time.Time
		mainTurnStarted     time.Time
		mainStreamDelta     time.Time
	)
	if preserveRequestActivity {
		mainActivity = m.activityForAgent("main")
		preserveRequestActivity = mainActivity.Type == agent.ActivityConnecting ||
			mainActivity.Type == agent.ActivityWaitingHeaders ||
			mainActivity.Type == agent.ActivityWaitingToken ||
			mainActivity.Type == agent.ActivityStreaming
		if preserveRequestActivity {
			mainActivityStart = m.activityStartTime["main"]
			mainActivityChanged = m.activityLastChanged["main"]
			mainProgress = m.requestProgress["main"]
			mainWorkStarted = m.workStartedAt["main"]
			mainTurnStarted = m.turnBusyStartedAt["main"]
			mainStreamDelta = m.streamLastDeltaAt["main"]
		}
	}
	clear(m.activities)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	clear(m.activityStartTime)
	clear(m.activityLastChanged)
	clear(m.requestProgress)
	clear(m.workStartedAt)
	clear(m.turnBusyStartedAt)
	clear(m.streamLastDeltaAt)
	m.stopActiveAnimationIfIdle()
	m.backgroundIdleSince = time.Time{}
	m.lastSweepAt = time.Time{}
	m.idleSweepScheduled = false
	m.idleSweepGeneration++
	if preserveRequestActivity {
		m.activities["main"] = mainActivity
		m.activityStartTime["main"] = mainActivityStart
		m.activityLastChanged["main"] = mainActivityChanged
		m.requestProgress["main"] = mainProgress
		m.workStartedAt["main"] = mainWorkStarted
		m.turnBusyStartedAt["main"] = mainTurnStarted
		m.streamLastDeltaAt["main"] = mainStreamDelta
	}
}

func (m *Model) markRequestProgressBaseline(agentID string) {
	if agentID == "" || agentID == "main" {
		agentID = "main"
	}
	state, ok := m.requestProgress[agentID]
	if !ok {
		log.Debugf("request progress baseline skipped agent_id=%v reason=%v", agentID, "no_state")
		return
	}
	state.BaseBytes = state.VisibleBytes
	state.BaseEvents = state.VisibleEvents
	m.requestProgress[agentID] = state
}

func (m *Model) flushVisibleRequestProgress(now time.Time) {
	if m == nil {
		return
	}
	for agentID, state := range m.requestProgress {
		updated := false
		if state.RawBytes > state.VisibleBytes {
			state.VisibleBytes = state.RawBytes
			updated = true
		}
		if state.RawEvents > state.VisibleEvents {
			state.VisibleEvents = state.RawEvents
			updated = true
		}
		if state.Done && state.RawBytes <= state.VisibleBytes && state.RawEvents <= state.VisibleEvents {
			if state.VisibleBytes == 0 && state.VisibleEvents == 0 {
				delete(m.requestProgress, agentID)
				continue
			}
		}
		if updated {
			state.LastUpdatedAt = now
		}
		m.requestProgress[agentID] = state
	}
}

func lastVisibleBlockStartedWall(v *Viewport) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	return v.LastVisibleBlockStartedWall()
}

func (m Model) renderActivityPrimaryText(a agent.AgentActivityEvent) string {
	if a.Type == agent.ActivityExecuting {
		return m.renderExecutingSummary(a.AgentID)
	}
	if summary := m.renderRequestProgressSummary(a.AgentID); summary != "" {
		return summary
	}
	return ""
}

func (m Model) renderActivitySummary(a agent.AgentActivityEvent) string {
	return m.renderActivityPrimaryText(a)
}
