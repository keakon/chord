package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
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
	IsMain     bool
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
			IsMain:     true,
		}, nil
	}
	sub := a.subAgentByID(callerAgentID)
	if sub == nil {
		return delegationCaller{}, fmt.Errorf("unknown caller agent %q", callerAgentID)
	}
	return delegationCaller{
		AgentID:    sub.instanceID,
		TaskID:     sub.taskID,
		Depth:      sub.depth,
		Delegation: sub.delegation,
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

func isActiveTaskState(state string) bool {
	return strings.TrimSpace(state) == string(SubAgentStateRunning)
}

func effectiveDirectActiveChildLimit(cfg config.DelegationConfig) int {
	limit := cfg.EffectiveMaxChildren()
	if limit > config.DefaultDelegationMaxChildren {
		return config.DefaultDelegationMaxChildren
	}
	return limit
}

func (a *MainAgent) directActiveChildCountLocked(ownerAgentID, ownerTaskID string) int {
	count := 0
	for _, rec := range a.taskRecords {
		if rec == nil {
			continue
		}
		if strings.TrimSpace(rec.OwnerAgentID) != strings.TrimSpace(ownerAgentID) {
			continue
		}
		if strings.TrimSpace(rec.OwnerTaskID) != strings.TrimSpace(ownerTaskID) {
			continue
		}
		if isActiveTaskState(rec.State) {
			count++
		}
	}
	return count
}

func (a *MainAgent) outstandingJoinChildTaskIDsLocked(taskID string) []string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	var out []string
	for _, rec := range a.taskRecords {
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
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.outstandingJoinChildTaskIDsLocked(taskID)
}

func (a *MainAgent) directChildTaskIDs(taskID string) []string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out []string
	for _, rec := range a.taskRecords {
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
		slog.Error("handleAgentDone: invalid payload type", "payload_type", fmt.Sprintf("%T", evt.Payload))
		return
	}

	a.mu.RLock()
	sub := a.subAgents[evt.SourceID]
	a.mu.RUnlock()
	if sub == nil {
		slog.Warn("handleAgentDone: unknown SubAgent", "source", evt.SourceID)
		return
	}
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: evt.SourceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateCompleted, Summary: result.Summary},
	})
	replyMessageID := firstReplyMessageID(sub)

	if focused := a.focusedAgent.Load(); focused != nil && focused.instanceID == evt.SourceID {
		a.focusedAgent.Store(nil)
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "done", Message: fmt.Sprintf("SubAgent %s completed; focus switched back to main", evt.SourceID)})
	}

	owner := a.subAgentByID(sub.ownerAgentID)
	transferredSlot := false
	if owner != nil && owner.State() == SubAgentStateWaitingDescendant && len(a.outstandingJoinChildTaskIDs(owner.taskID)) == 0 && sub.semHeld && !owner.semHeld {
		transferredSlot = a.transferSubAgentSlot(sub, owner)
	}
	if sub.semHeld && !transferredSlot {
		a.releaseSubAgentSlot(sub)
	}
	a.emitActivity(evt.SourceID, ActivityIdle, "")
	a.sendEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       sub.taskID,
		OwnerAgentID: sub.ownerAgentID,
		OwnerTaskID:  sub.ownerTaskID,
		InReplyTo:    replyMessageID,
		Kind:         SubAgentMailboxKindCompleted,
		Priority:     SubAgentMailboxPriorityUrgent,
		Summary:      result.Summary,
		Payload:      result.Summary,
		Completion:   a.buildCompletionEnvelope(sub, result),
		RequiresAck:  false,
	}})

	a.emitToTUI(AgentDoneEvent{AgentID: evt.SourceID, TaskID: sub.taskID, Summary: result.Summary})
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
	a.mu.RLock()
	sub := a.subAgents[evt.SourceID]
	a.mu.RUnlock()
	if sub == nil {
		return
	}
	a.nudgeCounts[evt.SourceID]++
	n := a.nudgeCounts[evt.SourceID]
	if n > maxIdleNudges {
		timeout, _ := evt.Payload.(time.Duration)
		a.sendEvent(Event{Type: EventAgentError, SourceID: evt.SourceID, Payload: fmt.Errorf("SubAgent idle after %d nudges (timeout=%v each)", n, timeout)})
		return
	}
	message := "You appear to be idle. If the task is complete, call Complete with a summary. "
	switch {
	case sub.hasVisibleTool("Escalate"):
		message += "If you need help, call Escalate. "
	case sub.hasVisibleTool("Notify"):
		message += "If you need help or owner-agent input, use Notify because Escalate is unavailable in this role. "
	default:
		message += "If you are blocked and no control tool is available, explain the blocker clearly in assistant text. "
	}
	message += "If you are waiting for user input, continue waiting."
	sub.InjectUserMessage(message)
	slog.Info("nudged idle SubAgent", "agent", evt.SourceID, "nudge_count", n)
}

