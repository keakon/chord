package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) subAgentByTaskID(taskID string) *SubAgent {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, sub := range a.subAgents {
		if sub != nil && strings.TrimSpace(sub.taskID) == taskID {
			return sub
		}
	}
	return nil
}

func (a *MainAgent) acquireSubAgentSlot(sub *SubAgent) error {
	return a.acquireSubAgentSlotWithBypass(sub, false)
}

func (a *MainAgent) acquireWakeReactivationSlot(sub *SubAgent) error {
	return a.acquireSubAgentSlotWithBypass(sub, true)
}

func (a *MainAgent) acquireSubAgentSlotWithBypass(sub *SubAgent, allowBypass bool) error {
	if sub == nil || sub.semHeld {
		return nil
	}
	select {
	case a.sem <- struct{}{}:
		sub.semHeld = true
		sub.semBypassed = false
		return nil
	default:
		if allowBypass {
			sub.semHeld = true
			sub.semBypassed = true
			return nil
		}
		return fmt.Errorf("max concurrent agents reached (cap=%d), wait for a running agent to complete", cap(a.sem))
	}
}

func (a *MainAgent) releaseSubAgentSlot(sub *SubAgent) {
	if sub == nil || !sub.semHeld {
		return
	}
	if !sub.semBypassed {
		<-a.sem
	}
	sub.semHeld = false
	sub.semBypassed = false
}

func (a *MainAgent) transferSubAgentSlot(from, to *SubAgent) bool {
	if from == nil || to == nil || !from.semHeld || to.semHeld {
		return false
	}
	to.semHeld = true
	to.semBypassed = from.semBypassed
	from.semHeld = false
	from.semBypassed = false
	return true
}

func normalizeSubAgentMessage(kind, message string) string {
	message = strings.TrimSpace(message)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return message
	}
	return fmt.Sprintf("[%s] %s", kind, message)
}

func respondSubAgentControl(reply chan subAgentControlResult, handle tools.TaskHandle, err error) {
	if reply == nil {
		return
	}
	reply <- subAgentControlResult{Handle: handle, Err: err}
}

