package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// SubAgentInfo carries read-only information about a running SubAgent for TUI
// display (sidebar listing). The fields are snapshot values safe to read from
// any goroutine.
type SubAgentInfo struct {
	InstanceID       string
	TaskID           string
	OwnerAgentID     string
	OwnerTaskID      string
	Depth            int
	AgentDefName     string
	TaskDesc         string
	ModelName        string
	Persistence      PersistenceHealth
	SelectedRef      string
	RunningRef       string
	State            string
	Color            string // optional ANSI color code from agent config
	LastSummary      string
	UrgentInboxCount int
	LastArtifact     tools.ArtifactRef
}

type delegationCaller struct {
	AgentID    string
	TaskID     string
	Depth      int
	Delegation config.DelegationConfig
	Ruleset    permission.Ruleset
	WriteScope tools.WriteScope
	WorkDir    string
	IsMain     bool
}

func (a *MainAgent) subAgentWorkDir() string {
	if a == nil {
		return ""
	}
	workDir := strings.TrimSpace(a.cachedWorkDir)
	if workDir != "" {
		return workDir
	}
	workDir, _ = os.Getwd()
	return workDir
}

// writeScopeBaseDir is the directory that relative declared scopes resolve
// against. Declared scopes are advisory coordination declarations: they feed
// duplicate/overlap hints between sibling tasks and the worker's own context,
// and are never enforced at tool execution time.
func (a *MainAgent) writeScopeBaseDir() string {
	return a.subAgentWorkDir()
}

func (a *MainAgent) baseSubAgentConfig(agentDef *config.AgentConfig, instanceID string, client *llm.Client, parentCtx context.Context, cancel context.CancelFunc, extraMCPTools []tools.Tool) SubAgentConfig {
	return SubAgentConfig{
		InstanceID:    instanceID,
		AgentDefName:  agentDef.Name,
		Delegation:    agentDef.Delegation,
		Color:         agentDef.Color,
		SystemPrompt:  agentDef.SystemPrompt,
		LLMClient:     client,
		Recovery:      a.recoveryManager(),
		SessionEpoch:  a.recoverySessionEpoch(),
		Parent:        a,
		ParentCtx:     parentCtx,
		Cancel:        cancel,
		BaseTools:     a.tools,
		ExtraMCPTools: extraMCPTools,
		Ruleset:       a.buildSubAgentRuleset(agentDef),
		WorkDir:       a.subAgentWorkDir(),
		VenvPath:      a.cachedVenvPath,
		SessionDir:    a.sessionDir,
		AgentsMD:      a.cachedAgentsMDSnapshot(),
		Skills:        a.loadedSkillsSnapshot(),
		ModelName:     a.ModelName(),
		Orchestration: effectiveOrchestrationConfig(a.globalConfig, a.projectConfig),
	}
}

func controlPlaneAgentID(agentID string) string {
	if strings.TrimSpace(agentID) == "" {
		return identity.MainAgentID
	}
	return strings.TrimSpace(agentID)
}

type subAgentDelegateCreator struct {
	parent  *MainAgent
	ruleset func() permission.Ruleset
}