func (a *MainAgent) handleAgentNotify(evt Event) {
	msg, ok := evt.Payload.(string)
	if !ok {
		slog.Error("handleAgentNotify: invalid payload type", "payload_type", fmt.Sprintf("%T", evt.Payload))
		return
	}
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		slog.Debug("dropping report from abandoned subagent", "agent_id", evt.SourceID)
		return
	}
	sub.setState(SubAgentStateRunning, msg)
	a.noteSubAgentStateTransition(sub, SubAgentStateRunning)
	a.persistSubAgentMeta(sub)
	a.sendEvent(Event{
		Type:     EventSubAgentProgressUpdated,
		SourceID: evt.SourceID,
		Payload:  &SubAgentProgressUpdatedPayload{Summary: msg},
	})
	a.sendEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       taskIDForSub(sub),
		OwnerAgentID: sub.ownerAgentID,
		OwnerTaskID:  sub.ownerTaskID,
		InReplyTo:    firstReplyMessageID(sub),
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      msg,
		Payload:      msg,
		RequiresAck:  false,
	}})
	a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "running", Message: msg})
	slog.Info("SubAgent report received", "agent", evt.SourceID, "message_len", len(msg))
}

func (a *MainAgent) handleEscalate(evt Event) {
	reason, ok := evt.Payload.(string)
	if !ok {
		slog.Error("handleEscalate: invalid payload type", "payload_type", fmt.Sprintf("%T", evt.Payload))
		return
	}
	slog.Info("SubAgent escalated to owner agent", "source", evt.SourceID, "reason", reason)
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		slog.Debug("dropping escalate from abandoned subagent", "agent_id", evt.SourceID)
		return
	}
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: evt.SourceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateWaitingPrimary, Summary: reason},
	})
	replyMessageID := firstReplyMessageID(sub)
	a.releaseSubAgentSlot(sub)
	a.emitActivity(evt.SourceID, ActivityIdle, "")
	a.sendEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: &SubAgentMailboxMessage{
		AgentID:      evt.SourceID,
		TaskID:       taskIDForSub(sub),
		OwnerAgentID: sub.ownerAgentID,
		OwnerTaskID:  sub.ownerTaskID,
		InReplyTo:    replyMessageID,
		Kind:         SubAgentMailboxKindDecisionRequired,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      reason,
		Payload:      reason,
		RequiresAck:  true,
	}})
}

func (a *MainAgent) handleAgentLog(evt Event) {
	msg, _ := evt.Payload.(string)
	slog.Info("SubAgent log", "agent", evt.SourceID, "message", msg)
	a.emitToTUI(InfoEvent{Message: msg, AgentID: evt.SourceID})
}

func (a *MainAgent) handleResetNudge(evt Event) {
	if _, ok := a.nudgeCounts[evt.SourceID]; ok {
		a.nudgeCounts[evt.SourceID] = 0
	}
}

