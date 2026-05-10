package agent

import (
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func (a *MainAgent) buildRecoverySnapshot() *recovery.SessionSnapshot {
	a.todoMu.RLock()
	todoStates := snapshotTodos(a.todoItems)
	a.todoMu.RUnlock()

	a.mu.RLock()
	agents := make([]recovery.AgentSnapshot, 0, len(a.subAgents))
	for _, sub := range a.subAgents {
		state := sub.State()
		summary := sub.LastSummary()
		pendingComplete := sub.PendingCompleteIntent()
		snap := recovery.AgentSnapshot{
			InstanceID:            sub.instanceID,
			TaskID:                sub.taskID,
			AgentDefName:          sub.agentDefName,
			TaskDesc:              sub.taskDesc,
			OwnerAgentID:          sub.OwnerAgentID(),
			OwnerTaskID:           sub.OwnerTaskID(),
			Depth:                 sub.Depth(),
			JoinToOwner:           sub.joinToOwner,
			State:                 string(state),
			LastSummary:           summary,
			PendingCompleteIntent: pendingComplete != nil,
		}
		if pendingComplete != nil {
			snap.PendingCompleteSummary = pendingComplete.Summary
			snap.PendingCompleteEnvelope = marshalCompletionEnvelope(pendingComplete.Envelope)
		}
		agents = append(agents, snap)
	}
	a.mu.RUnlock()

	modelPoolCurrentModelPool, modelPoolAgentOverrides := a.snapshotModelPoolState()
	usageSnap := a.usageTracker.SessionStats()
	return &recovery.SessionSnapshot{
		Todos:                     todoStates,
		ActiveAgents:              agents,
		ModelName:                 a.ModelName(),
		ActiveRole:                a.CurrentRole(),
		ModelPoolCurrentModelPool: modelPoolCurrentModelPool,
		ModelPoolAgentOverrides:   modelPoolAgentOverrides,
		CreatedAt:                 time.Now(),
		LastInputTokens:           a.ctxMgr.LastInputTokens(),
		LastTotalContextTokens:    a.ctxMgr.LastTotalContextTokens(),
		CompactionGeneration:      a.nextCompactionPlanID,
		LastHistoryIndex:          nextHistoryIndexMinusOne(a.sessionDir),
		SessionEpoch:              a.sessionEpoch,
		ActiveBackgroundObjects:   spawnStatesForSnapshot(),
		PendingCompactionResume:   a.snapshotPendingCompactionResume(),
		UsageInputTokens:          usageSnap.InputTokens,
		UsageOutputTokens:         usageSnap.OutputTokens,
		UsageCacheReadTokens:      usageSnap.CacheReadTokens,
		UsageCacheWriteTokens:     usageSnap.CacheWriteTokens,
		UsageReasoningTokens:      usageSnap.ReasoningTokens,
		UsageLLMCalls:             usageSnap.LLMCalls,
		UsageEstimatedCost:        usageSnap.EstimatedCost,
		UsageByModel:              usageSnap.ByModel,
		UsageByAgent:              usageSnap.ByAgent,
	}
}
