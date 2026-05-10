package agent

import (
	"strings"

	"github.com/keakon/chord/internal/recovery"
)

const maxOversizeRecoveryAttempts = 2

func clonePendingCompactionResume(state *recovery.PendingCompactionResume) *recovery.PendingCompactionResume {
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}

func (a *MainAgent) snapshotPendingCompactionResume() *recovery.PendingCompactionResume {
	if a == nil {
		return nil
	}
	if state := clonePendingCompactionResume(a.pendingCompactionResume); state != nil {
		return state
	}
	if a.turn == nil || (!a.turn.InLengthRecovery && a.turn.OversizeRecoveryCount == 0) {
		return nil
	}
	state := &recovery.PendingCompactionResume{}
	if a.turn.InLengthRecovery {
		state.Kind = string(compactionResumeLengthRecovery)
		state.Mode = compactionResumeModeResume
		state.RecoveryPrompt = strings.TrimSpace(a.pendingRecoveryPrompt)
		if state.RecoveryPrompt == "" {
			state.RecoveryPrompt = lengthRecoveryPrompt(a.turn.LastTruncatedToolName)
		}
	}
	if a.turn.OversizeRecoveryCount > 0 {
		if state.Kind == "" {
			state.Kind = string(compactionResumeAutoContinue)
		}
		state.OversizeRetryCount = a.turn.OversizeRecoveryCount
		state.OversizeSuspended = true
		if state.UserIntent == "" {
			state.UserIntent = strings.TrimSpace(a.latestRecoverableUserIntent())
		}
		if strings.TrimSpace(state.Mode) == "" {
			state.Mode = a.chooseCompactionResumeMode(state.UserIntent)
		}
	}
	if state.Kind == "" && strings.TrimSpace(state.RecoveryPrompt) == "" && strings.TrimSpace(state.UserIntent) == "" && state.OversizeRetryCount == 0 && !state.OversizeSuspended {
		return nil
	}
	return state
}

func (a *MainAgent) setPendingCompactionResume(state *recovery.PendingCompactionResume) {
	if a == nil {
		return
	}
	a.pendingCompactionResume = clonePendingCompactionResume(state)
}

func (a *MainAgent) clearPendingCompactionResume() {
	if a == nil {
		return
	}
	a.pendingCompactionResume = nil
}

func (a *MainAgent) syncPendingCompactionResumeSnapshot() {
	if a == nil || a.recovery == nil || a.shuttingDown.Load() {
		return
	}
	a.setPendingCompactionResume(a.snapshotPendingCompactionResume())
	a.saveRecoverySnapshot()
}

func (a *MainAgent) armOversizeAutoContinueResume() {
	if a == nil {
		return
	}
	state := &recovery.PendingCompactionResume{
		Kind:               string(compactionResumeAutoContinue),
		UserIntent:         strings.TrimSpace(a.latestRecoverableUserIntent()),
		OversizeRetryCount: 0,
	}
	state.Mode = a.chooseCompactionResumeMode(state.UserIntent)
	if a.turn != nil {
		state.OversizeRetryCount = a.turn.OversizeRecoveryCount
	}
	a.setPendingCompactionResume(state)
	a.syncPendingCompactionResumeSnapshot()
}

func (a *MainAgent) armLengthRecoveryResume(prompt string) {
	if a == nil {
		return
	}
	state := &recovery.PendingCompactionResume{
		Kind:           string(compactionResumeLengthRecovery),
		Mode:           compactionResumeModeResume,
		RecoveryPrompt: strings.TrimSpace(prompt),
	}
	a.setPendingCompactionResume(state)
	a.syncPendingCompactionResumeSnapshot()
}

func (a *MainAgent) applyPendingCompactionResumeOverlays(state *recovery.PendingCompactionResume) {
	if a == nil {
		return
	}
	if state == nil {
		state = a.pendingCompactionResume
	}
	state = clonePendingCompactionResume(state)
	if state == nil {
		return
	}
	if strings.TrimSpace(state.Mode) == "" {
		state.Mode = a.chooseCompactionResumeMode(state.UserIntent)
	}
	switch compactionContinuationKind(strings.TrimSpace(state.Kind)) {
	case compactionResumeLengthRecovery:
		if prompt := strings.TrimSpace(state.RecoveryPrompt); prompt != "" {
			a.pendingRecoveryPrompt = prompt
		}
	case compactionResumeAutoContinue, compactionResumeMainLLM:
		a.pendingAutoContinuePrompt = autoContinuePrompt()
		switch strings.TrimSpace(state.Mode) {
		case compactionResumeModeReplayUserIntent:
			if replay := autoContinueReplayPrompt(strings.TrimSpace(state.UserIntent)); replay != "" {
				a.pendingAutoContinueReplayPrompt = replay
			}
		case compactionResumeModeResume, compactionResumeModeSyntheticContinue, "":
			// Resume/synthetic continue intentionally do not replay the prior user
			// turn as a transient user overlay. Resume relies on the compacted
			// transcript plus any queued input/mailbox updates; synthetic continue
			// keeps the task moving without risking side-effectful user-turn replay.
		}
	}
}

func (a *MainAgent) applyPendingCompactionResumeOverlaysForContinue() {
	if a == nil || a.pendingCompactionResume == nil {
		return
	}
	state := clonePendingCompactionResume(a.pendingCompactionResume)
	if state == nil {
		return
	}
	a.applyPendingCompactionResumeOverlays(state)
	if state.OversizeRetryCount > 0 {
		a.newTurnOversizeRecoveryCount = state.OversizeRetryCount
	}
	a.clearPendingCompactionResume()
	a.saveRecoverySnapshot()
}