func (c subAgentDelegateCreator) CreateSubAgent(ctx context.Context, description, agentType string, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (tools.TaskHandle, error) {
	return c.parent.CreateSubAgent(ctx, description, agentType, planTaskRef, semanticTaskKey, expectedWriteScope)
}

// AgentRoleRegistersNoFileWriteTools implements tools.AgentFileWriteSurface for
// nested delegation; the classification depends only on the target agent
// definition's role, never on the delegating worker.
func (c subAgentDelegateCreator) AgentRoleRegistersNoFileWriteTools(agentType string) bool {
	if c.parent == nil {
		return false
	}
	return c.parent.agentRoleRegistersNoFileWriteTools(agentType)
}

func (c subAgentDelegateCreator) AvailableSubAgents() []tools.AgentInfo {
	if c.parent == nil {
		return nil
	}
	var ruleset permission.Ruleset
	if c.ruleset != nil {
		ruleset = c.ruleset()
	}
	return c.parent.availableSubAgentInfosForRuleset(ruleset, "")
}

func (a *MainAgent) delegationCallerFromContext(ctx context.Context) (delegationCaller, error) {
	callerAgentID := strings.TrimSpace(tools.AgentIDFromContext(ctx))
	if callerAgentID == "" || callerAgentID == a.instanceID {
		cfg := a.CurrentRoleConfig()
		if cfg == nil {
			cfg = config.DefaultBuilderAgent()
		}
		return delegationCaller{
			AgentID:    "",
			TaskID:     "",
			Depth:      0,
			Delegation: cfg.Delegation,
			Ruleset:    a.effectiveRuleset(),
			WriteScope: tools.WriteScope{},
			WorkDir:    a.writeScopeBaseDir(),
			IsMain:     true,
		}, nil
	}
	sub := a.subAgentByID(callerAgentID)
	if sub == nil {
		return delegationCaller{}, fmt.Errorf("unknown caller agent %q", callerAgentID)
	}
	_, _, depth, _ := sub.ownerSnapshot()
	return delegationCaller{
		AgentID:    sub.instanceID,
		TaskID:     sub.taskID,
		Depth:      depth,
		Delegation: sub.delegation,
		Ruleset:    sub.currentRuleset(),
		WriteScope: sub.currentWriteScope(),
		WorkDir:    sub.workDir,
		IsMain:     false,
	}, nil
}

func isNonTerminalTaskState(state string) bool {
	switch strings.TrimSpace(state) {
	case "", string(SubAgentStateCompleted), string(SubAgentStateCancelled), string(SubAgentStateFailed):
		return false
	default:
		return true
	}
}

// effectiveDirectActiveChildLimit is the per-owner fan-out cap. It honours the
// configured value instead of silently clamping it to the default: the ceiling
// that keeps the task tree bounded is MaxDelegationMaxChildren, already applied
// by EffectiveMaxChildren and rejected at config load when exceeded, and live
// concurrency is separately bounded by the runtime slot pool.
func effectiveDirectActiveChildLimit(cfg config.DelegationConfig) int {
	return cfg.EffectiveMaxChildren()
}

func (a *MainAgent) directNonTerminalChildCountLocked(ownerAgentID, ownerTaskID string) int {
	count := 0
	seenTaskIDs := make(map[string]struct{})
	for _, rec := range a.subs.taskRecords {
		if rec == nil {
			continue
		}
		if strings.TrimSpace(rec.OwnerAgentID) != strings.TrimSpace(ownerAgentID) {
			continue
		}
		if strings.TrimSpace(rec.OwnerTaskID) != strings.TrimSpace(ownerTaskID) {
			continue
		}
		if isNonTerminalTaskState(rec.State) {
			count++
			seenTaskIDs[strings.TrimSpace(rec.TaskID)] = struct{}{}
		}
	}
	for _, sub := range a.subs.subAgents {
		if sub == nil {
			continue
		}
		subOwnerAgentID, subOwnerTaskID, _, _ := sub.ownerSnapshot()
		if subOwnerAgentID != strings.TrimSpace(ownerAgentID) || subOwnerTaskID != strings.TrimSpace(ownerTaskID) {
			continue
		}
		if _, ok := seenTaskIDs[strings.TrimSpace(sub.taskID)]; ok {
			continue
		}
		if isNonTerminalTaskState(string(sub.State())) {
			count++
		}
	}
	for taskID, admission := range a.subs.admissions {
		if admission == nil || strings.TrimSpace(admission.ownerAgentID) != strings.TrimSpace(ownerAgentID) || strings.TrimSpace(admission.ownerTaskID) != strings.TrimSpace(ownerTaskID) {
			continue
		}
		if _, ok := seenTaskIDs[strings.TrimSpace(taskID)]; ok {
			continue
		}
		count++
	}
	return count
}

// duplicateTaskHandle builds the hard already_exists handle for a delegation
// that collided with a confirmed duplicate — an identical explicit
// semantic_task_key. A probable duplicate (the description-derived fallback key
// or plan_task_ref reuse without an explicit key in common) must never be
// rejected; it gets a started handle annotated by duplicateHintedTaskHandle
// instead. Write-scope overlap never reaches this handle either: it is
// advisory and annotated on the started handle by scopeConflictHintedTaskHandle.
func duplicateTaskHandle(existing *DurableTaskRecord) tools.TaskHandle {
	handle := tools.TaskHandle{
		Status:             "already_exists",
		TaskID:             existing.TaskID,
		AgentID:            existing.LatestInstanceID,
		Message:            "a task with the same explicit semantic_task_key already exists; continue it with `" + tools.NameNotify + "` instead of creating a duplicate delegate",
		PlanTaskRef:        existing.PlanTaskRef,
		SemanticTaskKey:    existing.SemanticTaskKey,
		ExpectedWriteScope: existing.ExpectedWriteScope,
		SuggestedTaskID:    existing.TaskID,
		SuggestedAgentID:   existing.LatestInstanceID,
		SuggestedAction:    "notify_existing",
		DuplicateDetected:  true,
	}
	return handle
}

// scopeConflictHintedTaskHandle annotates a started handle when the new task's
// declared write scope overlaps another non-terminal task. Write scopes are
// declarations, not authorities, so the overlap never rejects the delegation —
// the new task runs and the caller decides how to keep the two workers from
// editing the same files: serialize the tasks, coordinate shared edits with
// the other worker via notify, or move this work to its own git worktree.
func scopeConflictHintedTaskHandle(started tools.TaskHandle, existing *DurableTaskRecord) tools.TaskHandle {
	taskID := strings.TrimSpace(existing.TaskID)
	if taskID == "" {
		return started
	}
	started.ScopeConflict = true
	started.SuggestedTaskID = taskID
	started.SuggestedAgentID = strings.TrimSpace(existing.LatestInstanceID)
	started.SuggestedAction = "serialize_or_worktree"
	started.Message = fmt.Sprintf(
		"task started, but its declared write scope overlaps the still-active task %s: both workers may edit the same files, so run them serially, coordinate the shared edits with that task via %s (target_task_id=%s), or move this work to its own git worktree before touching the same paths",
		taskID, tools.NameNotify, taskID)
	return started
}

// duplicateHintedTaskHandle annotates a started handle when the new task
// collided with a probable duplicate: the description-derived fallback key or a
// plan_task_ref points at another task (or in-flight admission) from the same
// delegation context, but that collision is a heuristic, not proof that both
// are one deliverable. The new task is kept and the caller is told about the
// other task so the model can decide: continue the existing task with notify
// and cancel the fresh copy if they really are the same deliverable, or keep
// both if they are not.
func duplicateHintedTaskHandle(started tools.TaskHandle, existing *DurableTaskRecord, pending *subAgentAdmission) tools.TaskHandle {
	taskID := ""
	if existing != nil {
		taskID = strings.TrimSpace(existing.TaskID)
	} else if pending != nil {
		taskID = strings.TrimSpace(pending.taskID)
	}
	if taskID == "" {
		return started
	}
	started.DuplicateDetected = true
	started.SuggestedTaskID = taskID
	if existing != nil {
		started.SuggestedAgentID = strings.TrimSpace(existing.LatestInstanceID)
	}
	started.SuggestedAction = "notify_existing_if_same_deliverable"
	started.Message = fmt.Sprintf(
		"task started, but it may duplicate an earlier task %s from the same caller: if both are the same deliverable, continue that task with %s (target_task_id=%s) and %s this new one instead of running both; otherwise ignore this note and let the new task run",
		taskID, tools.NameNotify, taskID, tools.NameCancel)
	return started
}

// findPendingDuplicateOrConflictingTaskLocked is the admissions equivalent of
// findDuplicateOrConflictingTaskLocked: it screens still-admitting tasks
// (subAgentAdmission entries) as if they were running records, using the same
// disposition rules. A caller may only wait for and reuse a pending task's
// admission result when the match is taskDuplicateExplicitKey — the pending
// call asserted the same explicit semantic_task_key, so both really are the
// same deliverable. A probable match must never block the caller on another
// admission's handle.
func (a *MainAgent) findPendingDuplicateOrConflictingTaskLocked(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string, semanticKeyExplicit bool, expectedWriteScope tools.WriteScope) (*DurableTaskRecord, taskDuplicateDisposition, bool, *subAgentAdmission) {
	var probable *DurableTaskRecord
	var probableAdmission *subAgentAdmission
	var conflictRecord *DurableTaskRecord
	var conflictAdmission *subAgentAdmission
	var conflictDisposition taskDuplicateDisposition
	for _, pending := range a.subs.admissions {
		if pending == nil {
			continue
		}
		rec := &DurableTaskRecord{
			TaskID:             pending.taskID,
			AgentDefName:       pending.agentType,
			PlanTaskRef:        pending.planTaskRef,
			SemanticTaskKey:    pending.semanticTaskKey,
			ExpectedWriteScope: pending.expectedWriteScope,
			OwnerAgentID:       pending.ownerAgentID,
			OwnerTaskID:        pending.ownerTaskID,
			State:              string(SubAgentStateRunning),
		}
		disposition, conflict := a.duplicateOrConflictingTaskRecord(rec, ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey, semanticKeyExplicit, expectedWriteScope, a.writeScopeBaseDir())
		if disposition == taskDuplicateExplicitKey {
			// The identical explicit semantic_task_key is the only pending
			// match a caller may wait on, and it outranks advisory conflicts
			// from other admissions (iteration order is arbitrary).
			return rec, disposition, false, pending
		}
		if conflict && conflictRecord == nil {
			conflictRecord = rec
			conflictDisposition = disposition
			conflictAdmission = pending
		} else if disposition == taskDuplicateProbable && probable == nil {
			probable = rec
			probableAdmission = pending
		}
	}
	if conflictRecord != nil {
		return conflictRecord, conflictDisposition, true, conflictAdmission
	}
	if probable != nil {
		return probable, taskDuplicateProbable, false, probableAdmission
	}
	return nil, taskDuplicateNone, false, nil
}

func (a *MainAgent) releaseSubAgentAdmission(admission *subAgentAdmission) {
	if a == nil || admission == nil {
		return
	}
	releaseSlot := false
	a.subs.mu.Lock()
	if current := a.subs.admissions[admission.taskID]; current == admission {
		a.subs.removeAdmissionLocked(admission.taskID)
		releaseSlot = admission.slotHeld
		admission.slotHeld = false
	}
	a.subs.mu.Unlock()
	if !releaseSlot {
		return
	}
	if a.governor != nil {
		a.governor.releaseRuntime(false)
	}
}

func (a *MainAgent) cancelSubAgentAdmissions() {
	if a == nil {
		return
	}
	a.subs.mu.Lock()
	slots := a.subs.cancelAdmissionsLocked()
	a.subs.mu.Unlock()
	for range slots {
		if a.governor != nil {
			a.governor.releaseRuntime(false)
		}
	}
}

// countRuntimeSlotHolders returns how many pending admissions and live
// SubAgents are recorded as holding a runtime-pool token. Borrowed and bypass
// grants are excluded because they occupy the governor's borrow/bypass
// counters, not the runtime channel. The count is a best-effort signal rather
// than a lock-step invariant: park/close release their slot in two phases
// (registry removal, then flag clear), so a snapshot taken in that window can
// transiently under-count while the token is still in the channel.
func (a *MainAgent) countRuntimeSlotHolders() int {
	if a == nil {
		return 0
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	holders := 0
	for _, admission := range a.subs.admissions {
		if admission != nil && admission.slotHeld {
			holders++
		}
	}
	for _, sub := range a.subs.subAgents {
		if sub == nil {
			continue
		}
		sub.semMu.Lock()
		held := sub.semHeld && !sub.semBorrowed && !sub.semBypassed
		sub.semMu.Unlock()
		if held {
			holders++
		}
	}
	return holders
}

// warnOnRuntimeSlotDrift logs an anomaly when the runtime pool holds tokens
// that no admission or live SubAgent is recorded as owning (or holder
// bookkeeping ran ahead of the pool). A leaked slot is only directly
// observable when it makes a later acquisition refuse, so the capacity-refusal
// path of CreateSubAgent is its alarm point; the underlying delta stays
// available on resourceGovernorSnapshot for any sampler via
// runtimeGovernorSnapshot.
func (a *MainAgent) warnOnRuntimeSlotDrift(where string) {
	snap := a.runtimeGovernorSnapshot()
	if snap.RuntimeSlotDrift == 0 {
		return
	}
	log.Warnf("runtime slot accounting drift during %s runtime_in_use=%d runtime_holders=%d drift=%d; a leaked or over-released SubAgent slot is suspected", where, snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
}

// runtimeGovernorSnapshot fills the runtime-pool holder count and its drift
// from the raw channel occupancy into a governor snapshot. RuntimeHolders is
// only computable from the admission/SubAgent registries this agent owns, so
// it is filled here instead of in (*resourceGovernor).snapshot. A positive
// RuntimeSlotDrift means runtime tokens are held by no recorded owner (a
// leaked slot, e.g. the CreateSubAgent failure branch that used to drop a
// committed sub without releasing its slot); a negative drift means holder
// bookkeeping ran ahead of the pool.
func (a *MainAgent) runtimeGovernorSnapshot() resourceGovernorSnapshot {
	snap := resourceGovernorSnapshot{}
	if a.governor != nil {
		snap = a.governor.snapshot()
	} else {
		snap.RuntimeCapacity = cap(a.sem)
		snap.RuntimeInUse = len(a.sem)
	}
	snap.RuntimeHolders = a.countRuntimeSlotHolders()
	snap.RuntimeSlotDrift = snap.RuntimeInUse - snap.RuntimeHolders
	return snap
}

func (a *MainAgent) outstandingJoinChildTaskIDsLocked(taskID string) []string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	var out []string
	for _, rec := range a.subs.taskRecords {
		if rec == nil || !rec.JoinToOwner {
			continue
		}
		if strings.TrimSpace(rec.OwnerTaskID) != taskID {
			continue
		}
		if !isNonTerminalTaskState(rec.State) {
			continue
		}
		out = append(out, strings.TrimSpace(rec.TaskID))
	}
	sort.Strings(out)
	return out
}

func (a *MainAgent) outstandingJoinChildTaskIDs(taskID string) []string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	return a.outstandingJoinChildTaskIDsLocked(taskID)
}

func (a *MainAgent) directChildTaskIDs(taskID string) []string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	var out []string
	for _, rec := range a.subs.taskRecords {
		if rec == nil {
			continue
		}
		if strings.TrimSpace(rec.OwnerTaskID) != taskID {
			continue
		}
		if !isNonTerminalTaskState(rec.State) {
			continue
		}
		out = append(out, strings.TrimSpace(rec.TaskID))
	}
	sort.Strings(out)
	return out
}

