package tui

import (
	"slices"
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func (m *Model) handleTurnAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.IdleEvent:
		effects.invalidateUsage = true
		m.clearSessionSwitch()
		// The turn is over: a text or thinking delta still in flight (the final
		// batch a cancelled provider goroutine flushes after this event)
		// belongs to the settled card and must merge back instead of opening a
		// second card split mid-word.
		m.markAgentStreamSettled("")
		m.finalizeTurnForIdleEvent()
		cancelledByUser := m.pauseQueuedDraftDrainOnce
		prevMain := m.activities["main"].Type
		m.markAgentIdle("main")
		mainLoopBusy := m.agent != nil && m.agent.LoopKeepsMainBusy()
		if mainLoopBusy {
			m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main", Detail: "loop"}
			if m.terminalTitleBackgroundCompletedAgentID == "main" {
				m.terminalTitleBackgroundCompletedAgentID = ""
			}
		} else {
			m.maybeShowBackgroundCompletionTitle("main", prevMain, agent.ActivityIdle)
		}
		skipDrain := cancelledByUser
		m.pauseQueuedDraftDrainOnce = false
		m.inflightDraft = nil
		m.stopActiveAnimationIfIdle()
		if prevMain != "" && prevMain != agent.ActivityIdle && !mainLoopBusy {
			effects.addFollowup(m.scheduleBackgroundHousekeeping())
		}
		if !skipDrain && !m.queueSyncEnabled {
			effects.addFollowup(m.drainQueuedDrafts())
		}
		return true, effects
	case agent.GlobalIdleEvent:
		effects.invalidateUsage = true
		m.clearSessionSwitch()
		m.markAgentStreamSettled("")
		for agentID := range m.subAgentStreamStates {
			m.markAgentStreamSettled(agentID)
		}
		m.finalizeTurnForIdleEvent()
		m.markAgentIdle("main")
		m.stopActiveAnimationIfIdle()
		pendingAutoContinue := m.queuedDraftsAutoContinue() || (!m.queueSyncEnabled && len(m.visibleQueuedDrafts()) > 0)
		if !evt.SuppressUserNotification && !pendingAutoContinue {
			effects.addFollowup(m.maybeTerminalNotifyCmd(m.idleNotificationText()))
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
		if evt.AgentID == "" || evt.AgentID == "main" {
			m.finalizeTurnForIdleEvent()
		} else {
			m.finalizeAgentStreamForIdleEvent(evt.AgentID)
		}
		content := userBlockTextFromParts(draft.contentParts(), draft.Content)
		msgIndex := -1
		var msgs []message.Message
		if (evt.AgentID == "" || evt.AgentID == "main") && m.agent != nil {
			msgs = m.agent.GetMessages()
			for i, msg := range slices.Backward(msgs) {

				if !message.IsUserAuthored(msg) {
					continue
				}
				if userPromptMatchesContent(msg, content) {
					msgIndex = i
					break
				}
			}
		}
		// A compaction rewrite can rebuild the transcript from ctxMgr — and so
		// draw this durable message — before the queued draft's consumption
		// event arrives. The card then already exists, so adopt it by its
		// durable message index instead of appending a second card for the same
		// message.
		block := m.mainUserBlockForMsgIndex(msgs, msgIndex)
		if block == nil {
			_, imageCount := queuedDraftTextAndImageCount(draft)
			fileRefs := draft.FileRefs
			if fileRefs == nil {
				fileRefs = fileRefsFromParts(evt.Parts)
			}
			block = &Block{ID: m.nextBlockID, Type: BlockUser, Content: content, AgentID: evt.AgentID, LoopAnchor: draft.LoopAnchor, ImageCount: imageCount, ImageParts: imagePartsFromContentParts(draft.contentParts()), PDFNames: pdfNamesFromContentParts(draft.contentParts()), FileRefs: fileRefs, MsgIndex: msgIndex, StartedAt: draft.QueuedAt}
			m.nextBlockID++
			m.appendViewportBlock(block)
		} else {
			// The rebuilt card carries the durable content; only the live-only
			// loop marker and queue timestamp have no durable counterpart.
			block.LoopAnchor = block.LoopAnchor || draft.LoopAnchor
			if block.StartedAt.IsZero() {
				block.StartedAt = draft.QueuedAt
			}
		}
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
