package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func agentEventMayChangeKeyPool(msg agentEventMsg) bool {
	if msg.closed {
		return false
	}
	switch evt := msg.event.(type) {
	case agent.IdleEvent, agent.ErrorEvent, agent.RateLimitUpdatedEvent, agent.KeyPoolChangedEvent, agent.RunningModelChangedEvent, agent.SessionRestoredEvent, agent.UsageUpdatedEvent:
		return true
	case agent.AgentActivityEvent:
		switch evt.Type {
		case agent.ActivityCooling, agent.ActivityRetryingKey:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

const agentEventBatchMax = 32

func waitForAgentEvent(ch <-chan agent.AgentEvent) tea.Cmd {
	return func() tea.Msg {
		// Block until the first event arrives.
		evt, ok := <-ch
		batch := []agentEventMsg{{event: evt, closed: !ok}}
		if !ok {
			return agentEventBatchMsg(batch)
		}
		// Drain any additional events that are already buffered.
		for len(batch) < agentEventBatchMax {
			select {
			case ev, ok := <-ch:
				batch = append(batch, agentEventMsg{event: ev, closed: !ok})
				if !ok {
					return agentEventBatchMsg(batch)
				}
			default:
				return agentEventBatchMsg(batch)
			}
		}
		return agentEventBatchMsg(batch)
	}
}

func (m *Model) ensureToolCallBlock(id, name, argsJSON, agentID string, state agent.ToolCallExecutionState, includeArgProgress bool) (*Block, bool) {
	if m == nil || m.viewport == nil || strings.TrimSpace(id) == "" {
		return nil, false
	}
	if block, ok := m.viewport.FindBlockByToolID(id); ok {
		return block, false
	}
	block := &Block{
		ID:                 m.nextBlockID,
		Type:               BlockToolCall,
		Content:            eventToolDisplayArgs(name, argsJSON, ""),
		ToolName:           name,
		ToolID:             id,
		Collapsed:          true,
		AgentID:            agentID,
		ToolExecutionState: state,
		StartedAt:          time.Now(),
	}
	if includeArgProgress {
		if progress := inferToolArgProgress(name, argsJSON); progress != nil {
			cp := *progress
			block.ToolProgress = &cp
		}
	}
	m.nextBlockID++
	m.appendViewportBlock(block)
	return block, true
}

type agentEventEffects struct {
	followup        tea.Cmd
	refreshSidebar  bool
	invalidateUsage bool
}

func (e *agentEventEffects) addFollowup(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	e.followup = tea.Batch(e.followup, cmd)
}

func (e *agentEventEffects) merge(other agentEventEffects) {
	e.addFollowup(other.followup)
	e.refreshSidebar = e.refreshSidebar || other.refreshSidebar
	e.invalidateUsage = e.invalidateUsage || other.invalidateUsage
}

func (m *Model) handleAgentEvent(msg agentEventMsg) tea.Cmd {
	if !msg.closed {
		m.markBackgroundDirty("agent-event")
	}
	if msg.closed {
		// Event channel closed (e.g. remote connection dropped). Reset streaming
		// state so the UI does not stay stuck on "streaming (Xs)".
		m.resetStreamingToIdle()
		if m.reconnectFunc != nil {
			// Attempt auto-reconnect in the background.
			fn := m.reconnectFunc
			return func() tea.Msg {
				newAgent, err := fn()
				if err != nil {
					return reconnectFailedMsg{}
				}
				return reconnectedMsg{agent: newAgent}
			}
		}
		// Only show disconnect toast if we had received at least one event (avoid startup flash).
		if m.agentHadEvent {
			return m.enqueueToast("Connection lost, please reconnect", "warn")
		}
		return nil
	}

	m.agentHadEvent = true
	effects := agentEventEffects{}

	if handled, sub := m.handleStreamingAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleTurnAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleSubAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleSessionAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleToolAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleMiscAgentEvent(msg.event); handled {
		effects.merge(sub)
	} else if handled, sub := m.handleHygieneAgentEvent(msg.event); handled {
		effects.merge(sub)
	}

	if effects.refreshSidebar {
		effects.invalidateUsage = true
		m.refreshSidebar()
	}
	if effects.invalidateUsage {
		m.invalidateStatusBarAgentSnapshot()
		m.invalidateUsageStatsCache()
	}

	return effects.followup
}

func (m *Model) handleStreamingAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.StreamTextEvent:
		m.touchStreamDelta(evt.AgentID)
		if m.currentAssistantBlock != nil && (m.currentAssistantBlock.AgentID != evt.AgentID || !m.currentAssistantBlock.Streaming) {
			m.finalizeAssistantBlock()
			m.currentAssistantBlock = nil
			m.assistantBlockAppended = false
		}
		if m.currentAssistantBlock == nil {
			m.markRequestProgressBaseline(evt.AgentID)
			m.currentAssistantBlock = &Block{ID: m.nextBlockID, Type: BlockAssistant, Streaming: true, AgentID: evt.AgentID, StartedAt: time.Now()}
			m.nextBlockID++
			m.assistantBlockAppended = false
		}
		m.currentAssistantBlock.Content += evt.Text
		if !m.assistantBlockAppended {
			m.appendViewportBlock(m.currentAssistantBlock)
			m.assistantBlockAppended = true
			if m.displayState == stateForeground {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			}
		}
		m.currentAssistantBlock.InvalidateCache()
		if m.assistantBlockAppended {
			m.viewport.InvalidateLastBlock()
		}
		m.exitRenderFreeze()
		m.markStreamRenderDirty()
		if m.displayState == stateForeground && strings.Contains(evt.Text, "\n") {
			effects.addFollowup(m.requestStreamBoundaryFlush())
		} else {
			effects.addFollowup(m.scheduleStreamFlush(0))
		}
		return true, effects
	case agent.ThinkingStartedEvent:
		if m.thinkingStartTime.IsZero() {
			m.thinkingStartTime = time.Now()
		}
		if m.currentThinkingBlock == nil {
			m.currentThinkingBlock = &Block{ID: m.nextBlockID, Type: BlockThinking, Streaming: true}
			m.nextBlockID++
			m.thinkingBlockAppended = false
		}
		return true, effects
	case agent.StreamThinkingDeltaEvent:
		m.touchStreamDelta(evt.AgentID)
		if m.thinkingStartTime.IsZero() {
			m.thinkingStartTime = time.Now()
		}
		if m.currentThinkingBlock != nil && m.currentThinkingBlock.AgentID != evt.AgentID {
			m.finalizeAssistantBlock()
		}
		if m.currentThinkingBlock == nil {
			m.currentThinkingBlock = &Block{ID: m.nextBlockID, Type: BlockThinking, Streaming: true, AgentID: evt.AgentID}
			m.nextBlockID++
			m.thinkingBlockAppended = false
		}
		m.currentThinkingBlock.Content += evt.Text
		if strings.TrimSpace(m.currentThinkingBlock.Content) != "" && !m.thinkingBlockAppended {
			m.appendViewportBlock(m.currentThinkingBlock)
			m.thinkingBlockAppended = true
			if m.displayState == stateForeground {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			}
		}
		m.currentThinkingBlock.InvalidateCache()
		if m.thinkingBlockAppended {
			m.viewport.InvalidateLastBlock()
		}
		m.exitRenderFreeze()
		m.markStreamRenderDirty()
		if m.displayState == stateForeground && strings.Contains(evt.Text, "\n") {
			effects.addFollowup(m.requestStreamBoundaryFlush())
		} else {
			effects.addFollowup(m.scheduleStreamFlush(0))
		}
		return true, effects
	case agent.StreamThinkingEvent:
		if strings.TrimSpace(evt.Text) != "" {
			if m.currentThinkingBlock == nil {
				m.currentThinkingBlock = &Block{ID: m.nextBlockID, Type: BlockThinking, Streaming: true, AgentID: evt.AgentID}
				m.nextBlockID++
				m.thinkingBlockAppended = false
			}
			m.currentThinkingBlock.Content += evt.Text
			if !m.thinkingBlockAppended {
				m.appendViewportBlock(m.currentThinkingBlock)
				m.thinkingBlockAppended = true
				if m.displayState == stateForeground {
					effects.addFollowup(m.requestStreamBoundaryFlush())
				}
			}
			m.currentThinkingBlock.InvalidateCache()
			if m.thinkingBlockAppended {
				m.viewport.UpdateLastBlock()
			}
			m.exitRenderFreeze()
			m.markStreamRenderDirty()
			if m.displayState == stateForeground && strings.Contains(evt.Text, "\n") {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			} else {
				effects.addFollowup(m.scheduleStreamFlush(0))
			}
		}
		if m.currentThinkingBlock != nil {
			m.currentThinkingBlock.Streaming = false
			if !m.thinkingStartTime.IsZero() {
				m.currentThinkingBlock.ThinkingDuration = time.Since(m.thinkingStartTime)
				m.thinkingStartTime = time.Time{}
			}
			m.currentThinkingBlock.InvalidateCache()
			if m.thinkingBlockAppended {
				m.markBlockSettled(m.currentThinkingBlock)
				m.viewport.InvalidateLastBlock()
			}
			m.streamRenderForceView = true
			m.streamRenderDeferred = false
			m.streamRenderDeferNext = false
			// Detach the settled block so the next round of thinking starts
			// a fresh card. Without this, subsequent thinking deltas would
			// be appended to an already-frozen block and the footer would
			// render alongside still-streaming content.
			m.currentThinkingBlock = nil
			m.thinkingBlockAppended = false
		}
		effects.addFollowup(m.requestStreamBoundaryFlush())
		return true, effects
	case agent.StreamRollbackEvent:
		matchAgent := func(blockAgent string) bool { return blockAgent == evt.AgentID }
		if m.currentThinkingBlock != nil && matchAgent(m.currentThinkingBlock.AgentID) {
			if m.thinkingBlockAppended {
				m.removeViewportBlockByID(m.currentThinkingBlock.ID)
			}
			m.currentThinkingBlock = nil
			m.thinkingBlockAppended = false
			m.thinkingStartTime = time.Time{}
		}
		if m.currentAssistantBlock != nil && matchAgent(m.currentAssistantBlock.AgentID) {
			if m.assistantBlockAppended {
				m.removeViewportBlockByID(m.currentAssistantBlock.ID)
			}
			m.currentAssistantBlock = nil
			m.assistantBlockAppended = false
		}
		if strings.TrimSpace(evt.Reason) != "" {
			effects.addFollowup(m.enqueueToast(evt.Reason, "warn"))
		}
		m.streamRenderForceView = true
		m.streamRenderDeferred = false
		m.streamRenderDeferNext = false
		effects.addFollowup(m.requestStreamBoundaryFlush())
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) handleTurnAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.IdleEvent:
		m.clearSessionSwitch()
		m.finalizeTurn()
		m.markAgentIdle("main")
		mainLoopBusy := m.agent != nil && m.agent.LoopKeepsMainBusy()
		if mainLoopBusy {
			m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main", Detail: "loop"}
		}
		skipDrain := m.pauseQueuedDraftDrainOnce
		m.pauseQueuedDraftDrainOnce = false
		pendingAutoContinue := !skipDrain && (m.queuedDraftsAutoContinue() || (!m.queueSyncEnabled && len(m.queuedDrafts) > 0))
		m.inflightDraft = nil
		m.stopActiveAnimationIfIdle()
		if !skipDrain && !m.queueSyncEnabled {
			effects.addFollowup(m.drainQueuedDrafts())
		}
		if !pendingAutoContinue && !mainLoopBusy {
			effects.addFollowup(m.maybeOSC9NotifyCmd(m.osc9IdleNotificationText()))
		}
		return true, effects
	case agent.PendingDraftConsumedEvent:
		draft := queuedDraftFromParts(evt.Parts)
		draft.ID = evt.DraftID
		if idx := m.findQueuedDraftIndex(evt.DraftID); idx >= 0 {
			draft = m.removeQueuedDraftAt(idx)
		}
		if m.editingQueuedDraftID == evt.DraftID {
			m.editingQueuedDraftID = ""
		}
		m.finalizeTurn()
		imageCount := 0
		content := userBlockTextFromParts(draft.contentParts(), draft.Content)
		_, imageCount = queuedDraftTextAndImageCount(draft)
		fileRefs := draft.FileRefs
		if fileRefs == nil {
			fileRefs = fileRefsFromParts(evt.Parts)
		}
		msgIndex := -1
		if evt.AgentID == "" && m.agent != nil {
			msgs := m.agent.GetMessages()
			for i := len(msgs) - 1; i >= 0; i-- {
				msg := msgs[i]
				if msg.Role != "user" || msg.IsCompactionSummary {
					continue
				}
				if message.UserPromptPlainText(msg) == content {
					msgIndex = i
					break
				}
			}
		}
		block := &Block{ID: m.nextBlockID, Type: BlockUser, Content: content, AgentID: evt.AgentID, LoopAnchor: draft.LoopAnchor, ImageCount: imageCount, ImageParts: imagePartsFromContentParts(draft.contentParts()), FileRefs: fileRefs, MsgIndex: msgIndex, StartedAt: draft.QueuedAt}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		d := draft
		if strings.TrimSpace(d.AgentID) == "" {
			d.AgentID = evt.AgentID
		}
		m.inflightDraft = &d
		m.markRequestProgressBaseline(evt.AgentID)
		m.syncVisibleMainUserBlockMsgIndexes()
		m.recalcViewportSize()
		effects.addFollowup(m.imageProtocolCmd())
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) handleSubAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.AgentDoneEvent:
		m.markAgentIdle(evt.AgentID)
		m.sidebar.UpdateStatus(evt.AgentID, "done")
		effects.refreshSidebar = true
		if taskBlock, ok := m.viewport.FindBlockByLinkedTask(evt.TaskID); ok {
			m.recordTUIDiagnostic("agent-done", "task=%s agent=%s block=%d summary_len=%d", evt.TaskID, evt.AgentID, taskBlock.ID, len(evt.Summary))
			taskBlock.LinkedAgentID = evt.AgentID
			taskBlock.LinkedTaskID = evt.TaskID
			taskBlock.DoneSummary = evt.Summary
			taskBlock.InvalidateCache()
			m.updateViewportBlock(taskBlock)
			m.markBlockSettled(taskBlock)
		} else if taskBlock, ok := m.viewport.FindBlockByLinkedAgent(evt.AgentID); ok {
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
		if m.focusedAgentID == evt.AgentID {
			m.setFocusedAgent("")
		}
		m.stopActiveAnimationIfIdle()
		return true, effects
	case agent.AgentStatusEvent:
		m.sidebar.UpdateStatus(evt.AgentID, evt.Status)
		if evt.Status == "running" {
			m.sidebar.ResolvePendingTask()
		} else {
			switch evt.Status {
			case "idle", "done", "completed", "error", "cancelled", "waiting_primary", "waiting_descendant":
				m.markAgentIdle(evt.AgentID)
				m.stopActiveAnimationIfIdle()
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
			if evt.Type == agent.ActivityStreaming || evt.Type == agent.ActivityWaitingToken || evt.Type == agent.ActivityConnecting || evt.Type == agent.ActivityWaitingHeaders || evt.Type == agent.ActivityCompacting {
				m.markRequestProgressBaseline(evt.AgentID)
			}
			// Only clear request progress when explicitly done or a new cycle starts.
			// Do NOT clear on ActivityExecuting — tool arg streaming may still be
			// in flight and the status bar should continue showing transport progress
			// until RequestProgressEvent{Done:true} arrives.
			if evt.Type == agent.ActivityIdle {
				delete(m.requestProgress, normalizedAgentID)
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
			m.stopActiveAnimationIfIdle()
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
		if evt.Bytes > 0 || evt.Events > 0 {
			cadence := m.currentCadence().visualAnimDelay
			if cadence <= 0 {
				cadence = foregroundCadence.visualAnimDelay
				if cadence <= 0 {
					cadence = 200 * time.Millisecond
				}
			}
			if !m.animRunning {
				effects.addFollowup(animTickCmd(cadence))
				m.animRunning = true
			}
		}
		return true, effects
	case agent.RequestCycleStartedEvent:
		agentID := evt.AgentID
		if agentID == "" || agentID == "main" || strings.HasPrefix(agentID, "main-") {
			agentID = "main"
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

func (m *Model) handleSessionAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.RunningModelChangedEvent:
		effects.refreshSidebar = true
		effects.invalidateUsage = true
		return true, effects
	case agent.ModelSelectEvent:
		m.inflightDraft = nil
		m.openModelSelect()
		return true, effects
	case agent.SessionSelectEvent:
		effects.addFollowup(m.openSessionSelect(evt.Sessions))
		return true, effects
	case agent.SessionSwitchStartedEvent:
		m.beginSessionSwitch(evt.Kind, evt.SessionID)
		return true, effects
	case agent.SessionRestoredEvent:
		reason := "session_restored"
		if m.startupRestorePending {
			reason = "startup_restored"
		}
		m.resetPendingScrollFlush()
		m.setFocusedAgent("")
		effects.refreshSidebar = true
		effects.invalidateUsage = true
		effects.addFollowup(func() tea.Msg { return sessionRestoredRebuildMsg{reason: reason} })
		return true, effects
	case agent.ConfirmRequestEvent:
		if evt.ArgsJSON != "" {
			if block, ok := m.viewport.FindLastPendingToolBlockByName(evt.ToolName); ok {
				m.recordTUIDiagnostic("confirm-request", "tool=%s block=%d args_len=%d", evt.ToolName, block.ID, len(evt.ArgsJSON))
				block.Content = evt.ArgsJSON
				block.InvalidateCache()
				m.updateViewportBlock(block)
			}
		}
		req := ConfirmRequest{ToolName: evt.ToolName, ArgsJSON: evt.ArgsJSON, RequestID: evt.RequestID, Timeout: evt.Timeout, NeedsApproval: append([]string(nil), evt.NeedsApproval...), AlreadyAllowed: append([]string(nil), evt.AlreadyAllowed...)}
		effects.addFollowup(func() tea.Msg { return confirmRequestMsg{request: req} })
		effects.addFollowup(m.maybeOSC9NotifyCmd("Chord: Permission confirmation required"))
		return true, effects
	case agent.QuestionRequestEvent:
		effects.addFollowup(injectQuestionRequestFromEvent(evt))
		effects.addFollowup(m.maybeOSC9NotifyCmd("Chord: Question requires your input"))
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) handleToolAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.ToolCallStartEvent:
		m.touchStreamDelta(evt.AgentID)
		m.finalizeAssistantBlock()
		m.markRequestProgressBaseline(evt.AgentID)
		_, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, agent.ToolCallExecutionStateRunning, true)
		if created {
			if block, ok := m.viewport.FindBlockByToolID(evt.ID); ok {
				block.StartedAt = time.Time{}
			}
			m.recordToolArgRender(evt.ID, evt.ArgsJSON, time.Now())
		}
		if created && evt.Name == "Delegate" && evt.AgentID == "" {
			m.sidebar.AddPendingTask()
			effects.refreshSidebar = true
			m.recalcViewportSize()
		}
		return true, effects
	case agent.ToolCallUpdateEvent:
		m.touchStreamDelta(evt.AgentID)
		now := time.Now()
		block, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, agent.ToolCallExecutionStateRunning, !evt.ArgsStreamingDone)
		if created {
			if evt.ArgsStreamingDone {
				delete(m.toolArgRenderState, evt.ID)
				if block != nil {
					block.StartedAt = time.Time{}
					if block.ToolProgress != nil {
						block.ToolProgress = nil
					}
					block.InvalidateCache()
					m.updateViewportBlock(block)
				}
			} else {
				m.recordToolArgRender(evt.ID, evt.ArgsJSON, now)
			}
			return true, effects
		}
		allowArgRenderUpdate := evt.ArgsStreamingDone || m.shouldRefreshToolArgRender(evt.ID, evt.ArgsJSON, now)
		if !allowArgRenderUpdate {
			return true, effects
		}
		updated := false
		argsStreamingDone := evt.ArgsStreamingDone || (block != nil && !block.StartedAt.IsZero())
		displayArgs := eventToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
		if displayArgs != "" && displayArgs != block.Content {
			m.recordTUIDiagnostic("tool-call-update", "tool=%s id=%s block=%d len=%d->%d", evt.Name, evt.ID, block.ID, len(block.Content), len(displayArgs))
			block.Content = displayArgs
			updated = true
		}
		if argsStreamingDone {
			delete(m.toolArgRenderState, evt.ID)
			// Args have finished streaming but the tool may not have been dispatched yet
			// (execution-state events arrive only after the model response finalizes).
			// Mark as queued so fully-formed cards (notably TodoWrite) stop animating
			// while we wait for execution to begin.
			if block.StartedAt.IsZero() && (block.ToolExecutionState == "" || block.ToolExecutionState == agent.ToolCallExecutionStateRunning) {
				block.ToolExecutionState = agent.ToolCallExecutionStateQueued
				updated = true
			}
			if block.ToolProgress != nil {
				block.ToolProgress = nil
				updated = true
			}
		} else {
			if progress := inferToolArgProgress(evt.Name, evt.ArgsJSON); progress != nil {
				if block.ToolProgress == nil || *block.ToolProgress != *progress {
					cp := *progress
					block.ToolProgress = &cp
					updated = true
				}
			}
		}
		if updated {
			if !argsStreamingDone {
				m.recordToolArgRender(evt.ID, evt.ArgsJSON, now)
			}
			block.InvalidateCache()
			m.updateViewportBlock(block)
		}
		return true, effects
	case agent.ToolCallExecutionEvent:
		delete(m.toolArgRenderState, evt.ID)
		block, created := m.ensureToolCallBlock(evt.ID, evt.Name, evt.ArgsJSON, evt.AgentID, evt.State, false)
		if evt.State == agent.ToolCallExecutionStateRunning && block != nil && block.StartedAt.IsZero() {
			block.StartedAt = time.Now()
			m.markRequestProgressBaseline(evt.AgentID)
		}
		if created {
			return true, effects
		}
		updated := false
		displayArgs := eventToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent)
		if displayArgs != "" && displayArgs != block.Content {
			block.Content = displayArgs
			updated = true
		}
		if block.ToolExecutionState != evt.State {
			block.ToolExecutionState = evt.State
			updated = true
		}
		if block.ToolProgress != nil {
			block.ToolProgress = nil
			updated = true
		}
		if evt.State == agent.ToolCallExecutionStateQueued {
			block.Collapsed = true
		}
		if updated {
			block.InvalidateCache()
			m.updateViewportBlock(block)
		}
		return true, effects
	case agent.ToolProgressEvent:
		if block, ok := m.viewport.FindBlockByToolID(evt.CallID); ok {
			if block.ResultDone || block.ToolExecutionState == agent.ToolCallExecutionStateQueued {
				return true, effects
			}
			progress := evt.Progress
			if progress.Label == "" && progress.Current == 0 && progress.Total == 0 && strings.TrimSpace(progress.Text) == "" {
				if block.ToolProgress != nil {
					block.ToolProgress = nil
					block.InvalidateCache()
					m.updateViewportBlock(block)
				}
				return true, effects
			}
			if block.ToolProgress == nil || *block.ToolProgress != progress {
				cp := progress
				block.ToolProgress = &cp
				block.InvalidateCache()
				m.updateViewportBlock(block)
			}
		}
		return true, effects
	case agent.ToolResultEvent:
		if evt.Name == "Delegate" && evt.AgentID == "" {
			m.sidebar.ResolvePendingTask()
			effects.refreshSidebar = true
			m.recalcViewportSize()
		}
		if block, ok := m.viewport.FindBlockByToolID(evt.CallID); ok {
			delete(m.toolArgRenderState, evt.CallID)
			m.recordTUIDiagnostic("tool-result", "tool=%s call=%s block=%d status=%s result_len=%d had_diff=%t", evt.Name, evt.CallID, block.ID, evt.Status, len(evt.Result), evt.Diff != "")
			block.ResultContent = evt.Result
			block.Audit = evt.Audit.Clone()
			if displayArgs := eventToolDisplayArgs(evt.Name, evt.ArgsJSON, block.ResultContent); displayArgs != "" {
				block.Content = displayArgs
			}
			block.ResultStatus = evt.Status
			block.ResultDone = true
			block.ToolExecutionState = ""
			block.ToolProgress = nil
			if evt.Diff != "" {
				block.Diff = evt.Diff
			}
			if shouldExpandToolResult(evt.Name) {
				block.Collapsed = false
			}
			if shouldTrackSidebarFileEdit(evt.Name) && evt.Status != agent.ToolResultStatusError {
				if evt.Name == tools.NameDelete {
					groups := tools.ParseDeleteResult(evt.Result)
					for _, path := range groups.Deleted {
						m.sidebar.AddFileEdit(evt.AgentID, path, 0, 1)
						effects.refreshSidebar = true
						effects.invalidateUsage = true
					}
				} else {
					var args struct {
						Path string `json:"path"`
					}
					if err := json.Unmarshal([]byte(evt.ArgsJSON), &args); err == nil && args.Path != "" {
						m.sidebar.AddFileEdit(evt.AgentID, args.Path, evt.DiffAdded, evt.DiffRemoved)
						effects.refreshSidebar = true
						effects.invalidateUsage = true
					}
				}
			}
			if evt.Name == "Delegate" && evt.Status != agent.ToolResultStatusError && evt.Result != "" {
				if handle, ok := parseTaskToolHandle(evt.Result); ok {
					if handle.AgentID != "" {
						block.LinkedAgentID = handle.AgentID
					}
					if handle.TaskID != "" {
						block.LinkedTaskID = handle.TaskID
					}
				} else if id := parseTaskResultInstanceID(evt.Result); id != "" {
					block.LinkedAgentID = id
				}
			}
			if evt.Name == "Notify" && evt.Status != agent.ToolResultStatusError && evt.Result != "" {
				if handle, ok := parseTaskToolHandle(evt.Result); ok && handle.TaskID != "" && handle.AgentID != "" {
					if taskBlock, ok := m.viewport.FindBlockByLinkedTask(handle.TaskID); ok {
						taskBlock.LinkedAgentID = handle.AgentID
						taskBlock.LinkedTaskID = handle.TaskID
						taskBlock.InvalidateCache()
						m.updateViewportBlock(taskBlock)
					}
				}
			}
			block.InvalidateCache()
			m.updateViewportBlock(block)
			m.markBlockSettled(block)
		} else {
			block := &Block{ID: m.nextBlockID, Type: BlockToolResult, Content: toolExpandedResultContent(evt.Name, evt.Result), ToolName: evt.Name, ToolID: evt.CallID, ResultStatus: evt.Status, Collapsed: true, AgentID: evt.AgentID}
			m.nextBlockID++
			m.appendViewportBlock(block)
			m.markBlockSettled(block)
		}
		m.streamRenderForceView = true
		m.streamRenderDeferred = false
		m.streamRenderDeferNext = false
		effects.addFollowup(m.requestStreamBoundaryFlush())
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) handleMiscAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.LoopNoticeEvent:
		m.invalidateStatusBarAgentSnapshot()
		m.invalidateDrawCaches()
		m.finalizeAssistantBlock()
		content := strings.TrimSpace(evt.Text)
		if content == "" {
			return true, effects
		}
		m.exitRenderFreeze()
		wasNearBottom := m.viewport != nil && (m.viewport.sticky || m.viewport.TotalLines()-m.viewport.height-m.viewport.offset <= 1)
		block := &Block{ID: m.nextBlockID, Type: BlockStatus, StatusTitle: evt.Title, Content: content}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		if wasNearBottom && m.viewport != nil {
			m.viewport.ScrollToBottom()
		}
		return true, effects
	case agent.LoopStateChangedEvent:
		effects.invalidateUsage = true
		m.invalidateDrawCaches()
		return true, effects
	case agent.ErrorEvent:
		m.clearSessionSwitch()
		m.finalizeAssistantBlock()
		block := &Block{ID: m.nextBlockID, Type: BlockError, Content: evt.Err.Error(), AgentID: evt.AgentID}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		m.exitRenderFreeze()
		return true, effects
	case agent.RoleChangedEvent:
		effects.refreshSidebar = true
		m.invalidateDrawCaches()
		return true, effects
	case agent.HandoffEvent:
		m.finalizeAssistantBlock()
		block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: fmt.Sprintf("Plan saved to: %s", evt.PlanPath)}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		m.openHandoffSelect(evt.PlanPath)
		return true, effects
	case agent.InfoEvent:
		if isLoopInfoMessage(evt.Message) {
			effects.addFollowup(m.enqueueToast(evt.Message, "info"))
			return true, effects
		}
		if title, content, ok := formatExportStatusCard(evt.Message); ok {
			m.appendLocalStatusCard(title, content)
			return true, effects
		}
		m.finalizeAssistantBlock()
		block := &Block{ID: m.nextBlockID, Type: BlockStatus, Content: evt.Message, AgentID: evt.AgentID}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		return true, effects
	case agent.SpawnFinishedEvent:
		m.finalizeAssistantBlock()
		backgroundID := evt.EffectiveID()
		content := strings.TrimSpace(evt.Message)
		if content == "" {
			kind := strings.TrimSpace(evt.Kind)
			if kind == "" {
				kind = "job"
			}
			desc := strings.TrimSpace(evt.Description)
			if desc == "" {
				desc = evt.Command
			}
			label := strings.ToUpper(kind[:1]) + kind[1:]
			content = fmt.Sprintf("[%s %s finished]\n\nDescription: %s\nStatus: %s", label, backgroundID, desc, evt.Status)
		}
		if block, ok := m.viewport.FindStatusBlockByBackgroundObject(backgroundID); ok {
			block.Content = content
			block.AgentID = evt.AgentID
			block.InvalidateCache()
			m.updateViewportBlock(block)
			m.markBlockSettled(block)
		} else {
			block := &Block{ID: m.nextBlockID, Type: BlockStatus, Content: content, AgentID: evt.AgentID, BackgroundObjectID: backgroundID}
			m.nextBlockID++
			m.appendViewportBlock(block)
			m.markBlockSettled(block)
		}
		return true, effects
	case agent.ToastEvent:
		effects.addFollowup(m.enqueueToast(evt.Message, evt.Level))
		if m.shouldPriorityFlushToast(evt.Level) {
			effects.addFollowup(m.requestStreamBoundaryFlush())
		}
		return true, effects
	case agent.ContextUsageUpdateEvent:
		effects.invalidateUsage = true
		return true, effects
	case agent.ForkSessionEvent:
		if len(evt.Parts) > 0 {
			m.clearActiveSearch()
			m.editingQueuedDraftID = ""
			text, inlinePastes := displayTextAndInlinePastes(evt.Parts, "")
			nextPasteSeq := 0
			for _, paste := range inlinePastes {
				if paste.Seq > nextPasteSeq {
					nextPasteSeq = paste.Seq
				}
			}
			if text == "" {
				draft := queuedDraftFromParts(evt.Parts)
				text, _ = queuedDraftTextAndImageCount(draft)
			}
			m.input.SetDisplayValueAndPastes(text, inlinePastes, nextPasteSeq)
			m.input.syncHeight()
			m.attachments = attachmentsFromParts(evt.Parts)
			cmd := m.switchModeWithIME(ModeInsert)
			m.recalcViewportSize()
			effects.addFollowup(tea.Batch(m.input.Focus(), cmd))
		}
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) handleHygieneAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch event.(type) {
	case agent.EnvStatusUpdateEvent:
		effects.refreshSidebar = true
		return true, effects
	case agent.RateLimitUpdatedEvent, agent.KeyPoolChangedEvent, agent.TodosUpdatedEvent:
		effects.invalidateUsage = true
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) shouldRefreshToolArgRender(callID, argsJSON string, now time.Time) bool {
	if strings.TrimSpace(callID) == "" {
		return true
	}
	state, ok := m.toolArgRenderState[callID]
	if !ok {
		return true
	}
	currentBytes := len(argsJSON)
	if currentBytes <= state.lastBytes {
		return false
	}
	delay := m.currentCadence().visualAnimDelay
	if delay <= 0 {
		delay = foregroundCadence.visualAnimDelay
		if delay <= 0 {
			delay = 200 * time.Millisecond
		}
	}
	return now.Sub(state.lastAt) >= delay
}

