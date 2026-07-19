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
		m.finalizeTurn()
		cancelledByUser := m.pauseQueuedDraftDrainOnce
		if cancelledByUser {
			m.revealTrailingInterruptedTurnUserMessage()
		}
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
		m.finalizeTurn()
		m.markAgentIdle("main")
		m.stopActiveAnimationIfIdle()
		pendingAutoContinue := m.queuedDraftsAutoContinue() || (!m.queueSyncEnabled && len(m.visibleQueuedDrafts()) > 0)
		if !pendingAutoContinue {
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
			m.finalizeTurn()
		} else {
			m.finalizeAgentStream(evt.AgentID)
		}
		imageCount := 0
		content := userBlockTextFromParts(draft.contentParts(), draft.Content)
		_, imageCount = queuedDraftTextAndImageCount(draft)
		fileRefs := draft.FileRefs
		if fileRefs == nil {
			fileRefs = fileRefsFromParts(evt.Parts)
		}
		msgIndex := -1
		if (evt.AgentID == "" || evt.AgentID == "main") && m.agent != nil {
			msgs := m.agent.GetMessages()
			for i, msg := range slices.Backward(msgs) {

				if msg.Role != "user" || msg.IsCompactionSummary {
					continue
				}
				if message.UserPromptPlainText(msg) == content {
					msgIndex = i
					break
				}
			}
		}
		block := &Block{ID: m.nextBlockID, Type: BlockUser, Content: content, AgentID: evt.AgentID, LoopAnchor: draft.LoopAnchor, ImageCount: imageCount, ImageParts: imagePartsFromContentParts(draft.contentParts()), PDFNames: pdfNamesFromContentParts(draft.contentParts()), FileRefs: fileRefs, MsgIndex: msgIndex, StartedAt: draft.QueuedAt}
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