func (a *MainAgent) handleSpawnFinished(evt Event) {
	payload, ok := evt.Payload.(*tools.SpawnFinishedPayload)
	if !ok || payload == nil {
		slog.Error("handleSpawnFinished: invalid payload type", "payload_type", fmt.Sprintf("%T", evt.Payload))
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
	a.mu.RLock()
	sub := a.subAgents[payload.AgentID]
	a.mu.RUnlock()
	if sub == nil {
		slog.Warn("handleSpawnFinished: owner subagent not found", "agent_id", payload.AgentID, "background_id", backgroundID)
		a.emitToTUI(SpawnFinishedEvent{BackgroundID: backgroundID, AgentID: payload.AgentID, Kind: payload.Kind, Status: payload.Status, Command: payload.Command, Description: payload.Description, MaxRuntimeSec: payload.MaxRuntimeSec, Message: msg})
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Background %s %s finished", payload.Kind, backgroundID), Level: "info", AgentID: payload.AgentID})
		return
	}
	content := strings.TrimSpace(payload.Message)
	if content == "" {
		content = fmt.Sprintf("[Background %s %s completed]\n\nDescription: %s\nStatus: %s", payload.Kind, backgroundID, payload.Description, payload.Status)
	}
	if !sub.TryEnqueueContextAppend(message.Message{Role: "user", Content: content}) {
		slog.Warn("handleSpawnFinished: subagent context append rejected", "agent_id", payload.AgentID, "background_id", backgroundID, "state", sub.State())
	} else {
		sub.ContinueFromContext()
	}
	a.emitToTUI(SpawnFinishedEvent{BackgroundID: backgroundID, AgentID: payload.AgentID, Kind: payload.Kind, Status: payload.Status, Command: payload.Command, Description: payload.Description, MaxRuntimeSec: payload.MaxRuntimeSec, Message: msg})
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Background %s %s finished", payload.Kind, backgroundID), Level: "info", AgentID: payload.AgentID})
}

// getOrCreateAgentMCP returns extra MCP tools for the servers declared in
// mcpCfg that are not already cached (including main-agent servers registered
// as sentinels). Each server is a singleton for the lifetime of MainAgent.
func (a *MainAgent) getOrCreateAgentMCP(mcpCfg config.MCPConfig) []tools.Tool {
	a.mcpServerCacheMu.Lock()
	defer a.mcpServerCacheMu.Unlock()
	if a.mcpServerCache == nil {
		a.mcpServerCache = make(map[string]*mcpServerEntry)
	}
	connectCtx, connectCancel := context.WithTimeout(a.parentCtx, 30*time.Second)
	defer connectCancel()
	var extra []tools.Tool
	for name, sc := range mcpCfg {
		if _, ok := a.mcpServerCache[name]; ok {
			if e := a.mcpServerCache[name]; e.Mgr != nil {
				extra = append(extra, e.Tools...)
			}
			continue
		}
		cfg := mcp.ServerConfig{Name: name, Command: sc.Command, Args: sc.Args, Env: sc.Env, URL: sc.URL, AllowedTools: sc.AllowedTools}
		mgr, err := mcp.NewManagerWithClientInfo(connectCtx, []mcp.ServerConfig{cfg}, a.mcpClientInfo)
		if err != nil {
			slog.Warn("failed to create MCP manager for server", "server", name, "error", err)
			continue
		}
		discovered, err := mcp.DiscoverAllTools(connectCtx, mgr)
		if err != nil {
			slog.Warn("failed to discover MCP tools for server", "server", name, "error", err)
			mgr.Close()
			continue
		}
		a.mcpServerCache[name] = &mcpServerEntry{Mgr: mgr, Tools: discovered}
		slog.Info("subagent MCP server connected", "server", name, "tools", len(discovered))
		extra = append(extra, discovered...)
	}
	return extra
}

func (a *MainAgent) CreateSubAgent(ctx context.Context, description, agentType string, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (tools.TaskHandle, error) {
	caller, err := a.canCallerDelegate(ctx)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	a.mu.RLock()
	count := a.directActiveChildCountLocked(caller.AgentID, caller.TaskID)
	a.mu.RUnlock()
	maxChildren := effectiveDirectActiveChildLimit(caller.Delegation)
	if count >= maxChildren {
		return tools.TaskHandle{
			Status:  "child_limit_reached",
			TaskID:  "",
			AgentID: "",
			Message: fmt.Sprintf("direct active child limit reached (max_children=%d)", maxChildren),
		}, nil
	}
	if existing, conflict := a.findDuplicateOrConflictingTask(caller.AgentID, caller.TaskID, agentType, planTaskRef, semanticTaskKey, expectedWriteScope); existing != nil {
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
		return handle, nil
	}
	select {
	case a.sem <- struct{}{}:
	default:
		return tools.TaskHandle{}, fmt.Errorf("max concurrent agents reached (cap=%d), wait for a running agent to complete", cap(a.sem))
	}
	n := a.adhocSeq.Add(1)
	taskID := fmt.Sprintf("adhoc-%d", n)
	planTaskRef = strings.TrimSpace(planTaskRef)
	semanticTaskKey = strings.TrimSpace(semanticTaskKey)
	if semanticTaskKey == "" {
		semanticTaskKey = semanticTaskKeyFallback(planTaskRef)
	}
	expectedWriteScope = expectedWriteScope.Normalized()
	agentDef, err := a.resolveAgentDef(agentType)
	if err != nil {
		<-a.sem
		return tools.TaskHandle{}, err
	}
	if a.llmFactory == nil {
		<-a.sem
		return tools.TaskHandle{}, fmt.Errorf("LLM client factory not configured; call SetLLMFactory before creating SubAgents")
	}
	subLLMClient := a.llmFactory("", agentDef.Models, agentDef.Variant)
	agentRuleset := a.effectiveRuleset()
	if agentDef.Permission.Kind != 0 {
		agentPermRules := permission.ParsePermission(&agentDef.Permission)
		agentRuleset = permission.Merge(agentRuleset, agentPermRules)
	}
	var extraMCPTools []tools.Tool
	if len(agentDef.MCP) > 0 {
		extraMCPTools = a.getOrCreateAgentMCP(agentDef.MCP)
	}
	instanceID := NextInstanceID(agentDef.Name)
	ctx, cancel := context.WithCancel(a.parentCtx)
	workDir, _ := os.Getwd()
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:    instanceID,
		TaskID:        taskID,
		AgentDefName:  agentDef.Name,
		TaskDesc:      description,
		PlanTaskRef:   planTaskRef,
		SemanticKey:   semanticTaskKey,
		WriteScope:    expectedWriteScope,
		OwnerAgentID:  caller.AgentID,
		OwnerTaskID:   caller.TaskID,
		Depth:         caller.Depth + 1,
		JoinToOwner:   !caller.IsMain && caller.Delegation.ChildJoinEnabled(),
		Delegation:    agentDef.Delegation,
		Color:         agentDef.Color,
		SystemPrompt:  agentDef.SystemPrompt,
		LLMClient:     subLLMClient,
		Recovery:      a.recovery,
		Parent:        a,
		ParentCtx:     ctx,
		Cancel:        cancel,
		BaseTools:     a.tools,
		ExtraMCPTools: extraMCPTools,
		Ruleset:       agentRuleset,
		WorkDir:       workDir,
		VenvPath:      a.cachedVenvPath,
		SessionDir:    a.sessionDir,
		AgentsMD:      a.cachedAgentsMDSnapshot(),
		Skills:        a.loadedSkillsSnapshot(),
		ModelName:     a.ModelName(),
	})
	sub.semHeld = true
	a.mu.Lock()
	a.subAgents[instanceID] = sub
	a.mu.Unlock()
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	go sub.runLoop()
	sub.InjectUserMessage(description)
	slog.Info("SubAgent created and started", "instance", instanceID, "task_id", taskID, "agent_def", agentDef.Name)
	a.emitToTUI(AgentStatusEvent{AgentID: instanceID, Status: "running", Message: fmt.Sprintf("Started task %s: %s", taskID, truncateString(description, 80))})
	return tools.TaskHandle{
		Status:             "started",
		TaskID:             taskID,
		AgentID:            instanceID,
		Message:            "running in background",
		PlanTaskRef:        planTaskRef,
		SemanticTaskKey:    semanticTaskKey,
		ExpectedWriteScope: expectedWriteScope,
	}, nil
}