func (a *MainAgent) takeOutstandingMailboxForSub(sub *SubAgent) *SubAgentMailboxMessage {
	if a == nil || sub == nil {
		return nil
	}
	match := func(msg *SubAgentMailboxMessage) bool {
		if msg == nil {
			return false
		}
		return msg.AgentID == sub.instanceID || (strings.TrimSpace(msg.TaskID) != "" && msg.TaskID == sub.taskID)
	}
	removeFirstMatch := func(batch []*SubAgentMailboxMessage) ([]*SubAgentMailboxMessage, *SubAgentMailboxMessage) {
		for i, msg := range batch {
			if !match(msg) {
				continue
			}
			found := msg
			batch = append(batch[:i], batch[i+1:]...)
			return batch, found
		}
		return batch, nil
	}
	if len(a.activeSubAgentMailboxes) > 0 || match(a.activeSubAgentMailbox) {
		var msg *SubAgentMailboxMessage
		a.activeSubAgentMailboxes, msg = removeFirstMatch(a.activeSubAgentMailboxes)
		if msg == nil && match(a.activeSubAgentMailbox) {
			msg = a.activeSubAgentMailbox
		}
		if msg != nil {
			a.pendingSubAgentMailboxes, _ = removeFirstMatch(a.pendingSubAgentMailboxes)
			if len(a.activeSubAgentMailboxes) > 0 {
				a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
			} else {
				a.activeSubAgentMailbox = nil
				a.activeSubAgentMailboxAck = false
			}
			a.refreshSubAgentInboxSummary()
			return msg
		}
	}
	if len(a.pendingSubAgentMailboxes) > 0 {
		var msg *SubAgentMailboxMessage
		a.pendingSubAgentMailboxes, msg = removeFirstMatch(a.pendingSubAgentMailboxes)
		if msg != nil {
			a.refreshSubAgentInboxSummary()
			return msg
		}
	}
	for i := len(a.subAgentInbox.urgent) - 1; i >= 0; i-- {
		if !match(&a.subAgentInbox.urgent[i]) {
			continue
		}
		msg := a.subAgentInbox.urgent[i]
		a.subAgentInbox.urgent = append(a.subAgentInbox.urgent[:i], a.subAgentInbox.urgent[i+1:]...)
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	for i := len(a.subAgentInbox.normal) - 1; i >= 0; i-- {
		if !match(&a.subAgentInbox.normal[i]) {
			continue
		}
		msg := a.subAgentInbox.normal[i]
		a.subAgentInbox.normal = append(a.subAgentInbox.normal[:i], a.subAgentInbox.normal[i+1:]...)
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	return nil
}

func (a *MainAgent) canCallerControlTask(callerAgentID, callerTaskID, taskID string) (*DurableTaskRecord, error) {
	taskID = strings.TrimSpace(taskID)
	callerAgentID = strings.TrimSpace(callerAgentID)
	callerTaskID = strings.TrimSpace(callerTaskID)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	rec := a.taskRecordByTaskID(taskID)
	if rec == nil {
		return nil, fmt.Errorf("unknown task_id %q", taskID)
	}
	ownerAgentID := strings.TrimSpace(rec.OwnerAgentID)
	ownerTaskID := strings.TrimSpace(rec.OwnerTaskID)
	if ownerTaskID != "" {
		if ownerTaskID != callerTaskID {
			return nil, fmt.Errorf("task %s is owned by task %s; caller task %s is not allowed", taskID, ownerTaskID, blankToDefault(callerTaskID, "main"))
		}
		return rec, nil
	}
	if callerAgentID == "" {
		if ownerAgentID != "" {
			return nil, fmt.Errorf("task %s is owned by %s; only the direct owner may control it", taskID, ownerAgentID)
		}
		return rec, nil
	}
	if ownerAgentID != callerAgentID {
		return nil, fmt.Errorf("task %s is not owned by caller %s", taskID, callerAgentID)
	}
	return rec, nil
}

func (a *MainAgent) sendMessageToSubAgentNow(callerAgentID, callerTaskID, taskID, message, kind string) (tools.TaskHandle, error) {
	taskID = strings.TrimSpace(taskID)
	message = strings.TrimSpace(message)
	kind = strings.TrimSpace(kind)
	if taskID == "" {
		return tools.TaskHandle{}, fmt.Errorf("task_id is required")
	}
	if message == "" {
		return tools.TaskHandle{}, fmt.Errorf("message is required")
	}
	record, err := a.canCallerControlTask(callerAgentID, callerTaskID, taskID)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	sub := a.subAgentByTaskID(taskID)
	rehydrated := false
	previousAgentID := ""
	if sub == nil {
		if !record.allowsRehydrate() {
			return tools.TaskHandle{}, fmt.Errorf("task %s is %s; follow-up is not allowed without a live worker", taskID, strings.TrimSpace(record.State))
		}
		sub, previousAgentID, err = a.rehydrateCompletedTask(record)
		if err != nil {
			return tools.TaskHandle{}, err
		}
		rehydrated = true
	}

	status, statusMessage, err := a.deliverMessageToSubAgent(sub, message, kind)
	if err != nil {
		if rehydrated {
			a.closeSubAgent(sub.instanceID)
		}
		return tools.TaskHandle{}, err
	}

	handle := tools.TaskHandle{
		Status:  status,
		TaskID:  sub.taskID,
		AgentID: sub.instanceID,
		Message: statusMessage,
	}
	if rehydrated {
		handle.Status = "rehydrated"
		handle.PreviousAgentID = previousAgentID
		handle.Rehydrated = true
		handle.Message = "message delivered and task rehydrated"
	}
	return handle, nil
}

func (a *MainAgent) deliverMessageToSubAgent(sub *SubAgent, message, kind string) (string, string, error) {
	if sub == nil {
		return "", "", fmt.Errorf("missing worker")
	}
	state := sub.State()
	switch state {
	case SubAgentStateFailed, SubAgentStateCancelled:
		return "", "", fmt.Errorf("SubAgent %s for task %s is %s; follow-up is not allowed", sub.instanceID, sub.taskID, state)
	}

	status := "queued"
	statusMessage := "message delivered to running worker"
	payload := normalizeSubAgentMessage(kind, message)
	replyKind := normalizeReplyKind(kind)
	needsResume := state == SubAgentStateWaitingPrimary || state == SubAgentStateWaitingDescendant || state == SubAgentStateCompleted || state == SubAgentStateIdle

	if needsResume {
		if err := a.acquireSubAgentSlot(sub); err != nil {
			return "", "", err
		}
	}

	targetMailboxID := strings.TrimSpace(sub.LastMailboxID())
	if mailbox := a.takeOutstandingMailboxForSub(sub); mailbox != nil {
		targetMailboxID = strings.TrimSpace(mailbox.MessageID)
	}

	if targetMailboxID != "" {
		_, artifactRelPath, _ := a.markSubAgentMailboxConsumedWithReply(sub.instanceID, targetMailboxID, 0, message, replyKind)
		if artifactRelPath != "" {
			payload = fmt.Sprintf("[%s] Summary: %s\nDetailed instruction artifact: %s", replyKind, truncateMailboxReplySummary(message), artifactRelPath)
		}
	} else {
		replyMessageID := a.nextSubAgentReplyMessageID(sub.instanceID)
		if len(strings.TrimSpace(message)) > replyArtifactPayloadThreshold {
			artifactType := "execution_spec"
			artifactID, artifactRelPath, err := persistSubAgentArtifact(a.sessionDir, sub.instanceID, replyMessageID, artifactType, "MainAgent follow-up", message)
			if err == nil && artifactRelPath != "" {
				sub.setLastArtifact(tools.ArtifactRef{ID: artifactID, RelPath: artifactRelPath, Path: artifactRelPath, Type: artifactType})
				payload = fmt.Sprintf("[%s] Summary: %s\nDetailed instruction artifact: %s", replyKind, truncateMailboxReplySummary(message), artifactRelPath)
			}
		}
		sub.setReplyThread(replyMessageID, "", replyKind, truncateMailboxReplySummary(message))
	}

	if needsResume {
		sub.setState(SubAgentStateRunning, message)
		a.noteSubAgentStateTransition(sub, SubAgentStateRunning)
		a.emitActivity(sub.instanceID, ActivityExecuting, "resumed")
		a.emitToTUI(AgentStatusEvent{
			AgentID: sub.instanceID,
			Status:  "running",
			Message: message,
		})
		sub.InjectUserMessage(payload)
		status = "resumed"
		statusMessage = "message delivered and worker resumed"
	} else {
		sub.InjectUserMessage(payload)
	}

	a.saveRecoverySnapshot()
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	a.drainOwnedSubAgentMailboxes(sub.instanceID)
	return status, statusMessage, nil
}

func (a *MainAgent) rehydrateCompletedTask(record *DurableTaskRecord) (*SubAgent, string, error) {
	if record == nil {
		return nil, "", fmt.Errorf("missing task record")
	}
	agentDef, err := a.resolveAgentDef(record.AgentDefName)
	if err != nil {
		return nil, "", err
	}
	if a.llmFactory == nil {
		return nil, "", fmt.Errorf("LLM client factory not configured; call SetLLMFactory before rehydrating SubAgents")
	}
	msgs, err := loadTaskHistoryMessages(a.recovery, record)
	if err != nil {
		return nil, "", fmt.Errorf("load task history for %s: %w", record.TaskID, err)
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
	workDir := strings.TrimSpace(a.cachedWorkDir)
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:    instanceID,
		TaskID:        record.TaskID,
		AgentDefName:  agentDef.Name,
		TaskDesc:      record.TaskDesc,
		OwnerAgentID:  record.OwnerAgentID,
		OwnerTaskID:   record.OwnerTaskID,
		Depth:         record.Depth,
		JoinToOwner:   record.JoinToOwner,
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
	sub.RestoreMessages(msgs)
	sub.setState(SubAgentStateCompleted, strings.TrimSpace(record.LastSummary))
	if record.LastMailboxID != "" {
		sub.setLastMailboxID(record.LastMailboxID)
	}
	if record.LastReplyMessageID != "" || record.LastReplySummary != "" {
		sub.setReplyThread(record.LastReplyMessageID, record.LastReplyToMailboxID, record.LastReplyKind, record.LastReplySummary)
	}
	if len(record.LastArtifactRefs) > 0 {
		sub.setLastArtifact(record.LastArtifactRefs[0])
	}
	if err := a.acquireSubAgentSlot(sub); err != nil {
		cancel()
		return nil, "", err
	}
	a.mu.Lock()
	a.subAgents[instanceID] = sub
	a.mu.Unlock()
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	go sub.runLoop()
	return sub, strings.TrimSpace(record.LatestInstanceID), nil
}

func (a *MainAgent) stopSubAgentNow(callerAgentID, callerTaskID, taskID, reason string) (tools.TaskHandle, error) {
	taskID = strings.TrimSpace(taskID)
	reason = strings.TrimSpace(reason)
	if taskID == "" {
		return tools.TaskHandle{}, fmt.Errorf("task_id is required")
	}
	if _, err := a.canCallerControlTask(callerAgentID, callerTaskID, taskID); err != nil {
		return tools.TaskHandle{}, err
	}
	sub := a.subAgentByTaskID(taskID)
	if sub == nil {
		return tools.TaskHandle{}, fmt.Errorf("unknown task_id %q; cannot stop a missing worker", taskID)
	}

	state := sub.State()
	if state == SubAgentStateCancelled {
		return tools.TaskHandle{
			Status:  "cancelled",
			TaskID:  sub.taskID,
			AgentID: sub.instanceID,
			Message: "worker already cancelled",
		}, nil
	}
	if reason == "" {
		reason = "Stopped by MainAgent"
	}

	for _, childTaskID := range a.directChildTaskIDs(sub.taskID) {
		if childTaskID == "" || childTaskID == taskID {
			continue
		}
		if _, err := a.stopSubAgentNow(sub.instanceID, sub.taskID, childTaskID, fmt.Sprintf("cancelled because ancestor task %s was stopped", sub.taskID)); err != nil {
			return tools.TaskHandle{}, err
		}
	}

	if focused := a.focusedAgent.Load(); focused != nil && focused.instanceID == sub.instanceID {
		a.focusedAgent.Store(nil)
	}
	// Cancel synchronously for deterministic shutdown and tests.
	sub.cancelCurrentTurnFromLoop()
	a.releaseSubAgentSlot(sub)
	a.emitActivity(sub.instanceID, ActivityIdle, "")
	a.emitToTUI(ToastEvent{
		Message: reason,
		Level:   "warn",
		AgentID: sub.instanceID,
	})
	a.handleSubAgentCloseRequestedEvent(Event{
		Type:     EventSubAgentCloseRequested,
		SourceID: sub.instanceID,
		Payload: &SubAgentCloseRequestedPayload{
			Reason:       reason,
			ClosedReason: "stopped by main agent",
			FinalState:   SubAgentStateCancelled,
		},
	})
	a.saveRecoverySnapshot()

	return tools.TaskHandle{
		Status:  "cancelled",
		TaskID:  sub.taskID,
		AgentID: sub.instanceID,
		Message: "worker stopped",
	}, nil
}

func (a *MainAgent) NotifySubAgent(ctx context.Context, taskID, message, kind string) (tools.TaskHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callerAgentID := strings.TrimSpace(tools.AgentIDFromContext(ctx))
	callerTaskID := strings.TrimSpace(tools.TaskIDFromContext(ctx))
	if callerAgentID == strings.TrimSpace(a.instanceID) {
		callerAgentID = ""
	}
	if !a.started.Load() {
		return a.sendMessageToSubAgentNow(callerAgentID, callerTaskID, taskID, message, kind)
	}
	reply := make(chan subAgentControlResult, 1)
	a.sendEvent(Event{
		Type: EventSubAgentSendMessage,
		Payload: &SubAgentSendMessagePayload{
			Ctx:           ctx,
			CallerAgentID: callerAgentID,
			CallerTaskID:  callerTaskID,
			TaskID:        taskID,
			Message:       message,
			Kind:          kind,
			Reply:         reply,
		},
	})
	select {
	case result := <-reply:
		return result.Handle, result.Err
	case <-ctx.Done():
		return tools.TaskHandle{}, ctx.Err()
	case <-a.parentCtx.Done():
		return tools.TaskHandle{}, a.parentCtx.Err()
	}
}

func (a *MainAgent) CancelSubAgent(ctx context.Context, taskID, reason string) (tools.TaskHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	callerAgentID := strings.TrimSpace(tools.AgentIDFromContext(ctx))
	callerTaskID := strings.TrimSpace(tools.TaskIDFromContext(ctx))
	if callerAgentID == strings.TrimSpace(a.instanceID) {
		callerAgentID = ""
	}
	if !a.started.Load() {
		return a.stopSubAgentNow(callerAgentID, callerTaskID, taskID, reason)
	}
	reply := make(chan subAgentControlResult, 1)
	a.sendEvent(Event{
		Type: EventSubAgentStop,
		Payload: &SubAgentStopPayload{
			Ctx:           ctx,
			CallerAgentID: callerAgentID,
			CallerTaskID:  callerTaskID,
			TaskID:        taskID,
			Reason:        reason,
			Reply:         reply,
		},
	})
	select {
	case result := <-reply:
		return result.Handle, result.Err
	case <-ctx.Done():
		return tools.TaskHandle{}, ctx.Err()
	case <-a.parentCtx.Done():
		return tools.TaskHandle{}, a.parentCtx.Err()
	}
}

func (a *MainAgent) handleSubAgentSendMessageEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentSendMessagePayload)
	if !ok || payload == nil {
		return
	}
	if payload.Ctx != nil && payload.Ctx.Err() != nil {
		respondSubAgentControl(payload.Reply, tools.TaskHandle{}, payload.Ctx.Err())
		return
	}
	handle, err := a.sendMessageToSubAgentNow(payload.CallerAgentID, payload.CallerTaskID, payload.TaskID, payload.Message, payload.Kind)
	respondSubAgentControl(payload.Reply, handle, err)
}

func (a *MainAgent) handleSubAgentStopEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentStopPayload)
	if !ok || payload == nil {
		return
	}
	if payload.Ctx != nil && payload.Ctx.Err() != nil {
		respondSubAgentControl(payload.Reply, tools.TaskHandle{}, payload.Ctx.Err())
		return
	}
	handle, err := a.stopSubAgentNow(payload.CallerAgentID, payload.CallerTaskID, payload.TaskID, payload.Reason)
	respondSubAgentControl(payload.Reply, handle, err)
}
