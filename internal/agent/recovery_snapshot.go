package agent

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func subSelectedModelRef(sub *SubAgent) string {
	client, _ := sub.llmSnapshot()
	if client == nil {
		return ""
	}
	ref := strings.TrimSpace(client.PrimaryModelRef())
	if variant := strings.TrimSpace(client.ActiveVariant()); variant != "" && ref != "" {
		ref += "@" + variant
	}
	return ref
}

func subRunningModelRef(sub *SubAgent) string {
	client, _ := sub.llmSnapshot()
	if client == nil {
		return ""
	}
	return formatModelRefForNotification(client.RunningModelRef(), subSelectedModelRef(sub), client.ActiveVariant())
}

func (a *MainAgent) buildRecoverySnapshot() *recovery.SessionSnapshot {
	a.todoMu.RLock()
	todoStates := snapshotTodos(a.todoItems)
	a.todoMu.RUnlock()

	a.subs.mu.RLock()
	agents := make([]recovery.AgentSnapshot, 0, len(a.subs.subAgents))
	for _, sub := range a.subs.subAgents {
		state := sub.State()
		summary := sub.LastSummary()
		pendingComplete := sub.PendingCompleteIntent()
		snap := recovery.AgentSnapshot{
			InstanceID:            sub.instanceID,
			TaskID:                sub.taskID,
			AgentDefName:          sub.agentDefName,
			TaskDesc:              sub.taskDesc,
			PlanTaskRef:           sub.planTaskRef,
			SemanticTaskKey:       sub.semanticTaskKey,
			ExpectedWriteScope:    sub.currentWriteScope(),
			SelectedModelRef:      subSelectedModelRef(sub),
			RunningModelRef:       subRunningModelRef(sub),
			OwnerAgentID:          sub.OwnerAgentID(),
			OwnerTaskID:           sub.OwnerTaskID(),
			Depth:                 sub.Depth(),
			JoinToOwner:           sub.JoinToOwner(),
			State:                 string(state),
			LastSummary:           summary,
			WorkDir:               sub.effectiveToolBaseDir(),
			WorkDirGeneration:     sub.workDirState.load().Generation,
			PendingCompleteIntent: pendingComplete != nil,
		}
		if pendingComplete != nil {
			snap.PendingCompleteSummary = pendingComplete.Summary
			snap.PendingCompleteEnvelope = marshalCompletionEnvelope(pendingComplete.Envelope)
		}
		persistence := sub.PersistenceHealth()
		snap.Persistence.State = string(persistence.State)
		snap.Persistence.LastError = persistence.LastError
		snap.Persistence.FailedAt = persistence.FailedAt
		snap.Persistence.RecoveredAt = persistence.RecoveredAt
		agents = append(agents, snap)
	}
	a.subs.mu.RUnlock()

	modelPoolCurrentModelPool, modelPoolAgentOverrides := a.snapshotModelPoolState()
	return &recovery.SessionSnapshot{
		Todos:                                todoStates,
		ActiveAgents:                         agents,
		ModelName:                            a.ModelName(),
		ActiveRole:                           a.CurrentRole(),
		ModelPoolCurrentModelPool:            modelPoolCurrentModelPool,
		ModelPoolAgentOverrides:              modelPoolAgentOverrides,
		CreatedAt:                            time.Now(),
		LastInputTokens:                      a.ctxMgr.LastInputTokens(),
		LastTotalContextTokens:               a.ctxMgr.LastTotalContextTokens(),
		CompactionGeneration:                 a.nextCompactionPlanID,
		LastHistoryIndex:                     nextHistoryIndexMinusOne(a.sessionDir),
		SessionEpoch:                         a.sessionEpoch,
		ActiveBackgroundObjects:              jobStatesForSnapshot(),
		PendingCompactionResume:              a.snapshotPendingCompactionResume(),
		LastModelDrivenApplyBatch:            a.lastModelDrivenApplyBatch,
		LastModelDrivenCheckpointFingerprint: a.lastModelDrivenCheckpointFingerprint,
		AutoCompactRequestGeneration:         a.autoCompactRequestGeneration.Load(),
		ModelDrivenProposal:                  a.modelDrivenProposalSnapshot(),
		StageCompletionCandidateTurnID:       a.stageCompletionCandidateTurnID,
		StageCompletionCandidatePending:      a.stageCompletionCandidatePending,
	}
}

// modelDrivenProposalSnapshot renders the proposal lifecycle record for the
// recovery snapshot. The snapshot carries exactly the same object a restore
// reads back, so an accepted/preparing proposal that a crash interrupted is
// recovered as a not-applied record instead of being lost or mistaken for an
// applied reset.
func (a *MainAgent) modelDrivenProposalSnapshot() *recovery.ModelDrivenProposalSnapshot {
	if a == nil || a.modelDrivenProposal.isEmpty() {
		return nil
	}
	return &recovery.ModelDrivenProposalSnapshot{
		RequestID: a.modelDrivenProposal.requestID,
		Status:    a.modelDrivenProposal.status,
		ArgsJSON:  a.modelDrivenProposal.argsJSON,
		Reason:    a.modelDrivenProposal.reason,
		UpdatedAt: a.modelDrivenProposal.updatedAt,
	}
}
