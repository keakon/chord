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
			m.setAgentActivity(agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main", Detail: "loop"})
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
	case agent.InputQueueChangedEvent:
		m.applyInputQueueSnapshot(evt.Snapshot)
		return true, effects
	case agent.InputQueueOperationResultEvent:
		effects.addFollowup(m.handleInputQueueResult(evt))
		return true, effects
	case agent.UserMessageCommittedEvent:
		if m.inflightDraft != nil && evt.RequestID != "" &&
			m.inflightDraft.ID == evt.RequestID &&
			normalizeDraftAgentID(m.inflightDraft.AgentID) == normalizeDraftAgentID(evt.AgentID) {
			m.inflightDraft.Parts = append([]message.ContentPart(nil), evt.Parts...)
		}
		block := m.userBlockForCommittedMessage(evt)
		if block == nil {
			return true, effects
		}
		applyCommittedUserMessageParts(block, evt.Parts)
		m.updateViewportBlock(block)
		m.recalcViewportSize()
		effects.addFollowup(m.imageProtocolCmd())
		return true, effects
	case agent.PendingDraftConsumedEvent:
		if evt.InputID != "" && evt.Target.RuntimeID != "" && m.agent != nil && !sameInputQueueBinding(evt.Target, m.agent.InputDeliveryTarget()) {
			return true, effects
		}
		draft := queuedDraftFromParts(evt.Parts)
		draft.ID = evt.DraftID
		for idx, queued := range m.queuedDrafts {
			matches := evt.InputID != "" && queued.InputID == evt.InputID
			if evt.InputID == "" || evt.Revision == 0 && queued.DeliveryStatus == "Saving" {
				matches = queued.ID == evt.DraftID
			}
			if matches {
				draft = m.removeQueuedDraftAt(idx)
				break
			}
		}
		draft.Parts = append([]message.ContentPart(nil), evt.Parts...)
		m.maybeSetTerminalTitleFromFirstUserMessage(draft.Content, draft.Parts)
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
				if evt.InputID != "" && msg.InputID == evt.InputID || evt.InputID == "" && userPromptMatchesContent(msg, content) {
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
			blockParts := draft.contentParts()
			if msgIndex >= 0 && msgIndex < len(msgs) {
				blockParts = msgs[msgIndex].Parts
			}
			imageParts := imagePartsFromContentParts(blockParts)
			fileRefs := draft.FileRefs
			if fileRefs == nil {
				fileRefs = fileRefsFromParts(evt.Parts)
			}
			block = &Block{ID: m.nextBlockID, Type: BlockUser, Content: content, AgentID: evt.AgentID, InputRequestID: draft.ID, LoopAnchor: draft.LoopAnchor, ImageCount: len(imageParts), ImageParts: imageParts, PDFNames: pdfNamesFromContentParts(blockParts), FileRefs: fileRefs, MsgIndex: msgIndex, StartedAt: draft.QueuedAt}
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
		if msgIndex >= 0 && msgIndex < len(msgs) {
			// Do not keep the composer's inline copy alive in the in-flight
			// draft after the durable message has a session-owned path.
			d.Parts = append([]message.ContentPart(nil), msgs[msgIndex].Parts...)
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

func (m *Model) userBlockForCommittedMessage(evt agent.UserMessageCommittedEvent) *Block {
	if m.viewport == nil {
		return nil
	}
	for _, block := range slices.Backward(m.viewport.blocks) {
		if block == nil || block.Type != BlockUser || block.IsUserLocalShell() ||
			normalizeDraftAgentID(block.AgentID) != normalizeDraftAgentID(evt.AgentID) {
			continue
		}
		if evt.RequestID != "" {
			if block.InputRequestID != evt.RequestID {
				continue
			}
		} else if evt.MessageIndex < 0 || block.MsgIndex != evt.MessageIndex {
			continue
		}
		block.MsgIndex = evt.MessageIndex
		return block
	}
	return nil
}