func (m *Model) recordToolArgRender(callID, argsJSON string, now time.Time) {
	if strings.TrimSpace(callID) == "" {
		return
	}
	if m.toolArgRenderState == nil {
		m.toolArgRenderState = make(map[string]toolArgRenderState)
	}
	m.toolArgRenderState[callID] = toolArgRenderState{
		lastBytes: len(argsJSON),
		lastAt:    now,
	}
}

// scheduleKeyPoolTick schedules a one-shot refresh when key cooldown may end.
// Uses a bounded wait to avoid tight loops and to limit wakeups on long cooldowns.
func (m *Model) scheduleKeyPoolTick() tea.Cmd {
	type keyPooler interface {
		KeyPoolNextTransition() time.Duration
	}
	if m.agent == nil {
		return nil
	}
	now := time.Now()
	d := time.Duration(0)
	if kp, ok := m.agent.(keyPooler); ok {
		d = kp.KeyPoolNextTransition()
	}
	if snapDelay := nextRateLimitSnapshotDisplayTransition(m.agent.CurrentRateLimitSnapshot(), now); snapDelay > 0 && (d == 0 || snapDelay < d) {
		d = snapDelay
	}
	if d <= 0 {
		return nil
	}
	const minWait = 200 * time.Millisecond
	if d < minWait {
		d = minWait
	}
	gen := m.keyPoolTickGen
	return tea.Tick(d, func(time.Time) tea.Msg {
		return keyPoolTickMsg{gen: gen}
	})
}

// injectQuestionRequestFromEvent builds a questionRequestMsg from a remote
// QuestionRequestEvent so the TUI shows the question dialog (remote/connect mode).
func injectQuestionRequestFromEvent(evt agent.QuestionRequestEvent) tea.Cmd {
	opts := make([]tools.QuestionOption, len(evt.Options))
	for i, s := range evt.Options {
		opt := tools.QuestionOption{Label: s}
		if i < len(evt.OptionDetails) {
			opt.Description = evt.OptionDetails[i]
		}
		opts[i] = opt
	}
	req := QuestionRequest{
		Questions: []tools.QuestionItem{{
			Header:   evt.Header,
			Question: evt.Question,
			Options:  opts,
			Multiple: evt.Multiple,
		}},
		Timeout: evt.Timeout,
	}
	return func() tea.Msg {
		return questionRequestMsg{request: req, requestID: evt.RequestID}
	}
}
