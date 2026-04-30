package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	stdpath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	taskResumePolicyLiveOnly              = "live_only"
	taskResumePolicyCompletedFollowUpOnly = "completed_followup_only"
	taskResumePolicyDisallowed            = "disallowed"
)

type DurableTaskRecord struct {
	TaskID               string              `json:"task_id"`
	AgentDefName         string              `json:"agent_def_name,omitempty"`
	TaskDesc             string              `json:"task_desc,omitempty"`
	PlanTaskRef          string              `json:"plan_task_ref,omitempty"`
	SemanticTaskKey      string              `json:"semantic_task_key,omitempty"`
	ExpectedWriteScope   tools.WriteScope    `json:"expected_write_scope,omitempty"`
	OwnerAgentID         string              `json:"owner_agent_id,omitempty"`
	OwnerTaskID          string              `json:"owner_task_id,omitempty"`
	Depth                int                 `json:"depth,omitempty"`
	JoinToOwner          bool                `json:"join_to_owner,omitempty"`
	State                string              `json:"state,omitempty"`
	ResumePolicy         string              `json:"resume_policy,omitempty"`
	LatestInstanceID     string              `json:"latest_instance_id,omitempty"`
	InstanceHistory      []string            `json:"instance_history,omitempty"`
	LastSummary          string              `json:"last_summary,omitempty"`
	LastMailboxID        string              `json:"last_mailbox_id,omitempty"`
	LastReplyMessageID   string              `json:"last_reply_message_id,omitempty"`
	LastReplyToMailboxID string              `json:"last_reply_to_mailbox_id,omitempty"`
	LastReplyKind        string              `json:"last_reply_kind,omitempty"`
	LastReplySummary     string              `json:"last_reply_summary,omitempty"`
	LastArtifactRefs     []tools.ArtifactRef `json:"last_artifact_refs,omitempty"`
	LastCompletion       *CompletionEnvelope `json:"last_completion,omitempty"`
	SuspectedStallReason string              `json:"suspected_stall_reason,omitempty"`
	CreatedTurn          uint64              `json:"created_turn,omitempty"`
	LastUpdatedTurn      uint64              `json:"last_updated_turn,omitempty"`
	CreatedAt            time.Time           `json:"created_at,omitempty"`
	UpdatedAt            time.Time           `json:"updated_at,omitempty"`
	ClosedReason         string              `json:"closed_reason,omitempty"`
}

func durableTaskRegistryPath(sessionDir string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", "tasks.json")
}

func cloneDurableTaskRecord(in *DurableTaskRecord) *DurableTaskRecord {
	if in == nil {
		return nil
	}
	out := *in
	out.TaskID = strings.TrimSpace(out.TaskID)
	out.AgentDefName = strings.TrimSpace(out.AgentDefName)
	out.TaskDesc = strings.TrimSpace(out.TaskDesc)
	out.PlanTaskRef = strings.TrimSpace(out.PlanTaskRef)
	out.SemanticTaskKey = strings.TrimSpace(out.SemanticTaskKey)
	out.ExpectedWriteScope = out.ExpectedWriteScope.Normalized()
	out.OwnerAgentID = strings.TrimSpace(out.OwnerAgentID)
	out.OwnerTaskID = strings.TrimSpace(out.OwnerTaskID)
	out.State = strings.TrimSpace(out.State)
	out.ResumePolicy = strings.TrimSpace(out.ResumePolicy)
	out.LatestInstanceID = strings.TrimSpace(out.LatestInstanceID)
	out.LastSummary = strings.TrimSpace(out.LastSummary)
	out.LastMailboxID = strings.TrimSpace(out.LastMailboxID)
	out.LastReplyMessageID = strings.TrimSpace(out.LastReplyMessageID)
	out.LastReplyToMailboxID = strings.TrimSpace(out.LastReplyToMailboxID)
	out.LastReplyKind = strings.TrimSpace(out.LastReplyKind)
	out.LastReplySummary = strings.TrimSpace(out.LastReplySummary)
	out.LastArtifactRefs = tools.NormalizeArtifactRefs(out.LastArtifactRefs)
	out.LastCompletion = normalizeCompletionEnvelope(out.LastCompletion)
	out.SuspectedStallReason = strings.TrimSpace(out.SuspectedStallReason)
	out.ClosedReason = strings.TrimSpace(out.ClosedReason)
	out.InstanceHistory = dedupeTaskInstanceHistory(out.InstanceHistory)
	return &out
}

