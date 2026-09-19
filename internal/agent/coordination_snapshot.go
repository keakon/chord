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
	// Per-list cap for the completion lists a task contributes to the
	// coordination snapshot. One task's reported files/commands/limitations are
	// only runtime hints on top of the terminal state, so a small bound keeps a
	// single verbose completion from dominating the overlay token budget under
	// the 8-task ceiling; the truncated tail stays visible through the
	// "...N more" hint.
	coordinationSnapshotMaxListItems    = 3
	coordinationSnapshotStallAfter      = 10 * time.Minute
	coordinationSnapshotRecentTaskTurns = uint64(1)
	// subAgentToolHeartbeatInterval is how often the activity heartbeat is
	// refreshed while a SubAgent tool call executes. A single long-running
	// tool (a slow build, a full test run) can exceed the stall threshold on
	// its own; the interval stays far under coordinationSnapshotStallAfter so
	// the watchdog never sees a stale heartbeat mid-tool.
	subAgentToolHeartbeatInterval = coordinationSnapshotStallAfter / 6
	// SubAgentStallResolvedSubtype marks the risk_alert that closes a stall
	// episode whose earlier alert the owner was shown; the TUI renders it
	// distinctly from the still-active AGENT BLOCKED alert.
	SubAgentStallResolvedSubtype = "stall_resolved"
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

// joinCoordinationSnapshotItems joins a reported completion list for the
// coordination snapshot under the per-list length cap, mirroring the style of
// the rune-truncated summary: the retained head is joined verbatim and an
// explicit "...N more" hint reports how many items were cut, so the reader
// still knows the list continues past the cap.
func joinCoordinationSnapshotItems(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= coordinationSnapshotMaxListItems {
		return strings.Join(items, ", ")
	}
	joined := strings.Join(items[:coordinationSnapshotMaxListItems], ", ")
	return joined + fmt.Sprintf(", ...%d more", len(items)-coordinationSnapshotMaxListItems)
}

// completionAlreadyDeliveredByMailbox reports whether a terminal completion's
// fact is already expressed in the current request by its own completed
// mailbox text. When the mailbox that recorded the completion is already part
// of the request (delivered as a pending batch now or durable in the
// conversation), re-listing the task in the coordination snapshot would bill
// the same completion fact to the model twice. Non-terminal tasks are never
// elided here: their snapshot entry is ongoing coordination state, not a
// delivered mailbox fact.
func completionAlreadyDeliveredByMailbox(rec *DurableTaskRecord, injectedMailboxIDs map[string]struct{}) bool {
	if len(injectedMailboxIDs) == 0 {
		return false
	}
	if rec == nil || strings.TrimSpace(rec.State) != string(SubAgentStateCompleted) {
		return false
	}
	mailboxID := strings.TrimSpace(rec.LastMailboxID)
	if mailboxID == "" {
		return false
	}
	_, ok := injectedMailboxIDs[mailboxID]
	return ok
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
	if strings.TrimSpace(rec.State) == string(SubAgentStateCompleted) && rec.ResumePolicy == taskResumePolicyNotify && rec.LastUpdatedTurn+coordinationSnapshotRecentTaskTurns >= currentTurn {
		return true
	}
	return false
}

// buildCoordinationSnapshotOverlay formats the relevant task records without a
// request context (no mailbox-dedupe input, stall markers not refreshed here).
// It exists for callers that render the snapshot outside request assembly
// (tests); the production request path uses
// buildCoordinationSnapshotOverlayForRequest.
func (a *MainAgent) buildCoordinationSnapshotOverlay() string {
	return a.buildCoordinationSnapshotOverlayForRequest(nil)
}

