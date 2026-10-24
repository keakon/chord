package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

const (
	taskResumePolicyLiveOnly     = "live_only"
	taskResumePolicyNotify       = "notify"
	taskResumePolicyExplicitOnly = "explicit_only"
	maxRetainedTerminalTasks     = 256
)

type taskResumeTrigger string

const (
	taskResumeByTargetedNotify    taskResumeTrigger = "targeted_notify"
	taskResumeByDescendantMailbox taskResumeTrigger = "descendant_mailbox"
)

// ExternalTaskSession addresses a task executed by an external coding-agent
// backend. Backend is the adapter name ("claude-code"); SessionID is the
// external session Chord pinned.
type ExternalTaskSession struct {
	Backend      string       `json:"backend,omitempty"`
	SessionID    string       `json:"session_id,omitempty"`
	WorkDir      string       `json:"work_dir,omitempty"`
	WorkDirState WorkDirState `json:"work_dir_state"`
	Prompt       string       `json:"prompt,omitempty"`
	Command      string       `json:"command,omitempty"`
	LaunchArgs   []string     `json:"launch_args,omitempty"`
	EnvKeys      []string     `json:"env_keys,omitempty"`
	ProcessEnded bool         `json:"process_ended"`
	// SessionEstablished records that the CLI acknowledged initialize for
	// this session. A generated session id alone does not prove a transcript
	// exists; resume must not use --resume until this is true.
	SessionEstablished bool `json:"session_established,omitempty"`
	// RunID identifies the last supervisor run launched for this task. It is
	// persisted before the launch, so a receipt of exactly this run proves
	// whether its process group was torn down.
	RunID    string                    `json:"run_id,omitempty"`
	Inputs   []externalTaskInput       `json:"inputs,omitempty"`
	Baseline *externalCheckoutBaseline `json:"baseline,omitempty"`
}

type externalTaskInput struct {
	ID      string                   `json:"id"`
	Content string                   `json:"content"`
	State   string                   `json:"state"`
	Mailbox *message.MailboxMetadata `json:"mailbox,omitempty"`
}

const (
	externalInputQueued  = "queued"
	externalInputDropped = "dropped"
)

func cloneExternalTaskSession(session *ExternalTaskSession) *ExternalTaskSession {
	if session == nil {
		return nil
	}
	out := *session
	out.LaunchArgs = slices.Clone(session.LaunchArgs)
	out.EnvKeys = slices.Clone(session.EnvKeys)
	out.Inputs = slices.Clone(session.Inputs)
	for i := range out.Inputs {
		if session.Inputs[i].Mailbox != nil {
			mailbox := *session.Inputs[i].Mailbox
			out.Inputs[i].Mailbox = &mailbox
		}
	}
	if session.Baseline != nil {
		baseline := *session.Baseline
		out.Baseline = &baseline
	}
	out.Backend = strings.TrimSpace(out.Backend)
	out.SessionID = strings.TrimSpace(out.SessionID)
	if out.Backend == "" && out.SessionID == "" {
		return nil
	}
	return &out
}

