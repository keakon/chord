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
	a.mu.Lock()
	if a.taskRecords == nil {
		a.taskRecords = make(map[string]*DurableTaskRecord)
	}
	rec := cloneDurableTaskRecord(a.taskRecords[taskID])
	if rec == nil {
		rec = &DurableTaskRecord{TaskID: taskID, CreatedAt: now, CreatedTurn: a.explicitUserTurnCount}
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
	legacyRefs := artifactRefsFromLegacy(msg.ArtifactIDs, msg.ArtifactRelPaths, msg.ArtifactType)
	rec.LastArtifactRefs = mergeArtifactRefs(rec.LastArtifactRefs, legacyRefs)
	if len(rec.LastArtifactRefs) > 0 {
		first := rec.LastArtifactRefs[0]
		rec.LastArtifactID = first.ID
		rec.LastArtifactRelPath = first.RelPath
		rec.LastArtifactType = first.Type
	}
	switch msg.Kind {
	case SubAgentMailboxKindCompleted:
		rec.State = string(SubAgentStateCompleted)
		rec.ResumePolicy = taskResumePolicyCompletedFollowUpOnly
	case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired:
		rec.State = string(SubAgentStateWaitingPrimary)
		rec.ResumePolicy = taskResumePolicyLiveOnly
	case SubAgentMailboxKindProgress:
		if rec.State == "" {
			rec.State = string(SubAgentStateRunning)
			rec.ResumePolicy = taskResumePolicyLiveOnly
		}
	}
	rec.LastUpdatedTurn = a.explicitUserTurnCount
	rec.UpdatedAt = now
	a.taskRecords[taskID] = rec
	a.mu.Unlock()
	a.persistTaskRegistry()
}
