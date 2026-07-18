package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

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

func (a *MainAgent) baseSubAgentConfig(agentDef *config.AgentConfig, instanceID string, client *llm.Client, parentCtx context.Context, cancel context.CancelFunc, extraMCPTools []tools.Tool) SubAgentConfig {
	return SubAgentConfig{
		InstanceID:    instanceID,
		AgentDefName:  agentDef.Name,
		Delegation:    agentDef.Delegation,
		Color:         agentDef.Color,
		SystemPrompt:  agentDef.SystemPrompt,
		LLMClient:     client,
		Recovery:      a.recovery,
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
			WorkDir:    a.projectRoot,
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
		Ruleset:    append(permission.Ruleset(nil), sub.ruleset...),
		WriteScope: sub.writeScope.Normalized(),
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

func effectiveDirectActiveChildLimit(cfg config.DelegationConfig) int {
	limit := cfg.EffectiveMaxChildren()
	if limit > config.DefaultDelegationMaxChildren {
		return config.DefaultDelegationMaxChildren
	}
	return limit
}

func childWriteScopeWithinParent(parent, child tools.WriteScope, baseDir string) bool {
	parent = parent.Normalized()
	child = child.Normalized()
	if parent.Empty() {
		return true
	}
	if parent.ReadOnly {
		return child.ReadOnly
	}
	if child.Empty() || child.ReadOnly {
		return child.ReadOnly
	}
	for _, file := range child.Files {
		if !writeScopeAllowsPath(parent, normalizedScopeAbsPath(file, baseDir), baseDir) {
			return false
		}
	}
	for _, prefix := range child.PathPrefix {
		path := normalizedScopeAbsPath(prefix, baseDir)
		if !writeScopeAllowsPrefix(parent, path, baseDir) {
			return false
		}
	}
	for _, module := range child.Modules {
		if !slices.Contains(parent.Modules, module) {
			return false
		}
	}
	return true
}

func writeScopeAllowsPrefix(scope tools.WriteScope, targetPrefix, baseDir string) bool {
	for _, prefix := range scope.PathPrefix {
		if pathWithinScope(normalizedScopeAbsPath(prefix, baseDir), targetPrefix) {
			return true
		}
	}
	return false
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

func duplicateTaskHandle(existing *DurableTaskRecord, conflict bool) tools.TaskHandle {
	handle := tools.TaskHandle{
		Status:             "already_exists",
		TaskID:             existing.TaskID,
		AgentID:            existing.LatestInstanceID,
		Message:            "matching task already exists; continue it with Notify instead of creating a duplicate delegate",
		PlanTaskRef:        existing.PlanTaskRef,
		SemanticTaskKey:    existing.SemanticTaskKey,
		ExpectedWriteScope: existing.ExpectedWriteScope,
		SuggestedTaskID:    existing.TaskID,
		SuggestedAgentID:   existing.LatestInstanceID,
		SuggestedAction:    "notify_existing",
		DuplicateDetected:  !conflict,
		ScopeConflict:      conflict,
	}
	if conflict {
		handle.Status = "scope_conflict"
		handle.Message = "write scope overlaps with an existing live task; serialize or explicitly coordinate before delegating"
		handle.SuggestedAction = "serialize_or_notify_existing"
	}
	return handle
}

func (a *MainAgent) findPendingDuplicateOrConflictingTaskLocked(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (*DurableTaskRecord, bool, *subAgentAdmission) {
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
		if duplicate, conflict := duplicateOrConflictingTaskRecord(rec, ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey, expectedWriteScope, a.projectRoot); duplicate {
			return rec, conflict, pending
		}
	}
	return nil, false, nil
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
	maxDepth := caller.Delegation.EffectiveMaxDepth()
	if !caller.IsMain && caller.Depth >= maxDepth {
		return delegationCaller{}, fmt.Errorf("nested Delegate is not available at depth %d (max_depth=%d)", caller.Depth, maxDepth)
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
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: evt.SourceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateCompleted, Summary: result.Summary},
	})
	replyMessageID := firstReplyMessageID(sub)
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()

	a.releaseSubAgentSlot(sub)
	a.emitActivity(evt.SourceID, ActivityIdle, "")
	mailbox := &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       sub.taskID,
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    replyMessageID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      result.Summary,
		Payload:      result.Summary,
		Completion:   a.buildCompletionEnvelope(sub, result),
		RequiresAck:  false,
	}
	a.normalizeSubAgentMailboxMessage(mailbox)
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
		},
	})
}

