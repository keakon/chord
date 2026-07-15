package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

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
		m.finalizeAgentStream(evt.AgentID)
		block := &Block{ID: m.nextBlockID, Type: BlockStatus, Content: evt.Message, AgentID: evt.AgentID}
		m.nextBlockID++
		m.appendViewportBlock(block)
		m.markBlockSettled(block)
		return true, effects
	case agent.SpawnFinishedEvent:
		m.finalizeAgentStream(evt.AgentID)
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
		if block, ok := m.findStatusBlockByBackgroundObject(backgroundID); ok {
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
	}
	return func() tea.Msg {
		return questionRequestMsg{request: req, requestID: evt.RequestID}
	}
}