func (a *MainAgent) canCallerDelegate(ctx context.Context) (delegationCaller, error) {
	caller, err := a.delegationCallerFromContext(ctx)
	if err != nil {
		return delegationCaller{}, err
	}
	// EffectiveMaxDepth already clamps to MaxDelegationMaxDepth, so a caller
	// definition cannot lift its own ceiling: a parent's limit does not bind its
	// children (each worker evaluates its own delegation policy), which without
	// a global ceiling would let one self-delegating definition grow the chain
	// without bound.
	maxDepth := caller.Delegation.EffectiveMaxDepth()
	if !caller.IsMain && caller.Depth >= maxDepth {
		return delegationCaller{}, fmt.Errorf("nested Delegate is not available at depth %d (max_depth=%d, ceiling=%d)", caller.Depth, maxDepth, config.MaxDelegationMaxDepth)
	}
	return caller, nil
}

// handleAgentDone processes a SubAgent completion event. It releases resources,
// injects the completion result into the MainAgent's conversation for LLM
// review, and triggers a new LLM call so the MainAgent can decide next steps
// (e.g. mark todo as done, request revisions, start next task).
func (a *MainAgent) buildCompletionEnvelope(sub *SubAgent, result *AgentResult) *CompletionEnvelope {
	if result != nil && result.Envelope != nil {
		env := normalizeCompletionEnvelope(result.Envelope)
		if env != nil {
			return env
		}
	}
	summary := ""
	if result != nil {
		summary = result.Summary
	}
	env := &CompletionEnvelope{Summary: strings.TrimSpace(summary)}
	if sub != nil {
		if ref := sub.LastArtifact(); strings.TrimSpace(ref.RelPath) != "" || strings.TrimSpace(ref.ID) != "" {
			env.Artifacts = tools.NormalizeArtifactRefs([]tools.ArtifactRef{ref})
		}
	}
	return normalizeCompletionEnvelope(env)
}

func (a *MainAgent) handleAgentDone(evt Event) {
	result, ok := evt.Payload.(*AgentResult)
	if !ok {
		log.Errorf("handleAgentDone: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	a.subs.mu.RLock()
	sub := a.subs.subAgents[evt.SourceID]
	a.subs.mu.RUnlock()
	if sub == nil {
		log.Warnf("handleAgentDone: unknown SubAgent source=%v", evt.SourceID)
		return
	}
	replyMessageID := firstReplyMessageID(sub)
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()
	completion := a.buildCompletionEnvelope(sub, result)

	a.releaseSubAgentSlot(sub)
	a.emitActivity(evt.SourceID, ActivityIdle, "")
	mailbox := &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       sub.taskID,
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    replyMessageID,
		Kind:         SubAgentMailboxKindCompleted,
		MessageType:  AgentMessageTypeNotice,
		Subtype:      agentMessageSubtypeTaskCompletion,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      result.Summary,
		Payload:      result.Summary,
		Completion:   completion,
		RequiresAck:  false,
	}
	a.normalizeSubAgentMailboxMessage(mailbox)
	// Terminal-commit ordering: the completion mailbox must be persisted and
	// applied before commitTerminalTask (invoked through the close handler
	// below) makes the task terminal. The queued mailbox event below only
	// delivers it afterwards, so a crash after the settlement/registry write
	// can no longer lose the completion; restore then re-delivers it from the
	// mailbox log. Persistence stays best-effort: a failure is retried through
	// the queued event's own persist path and must never block the terminal
	// commit or leave the task stuck in a non-terminal state.
	if err := a.prepareSubAgentMailboxMessage(mailbox); err != nil {
		log.Warnf("completion mailbox durability degraded task_id=%v agent_id=%v error=%v (will retry through the mailbox queue)", sub.taskID, evt.SourceID, err)
	} else if messageID := strings.TrimSpace(mailbox.MessageID); messageID != "" {
		// Already durably recorded and applied here; the mailbox event must
		// only deliver it, not write or apply it a second time.
		a.markSubAgentMailboxSeen(messageID)
	}
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: mailbox})
	mailboxMessage := formatSubAgentMailboxInjectionText(mailbox)
	if ownerAgentID == "" {
		mailboxMessage = "<system-reminder>\n" + mailboxMessage + "\n</system-reminder>"
	}

	a.emitToTUI(AgentDoneEvent{
		AgentID:       evt.SourceID,
		TaskID:        sub.taskID,
		AgentType:     sub.agentDefName,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		Summary:       result.Summary,
		Message:       mailboxMessage,
	})
	a.handleSubAgentCloseRequestedEvent(Event{
		Type:     EventSubAgentCloseRequested,
		SourceID: evt.SourceID,
		Payload: &SubAgentCloseRequestedPayload{
			Reason:       result.Summary,
			ClosedReason: "task completed",
			FinalState:   SubAgentStateCompleted,
			Completion:   completion,
		},
	})
}

// agentMessageSubtypeDecision is the mailbox subtype of an escalation mailbox:
// the worker parked waiting on a decision the owner must make.
const agentMessageSubtypeDecision = "decision"