func (a *MainAgent) handleAgentIdle(evt Event) {
	a.subs.mu.RLock()
	sub := a.subs.subAgents[evt.SourceID]
	a.subs.mu.RUnlock()
	if sub == nil {
		return
	}
	n := a.subs.incrementNudge(evt.SourceID)
	if n > maxIdleNudges {
		timeout, _ := evt.Payload.(time.Duration)
		a.queueLoopEvent(Event{Type: EventAgentError, SourceID: evt.SourceID, Payload: fmt.Errorf("SubAgent idle after %d nudges (timeout=%v each)", n, timeout)})
		return
	}
	message := "You appear to be idle. If the task is complete, call Complete with a summary. "
	switch {
	case sub.hasVisibleTool(tools.NameEscalate):
		message += "If you need help, call Escalate. "
	case sub.hasVisibleTool(tools.NameNotify):
		message += "If you need help or owner-agent input, use Notify because Escalate is unavailable in this role. "
	default:
		message += "If you are blocked and no control tool is available, explain the blocker clearly in assistant text. "
	}
	message += "If you are waiting for user input, continue waiting."
	if !sub.InjectUserMessage(message) {
		a.queueLoopEvent(Event{Type: EventAgentError, SourceID: evt.SourceID, Payload: fmt.Errorf("SubAgent idle nudge %d could not be queued within the configured input limits", n)})
		return
	}
	log.Infof("nudged idle SubAgent agent=%v nudge_count=%v", evt.SourceID, n)
}

func (a *MainAgent) handleAgentNotify(evt Event) {
	payload, ok := evt.Payload.(tools.AgentNotifyPayload)
	if !ok {
		if msg, legacyOK := evt.Payload.(string); legacyOK {
			payload = tools.AgentNotifyPayload{Message: msg}
			ok = true
		}
	}
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
	sub.setState(SubAgentStateRunning, msg)
	a.noteSubAgentStateTransition(sub, SubAgentStateRunning)
	a.persistSubAgentMeta(sub)
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()
	a.queueLoopEvent(Event{
		Type:     EventSubAgentProgressUpdated,
		SourceID: evt.SourceID,
		Payload:  &SubAgentProgressUpdatedPayload{Summary: msg},
	})
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       taskIDForSub(sub),
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    firstReplyMessageID(sub),
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      msg,
		Payload:      msg,
		RequiresAck:  false,
	}})
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       evt.SourceID,
		TaskID:        sub.taskID,
		AgentType:     sub.agentDefName,
		ParentAgentID: controlPlaneAgentID(ownerAgentID),
		ParentTaskID:  ownerTaskID,
		TargetAgentID: controlPlaneAgentID(ownerAgentID),
		TargetTaskID:  ownerTaskID,
		Kind:          strings.TrimSpace(payload.Kind),
		Message:       msg,
	})
	a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "running", Message: msg})
	log.Debugf("SubAgent report received agent=%v message_len=%v", evt.SourceID, len(msg))
}

func (a *MainAgent) handleEscalate(evt Event) {
	reason, ok := evt.Payload.(string)
	if !ok {
		log.Errorf("handleEscalate: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	log.Infof("SubAgent escalated to owner agent source=%v reason=%v", evt.SourceID, reason)
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		log.Debugf("dropping escalate from abandoned subagent agent_id=%v", evt.SourceID)
		return
	}
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: evt.SourceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateWaitingMain, Summary: reason},
	})
	replyMessageID := firstReplyMessageID(sub)
	ownerAgentID, ownerTaskID, _, _ := sub.ownerSnapshot()
	a.releaseSubAgentSlot(sub)
	a.emitActivity(evt.SourceID, ActivityIdle, "")
	a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       taskIDForSub(sub),
		OwnerAgentID: ownerAgentID,
		OwnerTaskID:  ownerTaskID,
		InReplyTo:    replyMessageID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      reason,
		Payload:      reason,
		RequiresAck:  true,
	}})
	a.parkSubAgent(evt.SourceID)
}

func (a *MainAgent) handleAgentLog(evt Event) {
	msg, _ := evt.Payload.(string)
	log.Debugf("SubAgent log agent=%v message=%v", evt.SourceID, msg)
	a.emitToTUI(InfoEvent{Message: msg, AgentID: evt.SourceID})
}

func (a *MainAgent) handleResetNudge(evt Event) {
	a.subs.resetNudge(evt.SourceID)
}

