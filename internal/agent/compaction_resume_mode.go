package agent

import "strings"

const (
	compactionResumeModeResume            = "resume"
	compactionResumeModeReplayUserIntent  = "replay_user_intent"
	compactionResumeModeSyntheticContinue = "synthetic_continue"
)

func (a *MainAgent) hasQueuedUserInputForRecovery() bool {
	if a == nil {
		return false
	}
	for _, pending := range a.pendingUserMessages {
		if pending.FromUser || strings.TrimSpace(pendingUserMessageText(pending)) != "" {
			return true
		}
	}
	return false
}

func (a *MainAgent) hasPendingToolSideEffectsForRecovery() bool {
	if a == nil || a.turn == nil {
		return false
	}
	if len(a.turn.PendingToolMeta) > 0 {
		return true
	}
	if a.turn.PendingToolCalls.Load() > 0 {
		return true
	}
	return false
}

func (a *MainAgent) hasOutstandingMailboxPressureForRecovery() bool {
	if a == nil {
		return false
	}
	return len(a.pendingSubAgentMailboxes) > 0 || a.activeSubAgentMailbox != nil || len(a.activeSubAgentMailboxes) > 0
}

func (a *MainAgent) chooseCompactionResumeMode(userIntent string) string {
	userIntent = strings.TrimSpace(userIntent)
	if a == nil {
		if userIntent != "" {
			return compactionResumeModeReplayUserIntent
		}
		return compactionResumeModeSyntheticContinue
	}
	if a.hasQueuedUserInputForRecovery() {
		return compactionResumeModeResume
	}
	if a.hasPendingToolSideEffectsForRecovery() || a.hasOutstandingMailboxPressureForRecovery() {
		return compactionResumeModeSyntheticContinue
	}
	if userIntent != "" {
		return compactionResumeModeReplayUserIntent
	}
	return compactionResumeModeSyntheticContinue
}
