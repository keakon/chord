package agent

import (
	"encoding/json"
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
		Todos:                        todoStates,
		ActiveAgents:                 agents,
		ModelName:                    a.ModelName(),
		ActiveRole:                   a.CurrentRole(),
		ModelPoolCurrentModelPool:    modelPoolCurrentModelPool,
		ModelPoolAgentOverrides:      modelPoolAgentOverrides,
		CreatedAt:                    time.Now(),
		LastInputTokens:              a.ctxMgr.LastInputTokens(),
		LastTotalContextTokens:       a.ctxMgr.LastTotalContextTokens(),
		CompactionGeneration:         a.nextCompactionPlanID,
		LastHistoryIndex:             nextHistoryIndexMinusOne(a.sessionDir),
		SessionEpoch:                 a.sessionEpoch,
		ActiveBackgroundObjects:      spawnStatesForSnapshot(),
		PendingCompactionResume:      a.snapshotPendingCompactionResume(),
		LastModelDrivenApplyBatch:    a.lastModelDrivenApplyBatch,
		AutoCompactRequestGeneration: a.autoCompactRequestGeneration.Load(),
		PendingModelDrivenRequestID:  a.pendingModelDrivenRequestID,
		LastModelDrivenRequestID:     a.lastModelDrivenRequestID,
		PendingModelDrivenStatus:     a.pendingModelDrivenStatus,
		PendingModelDrivenArgsJSON:   a.pendingModelDrivenAuditArgsSnapshot(),
		ModelDrivenProposalReason:    a.modelDrivenProposalReason,
		ModelDrivenProposalUpdatedAt: a.modelDrivenProposalUpdatedAt,
		ModelDrivenProposal: &recovery.ModelDrivenProposalSnapshot{
			RequestID: a.pendingModelDrivenRequestID,
			Status:    a.pendingModelDrivenStatus,
			ArgsJSON:  a.pendingModelDrivenAuditArgsSnapshot(),
			Reason:    a.modelDrivenProposalReason,
			UpdatedAt: a.modelDrivenProposalUpdatedAt,
		},
		StageCompletionCandidateTurnID:  a.stageCompletionCandidateTurnID,
		StageCompletionCandidatePending: a.stageCompletionCandidatePending,
	}
}

func (a *MainAgent) pendingModelDrivenAuditArgsSnapshot() string {
	if a == nil || a.pendingModelDriven == nil {
		return ""
	}
	data, err := json.Marshal(a.pendingModelDriven.Args)
	if err != nil {
		return ""
	}
	return string(data)
}