type DurableTaskRecord struct {
	// Immutable UI evidence rebuilt from canonical history; never serialized.
	skillSnapshot        *parkedSkillSnapshot
	TaskID               string              `json:"task_id"`
	AgentDefName         string              `json:"agent_def_name,omitempty"`
	TaskDesc             string              `json:"task_desc,omitempty"`
	PlanTaskRef          string              `json:"plan_task_ref,omitempty"`
	SemanticTaskKey      string              `json:"semantic_task_key,omitempty"`
	ResultSchema         json.RawMessage     `json:"result_schema,omitempty"`
	OwnerAgentID         string              `json:"owner_agent_id,omitempty"`
	OwnerTaskID          string              `json:"owner_task_id,omitempty"`
	Depth                int                 `json:"depth,omitempty"`
	JoinToOwner          bool                `json:"join_to_owner,omitempty"`
	State                string              `json:"state,omitempty"`
	ResumePolicy         string              `json:"resume_policy,omitempty"`
	LatestInstanceID     string              `json:"latest_instance_id,omitempty"`
	SelectedModelRef     string              `json:"selected_model_ref,omitempty"`
	RunningModelRef      string              `json:"running_model_ref,omitempty"`
	InvokedSkillNames    []string            `json:"invoked_skill_names,omitempty"`
	InstanceHistory      []string            `json:"instance_history,omitempty"`
	LastSummary          string              `json:"last_summary,omitempty"`
	LastMailboxID        string              `json:"last_mailbox_id,omitempty"`
	LastReplyMessageID   string              `json:"last_reply_message_id,omitempty"`
	LastReplyToMailboxID string              `json:"last_reply_to_mailbox_id,omitempty"`
	LastReplyKind        string              `json:"last_reply_kind,omitempty"`
	LastReplySummary     string              `json:"last_reply_summary,omitempty"`
	LastArtifactRefs     []tools.ArtifactRef `json:"last_artifact_refs,omitempty"`
	// EscalationCount and EscalationMailboxID track needs_repair escalations the
	// owner has not answered. They live on the durable task rather than a turn so
	// the budget spans attempts: the loop they converge is exactly the one that
	// crosses attempts. EscalationMailboxID names the mailbox whose answer zeroes
	// the count (see buildTaskRecordFromSub).
	EscalationCount     int                 `json:"escalation_count,omitempty"`
	EscalationMailboxID string              `json:"escalation_mailbox_id,omitempty"`
	LastCompletion      *CompletionEnvelope `json:"last_completion,omitempty"`
	PendingCompletion   *CompletionEnvelope `json:"pending_completion,omitempty"`
	// ExternalSession addresses a task executed by an external coding-agent
	// backend. Chord owns the session id; the external CLI owns the transcript.
	ExternalSession      *ExternalTaskSession `json:"external_session,omitempty"`
	SuspectedStallReason string               `json:"suspected_stall_reason,omitempty"`
	Persistence          PersistenceHealth    `json:"persistence"`
	RuntimeParked        bool                 `json:"runtime_parked,omitempty"`
	CreatedTurn          uint64               `json:"created_turn,omitempty"`
	LastUpdatedTurn      uint64               `json:"last_updated_turn,omitempty"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
	ClosedReason         string               `json:"closed_reason,omitempty"`
	Attempt              uint64               `json:"attempt,omitempty"`
	LifecycleRevision    uint64               `json:"lifecycle_revision,omitempty"`
	LatestSettlement     *TaskSettlement      `json:"latest_settlement,omitempty"`
	SettlementDurable    bool                 `json:"settlement_durable,omitempty"`
}

func durableTaskRegistryPath(sessionDir string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", "tasks.json")
}

func durableTaskArchivePath(sessionDir string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "subagents", "tasks.archive.jsonl")
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
	out.ResultSchema = cloneRawJSON(out.ResultSchema)
	out.OwnerAgentID = strings.TrimSpace(out.OwnerAgentID)
	out.OwnerTaskID = strings.TrimSpace(out.OwnerTaskID)
	out.State = strings.TrimSpace(out.State)
	out.ResumePolicy = strings.TrimSpace(out.ResumePolicy)
	out.LatestInstanceID = strings.TrimSpace(out.LatestInstanceID)
	out.SelectedModelRef = strings.TrimSpace(out.SelectedModelRef)
	out.RunningModelRef = strings.TrimSpace(out.RunningModelRef)
	out.InvokedSkillNames = normalizeSkillNames(out.InvokedSkillNames)
	out.LastSummary = strings.TrimSpace(out.LastSummary)
	out.LastMailboxID = strings.TrimSpace(out.LastMailboxID)
	out.LastReplyMessageID = strings.TrimSpace(out.LastReplyMessageID)
	out.LastReplyToMailboxID = strings.TrimSpace(out.LastReplyToMailboxID)
	out.LastReplyKind = strings.TrimSpace(out.LastReplyKind)
	out.LastReplySummary = strings.TrimSpace(out.LastReplySummary)
	out.EscalationMailboxID = strings.TrimSpace(out.EscalationMailboxID)
	out.LastArtifactRefs = tools.NormalizeArtifactRefs(out.LastArtifactRefs)
	out.LastCompletion = normalizeCompletionEnvelope(out.LastCompletion)
	out.PendingCompletion = normalizeCompletionEnvelope(out.PendingCompletion)
	out.ExternalSession = cloneExternalTaskSession(out.ExternalSession)
	out.SuspectedStallReason = strings.TrimSpace(out.SuspectedStallReason)
	out.Persistence.State = normalizePersistenceHealthState(out.Persistence.State)
	out.Persistence.LastError = strings.TrimSpace(out.Persistence.LastError)
	out.ClosedReason = strings.TrimSpace(out.ClosedReason)
	out.InstanceHistory = dedupeTaskInstanceHistory(out.InstanceHistory)
	if out.Attempt == 0 {
		out.Attempt = 1
	}
	out.LatestSettlement = cloneTaskSettlement(out.LatestSettlement)
	return &out
}

func normalizeSkillNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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
	// records is caller-owned (every caller passes a freshly cloned map), so
	// cloning each record again here only doubled the copy cost per rewrite.
	ordered := make([]*DurableTaskRecord, 0, len(records))
	for _, rec := range records {
		if rec == nil || rec.TaskID == "" {
			continue
		}
		ordered = append(ordered, rec)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].TaskID < ordered[j].TaskID
	})
	return persistJSONAtomically(sessionDir, path, "tasks", ordered)
}

// durableTaskResumePolicy maps a task's restored state to the trigger policy
func durableTaskResumePolicy(state SubAgentState) string {
	switch state {
	case SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant, SubAgentStateCompleted:
		return taskResumePolicyNotify
	case SubAgentStateFailed, SubAgentStateCancelled:
		return taskResumePolicyExplicitOnly
	default:
		return taskResumePolicyLiveOnly
	}
}

func (r *DurableTaskRecord) allowsRehydrate(trigger taskResumeTrigger) bool {
	if r == nil {
		return false
	}
	state := SubAgentState(strings.TrimSpace(r.State))
	switch trigger {
	case taskResumeByDescendantMailbox:
		// A descendant mailbox (a child completion or a child request for a
		// decision) is exactly the input a parked owner is waiting on, so it
		// may wake any parked non-terminal owner. waiting_descendant is the
		// classic case; an owner parked in waiting_main or idle (for example
		// escalated to main while one of its children later finished) would
		// otherwise strand the child's completion in its owned queue forever.
		// Terminal states stay excluded: their mailboxes are forwarded to the
		// main inbox instead of being rehydrated.
		switch state {
		case SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant:
			return true
		}
		return false
	case taskResumeByTargetedNotify:
		switch strings.TrimSpace(r.ResumePolicy) {
		case taskResumePolicyNotify:
			return state == SubAgentStateIdle || state == SubAgentStateWaitingMain ||
				state == SubAgentStateWaitingDescendant || state == SubAgentStateCompleted
		case taskResumePolicyExplicitOnly:
			// A targeted follow-up is the explicit act this policy waits for.
			// It applies to a failed worker: the transcript that produced the
			// failure is still on disk, so telling that worker what went wrong
			// is far cheaper than making a fresh one re-derive the context —
			// and the run that failed on its wrap-up call usually has the whole
			// deliverable sitting in its own history. Cancelled stays excluded:
			// someone (a user, an owner, a cascade) decided to stop that task,
			// and a message must not quietly undo the decision.
			return state == SubAgentStateFailed
		}
		return false
	default:
		return false
	}
}

// stillOwnsItsDeliverable reports whether this record is where a re-delegation
// of the same deliverable would land — the question duplicate detection asks.
// It is deliberately narrower than allowsRehydrate: a failed task can be
// continued by a targeted follow-up, but an owner delegating the work again is
// making a legitimate fresh start, and rejecting that as a duplicate would
// leave no way to retry a failure from scratch. Only work that is still live,
// or completed under a resume policy that treats a follow-up as continuing the
// same deliverable, keeps ownership.
func (r *DurableTaskRecord) stillOwnsItsDeliverable() bool {
	if r == nil {
		return false
	}
	if isNonTerminalTaskState(r.State) {
		return true
	}
	return strings.TrimSpace(r.ResumePolicy) == taskResumePolicyNotify &&
		r.allowsRehydrate(taskResumeByTargetedNotify)
}

// taskDuplicateDisposition classifies a collision the registry found between a
// new delegation and an existing task record (or in-flight admission).
// taskDuplicateExplicitKey is a confirmed duplicate — the incoming delegation
// supplied the same explicit semantic_task_key the record stores — and is the
// only collision that may reject the call with already_exists. A shared
// plan_task_ref without an explicit key in common is taskDuplicateProbable:
// the caller still creates the new task and only marks the returned handle as a
// detected duplicate, leaving the decision of whether both describe the same
// deliverable to the model.
type taskDuplicateDisposition uint8

const (
	taskDuplicateNone taskDuplicateDisposition = iota
	taskDuplicateExplicitKey
	taskDuplicateProbable
)

func (a *MainAgent) findDuplicateTaskLocked(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string) (*DurableTaskRecord, taskDuplicateDisposition) {
	planTaskRef = strings.TrimSpace(planTaskRef)
	semanticTaskKey = strings.TrimSpace(semanticTaskKey)
	ownerLineage := a.taskOwnerLineageLocked(ownerTaskID)
	var probable *DurableTaskRecord
	for _, rec := range a.subs.taskRecords {
		if rec != nil {
			if _, ancestor := ownerLineage[strings.TrimSpace(rec.TaskID)]; ancestor {
				continue
			}
		}
		disposition := a.duplicateTaskRecord(rec, ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey)
		if disposition == taskDuplicateExplicitKey {
			// The confirmed duplicate is the only hard rejection, so the scan
			// stops at the first one even when an earlier record matched
			// probably (taskRecords is a map, so iteration order is arbitrary).
			return cloneDurableTaskRecord(rec), disposition
		}
		if disposition == taskDuplicateProbable && probable == nil {
			probable = cloneDurableTaskRecord(rec)
		}
	}
	if probable != nil {
		return probable, taskDuplicateProbable
	}
	return nil, taskDuplicateNone
}

func (a *MainAgent) taskOwnerLineageLocked(ownerTaskID string) map[string]struct{} {
	lineage := make(map[string]struct{})
	for taskID := strings.TrimSpace(ownerTaskID); taskID != ""; {
		if _, seen := lineage[taskID]; seen {
			break
		}
		lineage[taskID] = struct{}{}
		rec := a.subs.taskRecords[taskID]
		if rec == nil {
			break
		}
		taskID = strings.TrimSpace(rec.OwnerTaskID)
	}
	return lineage
}

// duplicateTaskRecord classifies one existing record against a new delegation.
// The first branch detects duplicates of the same deliverable: same owner
// agent, owner task and agent type, with the plan_task_ref or the semantic key
// in common and a state that may still be continued (non-terminal, or
// completed under the notify resume policy). Only a match through an explicit
// semantic_task_key — the identity the caller deliberately asserts — returns
// taskDuplicateExplicitKey and may reject the delegation. Two different
// explicit keys are two different deliverables by the caller's own assertion,
// so they suppress even the plan_task_ref hint; a shared plan_task_ref alone
// returns taskDuplicateProbable, leaving the merge decision to the model.
func (a *MainAgent) duplicateTaskRecord(rec *DurableTaskRecord, ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string) taskDuplicateDisposition {
	if rec == nil {
		return taskDuplicateNone
	}
	if strings.TrimSpace(rec.OwnerAgentID) == strings.TrimSpace(ownerAgentID) && strings.TrimSpace(rec.OwnerTaskID) == strings.TrimSpace(ownerTaskID) && strings.TrimSpace(rec.AgentDefName) == strings.TrimSpace(agentType) {
		recKey := strings.TrimSpace(rec.SemanticTaskKey)
		if semanticTaskKey != "" && recKey == semanticTaskKey {
			if rec.stillOwnsItsDeliverable() {
				return taskDuplicateExplicitKey
			}
			return taskDuplicateNone
		}
		if semanticTaskKey != "" && recKey != "" {
			return taskDuplicateNone
		}
		planTaskRefMatched := planTaskRef != "" && strings.TrimSpace(rec.PlanTaskRef) == planTaskRef
		if planTaskRefMatched && rec.stillOwnsItsDeliverable() {
			return taskDuplicateProbable
		}
	}
	return taskDuplicateNone
}

func (a *MainAgent) taskRecordByTaskID(taskID string) *DurableTaskRecord {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	a.subs.mu.RLock()
	rec := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	a.subs.mu.RUnlock()
	if rec != nil {
		return rec
	}
	rec, err := loadArchivedTaskRecordByTaskID(a.sessionDir, taskID)
	if err != nil {
		log.Warnf("failed to load archived task task_id=%v error=%v", taskID, err)
	}
	return rec
}

func (a *MainAgent) taskRecordByInstanceID(instanceID string) *DurableTaskRecord {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil
	}
	a.subs.mu.RLock()
	for _, rec := range a.subs.taskRecords {
		if rec != nil && durableTaskRecordIncludesInstance(rec, instanceID) {
			out := cloneDurableTaskRecord(rec)
			a.subs.mu.RUnlock()
			return out
		}
	}
	a.subs.mu.RUnlock()
	rec, err := loadArchivedTaskRecordByInstanceID(a.sessionDir, instanceID)
	if err != nil {
		log.Warnf("failed to load archived task instance_id=%v error=%v", instanceID, err)
	}
	return rec
}

func (a *MainAgent) setTaskRecords(records map[string]*DurableTaskRecord) {
	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	a.subs.taskRecords = cloneDurableTaskRecordMap(records)
	if a.subs.taskRecords == nil {
		a.subs.taskRecords = make(map[string]*DurableTaskRecord)
	}
	a.subs.notifyTaskChangeLocked()
}

func (a *MainAgent) persistTaskRegistry() error {
	if a == nil {
		return nil
	}
	a.taskRegistryPersistMu.Lock()
	defer a.taskRegistryPersistMu.Unlock()
	a.subs.mu.RLock()
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	sessionDir := a.sessionDir
	a.subs.mu.RUnlock()
	archived, err := a.archiveEligibleTerminalTasks(records, sessionDir)
	if err != nil {
		return fmt.Errorf("archive terminal task records: %w", err)
	} else if len(archived) != len(records) {
		a.subs.mu.Lock()
		for taskID, before := range records {
			if _, retained := archived[taskID]; retained || before == nil {
				continue
			}
			current := a.subs.taskRecords[taskID]
			if current != nil && current.State == before.State && current.UpdatedAt.Equal(before.UpdatedAt) {
				delete(a.subs.taskRecords, taskID)
			}
		}
		records = cloneDurableTaskRecordMap(a.subs.taskRecords)
		a.subs.mu.Unlock()
	}
	if hook := a.taskRegistryPersistHook; hook != nil {
		hook()
	}
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		log.Warnf("failed to persist durable task registry session=%v error=%v", sessionDir, err)
		return err
	}
	return nil
}

func (a *MainAgent) persistTaskRegistryRecord(sessionDir, taskID string, record *DurableTaskRecord) error {
	if a == nil {
		return nil
	}
	a.taskRegistryPersistMu.Lock()
	defer a.taskRegistryPersistMu.Unlock()
	a.subs.mu.RLock()
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	a.subs.mu.RUnlock()
	if records == nil {
		records = make(map[string]*DurableTaskRecord)
	}
	taskID = strings.TrimSpace(taskID)
	if record == nil {
		delete(records, taskID)
	} else {
		records[taskID] = cloneDurableTaskRecord(record)
	}
	return persistDurableTaskRecords(sessionDir, records)
}

func (a *MainAgent) persistSubAgentRegistration(sessionDir string, sub *SubAgent, record *DurableTaskRecord) error {
	if a == nil || sub == nil {
		return nil
	}
	a.subAgentMetaPersistMu.Lock()
	a.taskRegistryPersistMu.Lock()
	defer a.taskRegistryPersistMu.Unlock()
	defer a.subAgentMetaPersistMu.Unlock()
	if err := a.persistSubAgentMetaToSession(sub, sessionDir); err != nil {
		return err
	}
	a.subs.mu.RLock()
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	a.subs.mu.RUnlock()
	if records == nil {
		records = make(map[string]*DurableTaskRecord)
	}
	records[sub.taskID] = cloneDurableTaskRecord(record)
	if hook := a.taskRegistryPersistHook; hook != nil {
		hook()
	}
	return persistDurableTaskRecords(sessionDir, records)
}

func (a *MainAgent) syncTaskRecordFromSub(sub *SubAgent, closedReason string) {
	if !a.updateTaskRecordFromSub(sub, closedReason) {
		return
	}
	_ = a.persistTaskRegistry()
}

func (a *MainAgent) updateTaskRecordFromSub(sub *SubAgent, closedReason string) bool {
	if a == nil || sub == nil {
		return false
	}
	taskID := strings.TrimSpace(sub.taskID)
	if taskID == "" {
		return false
	}
	currentTurn := a.explicitUserTurnCount.Load()
	now := time.Now()
	var skillSnapshot *parkedSkillSnapshot
	if isTerminalSubAgentState(sub.State()) && sub.ctxMgr != nil {
		skillSnapshot = sub.parkedSkillSnapshot()
	}

	a.subs.mu.Lock()
	if a.subs.taskRecords == nil {
		a.subs.taskRecords = make(map[string]*DurableTaskRecord)
	}
	rec := buildTaskRecordFromSub(sub, a.subs.taskRecords[taskID], closedReason, currentTurn, now)
	rec.skillSnapshot = skillSnapshot
	a.subs.taskRecords[taskID] = rec
	a.subs.notifyTaskChangeLocked()
	a.subs.mu.Unlock()
	return true
}

func buildTaskRecordFromSub(sub *SubAgent, previous *DurableTaskRecord, closedReason string, currentTurn uint64, now time.Time) *DurableTaskRecord {
	if sub == nil {
		return nil
	}
	taskID := strings.TrimSpace(sub.taskID)
	state := sub.State()
	summary := sub.LastSummary()
	lastMailboxID := sub.LastMailboxID()
	lastReplyMessageID, lastReplyToMailboxID, lastReplyKind, lastReplySummary := sub.LastReplyThread()
	lastArtifact := sub.LastArtifact()
	ownerAgentID, ownerTaskID, depth, joinToOwner := sub.ownerSnapshot()
	rec := cloneDurableTaskRecord(previous)
	if rec == nil {
		rec = &DurableTaskRecord{
			TaskID:      taskID,
			CreatedAt:   now,
			CreatedTurn: currentTurn,
			Attempt:     1,
		}
	}
	if rec.Attempt == 0 {
		rec.Attempt = 1
	}
	rec.LifecycleRevision++
	rec.AgentDefName = strings.TrimSpace(sub.agentDefName)
	rec.TaskDesc = strings.TrimSpace(sub.taskDesc)
	if planTaskRef := strings.TrimSpace(sub.planTaskRef); planTaskRef != "" || rec.PlanTaskRef == "" {
		rec.PlanTaskRef = planTaskRef
	}
	if semanticTaskKey := strings.TrimSpace(sub.semanticTaskKey); semanticTaskKey != "" || rec.SemanticTaskKey == "" {
		rec.SemanticTaskKey = semanticTaskKey
	}
	// The live runtime is the truth for this attempt's contract: a re-delegation
	// without a contract must not inherit the previous attempt's, and a contract
	// dropped at rehydration because its bytes were unreadable must not be
	// reintroduced here.
	rec.ResultSchema = cloneRawJSON(sub.resultSchemaJSON)
	rec.OwnerAgentID = ownerAgentID
	rec.OwnerTaskID = ownerTaskID
	rec.Depth = depth
	rec.State = string(state)
	rec.JoinToOwner = joinToOwner
	rec.ResumePolicy = durableTaskResumePolicy(state)
	rec.LatestInstanceID = strings.TrimSpace(sub.instanceID)
	rec.SelectedModelRef = subSelectedModelRef(sub)
	rec.RunningModelRef = subRunningModelRef(sub)
	rec.InvokedSkillNames = append(rec.InvokedSkillNames, sub.invokedSkillNamesSnapshot()...)
	rec.InvokedSkillNames = normalizeSkillNames(rec.InvokedSkillNames)
	rec.InstanceHistory = append(rec.InstanceHistory, rec.LatestInstanceID)
	rec.InstanceHistory = dedupeTaskInstanceHistory(rec.InstanceHistory)
	rec.LastSummary = strings.TrimSpace(summary)
	rec.Persistence = sub.PersistenceHealth()
	if pending := sub.PendingCompleteIntent(); pending != nil {
		rec.PendingCompletion = normalizeCompletionEnvelope(&CompletionEnvelope{Summary: pending.Summary})
		if pending.Envelope != nil {
			rec.PendingCompletion = normalizeCompletionEnvelope(pending.Envelope)
		}
	} else {
		rec.PendingCompletion = nil
	}
	rec.LastMailboxID = strings.TrimSpace(lastMailboxID)
	rec.LastReplyMessageID = strings.TrimSpace(lastReplyMessageID)
	rec.LastReplyToMailboxID = strings.TrimSpace(lastReplyToMailboxID)
	rec.LastReplyKind = strings.TrimSpace(lastReplyKind)
	rec.LastReplySummary = strings.TrimSpace(lastReplySummary)
	// The owner answered the escalation this record is counting, so the budget
	// starts over. Only a reply to that exact mailbox clears it: owner-initiated
	// deliveries leave LastReplyToMailboxID empty, and a wake-up from a descendant
	// mailbox does not touch the reply thread at all, so neither can reset a spin.
	if rec.EscalationMailboxID != "" && rec.LastReplyToMailboxID == rec.EscalationMailboxID {
		rec.EscalationCount = 0
		rec.EscalationMailboxID = ""
	}
	if strings.TrimSpace(lastArtifact.ID) != "" || strings.TrimSpace(lastArtifact.RelPath) != "" {
		rec.LastArtifactRefs = tools.NormalizeArtifactRefs(append(rec.LastArtifactRefs, lastArtifact))
	}
	rec.LastUpdatedTurn = currentTurn
	rec.UpdatedAt = now
	rec.RuntimeParked = false
	rec.skillSnapshot = nil
	if strings.TrimSpace(closedReason) != "" {
		rec.ClosedReason = strings.TrimSpace(closedReason)
	} else if state == SubAgentStateRunning || state == SubAgentStateWaitingMain || state == SubAgentStateIdle {
		rec.ClosedReason = ""
	}
	return rec
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
		PlanTaskRef:          strings.TrimSpace(state.PlanTaskRef),
		SemanticTaskKey:      strings.TrimSpace(state.SemanticTaskKey),
		OwnerAgentID:         strings.TrimSpace(state.OwnerAgentID),
		OwnerTaskID:          strings.TrimSpace(state.OwnerTaskID),
		Depth:                state.Depth,
		JoinToOwner:          state.JoinToOwner,
		State:                string(state.State),
		ResumePolicy:         durableTaskResumePolicy(state.State),
		LatestInstanceID:     strings.TrimSpace(state.InstanceID),
		InstanceHistory:      dedupeTaskInstanceHistory([]string{state.InstanceID}),
		LastSummary:          strings.TrimSpace(state.LastSummary),
		Persistence:          state.Persistence,
		SelectedModelRef:     strings.TrimSpace(state.SelectedModelRef),
		RunningModelRef:      strings.TrimSpace(state.RunningModelRef),
		LastMailboxID:        strings.TrimSpace(state.LastMailboxID),
		LastReplyMessageID:   strings.TrimSpace(state.LastReplyMessageID),
		LastReplyToMailboxID: strings.TrimSpace(state.LastReplyToMailboxID),
		LastReplyKind:        strings.TrimSpace(state.LastReplyKind),
		LastReplySummary:     strings.TrimSpace(state.LastReplySummary),
		Attempt:              1,
		LifecycleRevision:    1,
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
				if next.PlanTaskRef == "" {
					next.PlanTaskRef = prev.PlanTaskRef
				}
				if next.SemanticTaskKey == "" {
					next.SemanticTaskKey = prev.SemanticTaskKey
				}
				if len(next.ResultSchema) == 0 {
					next.ResultSchema = cloneRawJSON(prev.ResultSchema)
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
				if next.SelectedModelRef == "" {
					next.SelectedModelRef = prev.SelectedModelRef
				}
				if next.RunningModelRef == "" {
					next.RunningModelRef = prev.RunningModelRef
				}
				next.InvokedSkillNames = normalizeSkillNames(append(prev.InvokedSkillNames, next.InvokedSkillNames...))
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
				if next.PendingCompletion == nil {
					next.PendingCompletion = prev.PendingCompletion
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
				if next.Attempt == 0 {
					next.Attempt = prev.Attempt
				}
				if next.LifecycleRevision == 0 {
					next.LifecycleRevision = prev.LifecycleRevision
				}
				if next.LatestSettlement == nil {
					next.LatestSettlement = prev.LatestSettlement
					next.SettlementDurable = prev.SettlementDurable
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

func loadTaskHistoryMessages(rm *recovery.RecoveryManager, rec *DurableTaskRecord, started map[recovery.ToolActivityKey]struct{}) ([]message.Message, error) {
	if rm == nil || rec == nil {
		return nil, nil
	}
	historyIDs := taskInstanceHistoryIDs(rec)
	var out []message.Message
	for _, id := range historyIDs {
		msgs, err := rm.LoadMessages(id)
		if err != nil {
			return nil, err
		}
		// Repair orphan tool calls per historical instance so the (agent_id,
		// call_id) journal scope stays correct before the transcripts are
		// concatenated.
		msgs = normalizeRestoredMessages(msgs, started, id)
		out = append(out, msgs...)
	}
	if len(out) == 0 && strings.TrimSpace(rec.TaskDesc) != "" {
		out = append(out, message.Message{Role: "user", Content: rec.TaskDesc})
	}
	return out, nil
}

func taskInstanceHistoryIDs(rec *DurableTaskRecord) []string {
	historyIDs := dedupeTaskInstanceHistory(rec.InstanceHistory)
	if len(historyIDs) == 0 && strings.TrimSpace(rec.LatestInstanceID) != "" {
		return []string{strings.TrimSpace(rec.LatestInstanceID)}
	}
	return historyIDs
}

func (a *MainAgent) taskInfosForCompaction() []SubAgentInfo {
	a.subs.mu.RLock()
	live := a.subs.subAgentsLocked()
	liveInfos := make([]SubAgentInfo, 0, len(live))
	seenTaskIDs := make(map[string]struct{}, len(live))
	for _, sub := range live {
		if sub == nil {
			continue
		}
		selectedRef := ""
		runningRef := ""
		client, modelName := sub.llmSnapshot()
		if client != nil {
			selectedRef = client.PrimaryModelRef()
			if v := client.ActiveVariant(); v != "" {
				selectedRef += "@" + v
			}
			runningRef = formatModelRefForNotification(client.RunningModelRef(), selectedRef, client.ActiveVariant())
		}
		ownerAgentID, ownerTaskID, depth, _ := sub.ownerSnapshot()
		state := sub.State()
		summary := sub.LastSummary()
		artifact := sub.LastArtifact()
		liveInfos = append(liveInfos, SubAgentInfo{
			InstanceID:       sub.instanceID,
			TaskID:           sub.taskID,
			OwnerAgentID:     ownerAgentID,
			OwnerTaskID:      ownerTaskID,
			Depth:            depth,
			AgentDefName:     sub.agentDefName,
			TaskDesc:         sub.taskDesc,
			ModelName:        modelName,
			Persistence:      sub.PersistenceHealth(),
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
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	a.subs.mu.RUnlock()

	var historical []SubAgentInfo
	for taskID, rec := range records {
		if rec == nil || rec.ExternalSession != nil {
			// External coding agents own their context and compaction. Chord
			// keeps their task metadata for focus and history, but must not
			// feed an external agent's work into its own compaction request.
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
			OwnerAgentID: strings.TrimSpace(rec.OwnerAgentID),
			OwnerTaskID:  strings.TrimSpace(rec.OwnerTaskID),
			Depth:        rec.Depth,
			AgentDefName: strings.TrimSpace(rec.AgentDefName),
			TaskDesc:     strings.TrimSpace(rec.TaskDesc),
			Persistence:  rec.Persistence,
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
