package agent

import (
	"strings"
	"time"
)

func (a *MainAgent) syncTaskRecordFromMailbox(msg SubAgentMailboxMessage) {
	if a == nil {
		return
	}
	taskID := strings.TrimSpace(msg.TaskID)
	if taskID == "" {
		return
	}
	now := time.Now()
	currentTurn := a.explicitUserTurnCount.Load()
	a.subs.mu.Lock()
	if a.subs.taskRecords == nil {
		a.subs.taskRecords = make(map[string]*DurableTaskRecord)
	}
	rec := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	if rec == nil {
		rec = &DurableTaskRecord{TaskID: taskID, CreatedAt: now, CreatedTurn: currentTurn}
	}
	if strings.TrimSpace(msg.AgentID) != "" {
		rec.LatestInstanceID = strings.TrimSpace(msg.AgentID)
		rec.InstanceHistory = append(rec.InstanceHistory, rec.LatestInstanceID)
		rec.InstanceHistory = dedupeTaskInstanceHistory(rec.InstanceHistory)
	}
	if strings.TrimSpace(msg.OwnerAgentID) != "" {
		rec.OwnerAgentID = strings.TrimSpace(msg.OwnerAgentID)
	}
	if strings.TrimSpace(msg.OwnerTaskID) != "" {
		rec.OwnerTaskID = strings.TrimSpace(msg.OwnerTaskID)
	}
	if strings.TrimSpace(msg.Summary) != "" {
		rec.LastSummary = strings.TrimSpace(msg.Summary)
	}
	if strings.TrimSpace(msg.MessageID) != "" {
		rec.LastMailboxID = strings.TrimSpace(msg.MessageID)
	}
	if msg.Completion != nil {
		rec.LastCompletion = normalizeCompletionEnvelope(msg.Completion)
		if rec.LastCompletion != nil {
			rec.LastArtifactRefs = mergeArtifactRefs(rec.LastArtifactRefs, rec.LastCompletion.Artifacts)
		}
	}
	switch msg.Kind {
	case SubAgentMailboxKindCompleted:
		rec.State = string(SubAgentStateCompleted)
		rec.ResumePolicy = taskResumePolicyNotify
	case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired:
		rec.State = string(SubAgentStateWaitingMain)
		rec.ResumePolicy = taskResumePolicyNotify
	case SubAgentMailboxKindProgress:
		if rec.State == "" {
			rec.State = string(SubAgentStateRunning)
			rec.ResumePolicy = taskResumePolicyLiveOnly
		}
	}
	rec.LastUpdatedTurn = currentTurn
	rec.UpdatedAt = now
	if live := a.subs.subAgents[rec.LatestInstanceID]; live == nil {
		rec.RuntimeParked = true
	}
	a.subs.taskRecords[taskID] = rec
	a.subs.mu.Unlock()
	a.persistTaskRegistry()
}