func (a *MainAgent) subAgentByID(agentID string) *SubAgent {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.subAgents[agentID]
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
	agents := a.resolveAvailableAgents()
	infos := make([]tools.AgentInfo, 0, len(agents))
	for _, ac := range agents {
		if ac.Name == activeName {
			continue
		}
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
	a.cachedSubMu.RLock()
	defer a.cachedSubMu.RUnlock()
	return a.cachedSubAgents
}

func (a *MainAgent) validFocusedSubAgent() *SubAgent {
	if a == nil {
		return nil
	}
	sub := a.focusedAgent.Load()
	if sub == nil {
		return nil
	}
	a.mu.RLock()
	current, exists := a.subAgents[sub.instanceID]
	a.mu.RUnlock()
	if exists && current == sub {
		return sub
	}
	a.focusedAgent.CompareAndSwap(sub, nil)
	return nil
}

func (a *MainAgent) SwitchFocus(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || agentID == "main" {
		a.focusedAgent.Store(nil)
		return
	}
	a.mu.RLock()
	sub := a.subAgents[agentID]
	a.mu.RUnlock()
	if sub != nil {
		a.focusedAgent.Store(sub)
		return
	}
	a.focusedAgent.Store(nil)
}

func (a *MainAgent) GetAllAgentsContextUsage() []AgentContextUsage {
	out := []AgentContextUsage{{AgentID: "main", ContextCurrent: a.ctxMgr.LastTotalContextTokens(), ContextLimit: a.ctxMgr.GetMaxTokens(), ContextMessageCount: a.ctxMgr.MessageCount()}}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for id, sub := range a.subAgents {
		cur, limit := sub.GetContextStats()
		out = append(out, AgentContextUsage{AgentID: id, ContextCurrent: cur, ContextLimit: limit, ContextMessageCount: sub.GetContextMessageCount()})
	}
	return out
}
