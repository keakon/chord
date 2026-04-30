package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

const (
	coordinationSnapshotMaxTasks        = 8
	coordinationSnapshotSummaryMaxRunes = 160
	coordinationSnapshotStallAfter      = 10 * time.Minute
	coordinationSnapshotRecentTaskTurns = uint64(1)
)

func truncateCoordinationSnapshotText(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func isRelevantCoordinationTask(rec *DurableTaskRecord, currentTurn uint64) bool {
	if rec == nil || strings.TrimSpace(rec.TaskID) == "" {
		return false
	}
	if isNonTerminalTaskState(rec.State) {
		return true
	}
	if strings.TrimSpace(rec.SuspectedStallReason) != "" {
		return true
	}
	if strings.TrimSpace(rec.LastMailboxID) != "" && rec.LastUpdatedTurn+coordinationSnapshotRecentTaskTurns >= currentTurn {
		return true
	}
	if strings.TrimSpace(rec.State) == string(SubAgentStateCompleted) && rec.ResumePolicy == taskResumePolicyCompletedFollowUpOnly && rec.LastUpdatedTurn+coordinationSnapshotRecentTaskTurns >= currentTurn {
		return true
	}
	return false
}

func (a *MainAgent) buildCoordinationSnapshotOverlay() string {
	if a == nil {
		return ""
	}
	a.updateSubAgentStallMarkers()

	a.mu.RLock()
	records := make([]*DurableTaskRecord, 0, len(a.taskRecords))
	for _, rec := range a.taskRecords {
		if clone := cloneDurableTaskRecord(rec); clone != nil && isRelevantCoordinationTask(clone, a.explicitUserTurnCount) {
			records = append(records, clone)
		}
	}
	currentTurn := a.explicitUserTurnCount
	a.mu.RUnlock()
	if len(records) == 0 {
		return ""
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].LastUpdatedTurn != records[j].LastUpdatedTurn {
			return records[i].LastUpdatedTurn > records[j].LastUpdatedTurn
		}
		return records[i].TaskID < records[j].TaskID
	})
	if len(records) > coordinationSnapshotMaxTasks {
		records = records[:coordinationSnapshotMaxTasks]
	}

	var b strings.Builder
	b.WriteString("SubAgent coordination snapshot (runtime state; use for orchestration, do not expose internal ids unless needed):")
	for _, rec := range records {
		b.WriteString("\n- task_id: ")
		b.WriteString(rec.TaskID)
		if strings.TrimSpace(rec.AgentDefName) != "" {
			b.WriteString(" agent: ")
			b.WriteString(rec.AgentDefName)
		}
		if strings.TrimSpace(rec.LatestInstanceID) != "" {
			b.WriteString(" instance: ")
			b.WriteString(rec.LatestInstanceID)
		}
		if strings.TrimSpace(rec.State) != "" {
			b.WriteString(" state: ")
			b.WriteString(rec.State)
		}
		if strings.TrimSpace(rec.OwnerTaskID) != "" || strings.TrimSpace(rec.OwnerAgentID) != "" {
			b.WriteString(" owner:")
			if strings.TrimSpace(rec.OwnerAgentID) != "" {
				b.WriteString(" agent=")
				b.WriteString(rec.OwnerAgentID)
			}
			if strings.TrimSpace(rec.OwnerTaskID) != "" {
				b.WriteString(" task=")
				b.WriteString(rec.OwnerTaskID)
			}
		}
		if strings.TrimSpace(rec.PlanTaskRef) != "" {
			b.WriteString(" plan_ref: ")
			b.WriteString(rec.PlanTaskRef)
		}
		if strings.TrimSpace(rec.SemanticTaskKey) != "" {
			b.WriteString(" semantic_key: ")
			b.WriteString(rec.SemanticTaskKey)
		}
		if !rec.ExpectedWriteScope.Empty() {
			b.WriteString(" write_scope: ")
			b.WriteString(formatWriteScope(rec.ExpectedWriteScope))
		}
		if strings.TrimSpace(rec.LastSummary) != "" {
			b.WriteString("\n  summary: ")
			b.WriteString(truncateCoordinationSnapshotText(rec.LastSummary, coordinationSnapshotSummaryMaxRunes))
		}
		if rec.LastCompletion != nil {
			if len(rec.LastCompletion.FilesChanged) > 0 {
				b.WriteString("\n  files_changed: ")
				b.WriteString(strings.Join(rec.LastCompletion.FilesChanged, ", "))
			}
			if len(rec.LastCompletion.VerificationRun) > 0 {
				b.WriteString("\n  verification_run: ")
				b.WriteString(strings.Join(rec.LastCompletion.VerificationRun, ", "))
			}
			if len(rec.LastCompletion.RemainingLimitations) > 0 {
				b.WriteString("\n  remaining_limitations: ")
				b.WriteString(strings.Join(rec.LastCompletion.RemainingLimitations, ", "))
			}
			if len(rec.LastCompletion.KnownRisks) > 0 {
				b.WriteString("\n  known_risks: ")
				b.WriteString(strings.Join(rec.LastCompletion.KnownRisks, ", "))
			}
		}
		refs := mergeArtifactRefs(rec.LastArtifactRefs, artifactRefsFromLegacy([]string{rec.LastArtifactID}, []string{rec.LastArtifactRelPath}, rec.LastArtifactType))
		if len(refs) > 0 {
			parts := make([]string, 0, len(refs))
			for _, ref := range refs {
				ref = tools.NormalizeArtifactRef(ref)
				label := ref.RelPath
				if label == "" {
					label = ref.ID
				}
				if label == "" {
					continue
				}
				if ref.Type != "" {
					label = fmt.Sprintf("%s(%s)", label, ref.Type)
				}
				parts = append(parts, label)
			}
			if len(parts) > 0 {
				b.WriteString("\n  artifact_refs: ")
				b.WriteString(strings.Join(parts, ", "))
			}
		}
		if strings.TrimSpace(rec.SuspectedStallReason) != "" {
			b.WriteString("\n  suspected_stall: ")
			b.WriteString(rec.SuspectedStallReason)
		}
		if rec.LastUpdatedTurn != 0 || rec.CreatedTurn != 0 {
			b.WriteString(fmt.Sprintf("\n  turns: created=%d updated=%d current=%d", rec.CreatedTurn, rec.LastUpdatedTurn, currentTurn))
		}
	}
	return b.String()
}

