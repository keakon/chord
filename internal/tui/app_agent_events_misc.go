package tui

import (
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// streamContinueCardTitle labels the status card rendered for a durable
// KindStreamContinue user message: the real continuation instruction that was
// appended after a preserved stream interruption and sent to the model. Live
// handling of StreamContinueEvent and restore-time rendering of the message
// share this chrome so the card looks identical before and after a restore.
const streamContinueCardTitle = "REPLY RESUMED"

// infoCardTitle names the card built from a plain runtime info event, which
// carries no title of its own.
const infoCardTitle = "NOTICE"

// contextNoticeTitle maps a ContextNoticeEvent level to the badge the card
// carries. The three levels mirror the request-scoped overlays the compaction
// gate attaches to outgoing requests; surfacing them as cards lets the user
// see the same context-pressure signal the model receives.
func contextNoticeTitle(level string) string {
	switch level {
	case "imminent":
		return "COMPACT IMMINENT"
	case "warning":
		return "COMPACT WARNING"
	default:
		return "CONTEXT PRESSURE"
	}
}

// removeContextNoticeBlocks drops every live context-pressure card after the
// agent removed their backing KindContextNotice messages (a model switch
// changed the compaction threshold). Cards are matched by NoticeLevel, the
// marker only this path sets. Removing the messages shifted every later
// transcript index, so main user block fork anchors are re-synced.
func (m *Model) removeContextNoticeBlocks() {
	if m == nil || m.viewport == nil {
		return
	}
	var ids []int
	for _, block := range m.viewport.blocks {
		if block != nil && block.NoticeLevel != "" {
			ids = append(ids, block.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		m.removeViewportBlockByID(id)
	}
	m.recalcViewportSize()
	m.syncVisibleMainUserBlockMsgIndexes()
}

func (m *Model) handleMiscAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.LoopNoticeEvent:
		m.invalidateStatusBarAgentSnapshot()
		m.invalidateDrawCaches()
		m.finalizeTurn()
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
	case agent.StreamContinueEvent:
		// A preserved stream interruption is being resumed. Settle the
		// interrupted assistant card first. Text mirrors the durable
		// KindStreamContinue message that was just appended (empty when the
		// pool resumes from the trailing interrupted assistant turn without
		// extra input), so a non-empty Text is rendered as the status card for
		// that message — what the user sees is exactly what the model was sent,
		// never a display-only notice.
		m.invalidateStatusBarAgentSnapshot()
		m.invalidateDrawCaches()
		m.finalizeAgentStream(evt.AgentID)
		content := strings.TrimSpace(evt.Text)
		if content == "" {
			return true, effects
		}
		m.exitRenderFreeze()
		wasNearBottom := m.viewport != nil && (m.viewport.sticky || m.viewport.TotalLines()-m.viewport.height-m.viewport.offset <= 1)
		block := &Block{ID: m.nextBlockID, Type: BlockStatus, StatusTitle: streamContinueCardTitle, Content: content, AgentID: evt.AgentID}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		if wasNearBottom && m.viewport != nil {
			m.viewport.ScrollToBottom()
		}
		return true, effects
	case agent.LoopStateChangedEvent, agent.YoloModeChangedEvent:
		effects.invalidateUsage = true
		m.invalidateDrawCaches()
		return true, effects
	case agent.ErrorEvent:
		if evt.Silent {
			m.recordAgentError(evt.AgentID, evt.Err, evt.Provider, evt.Model, evt.Key, evt.AccountID, evt.Email, true)
			// Silent retry errors arrive mid-stream while the attempt is being
			// retried; finalizing the streaming block or exiting render freeze
			// here would prematurely settle a card that the retry continues.
			return true, effects
		}
		// A non-retriable error with no fallback is emitted once as a silent
		// retry attempt and again here as the final error; record the final one
		// only when it does not merely repeat that last retry record.
		if !m.finalErrorDuplicatesLastRetry(evt.Err) {
			m.recordAgentError(evt.AgentID, evt.Err, evt.Provider, evt.Model, evt.Key, evt.AccountID, evt.Email, false)
		}
		m.clearSessionSwitch()
		m.finalizeAgentStream(evt.AgentID)
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
		m.finalizeTurn()
		// The handoff tool card itself shows the plan path and stays
		// non-terminal until the user confirms, rejects, or cancels, so no
		// separate "Plan saved to:" assistant block is inserted here. The
		// selector opens as a follow-up so it queues behind any dialog already
		// on screen instead of racing it.
		effects.addFollowup(func() tea.Msg {
			return handoffSelectRequestMsg{planPath: evt.PlanPath, requestID: evt.RequestID, agentID: evt.AgentID}
		})
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
		m.finalizeAgentStream(evt.AgentID)
		block := &Block{ID: m.nextBlockID, Type: BlockStatus, StatusTitle: infoCardTitle, Content: evt.Message, AgentID: evt.AgentID}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		return true, effects
	case agent.ContextNoticeEvent:
		// Surface the same context-pressure signal the model receives as a
		// card. The agent persisted the notice as a KindContextNotice message
		// before emitting this event, so the card is backed by a real
		// transcript slot (MsgIndex) that a restored session rebuilds from.
		// Emitted once per compaction window, never per-request, so it does
		// not spam.
		block := &Block{ID: m.nextBlockID, Type: BlockStatus, StatusTitle: contextNoticeTitle(evt.Level), Content: evt.Message, AgentID: "", MsgIndex: evt.MessageIndex, NoticeLevel: evt.Level}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		return true, effects
	case agent.ContextNoticeClearedEvent:
		m.removeContextNoticeBlocks()
		return true, effects
	case agent.SpawnFinishedEvent:
		// A finished background object no longer builds a card here: the card
		// comes from BackgroundResultAppendedEvent once the result is durably
		// in the owner's transcript, so a queued-but-undelivered result shows
		// in the pending area instead of as a card with no backing message.
		agentID := evt.AgentID
		if agentID == "main" {
			agentID = ""
		}
		m.finalizeAgentStream(agentID)
		return true, effects
	case agent.BackgroundResultAppendedEvent:
		// The result is durable now: drop its pending-area entry and build the
		// JOB RESULT card from the exact persisted text, using the same parser
		// the restore path uses so live and restored cards render identically.
		if evt.Message.Mailbox != nil {
			m.removeQueuedMailbox(evt.Message.Mailbox.MessageID)
		}
		agentID := evt.TargetAgentID
		if agentID == "main" {
			agentID = ""
		}
		content, backgroundID := formatBackgroundResultCardContent(evt.Message.Content, "", "", "", "")
		if backgroundID == "" && evt.Message.Mailbox != nil {
			// The result carries no job id in its headline. Fall back to the
			// durable message identity so two different results with the same
			// description are not merged into one card, while re-delivery of
			// the same result still updates the existing card.
			backgroundID = strings.TrimSpace(evt.Message.Mailbox.MessageID)
		}
		if block, ok := m.findStatusBlockByBackgroundObject(backgroundID); ok {
			block.Content = content
			block.BackgroundCopyContent = evt.Message.Content
			block.StatusTitle = backgroundResultCardTitle
			block.AgentID = agentID
			block.MsgIndex = evt.MessageIndex
			block.InvalidateCache()
			m.updateViewportBlock(block)
			m.markBlockSettled(block)
		} else {
			block := &Block{ID: m.nextBlockID, Type: BlockStatus, StatusTitle: backgroundResultCardTitle, Content: content, BackgroundCopyContent: evt.Message.Content, AgentID: agentID, BackgroundObjectID: backgroundID, MsgIndex: evt.MessageIndex}
			m.nextBlockID++
			m.appendViewportBlock(block)
			m.markBlockSettled(block)
		}
		return true, effects
	case agent.PersistenceHealthEvent:
		m.persistenceDegraded = evt.Degraded
		return true, effects
	case agent.ToastEvent:
		effects.addFollowup(m.enqueueToastWithCategory(evt.Message, evt.Level, evt.Category))
		if m.shouldPriorityFlushToast(evt.Level) {
			effects.addFollowup(m.requestStreamBoundaryFlush())
		}
		return true, effects
	case agent.UsageUpdatedEvent:
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
			if m.pendingSessionRestoreRebuild {
				m.preserveAttachmentsOnNextRebuild = true
			}
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
		if m.mode == ModeMCPSelect {
			m.refreshMCPSelectItems()
		}
		return true, effects
	case agent.RateLimitUpdatedEvent, agent.KeyPoolChangedEvent, agent.TodosUpdatedEvent:
		effects.invalidateUsage = true
		return true, effects
	default:
		return false, effects
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
	const minWait = 200 * time.Millisecond
	snap := m.agent.CurrentRateLimitSnapshot()
	if snapDelay := nextRateLimitSnapshotDisplayTransition(snap, now); snapDelay > 0 && (d == 0 || snapDelay < d) {
		d = snapDelay
	}
	if m.hasActiveAgentActivity() && snap != nil && !snap.CapturedAt.IsZero() {
		pollDelay := snap.CapturedAt.Add(codexActiveRateLimitPollInterval).Sub(now)
		if pollDelay <= 0 {
			pollDelay = codexActiveRateLimitPollInterval
		}
		if d == 0 || pollDelay < d {
			d = pollDelay
		}
	}
	if d <= 0 {
		return nil
	}
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
		AgentID: evt.AgentID,
	}
	return func() tea.Msg {
		return questionRequestMsg{request: req, requestID: evt.RequestID}
	}
}