func (a *MainAgent) handleAgentNotify(evt Event) {
	payload, ok := evt.Payload.(tools.AgentNotifyPayload)
	if !ok {
		log.Errorf("handleAgentNotify: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	msg := strings.TrimSpace(payload.Message)
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		log.Debugf("dropping report from abandoned subagent agent_id=%v", evt.SourceID)
		return
	}
	if isTerminalSubAgentState(sub.State()) {
		// A settled runtime must not be resurrected by a queued or late
		// progress notice. Durable terminal outcomes (completion, failure,
		// user cancel) are absorbing: they are committed on this runtime, so
		// accepting the notice would revive a cancelled/completed attempt
		// without the explicit new-attempt machinery (resetForAttempt and the
		// task-record attempt bump) that terminal reuse requires.
		log.Debugf("dropping notify from settled subagent agent_id=%v state=%v", evt.SourceID, sub.State())
		return
	}
	if !sub.setState(SubAgentStateRunning, msg) {
		return
	}
	a.noteSubAgentStateTransition(sub, SubAgentStateRunning)
	a.persistSubAgentMeta(sub)
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()
	messageType := AgentMessageType(strings.TrimSpace(payload.MessageType))
	if messageType == "" {
		messageType = AgentMessageTypeProgress
	}
	// The durable mailbox row and the AgentNotifyEvent share one (kind,
	// subtype) source so a restored session badges the notice exactly as the
	// live card did (see the TUI's subAgentMailboxCardTitle). Progress stays
	// the default: kind is an optional hint, and without a value the row must
	// keep its progress snapshot routing.
	kind := SubAgentMailboxKind(strings.TrimSpace(payload.Kind))
	if kind == "" {
		kind = SubAgentMailboxKindProgress
	}
	notifyMsg := &SubAgentMailboxMessage{
		AgentID:        evt.SourceID,
		TaskID:         taskIDForSub(sub),
		OwnerAgentID:   ownerAgentID,
		OwnerTaskID:    ownerTaskID,
		InReplyTo:      firstReplyMessageID(sub),
		Kind:           kind,
		MessageType:    messageType,
		Subtype:        strings.TrimSpace(payload.Subtype),
		CorrelationID:  strings.TrimSpace(payload.CorrelationID),
		MessagePayload: append(json.RawMessage(nil), payload.Payload...),
		Priority:       SubAgentMailboxPriorityNotify,
		Summary:        msg,
		Payload:        msg,
		RequiresAck:    false,
		// A notify's kind/subtype describe what the worker says about itself;
		// they are display metadata, never a trusted lifecycle event. Mark the
		// row report-only so the registry keeps mirroring the notice's text but
		// never flips the task to completed/blocked/decision_required on a
		// running worker's say-so (see syncTaskRecordFromMailbox).
		ReportOnly: true,
	}
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: notifyMsg})
	if strings.TrimSpace(notifyMsg.MessageID) == "" {
		notifyMsg.MessageID = a.nextSubAgentMailboxMessageID(evt.SourceID)
	}
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       evt.SourceID,
		TaskID:        sub.taskID,
		AgentType:     sub.agentDefName,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		TargetAgentID: controlPlaneAgentID(ownerAgentID),
		TargetTaskID:  ownerTaskID,
		Kind:          string(kind),
		Subtype:       strings.TrimSpace(payload.Subtype),
		Message:       msg,
	})
	a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "running", Message: msg})
	log.Debugf("SubAgent report received agent=%v message_len=%v", evt.SourceID, len(msg))
}

func (a *MainAgent) handleEscalate(evt Event) {
	payload, ok := evt.Payload.(tools.AgentRequestPayload)
	if !ok {
		log.Errorf("handleEscalate: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	reason := strings.TrimSpace(payload.Reason)
	log.Infof("SubAgent escalated to owner agent source=%v reason=%v", evt.SourceID, reason)
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		log.Debugf("dropping escalate from abandoned subagent agent_id=%v", evt.SourceID)
		return
	}
	request, err := a.createAgentRequest(sub, payload)
	if err != nil {
		a.queueLoopEvent(Event{Type: EventAgentError, SourceID: evt.SourceID, Payload: fmt.Errorf("persist agent request: %w", err)})
		return
	}
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: evt.SourceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateWaitingMain, Summary: reason, InstanceID: sub.instanceID, TaskID: sub.taskID},
	})
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()
	a.releaseSubAgentSlot(sub)
	a.emitActivity(evt.SourceID, ActivityIdle, "")
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: &SubAgentMailboxMessage{
		AgentID:       evt.SourceID,
		TaskID:        taskIDForSub(sub),
		OwnerAgentID:  ownerAgentID,
		OwnerTaskID:   ownerTaskID,
		MessageID:     request.RequestMessageID,
		Kind:          SubAgentMailboxKindDecisionRequired,
		MessageType:   AgentMessageTypeRequest,
		Subtype:       agentMessageSubtypeDecision,
		CorrelationID: request.CorrelationID,
		Priority:      SubAgentMailboxPriorityInterrupt,
		Summary:       reason,
		Payload:       reason,
		RequiresAck:   true,
	}})
	a.parkSubAgent(evt.SourceID)
}

func (a *MainAgent) handleAgentLog(evt Event) {
	msg, _ := evt.Payload.(string)
	log.Debugf("SubAgent log agent=%v message=%v", evt.SourceID, msg)
	a.emitToTUI(InfoEvent{Message: msg, AgentID: evt.SourceID})
}

