package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
)

func (m *Model) handleSubAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.AgentDoneEvent:
		prevType := m.activities[evt.AgentID].Type
		if m.inflightDraftBelongsToAgent(evt.AgentID) {
			m.inflightDraft = nil
		}
		m.markAgentIdle(evt.AgentID)
		m.maybeShowBackgroundCompletionTitle(evt.AgentID, prevType, agent.ActivityIdle)
		m.sidebar.UpdateStatus(evt.AgentID, "done")
		effects.refreshSidebar = true
		if m.focusedAgentID == evt.AgentID {
			m.setFocusedAgent("")
		}
		if taskBlock, ok := m.findBlockByLinkedTask(evt.TaskID); ok {
			m.recordTUIDiagnostic("agent-done", "task=%s agent=%s block=%d summary_len=%d", evt.TaskID, evt.AgentID, taskBlock.ID, len(evt.Summary))
			taskBlock.LinkedAgentID = evt.AgentID
			taskBlock.LinkedTaskID = evt.TaskID
			taskBlock.DoneSummary = evt.Summary
			taskBlock.InvalidateCache()
			m.updateViewportBlock(taskBlock)
			m.markBlockSettled(taskBlock)
		} else if taskBlock, ok := m.findBlockByLinkedAgent(evt.AgentID); ok {
			m.recordTUIDiagnostic("agent-done", "agent=%s block=%d summary_len=%d", evt.AgentID, taskBlock.ID, len(evt.Summary))
			taskBlock.DoneSummary = evt.Summary
			taskBlock.InvalidateCache()
			m.updateViewportBlock(taskBlock)
			m.markBlockSettled(taskBlock)
		} else {
			block := &Block{ID: m.nextBlockID, Type: BlockStatus, Content: fmt.Sprintf("[%s] completed: %s", evt.AgentID, evt.Summary), AgentID: evt.AgentID}
			m.nextBlockID++
			m.appendViewportBlock(block)
			m.markBlockSettled(block)
		}
		m.stopActiveAnimationIfIdle()
		if prevType != "" && prevType != agent.ActivityIdle {
			effects.addFollowup(m.scheduleBackgroundHousekeeping())
		}
		return true, effects
	case agent.AgentStatusEvent:
		m.sidebar.UpdateStatus(evt.AgentID, evt.Status)
		if evt.Status == "running" {
			m.sidebar.ResolvePendingTask()
		} else {
			switch evt.Status {
			case "idle", "done", "completed", "error", "cancelled", "waiting_main", "waiting_descendant":
				prevType := m.activities[evt.AgentID].Type
				if m.inflightDraftBelongsToAgent(evt.AgentID) {
					m.inflightDraft = nil
				}
				m.markAgentIdle(evt.AgentID)
				m.maybeShowBackgroundCompletionTitle(evt.AgentID, prevType, agent.ActivityIdle)
				m.stopActiveAnimationIfIdle()
				if prevType != "" && prevType != agent.ActivityIdle {
					effects.addFollowup(m.scheduleBackgroundHousekeeping())
				}
			}
		}
		effects.refreshSidebar = true
		m.recalcViewportSize()
		if evt.Status == "error" && m.focusedAgentID == evt.AgentID {
			m.setFocusedAgent("")
		}
		return true, effects
	case agent.AgentActivityEvent:
		prev := m.activities[evt.AgentID]
		tbk := turnBusyKey(evt.AgentID)
		switch evt.Type {
		case agent.ActivityIdle:
			delete(m.workStartedAt, tbk)
			delete(m.turnBusyStartedAt, tbk)
		case agent.ActivityConnecting, agent.ActivityCompacting:
			m.workStartedAt[tbk] = time.Now()
			if prev.Type == agent.ActivityIdle || prev.Type == "" {
				m.turnBusyStartedAt[tbk] = time.Now()
			}
		default:
			if prev.Type == agent.ActivityIdle || prev.Type == "" {
				m.turnBusyStartedAt[tbk] = time.Now()
			}
		}

		// Track compaction background status separately from foreground activities
		if evt.AgentID == "main" || evt.AgentID == "" {
			switch evt.Type {
			case agent.ActivityCompacting:
				if !m.compactionBgStatus.Active {
					m.compactionBgStatus = compactionBackgroundStatus{
						Active:    true,
						StartedAt: time.Now(),
					}
				}
			}
		}

		m.activities[evt.AgentID] = evt
		m.sidebar.UpdateActivity(evt.AgentID, strings.TrimSpace(stripANSI(m.renderActivitySummary(evt))))
		effects.refreshSidebar = true
		if evt.Type != prev.Type {
			now := time.Now()
			normalizedAgentID := evt.AgentID
			if normalizedAgentID == "" || normalizedAgentID == "main" || strings.HasPrefix(normalizedAgentID, "main-") {
				normalizedAgentID = "main"
			}
			if lastChanged, ok := m.activityLastChanged[evt.AgentID]; !ok || now.Sub(lastChanged) >= 100*time.Millisecond {
				m.activityStartTime[evt.AgentID] = now
			}
			m.activityLastChanged[evt.AgentID] = now
			if evt.Type != agent.ActivityIdle {
				if m.terminalTitleBackgroundCompletedAgentID == normalizedAgentID {
					m.terminalTitleBackgroundCompletedAgentID = ""
				}
			}
			if evt.Type == agent.ActivityStreaming || evt.Type == agent.ActivityWaitingToken || evt.Type == agent.ActivityConnecting || evt.Type == agent.ActivityWaitingHeaders || evt.Type == agent.ActivityCompacting {
				m.markRequestProgressBaseline(evt.AgentID)
			}
			// Only clear request progress when explicitly done or a new cycle starts.
			// Do NOT clear on ActivityExecuting — tool arg streaming may still be
			// in flight and the status bar should continue showing transport progress
			// until RequestProgressEvent{Done:true} arrives.
			if evt.Type == agent.ActivityIdle {
				delete(m.requestProgress, normalizedAgentID)
				m.maybeShowBackgroundCompletionTitle(normalizedAgentID, prev.Type, evt.Type)
			}
			if evt.Type == agent.ActivityStreaming || evt.Type == agent.ActivityWaitingToken || evt.Type == agent.ActivityConnecting || evt.Type == agent.ActivityWaitingHeaders {
				effects.addFollowup(m.scheduleStreamFlush(0))
			}
			if m.displayState == stateBackground && evt.AgentID == m.focusedAgentIDOrMain() {
				effects.addFollowup(m.updateBackgroundIdleSweepState())
			}
		}
		effects.addFollowup(m.startActiveAnimation())
		if evt.Type == agent.ActivityIdle {
			if m.inflightDraftBelongsToAgent(evt.AgentID) {
				m.inflightDraft = nil
			}
			m.stopActiveAnimationIfIdle()
			if prev.Type != "" && prev.Type != agent.ActivityIdle {
				effects.addFollowup(m.scheduleBackgroundHousekeeping())
			}
		}
		return true, effects
	case agent.RequestProgressEvent:
		agentID := evt.AgentID
		if agentID == "" || agentID == "main" || strings.HasPrefix(agentID, "main-") {
			agentID = "main"
		}
		if !evt.Done {
			m.touchStreamDelta(agentID)
		}
		state := m.requestProgress[agentID]
		state.RawBytes = evt.Bytes
		state.RawEvents = evt.Events
		state.LastUpdatedAt = time.Now()
		state.Done = evt.Done
		state.VisibleBytes = evt.Bytes
		state.VisibleEvents = evt.Events
		if evt.Done {
			delete(m.requestProgress, agentID)
		} else {
			m.requestProgress[agentID] = state
		}
		m.cachedStatusBarActivityKey = ""
		m.cachedStatusBarActivityText = ""
		m.cachedStatusBarActivityWidth = 0
		effects.addFollowup(m.startActiveAnimation())
		return true, effects
	case agent.RequestCycleStartedEvent:
		agentID := evt.AgentID
		if agentID == "" || agentID == "main" || strings.HasPrefix(agentID, "main-") {
			agentID = "main"
		}
		if agentID == "main" {
			m.thinkingStreamMsgIndex = m.currentMainAssistantMsgIndex()
			m.thinkingStreamBlockIndex = 0
		}
		delete(m.requestProgress, agentID)
		m.cachedStatusBarActivityKey = ""
		m.cachedStatusBarActivityText = ""
		m.cachedStatusBarActivityWidth = 0
		log.Debugf("tui reset request progress for new request cycle agent_id=%v turn_id=%v", agentID, evt.TurnID)
		return true, effects
	case agent.CompactionStatusEvent:
		now := time.Now()
		switch evt.Status {
		case "started":
			m.compactionBgStatus = compactionBackgroundStatus{
				Active:    true,
				StartedAt: now,
				Bytes:     evt.Bytes,
				Events:    evt.Events,
			}
		case "progress":
			if m.compactionBgStatus.StartedAt.IsZero() {
				m.compactionBgStatus.StartedAt = now
			}
			m.compactionBgStatus.Active = true
			m.compactionBgStatus.Bytes = evt.Bytes
			m.compactionBgStatus.Events = evt.Events
		case "succeeded", "failed":
			// Terminal flush state: show ✓/✗ for ~2s, then disappear
			if m.compactionBgStatus.StartedAt.IsZero() {
				m.compactionBgStatus.StartedAt = now
			}
			m.compactionBgStatus.Active = false
			m.compactionBgStatus.Terminal = evt.Status
			m.compactionBgStatus.TerminalAt = now
			m.compactionBgStatus.Bytes = evt.Bytes
			m.compactionBgStatus.Events = evt.Events
		case "cancelled":
			// Cancel disappears immediately, no terminal flush
			m.compactionBgStatus = compactionBackgroundStatus{}
		}
		m.cachedStatusBarRightKey = ""
		m.cachedStatusBarRightSide = ""
		m.cachedStatusBarRightWidth = 0
		return true, effects
	default:
		return false, effects
	}
}