// buildCoordinationSnapshotOverlayForRequest formats the relevant task records
// for the request being assembled. It is deliberately side-effect free: stall
// markers are refreshed by the caller at the request-dispatch boundary
// (buildTurnOverlayMessages) before this formatter runs, and
// injectedMailboxIDs carries the mailbox message IDs already part of the
// request's context so a terminal completion whose mailbox text is in the same
// request is not listed a second time.
func (a *MainAgent) buildCoordinationSnapshotOverlayForRequest(injectedMailboxIDs map[string]struct{}) string {
	if a == nil {
		return ""
	}
	currentTurn := a.explicitUserTurnCount.Load()
	a.subs.mu.RLock()
	records := make([]*DurableTaskRecord, 0, len(a.subs.taskRecords))
	for _, rec := range a.subs.taskRecords {
		if clone := cloneDurableTaskRecord(rec); clone != nil && isRelevantCoordinationTask(clone, currentTurn) {
			if completionAlreadyDeliveredByMailbox(clone, injectedMailboxIDs) {
				continue
			}
			records = append(records, clone)
		}
	}
	a.subs.mu.RUnlock()
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
			b.WriteString(" agent_type: ")
			b.WriteString(rec.AgentDefName)
		}
		if strings.TrimSpace(rec.LatestInstanceID) != "" {
			b.WriteString(" agent_id: ")
			b.WriteString(rec.LatestInstanceID)
		}
		if strings.TrimSpace(rec.State) != "" {
			b.WriteString(" state: ")
			b.WriteString(rec.State)
		}
		if strings.TrimSpace(rec.OwnerAgentID) != "" {
			b.WriteString(" owner_agent_id: ")
			b.WriteString(rec.OwnerAgentID)
		}
		if strings.TrimSpace(rec.OwnerTaskID) != "" {
			b.WriteString(" owner_task_id: ")
			b.WriteString(rec.OwnerTaskID)
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
			if rec.LastCompletion.ResultType != "" {
				b.WriteString("\n  result_type: ")
				b.WriteString(rec.LastCompletion.ResultType)
			}
			if rec.LastCompletion.ResultRef != nil {
				b.WriteString("\n  result_ref: ")
				b.WriteString(rec.LastCompletion.ResultRef.RelPath)
			}
			if len(rec.LastCompletion.FilesChanged) > 0 {
				b.WriteString("\n  files_changed: ")
				b.WriteString(joinCoordinationSnapshotItems(rec.LastCompletion.FilesChanged))
			}
			if len(rec.LastCompletion.RemainingLimitations) > 0 {
				b.WriteString("\n  remaining_limitations: ")
				b.WriteString(joinCoordinationSnapshotItems(rec.LastCompletion.RemainingLimitations))
			}
			if len(rec.LastCompletion.KnownRisks) > 0 {
				b.WriteString("\n  known_risks: ")
				b.WriteString(joinCoordinationSnapshotItems(rec.LastCompletion.KnownRisks))
			}
		}
		refs := tools.NormalizeArtifactRefs(rec.LastArtifactRefs)
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

// runningSubAgentStallReason reports why a live Running worker is suspected of
// stalling, or "" while it is healthy. It compares the worker's activity
// heartbeat — refreshed on state transitions AND real progress (LLM request
// issue, stream deltas, tool results, response handling) — against the wall
// clock, so a busy worker that merely stays in Running is never flagged, while
// one that has produced no state change and no activity for the full threshold
// is. Only slot holders are coordination-tracked work.
func runningSubAgentStallReason(sub *SubAgent, now time.Time) string {
	if sub == nil {
		return ""
	}
	if held, _ := sub.slotState(); !held {
		return ""
	}
	// A worker sleeping out an API key cooldown is silent by design: the wait
	// has a known end and the request resumes on its own, so reporting it as a
	// suspected stall would send the owner chasing healthy work.
	if !sub.llmCoolingWaitDeadline().IsZero() {
		return ""
	}
	if now.Sub(sub.StateChangedAt()) > coordinationSnapshotStallAfter {
		return "running with no recent state/progress update"
	}
	return ""
}

func formatWriteScope(scope tools.WriteScope) string {
	scope = scope.Normalized()
	if scope.Empty() {
		return ""
	}
	var parts []string
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

// updateSubAgentStallMarkers recomputes the SuspectedStallReason stored on each
// coordination task record from the live worker's current state and activity
// heartbeat. It is the only writer of that marker and is invoked at the main
// request-dispatch boundary (buildTurnOverlayMessages) so the coordination
// snapshot that reads the marker for relevance and rendering always sees a
// fresh evaluation; buildCoordinationSnapshotOverlay itself stays read-only.
func (a *MainAgent) updateSubAgentStallMarkers() {
	if a == nil {
		return
	}
	now := time.Now()
	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	for taskID, rec := range a.subs.taskRecords {
		if rec == nil {
			continue
		}
		reason := ""
		if sub := a.subs.subAgents[rec.LatestInstanceID]; sub != nil {
			state := sub.State()
			switch state {
			case SubAgentStateWaitingMain:
				reason = ""
			case SubAgentStateWaitingDescendant:
				if len(a.outstandingJoinChildTaskIDsLocked(sub.taskID)) == 0 && now.Sub(sub.StateChangedAt()) > coordinationSnapshotStallAfter {
					reason = "waiting_descendant without active child progress"
				}
			case SubAgentStateRunning:
				reason = runningSubAgentStallReason(sub, now)
			}
		}
		if rec.SuspectedStallReason != reason {
			next := cloneDurableTaskRecord(rec)
			next.SuspectedStallReason = reason
			next.UpdatedAt = now
			a.subs.taskRecords[taskID] = next
		}
	}
}
