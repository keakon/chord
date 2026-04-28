package agent

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