func formatWriteScope(scope tools.WriteScope) string {
	scope = scope.Normalized()
	if scope.Empty() {
		return ""
	}
	var parts []string
	if scope.ReadOnly {
		parts = append(parts, "read_only")
	}
	for _, item := range scope.Files {
		parts = append(parts, "file:"+item)
	}
	for _, item := range scope.PathPrefix {
		parts = append(parts, "path:"+item)
	}
	for _, item := range scope.Modules {
		parts = append(parts, "module:"+item)
	}
	return strings.Join(parts, ",")
}

func (a *MainAgent) updateSubAgentStallMarkers() {
	if a == nil {
		return
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for taskID, rec := range a.taskRecords {
		if rec == nil {
			continue
		}
		reason := ""
		if sub := a.subAgents[rec.LatestInstanceID]; sub != nil {
			state := sub.State()
			switch state {
			case SubAgentStateWaitingPrimary:
				reason = ""
			case SubAgentStateWaitingDescendant:
				if len(a.outstandingJoinChildTaskIDsLocked(sub.taskID)) == 0 && now.Sub(sub.StateChangedAt()) > coordinationSnapshotStallAfter {
					reason = "waiting_descendant without active child progress"
				}
			case SubAgentStateRunning:
				if sub.semHeld && now.Sub(sub.StateChangedAt()) > coordinationSnapshotStallAfter {
					reason = "running with no recent state/progress update"
				}
			}
		}
		if rec.SuspectedStallReason != reason {
			next := cloneDurableTaskRecord(rec)
			next.SuspectedStallReason = reason
			next.UpdatedAt = now
			a.taskRecords[taskID] = next
		}
	}
}