func (a *MainAgent) handleSpawnFinished(evt Event) {
	payload, ok := evt.Payload.(*tools.SpawnFinishedPayload)
	if !ok || payload == nil {
		log.Errorf("handleSpawnFinished: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	msg := strings.TrimSpace(payload.Message)
	backgroundID := payload.EffectiveID()
	if msg == "" {
		kind := strings.TrimSpace(payload.Kind)
		if kind == "" {
			kind = "job"
		}
		msg = fmt.Sprintf("Background %s %s finished (%s).", kind, backgroundID, payload.Status)
	}
	if payload.AgentID == "" || payload.AgentID == a.instanceID {
		content := a.mainBackgroundResultContent(payload)
		if a.turn != nil {
			a.emitToTUI(SpawnFinishedEvent{BackgroundID: backgroundID, AgentID: payload.AgentID, Kind: payload.Kind, Status: payload.Status, Command: payload.Command, Description: payload.Description, MaxRuntimeSec: payload.MaxRuntimeSec, Message: msg})
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Background %s %s finished", payload.Kind, backgroundID), Level: "info", AgentID: payload.AgentID})
			a.pendingUserMessages = enqueuePendingUserMessage(a.pendingUserMessages, pendingUserMessage{
				Content:     content,
				CoalesceKey: "main_background_completion",
			})
			return
		}
		a.newTurn()
		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		a.handleSpawnResultForMain(payload)
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "main")
		return
	}
	a.subs.mu.RLock()
	sub := a.subs.subAgents[payload.AgentID]
	a.subs.mu.RUnlock()
	if sub == nil {
		log.Warnf("handleSpawnFinished: owner subagent not found agent_id=%v background_id=%v", payload.AgentID, backgroundID)
		a.emitToTUI(SpawnFinishedEvent{BackgroundID: backgroundID, AgentID: payload.AgentID, Kind: payload.Kind, Status: payload.Status, Command: payload.Command, Description: payload.Description, MaxRuntimeSec: payload.MaxRuntimeSec, Message: msg})
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Background %s %s finished", payload.Kind, backgroundID), Level: "info", AgentID: payload.AgentID})
		return
	}
	content := strings.TrimSpace(payload.Message)
	if content == "" {
		content = fmt.Sprintf("[Background %s %s completed]\n\nDescription: %s\nStatus: %s", payload.Kind, backgroundID, payload.Description, payload.Status)
	}
	if !sub.TryEnqueueContextAppend(message.Message{Role: "user", Content: content}) {
		log.Warnf("handleSpawnFinished: subagent context append rejected agent_id=%v background_id=%v state=%v", payload.AgentID, backgroundID, sub.State())
	} else {
		sub.ContinueFromContext()
	}
	a.emitToTUI(SpawnFinishedEvent{BackgroundID: backgroundID, AgentID: payload.AgentID, Kind: payload.Kind, Status: payload.Status, Command: payload.Command, Description: payload.Description, MaxRuntimeSec: payload.MaxRuntimeSec, Message: msg})
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Background %s %s finished", payload.Kind, backgroundID), Level: "info", AgentID: payload.AgentID})
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
		cfg := mcp.ServerConfig{Name: name, Command: sc.Command, Args: sc.Args, Env: sc.Env, URL: sc.URL, AllowedTools: sc.AllowedTools}
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
	semanticTaskKey = strings.TrimSpace(semanticTaskKey)
	if semanticTaskKey == "" {
		semanticTaskKey = semanticTaskKeyFallback(planTaskRef)
	}
	expectedWriteScope = expectedWriteScope.Normalized()
	if !caller.IsMain && !childWriteScopeWithinParent(caller.WriteScope, expectedWriteScope, caller.WorkDir) {
		return tools.TaskHandle{}, fmt.Errorf("child expected_write_scope must not be broader than the parent SubAgent task scope")
	}
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
	existing, conflict := a.findDuplicateOrConflictingTaskLocked(caller.AgentID, caller.TaskID, agentType, planTaskRef, semanticTaskKey, expectedWriteScope)
	var pendingDuplicate *subAgentAdmission
	if existing == nil {
		existing, conflict, pendingDuplicate = a.findPendingDuplicateOrConflictingTaskLocked(caller.AgentID, caller.TaskID, agentType, planTaskRef, semanticTaskKey, expectedWriteScope)
	}
	if existing != nil {
		a.subs.mu.Unlock()
		if conflict {
			a.orchestrationMetrics.scopeConflicts.Add(1)
		}
		a.admissionMu.Unlock()
		if pendingDuplicate != nil && !conflict {
			select {
			case <-pendingDuplicate.done:
				return pendingDuplicate.result, pendingDuplicate.err
			case <-requestCtx.Done():
				return tools.TaskHandle{}, requestCtx.Err()
			case <-a.parentCtx.Done():
				return tools.TaskHandle{}, a.parentCtx.Err()
			}
		}
		return duplicateTaskHandle(existing, conflict), nil
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
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.taskRecords[taskID] = cloneDurableTaskRecord(registrationRecord)
	a.subs.mu.Unlock()
	if a.recovery != nil {
		if err := a.recovery.SaveSnapshot(a.buildRecoverySnapshot()); err != nil {
			a.subs.mu.Lock()
			delete(a.subs.subAgents, sub.instanceID)
			delete(a.subs.taskRecords, taskID)
			a.subs.mu.Unlock()
			_ = os.Remove(subAgentMetaPath(registrationSessionDir, sub.instanceID))
			_ = a.persistTaskRegistryRecord(registrationSessionDir, taskID, nil)
			a.admissionMu.Unlock()
			cancel()
			return tools.TaskHandle{}, fmt.Errorf("persist initial recovery snapshot: %w", err)
		}
	}
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
	return handle, nil
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
	return focusedAgentSnapshot{}, false
}

func (a *MainAgent) focusedAgentSnapshot() focusedAgentSnapshot {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return focusedAgentSnapshot{sub: sub, task: a.taskRecordByTaskID(sub.taskID)}
	}
	if rec := a.focusedDurableTask(); rec != nil && rec.RuntimeParked {
		return focusedAgentSnapshot{task: rec, parked: true}
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
	if rec := a.taskRecordByInstanceID(agentID); rec != nil && rec.RuntimeParked {
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
