package agent

import "github.com/keakon/golog/log"

func (a *MainAgent) scheduleCompactionForLengthRecovery() bool {
	if a == nil || a.turn == nil {
		return false
	}
	if a.turn.LengthRecoveryAutoCompactAttempted {
		return false
	}
	if a.IsCompactionRunning() {
		return false
	}
	a.turn.LengthRecoveryAutoCompactAttempted = true
	snapshot := a.ctxMgr.Snapshot()
	a.fireBeforeCompressHook(snapshot, false)
	planID, target := a.nextCompactionPlan()
	target.turnID = a.turn.ID
	target.turnEpoch = a.turn.Epoch
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, compactionTrigger{LengthRecovery: true}, continuationPlan{
		kind:             compactionResumeLengthRecovery,
		turnID:           a.turn.ID,
		turnEpoch:        a.turn.Epoch,
		agentErrSourceID: "",
	}, false)
	return true
}

func (a *MainAgent) ensureOversizeDrivenCompaction() bool {
	if a == nil || a.turn == nil || a.ctxMgr == nil {
		return false
	}
	if !a.ctxMgr.IsAutoCompactEnabled() || a.IsCompactionRunning() {
		return false
	}
	if a.turn.OversizeRecoveryCount >= maxOversizeRecoveryAttempts {
		log.Warnf("oversize-driven compaction attempt limit reached turn_id=%v attempts=%v", a.turn.ID, a.turn.OversizeRecoveryCount)
		return false
	}
	a.turn.OversizeRecoveryCount++
	a.armOversizeAutoContinueResume()
	snapshot := a.ctxMgr.Snapshot()
	a.fireBeforeCompressHook(snapshot, false)
	planID, target := a.nextCompactionPlan()
	target.turnID = a.turn.ID
	target.turnEpoch = a.turn.Epoch
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, compactionTrigger{OversizeDriven: true}, continuationPlan{
		kind:             compactionResumeMainLLM,
		turnID:           a.turn.ID,
		turnEpoch:        a.turn.Epoch,
		agentErrSourceID: "",
	}, false)
	a.emitToTUI(ToastEvent{Message: "Current context exceeds all available models; compacting context before retry", Level: "warn"})
	return true
}
