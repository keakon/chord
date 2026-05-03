package tui

import (
	"strings"
	"time"

	"github.com/keakon/golog/log"

	tea "charm.land/bubbletea/v2"

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

func (m Model) hasActiveAnimation() bool {
	if m.viewport != nil && m.viewport.HasUserLocalShellPending() {
		return true
	}
	for _, act := range m.activities {
		if activityNeedsVisualAnimation(act.Type) {
			return true
		}
	}
	return false
}

// startActiveAnimation routes activity-driven animation restarts through the
// shared guarded entry point so viewport animation and terminal-title spinner
// remain in sync.
func (m *Model) startActiveAnimation() tea.Cmd {
	if m.animRunning || !m.hasActiveAnimation() {
		return m.syncTerminalTitleState()
	}
	return m.startAnimTick()
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
	tbk := turnBusyKey(agentID)
	delete(m.workStartedAt, tbk)
	delete(m.turnBusyStartedAt, tbk)
	delete(m.streamLastDeltaAt, agentID)
}

func animTickCmd(delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = foregroundCadence.visualAnimDelay
		if delay <= 0 {
			delay = 200 * time.Millisecond
		}
	}
	return tea.Tick(delay, func(t time.Time) tea.Msg {
		return animTickMsg(t)
	})
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
		return m.syncTerminalTitleState()
	}
	m.animRunning = true
	titleCmd := m.syncTerminalTitleState()
	return tea.Batch(animTickCmd(cadence.visualAnimDelay), titleCmd)
}

// activityFrame returns a non-empty string when animation is active,
// used by the viewport as a flag to trigger tool-call block animation.
func (m Model) activityFrame() string {
	if m.animRunning {
		return activeToolSpinnerSegments[(time.Now().UnixMilli()/150)%int64(len(activeToolSpinnerSegments))]
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
	if b == nil {
		return
	}
	b.SettledAt = time.Now()
}

func clearBlocksSettledAt(blocks []*Block) {
	for _, b := range blocks {
		if b != nil {
			b.SettledAt = time.Time{}
		}
	}
}

func (m *Model) resetTimingStateForSessionRestore() {
	clear(m.activities)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	clear(m.activityStartTime)
	clear(m.activityLastChanged)
	clear(m.requestProgress)
	clear(m.workStartedAt)
	clear(m.turnBusyStartedAt)
	clear(m.streamLastDeltaAt)
	m.localShellStartedAt = time.Time{}
	m.stopActiveAnimationIfIdle()
	m.backgroundIdleSince = time.Time{}
	m.lastSweepAt = time.Time{}
	m.idleSweepScheduled = false
	m.idleSweepGeneration++
}

func (m *Model) markRequestProgressBaseline(agentID string) {
	if agentID == "" || agentID == "main" || strings.HasPrefix(agentID, "main-") {
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
	blocks := v.visibleBlocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		if t := blocks[i].StartedAt; !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
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