func (a *MainAgent) handleJobFinished(evt Event) {
	payload, ok := evt.Payload.(*tools.JobFinishedPayload)
	if !ok || payload == nil {
		log.Errorf("handleJobFinished: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	backgroundID := payload.EffectiveID()

	// A job that finished after a session switch must not be written into the
	// new session's transcript. The completion event carries the session the
	// job was started in; an empty value means the sender could not determine
	// it, in which case delivery proceeds rather than dropping the result.
	if payload.SessionDir != "" && payload.SessionDir != a.SessionDir() {
		log.Debugf("handleJobFinished: dropping cross-session completion background_id=%v job_session=%v active_session=%v", backgroundID, payload.SessionDir, a.SessionDir())
		return
	}

	// A terminal result already surfaced to the model (a foreground result or a
	// terminal job_output read) must not be delivered again as a background
	// completion. An unknown id still claims successfully so a replayed or
	// evicted notification is never silently dropped.
	if !tools.ClaimJobReported(backgroundID) {
		return
	}

	// Resolve the owner: "" and the main agent's own instanceID belong to the
	// main transcript, a live sub-agent owns its own AgentID, and an owner that
	// already terminated falls back to the main transcript below.
	var sub *SubAgent
	if payload.AgentID != "" && payload.AgentID != a.instanceID {
		a.subs.mu.RLock()
		sub = a.subs.subAgents[payload.AgentID]
		a.subs.mu.RUnlock()
	}
	if sub == nil && payload.AgentID != "" && payload.AgentID != a.instanceID {
		log.Warnf("handleJobFinished: owner subagent not found, attributing to main agent_id=%v background_id=%v", payload.AgentID, backgroundID)
	}

	// Every finished job becomes one durable background_result mailbox row,
	// persisted before it is shown. The owner's transcript receives it at the
	// next request boundary (immediately when the owner is idle), so a result
	// queued behind a busy turn survives a restart or a session switch and is
	// replayed by restore. The JOB RESULT card is emitted only once that
	// transcript append is durable, so a queued-but-undelivered result shows up
	// in the pending area instead of as a card with no backing message.
	mailbox := SubAgentMailboxMessage{
		Kind:        SubAgentMailboxKindBackgroundResult,
		Priority:    SubAgentMailboxPriorityNotify,
		MessageType: AgentMessageTypeNotice,
		Summary:     backgroundResultContent(payload),
	}
	if sub != nil {
		mailbox.OwnerAgentID = sub.instanceID
		mailbox.OwnerTaskID = taskIDForSub(sub)
	}
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Background job %s finished", backgroundID), Level: backgroundCompletionToastLevel(payload.Status), AgentID: payload.AgentID})
	a.enqueueSubAgentMailbox(mailbox)
	if sub == nil && a.turn == nil && !a.mailboxDeliveryPaused.Load() {
		a.drainSubAgentInbox()
	}
}

// backgroundResultContent is the exact text stored on the durable
// background_result row and later appended to the owner's transcript, so the
// live card and the restored card derive from the same raw text. The job
// registry is the only producer and always fills Message with the completion
// report, so there is no fallback format to reconcile.
func backgroundResultContent(payload *tools.JobFinishedPayload) string {
	return strings.TrimSpace(payload.Message)
}

func backgroundCompletionToastLevel(status string) string {
	lower := strings.ToLower(strings.TrimSpace(status))
	if strings.Contains(lower, "cancel") {
		return "warn"
	}
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "timed out") || strings.Contains(lower, "exit status") {
		return "error"
	}
	return "info"
}

// getOrCreateAgentMCP returns the private MCP tools declared by one agent
// definition. Instances of that definition share connections; other agent
// definitions may use the same local server names independently.
func (a *MainAgent) getOrCreateAgentMCP(agentName string, mcpCfg config.MCPConfig) ([]tools.Tool, error) {
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	if a.mcpServerCache == nil {
		a.mcpServerCache = make(map[string]*mcpServerEntry)
	}
	for name := range mcpCfg {
		if _, inherited := a.mcpServerCache[mainMCPServerCacheKey(name)]; inherited {
			return nil, fmt.Errorf("agent %q declares MCP server %q, but that name is already defined by project/global MCP config; remove the agent entry to inherit it or rename the agent server", agentName, name)
		}
		if mcpCfg[name].Manual {
			return nil, fmt.Errorf("agent %q MCP server %q sets manual: true, but agent-scoped MCP servers cannot be enabled at runtime; remove manual or configure the server in project/global MCP config", agentName, name)
		}
	}
	connectCtx, connectCancel := context.WithTimeout(a.parentCtx, 30*time.Second)
	defer connectCancel()
	var extra []tools.Tool
	for name, sc := range mcpCfg {
		key := agentMCPServerCacheKey(agentName, name)
		if entry, ok := a.mcpServerCache[key]; ok {
			extra = append(extra, entry.Tools...)
			continue
		}
		cfg := mcp.ServerConfig{Name: name, Command: sc.Command, Args: sc.Args, Env: sc.Env, URL: sc.URL, Headers: sc.Headers, AllowedTools: sc.AllowedTools}
		mgr, err := mcp.NewManagerWithClientInfo(connectCtx, []mcp.ServerConfig{cfg}, a.mcpClientInfo)
		if err != nil {
			log.Warnf("failed to create MCP manager for server server=%v error=%v", name, err)
			continue
		}
		discovered, err := mcp.DiscoverAllTools(connectCtx, mgr)
		if err != nil {
			log.Warnf("failed to discover MCP tools for server server=%v error=%v", name, err)
			mgr.Close()
			continue
		}
		a.mcpServerCache[key] = &mcpServerEntry{Mgr: mgr, Tools: discovered}
		log.Debugf("subagent MCP server connected server=%v tools=%v", name, len(discovered))
		extra = append(extra, discovered...)
	}
	return extra, nil
}

func (a *MainAgent) CreateSubAgent(ctx context.Context, description, agentType string, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (result tools.TaskHandle, resultErr error) {
	requestCtx := ctx
	caller, err := a.canCallerDelegate(ctx)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	if a.shuttingDown.Load() || a.admissionPaused.Load() {
		return tools.TaskHandle{}, fmt.Errorf("cannot delegate during shutdown or session transition")
	}
	admissionEpoch := a.admissionEpoch.Load()
	agentType = strings.TrimSpace(agentType)
	if !delegateAgentAvailable(caller.Ruleset, agentType) {
		return tools.TaskHandle{}, fmt.Errorf("agent type %q is denied by Delegate permission policy", agentType)
	}
	maxChildren := effectiveDirectActiveChildLimit(caller.Delegation)
	n := a.adhocSeq.Add(1)
	taskID := fmt.Sprintf("adhoc-%d", n)
	planTaskRef = strings.TrimSpace(planTaskRef)
	// Only an explicitly supplied semantic_task_key is a confirmed duplicate
	// identity; the key derived from the description is a heuristic that may
	// only hint at duplicates (see duplicateOrConflictingTaskRecord).
	semanticKeyExplicit := strings.TrimSpace(semanticTaskKey) != ""
	semanticTaskKey = resolveSemanticTaskKey(semanticTaskKey, description)
	expectedWriteScope = expectedWriteScope.Normalized()
	agentDef, err := a.resolveAgentDef(agentType)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	admission := &subAgentAdmission{
		taskID:             taskID,
		ownerAgentID:       caller.AgentID,
		ownerTaskID:        caller.TaskID,
		agentType:          agentType,
		planTaskRef:        planTaskRef,
		semanticTaskKey:    semanticTaskKey,
		expectedWriteScope: expectedWriteScope,
	}
	admissionStartedAt := time.Now()
	a.admissionMu.Lock()
	a.orchestrationMetrics.recordAdmissionWait(time.Since(admissionStartedAt))
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch || requestCtx.Err() != nil {
		a.admissionMu.Unlock()
		if err := requestCtx.Err(); err != nil {
			return tools.TaskHandle{}, err
		}
		return tools.TaskHandle{}, fmt.Errorf("delegate invalidated by session or lifecycle change")
	}
	a.subs.mu.Lock()
	if !caller.IsMain {
		owner := a.subs.subAgents[caller.AgentID]
		if owner == nil || strings.TrimSpace(owner.taskID) != strings.TrimSpace(caller.TaskID) || !isNonTerminalTaskState(string(owner.State())) {
			a.subs.mu.Unlock()
			a.admissionMu.Unlock()
			return tools.TaskHandle{}, fmt.Errorf("delegate owner task is no longer active")
		}
	}
	count := a.directNonTerminalChildCountLocked(caller.AgentID, caller.TaskID)
	if count >= maxChildren {
		a.subs.mu.Unlock()
		a.admissionMu.Unlock()
		return tools.TaskHandle{
			Status:  "child_limit_reached",
			Message: fmt.Sprintf("direct non-terminal child limit reached (max_children=%d)", maxChildren),
		}, nil
	}
	existing, duplicate, conflict := a.findDuplicateOrConflictingTaskLocked(caller.AgentID, caller.TaskID, agentType, planTaskRef, semanticTaskKey, semanticKeyExplicit, expectedWriteScope)
	conflictRecord := (*DurableTaskRecord)(nil)
	if conflict {
		conflictRecord = existing
	}
	// Only a confirmed duplicate — the identical explicit semantic_task_key —
	// rejects the delegation here. A write-scope conflict is advisory: declared
	// scopes gate nothing, so two non-terminal tasks may overlap and still both
	// be created; the started handle points at the other task so the caller can
	// serialize, coordinate shared edits, or move the new work to its own
	// worktree. A probable duplicate (the description-derived fallback key or a
	// plan_task_ref match without an explicit key) also falls through and
	// creates a real task: that key is a heuristic, and whether the two
	// descriptions really are one deliverable is the model's decision. A
	// probable match must also never join a pending admission, so it never
	// waits for another task's handle.
	rejected := existing != nil && duplicate == taskDuplicateExplicitKey
	var pendingDuplicate *subAgentAdmission
	if !rejected {
		// A probable registry match (or no registry match at all) still leaves
		// pending admissions to screen: a rejecting pending admission — the
		// identical explicit semantic_task_key — dominates, and only such a
		// pending match may later be waited on. A pending scope conflict or
		// probable match annotates the started handle instead.
		var pendingExisting *DurableTaskRecord
		var pendingDup taskDuplicateDisposition
		var pendingConflict bool
		pendingExisting, pendingDup, pendingConflict, pendingDuplicate = a.findPendingDuplicateOrConflictingTaskLocked(caller.AgentID, caller.TaskID, agentType, planTaskRef, semanticTaskKey, semanticKeyExplicit, expectedWriteScope)
		if pendingExisting != nil && pendingDup == taskDuplicateExplicitKey {
			rejected = true
		} else if pendingExisting != nil {
			if existing == nil {
				existing, duplicate = pendingExisting, pendingDup
			}
			if pendingConflict && conflictRecord == nil {
				conflictRecord = pendingExisting
			}
		}
	}
	probableDuplicate := !rejected && existing != nil && duplicate == taskDuplicateProbable
	if rejected {
		a.subs.mu.Unlock()
		a.admissionMu.Unlock()
		if pendingDuplicate != nil {
			select {
			case <-pendingDuplicate.done:
				return pendingDuplicate.result, pendingDuplicate.err
			case <-requestCtx.Done():
				return tools.TaskHandle{}, requestCtx.Err()
			case <-a.parentCtx.Done():
				return tools.TaskHandle{}, a.parentCtx.Err()
			}
		}
		return duplicateTaskHandle(existing), nil
	}
	if conflictRecord != nil {
		a.orchestrationMetrics.scopeConflicts.Add(1)
	}
	if a.llmFactory == nil {
		a.subs.mu.Unlock()
		a.admissionMu.Unlock()
		return tools.TaskHandle{}, fmt.Errorf("LLM client factory not configured; call SetLLMFactory before creating SubAgents")
	}
	if a.governor != nil && a.governor.tryAcquireRuntime() {
		admission.slotHeld = true
	} else {
		a.subs.mu.Unlock()
		a.admissionMu.Unlock()
		a.warnOnRuntimeSlotDrift("CreateSubAgent capacity refusal")
		return tools.TaskHandle{}, fmt.Errorf("max concurrent agents reached (cap=%d), wait for a running agent to complete", cap(a.sem))
	}
	a.subs.addAdmissionLocked(admission)
	a.subs.mu.Unlock()
	a.admissionMu.Unlock()
	admissionCommitted := false
	defer func() {
		if !admissionCommitted {
			a.releaseSubAgentAdmission(admission)
		}
		admission.complete(result, resultErr)
	}()
	subLLMClient := a.llmFactory("", a.effectiveSubAgentModels(agentDef), agentDef.Variant)
	clientCommitted := false
	defer func() {
		if !clientCommitted && subLLMClient != nil {
			subLLMClient.Close()
		}
	}()
	a.applyServiceTierToClient(subLLMClient)
	var extraMCPTools []tools.Tool
	if len(agentDef.MCP) > 0 {
		extraMCPTools, err = a.getOrCreateAgentMCP(agentDef.Name, agentDef.MCP)
		if err != nil {
			return tools.TaskHandle{}, err
		}
	}
	instanceID := NextInstanceID(agentDef.Name)
	subCtx, cancel := context.WithCancel(a.parentCtx)
	subCfg := a.baseSubAgentConfig(agentDef, instanceID, subLLMClient, subCtx, cancel, extraMCPTools)
	subCfg.TaskID = taskID
	subCfg.TaskDesc = description
	subCfg.PlanTaskRef = planTaskRef
	subCfg.SemanticKey = semanticTaskKey
	subCfg.WriteScope = expectedWriteScope
	subCfg.OwnerAgentID = caller.AgentID
	subCfg.OwnerTaskID = caller.TaskID
	subCfg.Depth = caller.Depth + 1
	subCfg.JoinToOwner = !caller.IsMain && caller.Delegation.ChildJoinEnabled()
	sub := NewSubAgent(subCfg)
	admissionStartedAt = time.Now()
	a.admissionMu.Lock()
	a.orchestrationMetrics.recordAdmissionWait(time.Since(admissionStartedAt))
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch || requestCtx.Err() != nil {
		a.admissionMu.Unlock()
		cancel()
		if err := requestCtx.Err(); err != nil {
			return tools.TaskHandle{}, err
		}
		return tools.TaskHandle{}, fmt.Errorf("delegate invalidated by session or lifecycle change")
	}
	a.subs.mu.Lock()
	if a.subs.admissions[taskID] != admission {
		a.subs.mu.Unlock()
		a.admissionMu.Unlock()
		cancel()
		return tools.TaskHandle{}, fmt.Errorf("delegate admission was cancelled")
	}
	if !caller.IsMain {
		owner := a.subs.subAgents[caller.AgentID]
		if owner == nil || strings.TrimSpace(owner.taskID) != strings.TrimSpace(caller.TaskID) || !isNonTerminalTaskState(string(owner.State())) {
			a.subs.mu.Unlock()
			a.admissionMu.Unlock()
			cancel()
			return tools.TaskHandle{}, fmt.Errorf("delegate owner task is no longer active")
		}
	}
	if !sub.InjectUserMessage(description) {
		a.subs.mu.Unlock()
		a.admissionMu.Unlock()
		cancel()
		return tools.TaskHandle{}, fmt.Errorf("SubAgent %s rejected its initial task", sub.instanceID)
	}
	initialMessageCommitted := false
	defer func() {
		if !initialMessageCommitted {
			sub.removeInitialUserMessage()
		}
	}()
	registrationSessionDir := a.sessionDir
	registrationRecord := buildTaskRecordFromSub(sub, nil, "", a.explicitUserTurnCount.Load(), time.Now())
	a.subs.mu.Unlock()

	persistErr := a.persistSubAgentRegistration(registrationSessionDir, sub, registrationRecord)
	if persistErr != nil {
		_ = os.Remove(subAgentMetaPath(registrationSessionDir, sub.instanceID))
		a.admissionMu.Unlock()
		cancel()
		return tools.TaskHandle{}, fmt.Errorf("persist initial durable task registration: %w", persistErr)
	}

	a.subs.mu.Lock()
	if a.subs.admissions[taskID] != admission || requestCtx.Err() != nil {
		a.subs.mu.Unlock()
		_ = os.Remove(subAgentMetaPath(registrationSessionDir, sub.instanceID))
		_ = a.persistTaskRegistryRecord(registrationSessionDir, taskID, nil)
		a.admissionMu.Unlock()
		cancel()
		if err := requestCtx.Err(); err != nil {
			return tools.TaskHandle{}, err
		}
		return tools.TaskHandle{}, fmt.Errorf("delegate admission was cancelled after persistence")
	}
	a.subs.removeAdmissionLocked(taskID)
	admission.slotHeld = false
	sub.semMu.Lock()
	sub.semHeld = true
	sub.semMu.Unlock()
	a.publishSubAgentLocked(sub, taskID, registrationRecord)
	a.subs.mu.Unlock()
	a.persistSubAgentRecoverySnapshot(sub, taskID)
	admissionCommitted = true
	clientCommitted = true
	initialMessageCommitted = true
	a.admissionMu.Unlock()

	sub.startRunLoop()
	sub.armStartupWatchdog()
	log.Infof("SubAgent created and started instance=%v task_id=%v agent_def=%v", instanceID, taskID, agentDef.Name)
	a.emitToTUI(AgentStartedEvent{
		AgentID:       instanceID,
		TaskID:        taskID,
		AgentType:     agentDef.Name,
		Description:   description,
		ParentAgentID: controlPlaneAgentID(caller.AgentID),
		ParentTaskID:  caller.TaskID,
	})
	a.emitToTUI(AgentStatusEvent{AgentID: instanceID, Status: "running", Message: fmt.Sprintf("Started task %s: %s", taskID, truncateString(description, 80))})
	handle := tools.TaskHandle{
		Status:             "started",
		TaskID:             taskID,
		AgentID:            instanceID,
		Message:            "running in background",
		PlanTaskRef:        planTaskRef,
		SemanticTaskKey:    semanticTaskKey,
		ExpectedWriteScope: expectedWriteScope,
	}
	if probableDuplicate {
		handle = duplicateHintedTaskHandle(handle, existing, pendingDuplicate)
	}
	if conflictRecord != nil {
		if probableDuplicate {
			// The collision is probably the same deliverable, whose owner is
			// told to notify the earlier task; keep that guidance and surface
			// the overlap as a flag only.
			handle.ScopeConflict = true
		} else {
			handle = scopeConflictHintedTaskHandle(handle, conflictRecord)
		}
	}
	return handle, nil
}

// publishSubAgentLocked makes sub visible to the runtime (the live-agent
// registry, the durable task record, and the task-change signal). The caller
// must hold a.subs.mu. This is the shared commit step of CreateSubAgent and
// rehydrateTaskAsActivationLeader: both call it only after their durable
// registration writes have succeeded and their admission/activation slot
// ownership has been transferred, so from this point on nothing is rolled back
// and the two failure paths stay symmetric by construction.
func (a *MainAgent) publishSubAgentLocked(sub *SubAgent, taskID string, record *DurableTaskRecord) {
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.taskRecords[taskID] = cloneDurableTaskRecord(record)
	a.subs.notifyTaskChangeLocked()
}

// persistSubAgentRecoverySnapshot best-effort persists a recovery snapshot
// that reflects sub's publish. A write failure must not fail the surrounding
// creation or reactivation: the task registry and the instance meta file are
// the durable source of truth for restore, and the next saveRecoverySnapshot
// rewrites the snapshot from live state, so the only cost of a failed write is
// a wider pre-existing crash-to-next-snapshot window. Log instead of rolling
// the already-published sub back (rolling back would also need to release the
// runtime slot, which is exactly the asymmetric path that used to leak it).
func (a *MainAgent) persistSubAgentRecoverySnapshot(sub *SubAgent, taskID string) {
	if a.recoveryManager() == nil {
		return
	}
	if err := a.persistSnapshotLocked(a.buildRecoverySnapshot); err != nil {
		log.Warnf("failed to persist recovery snapshot after publishing SubAgent instance=%v task_id=%v error=%v", sub.instanceID, taskID, err)
	}
}

func (a *MainAgent) subAgentByID(agentID string) *SubAgent {
	return a.subs.subAgent(agentID)
}

func taskIDForSub(sub *SubAgent) string {
	if sub == nil {
		return ""
	}
	return sub.taskID
}

// agentDefConfigForRole mirrors resolveAgentDef's lookup (registered config
// first, built-in fallback) without its validation errors, and takes a state
// snapshot so callers inside registry locks can classify a role from a record's
// AgentDefName without racing config replacement. Returns nil when the role has
// no config document.
func (a *MainAgent) agentDefConfigForRole(agentType string) *config.AgentConfig {
	if a == nil || strings.TrimSpace(agentType) == "" {
		return nil
	}
	if cfg := a.snapshotAgentConfigByName(agentType); cfg != nil {
		return cfg
	}
	return config.BuiltinAgentConfigs()[agentType]
}

// rulesetRegistersNoFileWriteTools reports whether the effective permission
// ruleset keeps every canonical file-modifying tool (write, edit, delete,
// apply_patch) out of the worker's registry. A role that denies all four
// cannot modify files through file tools, which is what makes an empty write
// scope safe for its delegates and what keeps its tasks from conflicting with
// any other task over file paths.
func rulesetRegistersNoFileWriteTools(ruleset permission.Ruleset) bool {
	for _, name := range []string{tools.NameWrite, tools.NameEdit, tools.NameApplyPatch, tools.NameDelete} {
		if !ruleset.IsDisabled(name) {
			return false
		}
	}
	return true
}

// agentRoleRegistersNoFileWriteTools reports whether the agent definition's
// role registers no file-modifying tool under the current effective ruleset.
// Unknown or unresolvable roles default to false (write-capable) so the
// conservative path-scope and overlap semantics apply.
func (a *MainAgent) agentRoleRegistersNoFileWriteTools(agentType string) bool {
	cfg := a.agentDefConfigForRole(agentType)
	if cfg == nil {
		return false
	}
	return rulesetRegistersNoFileWriteTools(a.buildSubAgentRuleset(cfg))
}

// AgentRoleRegistersNoFileWriteTools implements tools.AgentFileWriteSurface so
// the Delegate tool can accept an empty expected_write_scope when the chosen
// agent_type's role registers no file-modifying tools.
func (a *MainAgent) AgentRoleRegistersNoFileWriteTools(agentType string) bool {
	return a.agentRoleRegistersNoFileWriteTools(agentType)
}

// taskScopesConflict reports whether two delegated tasks would run as
// concurrent writers over an overlapping boundary. A task whose role registers
// no file-modifying tools never writes files, so it conflicts with nothing; a
// write-capable task conflicts with another write-capable task exactly when
// their boundaries overlap, where an empty scope is the conservative
// whole-workspace boundary.
func (a *MainAgent) taskScopesConflict(agentTypeA string, scopeA tools.WriteScope, agentTypeB string, scopeB tools.WriteScope, baseDir string) bool {
	if a.agentRoleRegistersNoFileWriteTools(agentTypeA) || a.agentRoleRegistersNoFileWriteTools(agentTypeB) {
		return false
	}
	return writeScopesOverlap(scopeA, scopeB, baseDir)
}

func (a *MainAgent) resolveAgentDef(agentType string) (*config.AgentConfig, error) {
	if agentType == "" {
		available := a.resolveAvailableAgents()
		names := make([]string, 0, len(available))
		for _, ac := range available {
			names = append(names, ac.Name)
		}
		return nil, fmt.Errorf("agent_type is required; available types: %v", names)
	}
	var cfg *config.AgentConfig
	if a.agentConfigs != nil {
		if c, ok := a.agentConfigs[agentType]; ok {
			cfg = c
		}
	}
	if cfg == nil {
		builtins := config.BuiltinAgentConfigs()
		if c, ok := builtins[agentType]; ok {
			cfg = c
		}
	}
	if cfg == nil {
		available := a.resolveAvailableAgents()
		names := make([]string, 0, len(available))
		for _, ac := range available {
			names = append(names, ac.Name)
		}
		return nil, fmt.Errorf("unknown agent type %q; available subagent types: %v", agentType, names)
	}
	if !cfg.IsSubAgent() {
		return nil, fmt.Errorf("agent %q has mode %q and cannot be used as a SubAgent; only subagent-mode agents are allowed", agentType, cfg.Mode)
	}
	return cfg, nil
}

func (a *MainAgent) SetLLMFactory(fn func(systemPrompt string, agentModels []string, variant string) *llm.Client) {
	a.llmFactory = fn
}

func (a *MainAgent) HasAvailableSubAgents() bool {
	return len(a.resolveAvailableAgents()) > 0
}

func (a *MainAgent) AvailableSubAgents() []tools.AgentInfo {
	activeName := ""
	if cfg := a.currentActiveConfig(); cfg != nil {
		activeName = cfg.Name
	}
	return a.availableSubAgentInfosForRuleset(a.effectiveRuleset(), activeName)
}

func delegateAgentAvailable(ruleset permission.Ruleset, agentType string) bool {
	if len(ruleset) == 0 {
		return true
	}
	return ruleset.Evaluate(tools.NameDelegate, strings.TrimSpace(agentType)) != permission.ActionDeny
}

func (a *MainAgent) availableSubAgentInfosForRuleset(ruleset permission.Ruleset, excludedName string) []tools.AgentInfo {
	agents := a.availableSubAgentsForRuleset(ruleset, excludedName)
	infos := make([]tools.AgentInfo, 0, len(agents))
	for _, ac := range agents {
		infos = append(infos, tools.AgentInfo{
			Name:             ac.Name,
			Description:      ac.Description,
			Capabilities:     append([]string(nil), ac.Capabilities...),
			PreferredTasks:   append([]string(nil), ac.PreferredTasks...),
			WriteMode:        strings.TrimSpace(ac.WriteMode),
			DelegationPolicy: strings.TrimSpace(ac.DelegationPolicy),
		})
	}
	return infos
}

func (a *MainAgent) rebuildCachedSubAgents() {
	agents := a.resolveAvailableAgents()
	result := make([]*config.AgentConfig, 0, len(agents))
	result = append(result, agents...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	a.cachedSubMu.Lock()
	a.cachedSubAgents = result
	a.cachedSubMu.Unlock()
}

func (a *MainAgent) availableSubAgentsForPrompt() []*config.AgentConfig {
	excludedName := ""
	if cfg := a.currentActiveConfig(); cfg != nil {
		excludedName = cfg.Name
	}
	return a.availableSubAgentsForRuleset(a.effectiveRuleset(), excludedName)
}

func (a *MainAgent) availableSubAgentsForRuleset(ruleset permission.Ruleset, excludedName string) []*config.AgentConfig {
	a.cachedSubMu.RLock()
	defer a.cachedSubMu.RUnlock()
	agents := make([]*config.AgentConfig, 0, len(a.cachedSubAgents))
	for _, ac := range a.cachedSubAgents {
		if ac == nil || ac.Name == excludedName || !delegateAgentAvailable(ruleset, ac.Name) {
			continue
		}
		agents = append(agents, ac)
	}
	return agents
}

func (a *MainAgent) validFocusedSubAgent() *SubAgent {
	if a == nil {
		return nil
	}
	sub := a.focusedAgent.Load()
	if sub == nil {
		return nil
	}
	a.subs.mu.RLock()
	current, exists := a.subs.subAgents[sub.instanceID]
	a.subs.mu.RUnlock()
	if exists && current == sub {
		return sub
	}
	a.focusedAgent.CompareAndSwap(sub, nil)
	return nil
}

func (a *MainAgent) enqueueRegisteredSubAgent(sub *SubAgent, enqueue func(*SubAgent) bool) bool {
	return a.withRegisteredSubAgent(sub, enqueue)
}

func (a *MainAgent) withRegisteredSubAgent(sub *SubAgent, fn func(*SubAgent) bool) bool {
	if a == nil || sub == nil || fn == nil {
		return false
	}
	sub.lifecycleMu.Lock()
	defer sub.lifecycleMu.Unlock()
	registered := a.subs.withSubAgent(sub.instanceID, func(current *SubAgent) bool { return current == sub })
	return registered && fn(sub)
}

func (a *MainAgent) focusedDurableTask() *DurableTaskRecord {
	if a == nil {
		return nil
	}
	a.focusedTaskMu.RLock()
	taskID := a.focusedTaskID
	a.focusedTaskMu.RUnlock()
	return a.taskRecordByTaskID(taskID)
}

type focusedAgentSnapshot struct {
	sub    *SubAgent
	task   *DurableTaskRecord
	parked bool
	// settled marks a terminal task whose runtime is gone without ever being
	// parked (a crash between settlement and park): its transcript stays
	// readable, while actions must refuse it until the task is delegated anew.
	settled bool
}

func (a *MainAgent) focusedConversationTarget() ConversationTarget {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return ConversationTarget{AgentID: sub.instanceID, TaskID: sub.taskID}
	}
	if rec := a.focusedDurableTask(); rec != nil {
		return ConversationTarget{AgentID: rec.LatestInstanceID, TaskID: rec.TaskID}
	}
	return ConversationTarget{AgentID: identity.MainAgentID}
}

func (a *MainAgent) resolveConversationTarget(target ConversationTarget) (focusedAgentSnapshot, bool) {
	agentID := strings.TrimSpace(target.AgentID)
	taskID := strings.TrimSpace(target.TaskID)
	if taskID == "" && (agentID == "" || agentID == identity.MainAgentID) {
		return focusedAgentSnapshot{}, true
	}

	var rec *DurableTaskRecord
	if taskID != "" {
		if agentID != "" && agentID != identity.MainAgentID {
			if sub := a.subAgentByID(agentID); sub != nil {
				if strings.TrimSpace(sub.taskID) != taskID {
					return focusedAgentSnapshot{}, false
				}
				return focusedAgentSnapshot{sub: sub, task: a.taskRecordByTaskID(taskID)}, true
			}
		}
		rec = a.taskRecordByTaskID(taskID)
		if rec == nil || (agentID != "" && agentID != identity.MainAgentID && !durableTaskRecordIncludesInstance(rec, agentID)) {
			return focusedAgentSnapshot{}, false
		}
	} else {
		if sub := a.subAgentByID(agentID); sub != nil {
			return focusedAgentSnapshot{sub: sub, task: a.taskRecordByTaskID(sub.taskID)}, true
		}
		rec = a.taskRecordByInstanceID(agentID)
		if rec == nil {
			return focusedAgentSnapshot{}, false
		}
	}
	if sub := a.subAgentByTaskID(rec.TaskID); sub != nil {
		return focusedAgentSnapshot{sub: sub, task: rec}, true
	}
	if rec.RuntimeParked {
		return focusedAgentSnapshot{task: rec, parked: true}, true
	}
	if isTerminalSubAgentState(SubAgentState(strings.TrimSpace(rec.State))) {
		return focusedAgentSnapshot{task: rec, settled: true}, true
	}
	return focusedAgentSnapshot{}, false
}

func (a *MainAgent) focusedAgentSnapshot() focusedAgentSnapshot {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return focusedAgentSnapshot{sub: sub, task: a.taskRecordByTaskID(sub.taskID)}
	}
	if rec := a.focusedDurableTask(); rec != nil {
		if rec.RuntimeParked {
			return focusedAgentSnapshot{task: rec, parked: true}
		}
		if isTerminalSubAgentState(SubAgentState(strings.TrimSpace(rec.State))) {
			// A settled terminal task (runtime gone without ever being parked)
			// keeps its transcript readable. Facades must surface it explicitly —
			// falling back to the main agent here would show the main role's
			// model/context/usage data while the user views the settled worker.
			return focusedAgentSnapshot{task: rec, settled: true}
		}
	}
	return focusedAgentSnapshot{}
}

func (a *MainAgent) setFocusedTaskID(taskID string) {
	a.focusedTaskMu.Lock()
	a.focusedTaskID = strings.TrimSpace(taskID)
	a.focusedTaskMu.Unlock()
}

func (a *MainAgent) SwitchFocus(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || agentID == "main" {
		a.focusedAgent.Store(nil)
		a.setFocusedTaskID("")
		return
	}
	a.subs.mu.RLock()
	sub := a.subs.subAgents[agentID]
	a.subs.mu.RUnlock()
	if sub != nil {
		a.focusedAgent.Store(sub)
		a.setFocusedTaskID(sub.taskID)
		return
	}
	a.subs.mu.RLock()
	for taskID, rec := range a.subs.taskRecords {
		if rec != nil && rec.RuntimeParked && strings.TrimSpace(rec.LatestInstanceID) == agentID {
			a.subs.mu.RUnlock()
			a.focusedAgent.Store(nil)
			a.setFocusedTaskID(taskID)
			return
		}
	}
	a.subs.mu.RUnlock()
	if rec := a.taskRecordByInstanceID(agentID); rec != nil && (rec.RuntimeParked || isTerminalSubAgentState(SubAgentState(strings.TrimSpace(rec.State)))) {
		a.focusedAgent.Store(nil)
		a.setFocusedTaskID(rec.TaskID)
		return
	}
	a.focusedAgent.Store(nil)
	a.setFocusedTaskID("")
}

func (a *MainAgent) GetAllAgentsContextUsage() []AgentContextUsage {
	out := []AgentContextUsage{{AgentID: "main", ContextCurrent: a.ctxMgr.LastTotalContextTokens(), ContextLimit: a.ctxMgr.GetUsableInputBudget(), ContextMessageCount: a.ctxMgr.MessageCount()}}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	for id, sub := range a.subs.subAgents {
		cur, limit := sub.GetContextStats()
		out = append(out, AgentContextUsage{AgentID: id, ContextCurrent: cur, ContextLimit: limit, ContextMessageCount: sub.GetContextMessageCount()})
	}
	return out
}
