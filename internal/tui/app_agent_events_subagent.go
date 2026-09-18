package tui

import (
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

const (
	subAgentStatusDone  = "done"
	subAgentStatusError = "error"
)

// subAgentMailboxCardTitle maps a mailbox kind to the badge the card carries.
// A risk alert is "AGENT BLOCKED" because that is what the user is meant to
// read: docs/tools.md has terminal worker failures shown as AGENT BLOCKED, and
// the stall watchdog raises them as kind "risk_alert". The restore path used
// to badge the same event "AGENT RISK", so a resumed session disagreed with
// the run it was replaying. A risk_alert whose subtype closes a stall episode
// (stall_resolved) is badged separately: the worker recovered, so the card
// must not keep reading like an active block.
func subAgentMailboxCardTitle(kind, subtype string) string {
	switch agent.SubAgentMailboxKind(kind) {
	case agent.SubAgentMailboxKindCompleted:
		return "AGENT COMPLETE"
	case agent.SubAgentMailboxKindRiskAlert, agent.SubAgentMailboxKindBlocked:
		if subtype == agent.SubAgentStallResolvedSubtype {
			return "AGENT BLOCKED RESOLVED"
		}
		return "AGENT BLOCKED"
	}
	return "AGENT MESSAGE"
}

// newSubAgentMailboxBlock builds the status card for one sub-agent mailbox
// entry. The live event path and the session-restore path both go through it:
// they used to build the card by hand and drifted, so the same message was
// badged "AGENT BLOCKED" while running but "AGENT RISK" after a restart, and
// only the live card carried a "[agent] kind:" prefix. One constructor means
// both render the same badge and the same From/Kind rows.
func newSubAgentMailboxBlock(id int, kind, subtype, agentID, taskID, content, targetAgentID string) *Block {
	return &Block{
		ID:            id,
		Type:          BlockStatus,
		StatusTitle:   subAgentMailboxCardTitle(kind, subtype),
		StatusFrom:    strings.TrimSpace(agentID),
		StatusKind:    strings.TrimSpace(kind),
		Content:       content,
		AgentID:       targetAgentID,
		LinkedAgentID: strings.TrimSpace(agentID),
		LinkedTaskID:  strings.TrimSpace(taskID),
	}
}

func (m *Model) handleSubAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.MailboxQueuedEvent:
		m.upsertQueuedMailbox(evt.Message)
		return true, effects
	case agent.MailboxDeliveryDroppedEvent:
		// The session can no longer deliver this message, so its waiting row
		// must go: the agent-side exit already emitted a warn toast with the
		// reason, and the durable row is left for a later restore to replay.
		m.removeQueuedMailbox(evt.MessageID)
		return true, effects
	case agent.MailboxTranscriptAppendedEvent:
		if evt.Message.Mailbox == nil || strings.TrimSpace(evt.Message.Mailbox.MessageID) == "" {
			return true, effects
		}
		m.removeQueuedMailbox(evt.Message.Mailbox.MessageID)
		targetAgentID := evt.TargetAgentID
		if targetAgentID == "main" {
			targetAgentID = ""
		}
		if block := m.findBlockByMailboxMessageID(evt.Message.Mailbox); block == nil {
			block := newSubAgentMailboxBlock(m.nextBlockID, evt.Message.Mailbox.Kind, evt.Message.Mailbox.Subtype, evt.Message.Mailbox.AgentID, evt.Message.Mailbox.TaskID, evt.Message.Content, targetAgentID)
			block.MailboxMessageID = strings.TrimSpace(evt.Message.Mailbox.MessageID)
			block.MsgIndex = evt.MessageIndex
			m.nextBlockID++
			m.appendViewportBlock(block)
			m.markBlockSettled(block)
			m.recalcViewportSize()
		}
		return true, effects
	case agent.AgentNotifyEvent:
		// AgentNotifyEvent is a control-plane wake/status signal. The durable
		// mailbox or target transcript emits the visible card after persistence,
		// so this event must never create a live-only block.
		return true, effects
	case agent.AgentStartedEvent:
		previousAgentID := strings.TrimSpace(evt.PreviousAgentID)
		if previousAgentID != "" && previousAgentID != evt.AgentID {
			if state, ok := m.subAgentStreamStates[previousAgentID]; ok {
				m.subAgentStreamStates[evt.AgentID] = state
				delete(m.subAgentStreamStates, previousAgentID)
				if state.assistant != nil {
					state.assistant.AgentID = evt.AgentID
				}
				if state.thinking != nil {
					state.thinking.AgentID = evt.AgentID
				}
			}
			if state, ok := m.agentComposerStates[normalizeDraftAgentID(previousAgentID)]; ok {
				m.agentComposerStates[normalizeDraftAgentID(evt.AgentID)] = state
				delete(m.agentComposerStates, normalizeDraftAgentID(previousAgentID))
			}
			if activity, ok := m.activities[previousAgentID]; ok {
				activity.AgentID = evt.AgentID
				m.activities[evt.AgentID] = activity
				delete(m.activities, previousAgentID)
			}
			if changed, ok := m.activityLastChanged[previousAgentID]; ok {
				m.activityLastChanged[evt.AgentID] = changed
				delete(m.activityLastChanged, previousAgentID)
			}
			for i := range m.queuedDrafts {
				if m.queuedDrafts[i].AgentID == previousAgentID {
					m.queuedDrafts[i].AgentID = evt.AgentID
				}
			}
			if m.inflightDraft != nil && m.inflightDraft.AgentID == previousAgentID {
				m.inflightDraft.AgentID = evt.AgentID
			}
			if m.focusedAgentID == previousAgentID {
				m.setFocusedAgent(evt.AgentID)
				delete(m.agentComposerStates, normalizeDraftAgentID(previousAgentID))
			}
		}
		return true, effects
	case agent.AgentDoneEvent:
		prevType := m.activities[evt.AgentID].Type
		m.finalizeAgentStream(evt.AgentID)
		if m.inflightDraftBelongsToAgent(evt.AgentID) {
			m.inflightDraft = nil
		}
		m.markAgentIdle(evt.AgentID)
		m.maybeShowBackgroundCompletionTitle(evt.AgentID, prevType, agent.ActivityIdle)
		m.sidebar.UpdateStatus(evt.AgentID, subAgentStatusDone)
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
		}
		// The durable completion mailbox row is the single card source: the
		// delivery path emits MailboxTranscriptAppendedEvent once the
		// completion is appended to the owner transcript, and restore rebuilds
		// the same card from that persisted message. Building a second card
		// here would show the completion twice live while a restored session
		// shows it once — exactly the live/restore drift AgentNotifyEvent
		// already avoids.
		m.stopActiveAnimationIfIdle()
		if prevType != "" && prevType != agent.ActivityIdle {
			effects.addFollowup(m.scheduleBackgroundHousekeeping())
		}
		return true, effects
	case agent.AgentStatusEvent:
		m.sidebar.UpdateStatus(evt.AgentID, evt.Status)
		if subAgentStatusSuspendsActivity(evt.Status) {
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
		effects.refreshSidebar = true
		m.recalcViewportSize()
		if evt.Status == subAgentStatusError && m.focusedAgentID == evt.AgentID {
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
			// Compacting is re-emitted periodically as a keep-alive while the
			// compaction request runs; a same-type repeat must not move the
			// "since" anchor, only a real activity transition may.
			if prev.Type != evt.Type {
				m.workStartedAt[tbk] = time.Now()
			}
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
			if normalizedAgentID == "" || normalizedAgentID == "main" {
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
			if evt.AgentID != "" && evt.AgentID != "main" {
				m.finalizeAgentStreamForIdleEvent(evt.AgentID)
			}
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
		if status, ok := m.sidebar.FindStatus(evt.AgentID); ok && subAgentStatusSuspendsActivity(status) {
			delete(m.requestProgress, turnBusyKey(evt.AgentID))
			return true, effects
		}
		agentID := evt.AgentID
		if agentID == "" || agentID == "main" {
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
		if agentID == "" || agentID == "main" {
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
		case agent.CompactionStatusStarted:
			// A synthetic started (the synchronous interval/cooldown skip of a
			// model-driven request) never occupies the
			// compaction slot: the pill keeps showing the compaction that is
			// actually running, or stays idle when none is. The skipped
			// terminal that follows is applied by the terminal branch below —
			// an idle slot always applies a terminal outcome.
			if evt.Synthetic {
				break
			}
			m.compactionBgStatus = compactionBackgroundStatus{
				Active:    true,
				StartedAt: now,
				Bytes:     evt.Bytes,
				Events:    evt.Events,
				Trigger:   evt.Trigger,
				PlanID:    evt.PlanID,
			}
		case agent.CompactionStatusProgress:
			if m.compactionBgStatus.StartedAt.IsZero() {
				m.compactionBgStatus.StartedAt = now
			}
			m.compactionBgStatus.Active = true
			m.compactionBgStatus.Bytes = evt.Bytes
			m.compactionBgStatus.Events = evt.Events
		case agent.CompactionStatusSucceeded, agent.CompactionStatusFailed, agent.CompactionStatusSkipped, agent.CompactionStatusCancelled:
			// The slot shows a running compaction: only that compaction's own
			// terminal may resolve it. A terminal from another plan id — the
			// skipped half of a synthetic interval/cooldown skip, or the
			// late-arriving terminal of a superseded plan — must not overwrite
			// the running state. Idle or terminal-flush slots apply any
			// terminal (a lone synthetic skip, or a terminal that arrived
			// without a plan id on older usage-driven paths).
			if m.compactionBgStatus.Active && m.compactionBgStatus.PlanID != "" && evt.PlanID != "" && evt.PlanID != m.compactionBgStatus.PlanID {
				return true, effects
			}
			// Terminal flush state: show the outcome for ~2s, then disappear.
			// Skipped is a terminal outcome too — nothing was rewritten, but
			// the model requested a checkpoint and the runtime declined, so the
			// reason is surfaced instead of a silent no-op. Cancelled is a
			// terminal outcome as well: a model checkpoint discarded by a turn
			// cancellation, stale-turn settlement, or higher-priority queued
			// work disappears silently otherwise, leaving the user unable to
			// tell the accepted request was voided. A usage-driven compaction
			// cancelled by the user shows the same short confirmation.
			if m.compactionBgStatus.StartedAt.IsZero() {
				m.compactionBgStatus.StartedAt = now
			}
			m.compactionBgStatus.Active = false
			m.compactionBgStatus.PlanID = ""
			m.compactionBgStatus.Terminal = evt.Status
			m.compactionBgStatus.TerminalAt = now
			m.compactionBgStatus.Bytes = evt.Bytes
			m.compactionBgStatus.Events = evt.Events
			m.compactionBgStatus.Trigger = evt.Trigger
			m.compactionBgStatus.Reason = evt.Reason
		}
		m.cachedStatusBarRightKey = ""
		m.cachedStatusBarRightSide = ""
		m.cachedStatusBarRightWidth = 0
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) findBlockByMailboxMessageID(meta *message.MailboxMetadata) *Block {
	if meta == nil {
		return nil
	}
	for _, block := range m.viewport.blocks {
		if block == nil || block.Type != BlockStatus {
			continue
		}
		if block.MailboxMessageID == meta.MessageID {
			return block
		}
	}
	return nil
}

func subAgentStatusSuspendsActivity(status string) bool {
	switch status {
	case subAgentStatusDone,
		subAgentStatusError,
		string(agent.SubAgentStateIdle),
		string(agent.SubAgentStateCompleted),
		string(agent.SubAgentStateFailed),
		string(agent.SubAgentStateCancelled),
		string(agent.SubAgentStateWaitingMain),
		string(agent.SubAgentStateWaitingDescendant):
		return true
	default:
		return false
	}
}
