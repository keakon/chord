package agent

import (
	"strconv"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

type stageBoundaryRequest struct {
	eligible      bool
	todoCandidate bool
}

// Capture event-loop-owned policy before request assembly moves to its worker.
func (a *MainAgent) snapshotStageBoundaryRequest() stageBoundaryRequest {
	if !a.compactContextVisible() || a.manualNoticeActive() || a.IsCompactionRunning() ||
		a.pendingModelDriven != nil || a.pendingModelDrivenNotice != "" {
		return stageBoundaryRequest{}
	}
	bundle := modelDrivenBarrierSnapshot{
		currentRequestBatch:       a.currentRequestBatch(nil),
		lastModelDrivenApplyBatch: a.lastModelDrivenApplyBatch,
		lastModelDrivenSkipBatch:  a.lastModelDrivenSkipBatch,
		lastModelDrivenSkipReason: a.lastModelDrivenSkipReason,
	}
	if _, _, skip := a.modelDrivenIntervalCooldownVerdict(bundle); skip {
		return stageBoundaryRequest{}
	}
	return stageBoundaryRequest{
		eligible:      true,
		todoCandidate: a.stageCompletionCandidatePending && !a.stageCompletionCandidatePromptDelivered,
	}
}

// The projection includes mailbox rows only after the ordinary formatter and
// deduplication path appends them. Undeliverable inbox entries are not billed.
func (a *MainAgent) appendStageBoundaryReminder(input llmRequestInput, prepared, overlays []message.Message, modelRef string) []message.Message {
	if !input.stageBoundary.eligible {
		return overlays
	}
	decision := a.ctxMgr.AutoCompactDecision()
	reminder := a.effectiveReminderPctForModelRef(modelRef, decision.Threshold)
	if decision.UsageState == ctxmgr.ContextUsageUnknown || decision.UsableCompactionBudget <= 0 ||
		decision.Threshold <= 0 || reminder <= 0 || reminder >= decision.Threshold {
		return overlays
	}
	line := float64(decision.UsableCompactionBudget) * reminder
	if float64(decision.EffectiveInputTokens) >= line || float64(decision.ProjectedContextTokens) < line {
		return overlays
	}
	for _, messages := range [][]message.Message{prepared, overlays} {
		for _, msg := range messages {
			if pressureNoticeSlot(msg.NoticeLevel) >= 0 {
				return overlays
			}
		}
	}
	completion := false
	var previousIDs map[string]struct{}
	for _, msg := range overlays {
		if msg.Kind != message.KindSubAgentMailbox || msg.Mailbox == nil ||
			msg.Mailbox.Kind != string(SubAgentMailboxKindCompleted) {
			continue
		}
		if previousIDs == nil {
			previousIDs = conversationMailboxIDs(input.Messages)
		}
		if !mapContains(previousIDs, msg.Mailbox.MessageID) {
			completion = true
			break
		}
	}
	source := ""
	switch {
	case completion && input.stageBoundary.todoCandidate:
		source = "completion_and_todo"
	case completion:
		source = "completion"
	case input.stageBoundary.todoCandidate:
		source = "todo"
	default:
		return overlays
	}
	overlays = append(overlays, message.Message{
		Role:    message.RoleUser,
		Kind:    message.KindTurnOverlay,
		Content: message.SystemReminderOverlayText(stageBoundaryPressureReminder()),
	})
	a.recordContextDiagnosticEvent(compactionPolicyAnalyticsPurpose+"/boundary_notice", map[string]string{
		"source":           source,
		"observed_tokens":  strconv.Itoa(decision.EffectiveInputTokens),
		"projected_tokens": strconv.Itoa(decision.ProjectedContextTokens),
		"usable_budget":    strconv.Itoa(decision.UsableCompactionBudget),
		"reminder_ratio":   strconv.FormatFloat(reminder, 'f', -1, 64),
	})
	return overlays
}

func stageBoundaryPressureReminder() string {
	return "This request contains a task-completion report or a terminal todo list. Treat it as a candidate stage boundary, not proof that the work is verified or the whole request is done. " +
		"Content added since the last usage reading is estimated to bring context to the reminder line.\n" +
		contextPressureReminderActionFor(contextPressureAudienceMain) +
		" Set stage_status from the verified state and preserve every unfinished workstream."
}