func dedupeTaskInstanceHistory(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func cloneDurableTaskRecordMap(in map[string]*DurableTaskRecord) map[string]*DurableTaskRecord {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*DurableTaskRecord, len(in))
	for taskID, rec := range in {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" || rec == nil {
			continue
		}
		out[taskID] = cloneDurableTaskRecord(rec)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func loadDurableTaskRecords(sessionDir string) (map[string]*DurableTaskRecord, error) {
	path := durableTaskRegistryPath(sessionDir)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []*DurableTaskRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	out := make(map[string]*DurableTaskRecord, len(records))
	for _, rec := range records {
		rec = cloneDurableTaskRecord(rec)
		if rec == nil || rec.TaskID == "" {
			continue
		}
		out[rec.TaskID] = rec
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func persistDurableTaskRecords(sessionDir string, records map[string]*DurableTaskRecord) error {
	path := durableTaskRegistryPath(sessionDir)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ordered := make([]*DurableTaskRecord, 0, len(records))
	for _, rec := range records {
		rec = cloneDurableTaskRecord(rec)
		if rec == nil || rec.TaskID == "" {
			continue
		}
		ordered = append(ordered, rec)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].TaskID < ordered[j].TaskID
	})
	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(filepath.Dir(path), fmt.Sprintf("tasks.%d.json.tmp", time.Now().UnixNano()))
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func durableTaskResumePolicy(state SubAgentState) string {
	switch state {
	case SubAgentStateCompleted:
		return taskResumePolicyCompletedFollowUpOnly
	case SubAgentStateFailed, SubAgentStateCancelled:
		return taskResumePolicyDisallowed
	default:
		return taskResumePolicyLiveOnly
	}
}

func shouldRestoreLiveSubAgentState(state SubAgentState) bool {
	switch state {
	case SubAgentStateRunning, SubAgentStateIdle, SubAgentStateWaitingPrimary, SubAgentStateWaitingDescendant:
		return true
	default:
		return false
	}
}

func (r *DurableTaskRecord) allowsRehydrate() bool {
	if r == nil {
		return false
	}
	return strings.TrimSpace(r.ResumePolicy) == taskResumePolicyCompletedFollowUpOnly &&
		strings.TrimSpace(r.State) == string(SubAgentStateCompleted)
}

func semanticTaskKeyFallback(desc string) string {
	desc = strings.ToLower(strings.TrimSpace(desc))
	if desc == "" {
		return ""
	}
	fields := strings.Fields(desc)
	if len(fields) > 12 {
		fields = fields[:12]
	}
	return strings.Join(fields, " ")
}

func normalizeWriteScopePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = stdpath.Clean(strings.ReplaceAll(path, "\\", "/"))
	if path == "." {
		return ""
	}
	return strings.TrimPrefix(path, "./")
}

func pathContainsPath(base, target string) bool {
	base = normalizeWriteScopePath(base)
	target = normalizeWriteScopePath(target)
	if base == "" || target == "" {
		return false
	}
	if base == target {
		return true
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return strings.HasPrefix(target, base)
}

func sameOrNestedPath(a, b string) bool {
	return pathContainsPath(a, b) || pathContainsPath(b, a)
}

func writeScopesOverlap(a, b tools.WriteScope) bool {
	a = a.Normalized()
	b = b.Normalized()
	if a.ReadOnly || b.ReadOnly {
		return false
	}
	for _, fa := range a.Files {
		for _, fb := range b.Files {
			if sameOrNestedPath(fa, fb) {
				return true
			}
		}
		for _, pb := range b.PathPrefix {
			if sameOrNestedPath(fa, pb) {
				return true
			}
		}
	}
	for _, fb := range b.Files {
		for _, pa := range a.PathPrefix {
			if sameOrNestedPath(fb, pa) {
				return true
			}
		}
	}
	for _, pa := range a.PathPrefix {
		for _, pb := range b.PathPrefix {
			if sameOrNestedPath(pa, pb) {
				return true
			}
		}
	}
	for _, ma := range a.Modules {
		for _, mb := range b.Modules {
			if ma == mb {
				return true
			}
		}
	}
	return false
}

func (a *MainAgent) findDuplicateOrConflictingTask(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (*DurableTaskRecord, bool) {
	planTaskRef = strings.TrimSpace(planTaskRef)
	semanticTaskKey = strings.TrimSpace(semanticTaskKey)
	if semanticTaskKey == "" {
		semanticTaskKey = semanticTaskKeyFallback(planTaskRef)
	}
	expectedWriteScope = expectedWriteScope.Normalized()
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, rec := range a.taskRecords {
		if rec == nil {
			continue
		}
		if strings.TrimSpace(rec.OwnerAgentID) != strings.TrimSpace(ownerAgentID) || strings.TrimSpace(rec.OwnerTaskID) != strings.TrimSpace(ownerTaskID) {
			continue
		}
		if strings.TrimSpace(rec.AgentDefName) != strings.TrimSpace(agentType) {
			continue
		}
		if !isNonTerminalTaskState(rec.State) && !rec.allowsRehydrate() {
			continue
		}
		duplicate := false
		if planTaskRef != "" && strings.TrimSpace(rec.PlanTaskRef) == planTaskRef {
			duplicate = true
		}
		if !duplicate && semanticTaskKey != "" && strings.TrimSpace(rec.SemanticTaskKey) == semanticTaskKey {
			duplicate = true
		}
		if duplicate {
			return cloneDurableTaskRecord(rec), false
		}
		if !expectedWriteScope.Empty() && !rec.ExpectedWriteScope.Empty() && writeScopesOverlap(expectedWriteScope, rec.ExpectedWriteScope) {
			return cloneDurableTaskRecord(rec), true
		}
	}
	return nil, false
}

func (a *MainAgent) taskRecordByTaskID(taskID string) *DurableTaskRecord {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneDurableTaskRecord(a.taskRecords[taskID])
}

func (a *MainAgent) setTaskRecords(records map[string]*DurableTaskRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.taskRecords = cloneDurableTaskRecordMap(records)
	if a.taskRecords == nil {
		a.taskRecords = make(map[string]*DurableTaskRecord)
	}
}

func (a *MainAgent) persistTaskRegistry() {
	if a == nil {
		return
	}
	a.mu.RLock()
	records := cloneDurableTaskRecordMap(a.taskRecords)
	sessionDir := a.sessionDir
	a.mu.RUnlock()
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		slog.Warn("failed to persist durable task registry", "session", sessionDir, "error", err)
	}
}

func (a *MainAgent) syncTaskRecordFromSub(sub *SubAgent, closedReason string) {
	if a == nil || sub == nil {
		return
	}
	taskID := strings.TrimSpace(sub.taskID)
	if taskID == "" {
		return
	}
	state := sub.State()
	summary := sub.LastSummary()
	lastMailboxID := sub.LastMailboxID()
	lastReplyMessageID, lastReplyToMailboxID, lastReplyKind, lastReplySummary := sub.LastReplyThread()
	lastArtifact := sub.LastArtifact()
	now := time.Now()

	a.mu.Lock()
	if a.taskRecords == nil {
		a.taskRecords = make(map[string]*DurableTaskRecord)
	}
	rec := cloneDurableTaskRecord(a.taskRecords[taskID])
	if rec == nil {
		rec = &DurableTaskRecord{
			TaskID:      taskID,
			CreatedAt:   now,
			CreatedTurn: a.explicitUserTurnCount,
		}
	}
	rec.AgentDefName = strings.TrimSpace(sub.agentDefName)
	rec.TaskDesc = strings.TrimSpace(sub.taskDesc)
	rec.PlanTaskRef = strings.TrimSpace(sub.planTaskRef)
	rec.SemanticTaskKey = strings.TrimSpace(sub.semanticTaskKey)
	rec.ExpectedWriteScope = sub.writeScope.Normalized()
	rec.OwnerAgentID = strings.TrimSpace(sub.ownerAgentID)
	rec.OwnerTaskID = strings.TrimSpace(sub.ownerTaskID)
	rec.Depth = sub.depth
	rec.State = string(state)
	rec.JoinToOwner = sub.joinToOwner
	rec.ResumePolicy = durableTaskResumePolicy(state)
	rec.LatestInstanceID = strings.TrimSpace(sub.instanceID)
	rec.InstanceHistory = append(rec.InstanceHistory, rec.LatestInstanceID)
	rec.InstanceHistory = dedupeTaskInstanceHistory(rec.InstanceHistory)
	rec.LastSummary = strings.TrimSpace(summary)
	rec.LastMailboxID = strings.TrimSpace(lastMailboxID)
	rec.LastReplyMessageID = strings.TrimSpace(lastReplyMessageID)
	rec.LastReplyToMailboxID = strings.TrimSpace(lastReplyToMailboxID)
	rec.LastReplyKind = strings.TrimSpace(lastReplyKind)
	rec.LastReplySummary = strings.TrimSpace(lastReplySummary)
	if strings.TrimSpace(lastArtifact.ID) != "" || strings.TrimSpace(lastArtifact.RelPath) != "" {
		rec.LastArtifactRefs = tools.NormalizeArtifactRefs(append(rec.LastArtifactRefs, lastArtifact))
	}
	rec.LastUpdatedTurn = a.explicitUserTurnCount
	rec.UpdatedAt = now
	if strings.TrimSpace(closedReason) != "" {
		rec.ClosedReason = strings.TrimSpace(closedReason)
	} else if state == SubAgentStateRunning || state == SubAgentStateWaitingPrimary || state == SubAgentStateIdle {
		rec.ClosedReason = ""
	}
	a.taskRecords[taskID] = rec
	a.mu.Unlock()

	a.persistTaskRegistry()
}

func taskRecordFromLoadedState(state loadedSubAgentState) *DurableTaskRecord {
	taskID := strings.TrimSpace(state.TaskID)
	if taskID == "" {
		return nil
	}
	rec := &DurableTaskRecord{
		TaskID:               taskID,
		AgentDefName:         strings.TrimSpace(state.AgentDefName),
		TaskDesc:             strings.TrimSpace(state.TaskDesc),
		OwnerAgentID:         strings.TrimSpace(state.OwnerAgentID),
		OwnerTaskID:          strings.TrimSpace(state.OwnerTaskID),
		Depth:                state.Depth,
		JoinToOwner:          state.JoinToOwner,
		State:                string(state.State),
		ResumePolicy:         durableTaskResumePolicy(state.State),
		LatestInstanceID:     strings.TrimSpace(state.InstanceID),
		InstanceHistory:      dedupeTaskInstanceHistory([]string{state.InstanceID}),
		LastSummary:          strings.TrimSpace(state.LastSummary),
		LastMailboxID:        strings.TrimSpace(state.LastMailboxID),
		LastReplyMessageID:   strings.TrimSpace(state.LastReplyMessageID),
		LastReplyToMailboxID: strings.TrimSpace(state.LastReplyToMailboxID),
		LastReplyKind:        strings.TrimSpace(state.LastReplyKind),
		LastReplySummary:     strings.TrimSpace(state.LastReplySummary),
	}
	if ref := state.LastArtifact; strings.TrimSpace(ref.ID) != "" || strings.TrimSpace(ref.RelPath) != "" {
		rec.LastArtifactRefs = tools.NormalizeArtifactRefs([]tools.ArtifactRef{ref})
	}
	return cloneDurableTaskRecord(rec)
}

func mergeDurableTaskRecords(base map[string]*DurableTaskRecord, extra ...map[string]*DurableTaskRecord) map[string]*DurableTaskRecord {
	out := cloneDurableTaskRecordMap(base)
	if out == nil {
		out = make(map[string]*DurableTaskRecord)
	}
	for _, batch := range extra {
		for taskID, rec := range batch {
			taskID = strings.TrimSpace(taskID)
			if taskID == "" || rec == nil {
				continue
			}
			next := cloneDurableTaskRecord(rec)
			if next == nil {
				continue
			}
			if prev, ok := out[taskID]; ok && prev != nil {
				if next.AgentDefName == "" {
					next.AgentDefName = prev.AgentDefName
				}
				if next.TaskDesc == "" {
					next.TaskDesc = prev.TaskDesc
				}
				if next.OwnerAgentID == "" {
					next.OwnerAgentID = prev.OwnerAgentID
				}
				if next.OwnerTaskID == "" {
					next.OwnerTaskID = prev.OwnerTaskID
				}
				if next.Depth == 0 {
					next.Depth = prev.Depth
				}
				if !next.JoinToOwner {
					next.JoinToOwner = prev.JoinToOwner
				}
				if next.State == "" {
					next.State = prev.State
				}
				if next.ResumePolicy == "" {
					next.ResumePolicy = prev.ResumePolicy
				}
				if next.LatestInstanceID == "" {
					next.LatestInstanceID = prev.LatestInstanceID
				}
				next.InstanceHistory = dedupeTaskInstanceHistory(append(prev.InstanceHistory, next.InstanceHistory...))
				if next.LastSummary == "" {
					next.LastSummary = prev.LastSummary
				}
				if next.LastMailboxID == "" {
					next.LastMailboxID = prev.LastMailboxID
				}
				if next.LastReplyMessageID == "" {
					next.LastReplyMessageID = prev.LastReplyMessageID
				}
				if next.LastReplyToMailboxID == "" {
					next.LastReplyToMailboxID = prev.LastReplyToMailboxID
				}
				if next.LastReplyKind == "" {
					next.LastReplyKind = prev.LastReplyKind
				}
				if next.LastReplySummary == "" {
					next.LastReplySummary = prev.LastReplySummary
				}
				if len(next.LastArtifactRefs) == 0 {
					next.LastArtifactRefs = prev.LastArtifactRefs
				}
				if next.LastCompletion == nil {
					next.LastCompletion = prev.LastCompletion
				}
				if next.SuspectedStallReason == "" {
					next.SuspectedStallReason = prev.SuspectedStallReason
				}
				if next.CreatedAt.IsZero() {
					next.CreatedAt = prev.CreatedAt
				}
				if next.CreatedTurn == 0 {
					next.CreatedTurn = prev.CreatedTurn
				}
				if next.UpdatedAt.IsZero() {
					next.UpdatedAt = prev.UpdatedAt
				}
				if next.LastUpdatedTurn == 0 {
					next.LastUpdatedTurn = prev.LastUpdatedTurn
				}
				if next.ClosedReason == "" {
					next.ClosedReason = prev.ClosedReason
				}
			}
			out[taskID] = next
		}
	}
	return out
}

func buildDurableTaskRecordsFromLoadedStates(states []loadedSubAgentState) map[string]*DurableTaskRecord {
	if len(states) == 0 {
		return nil
	}
	out := make(map[string]*DurableTaskRecord, len(states))
	for _, state := range states {
		rec := taskRecordFromLoadedState(state)
		if rec == nil || rec.TaskID == "" {
			continue
		}
		out[rec.TaskID] = rec
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterRestorableSubAgentStates(states []loadedSubAgentState) []loadedSubAgentState {
	if len(states) == 0 {
		return nil
	}
	out := make([]loadedSubAgentState, 0, len(states))
	for _, state := range states {
		restoreState := state.State
		if restoreState == "" {
			restoreState = SubAgentStateIdle
		}
		if restoreState == SubAgentStateRunning {
			restoreState = SubAgentStateIdle
		}
		if !shouldRestoreLiveSubAgentState(restoreState) {
			continue
		}
		state.State = restoreState
		out = append(out, state)
	}
	return out
}

func nextAdhocSeqFromTaskRecords(records map[string]*DurableTaskRecord) uint64 {
	var maxSeq uint64
	for taskID, rec := range records {
		taskID = strings.TrimSpace(taskID)
		if taskID == "" && rec != nil {
			taskID = strings.TrimSpace(rec.TaskID)
		}
		if !strings.HasPrefix(taskID, "adhoc-") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimPrefix(taskID, "adhoc-"), 10, 64)
		if err == nil && n > maxSeq {
			maxSeq = n
		}
	}
	return maxSeq
}

func advanceInstanceCountersForTaskRecords(records map[string]*DurableTaskRecord) {
	for _, rec := range records {
		if rec == nil {
			continue
		}
		for _, id := range rec.InstanceHistory {
			AdvancePastID(id)
		}
		if rec.LatestInstanceID != "" {
			AdvancePastID(rec.LatestInstanceID)
		}
	}
}

func loadTaskHistoryMessages(rm *recovery.RecoveryManager, rec *DurableTaskRecord) ([]message.Message, error) {
	if rm == nil || rec == nil {
		return nil, nil
	}
	historyIDs := dedupeTaskInstanceHistory(rec.InstanceHistory)
	if len(historyIDs) == 0 && strings.TrimSpace(rec.LatestInstanceID) != "" {
		historyIDs = []string{strings.TrimSpace(rec.LatestInstanceID)}
	}
	var out []message.Message
	for _, id := range historyIDs {
		msgs, err := rm.LoadMessages(id)
		if err != nil {
			return nil, err
		}
		out = append(out, normalizeRestoredMessages(msgs)...)
	}
	if len(out) == 0 && strings.TrimSpace(rec.TaskDesc) != "" {
		out = append(out, message.Message{Role: "user", Content: rec.TaskDesc})
	}
	return out, nil
}

func (a *MainAgent) taskInfosForCompaction() []SubAgentInfo {
	a.mu.RLock()
	liveInfos := make([]SubAgentInfo, 0, len(a.subAgents))
	seenTaskIDs := make(map[string]struct{}, len(a.subAgents))
	for _, sub := range a.subAgents {
		if sub == nil {
			continue
		}
		selectedRef := ""
		runningRef := ""
		if sub.llmClient != nil {
			selectedRef = sub.llmClient.PrimaryModelRef()
			if v := sub.llmClient.ActiveVariant(); v != "" {
				selectedRef += "@" + v
			}
			runningRef = formatModelRefForNotification(sub.llmClient.RunningModelRef(), selectedRef, sub.llmClient.ActiveVariant())
		}
		state := sub.State()
		summary := sub.LastSummary()
		artifact := sub.LastArtifact()
		liveInfos = append(liveInfos, SubAgentInfo{
			InstanceID:       sub.instanceID,
			TaskID:           sub.taskID,
			AgentDefName:     sub.agentDefName,
			TaskDesc:         sub.taskDesc,
			ModelName:        sub.modelName,
			SelectedRef:      selectedRef,
			RunningRef:       runningRef,
			State:            string(state),
			Color:            sub.color,
			LastSummary:      summary,
			UrgentInboxCount: a.subAgentUrgentInboxCountLocked(sub.instanceID),
			LastArtifact:     artifact,
		})
		if taskID := strings.TrimSpace(sub.taskID); taskID != "" {
			seenTaskIDs[taskID] = struct{}{}
		}
	}
	records := cloneDurableTaskRecordMap(a.taskRecords)
	a.mu.RUnlock()

	var historical []SubAgentInfo
	for taskID, rec := range records {
		if rec == nil {
			continue
		}
		if _, ok := seenTaskIDs[taskID]; ok {
			continue
		}
		state := strings.TrimSpace(rec.State)
		if state != string(SubAgentStateCompleted) &&
			state != string(SubAgentStateCancelled) &&
			state != string(SubAgentStateFailed) {
			continue
		}
		var lastArtifact tools.ArtifactRef
		if len(rec.LastArtifactRefs) > 0 {
			lastArtifact = tools.NormalizeArtifactRef(rec.LastArtifactRefs[0])
		}
		historical = append(historical, SubAgentInfo{
			InstanceID:   strings.TrimSpace(rec.LatestInstanceID),
			TaskID:       taskID,
			AgentDefName: strings.TrimSpace(rec.AgentDefName),
			TaskDesc:     strings.TrimSpace(rec.TaskDesc),
			State:        state,
			LastSummary:  strings.TrimSpace(rec.LastSummary),
			LastArtifact: lastArtifact,
		})
	}
	sort.Slice(historical, func(i, j int) bool {
		return historical[i].TaskID < historical[j].TaskID
	})
	return append(liveInfos, historical...)
}
