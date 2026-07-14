package agent

import (
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// isTUILocalOnlySlashCommand reports whether content is a local-only slash
// command (/export, /models, /tier, /rename, /compact) that must run on the main agent's event
// loop and must never be routed to a focused SubAgent. Predicate only —
// execution lives in executeLocalOnlySlashCommand, which the event-loop
// goroutine calls.
func isTUILocalOnlySlashCommand(content string) bool {
	c := strings.TrimSpace(content)
	switch {
	case c == "/export" || strings.HasPrefix(c, "/export "):
		return true
	case c == "/models" || strings.HasPrefix(c, "/models "):
		return true
	case c == "/tier" || strings.HasPrefix(c, "/tier "):
		return true
	case c == "/rename" || strings.HasPrefix(c, "/rename "):
		return true
	case c == "/compact":
		return true
	case c == "/yolo" || strings.HasPrefix(c, "/yolo "):
		return true
	case c == "/mcp" || strings.HasPrefix(c, "/mcp "):
		// MCP control affects the tool surface and must always be routed to the main agent.
		return true
	default:
		return false
	}
}

// executeLocalOnlySlashCommand runs local-only slash commands on the event-loop
// goroutine. busy reports whether an active turn is in flight (a.turn != nil)
// so handlers can skip setIdleAndDrainPending — clearing a.turn mid-retry
// corrupts turn state and breaks esc-cancel.
//
// parts is accepted for API symmetry with the user-message dispatch path; the
// current local-only handlers operate on the text form only.
func (a *MainAgent) executeLocalOnlySlashCommand(content string, _ []message.ContentPart, busy bool) bool {
	c := strings.TrimSpace(content)
	switch {
	case c == "/export" || strings.HasPrefix(c, "/export "):
		a.handleExportCommand(c, busy)
		return true
	case c == "/models" || strings.HasPrefix(c, "/models "):
		a.handleModelsCommand(c, busy)
		return true
	case c == "/tier" || strings.HasPrefix(c, "/tier "):
		a.handleTierCommand(c, busy)
		return true
	case c == "/rename" || strings.HasPrefix(c, "/rename "):
		a.handleRenameCommand(strings.TrimSpace(strings.TrimPrefix(c, "/rename")))
		return true
	case c == "/compact":
		a.handleCompactCommand()
		return true
	case c == "/yolo" || strings.HasPrefix(c, "/yolo "):
		a.handleYoloCommand(c, busy)
		return true
	case c == "/mcp" || strings.HasPrefix(c, "/mcp "):
		a.handleMCPCommand(c, busy)
		return true
	default:
		return false
	}
}

// SendUserMessage enqueues a user message for processing. It is safe to call
// from any goroutine (typically the TUI input handler).
//
// If a SubAgent is currently focused (via Tab), the message is routed directly
// to that SubAgent instead of the MainAgent's event loop. Local-only slash
// commands bypass SubAgent routing because they belong to the main agent —
// they're sent to the main event loop unchanged.
func (a *MainAgent) SendUserMessage(content string) {
	a.SendUserMessageToTarget(a.focusedConversationTarget(), content)
}

// SendUserMessageToTarget delivers a message to a previously captured
// conversation rather than consulting the current TUI focus.
func (a *MainAgent) SendUserMessageToTarget(conversation ConversationTarget, content string) {
	if isTUILocalOnlySlashCommand(content) {
		a.sendEvent(Event{Type: EventUserMessage, Payload: content})
		return
	}
	target, ok := a.resolveConversationTarget(conversation)
	if !ok {
		a.emitToTUI(ToastEvent{Message: "Conversation is no longer available; retry the message", Level: "warn", AgentID: conversation.AgentID})
		return
	}
	if focused := target.sub; focused != nil {
		kind := "follow_up"
		if focused.State() == SubAgentStateWaitingMain {
			kind = "reply"
		}
		if _, _, err := a.deliverManualMessageToSubAgent(focused, content, kind); err != nil {
			rec := a.taskRecordByTaskID(focused.taskID)
			if rec == nil || !rec.RuntimeParked {
				a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: focused.instanceID})
				return
			}
			sub, _, rehydrateErr := a.rehydrateTask(rec)
			if rehydrateErr != nil {
				a.emitToTUI(ToastEvent{Message: rehydrateErr.Error(), Level: "warn", AgentID: focused.instanceID})
				return
			}
			if _, _, retryErr := a.deliverManualMessageToSubAgent(sub, content, kind); retryErr != nil {
				a.emitToTUI(ToastEvent{Message: retryErr.Error(), Level: "warn", AgentID: sub.instanceID})
			}
		}
		return
	}
	if rec := target.task; target.parked && rec != nil {
		sub, _, err := a.rehydrateTask(rec)
		if err != nil {
			a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: rec.LatestInstanceID})
			return
		}
		if _, _, err := a.deliverManualMessageToSubAgent(sub, content, "follow_up"); err != nil {
			a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: sub.instanceID})
		}
		return
	}
	a.sendEvent(Event{
		Type:    EventUserMessage,
		Payload: content,
	})
}

// SendUserMessageWithParts enqueues a multi-part user message (text + images).
func (a *MainAgent) SendUserMessageWithParts(parts []message.ContentPart) {
	var content strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			content.WriteString(part.Text)
		}
	}
	if isTUILocalOnlySlashCommand(content.String()) {
		a.sendEvent(Event{Type: EventUserMessage, Payload: parts})
		return
	}
	if focused := a.validFocusedSubAgent(); focused != nil {
		if !a.withRegisteredSubAgent(focused, func(sub *SubAgent) bool {
			if !a.reactivateFocusedSubAgentForManualInput(sub) {
				return false
			}
			a.mailboxDeliveryPaused.Store(false)
			return sub.InjectManualUserMessageWithParts(parts, a.drainOwnedSubAgentMailboxes(sub.instanceID))
		}) {
			a.emitToTUI(ToastEvent{Message: "Focused SubAgent is no longer available; retry the message", Level: "warn", AgentID: focused.instanceID})
		}
		return
	}
	if rec := a.focusedDurableTask(); rec != nil && rec.RuntimeParked {
		sub, _, err := a.rehydrateTask(rec)
		if err != nil {
			a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: rec.LatestInstanceID})
			return
		}
		if !a.reactivateFocusedSubAgentForManualInput(sub) {
			return
		}
		if !a.enqueueRegisteredSubAgent(sub, func(current *SubAgent) bool {
			a.mailboxDeliveryPaused.Store(false)
			return current.InjectManualUserMessageWithParts(parts, a.drainOwnedSubAgentMailboxes(current.instanceID))
		}) {
			a.emitToTUI(ToastEvent{Message: "Rehydrated SubAgent is no longer available; retry the message", Level: "warn", AgentID: sub.instanceID})
		}
		return
	}
	a.sendEvent(Event{
		Type:    EventUserMessage,
		Payload: parts,
	})
}

// QueuePendingUserDraft mirrors a busy local TUI draft into the agent's
// pending queue so it can be consumed in-turn or at the next idle drain.
func (a *MainAgent) QueuePendingUserDraft(draftID string, parts []message.ContentPart) bool {
	if strings.TrimSpace(draftID) == "" || len(parts) == 0 {
		return false
	}
	if focused := a.validFocusedSubAgent(); focused != nil && focused.State() == SubAgentStateRunning {
		return a.enqueueRegisteredSubAgent(focused, func(sub *SubAgent) bool {
			return sub.QueuePendingUserDraft(draftID, parts)
		})
	}
	a.sendEvent(Event{
		Type:    EventPendingDraftUpsert,
		Payload: pendingUserMessageFromDraft(draftID, parts),
	})
	return true
}

// UpdatePendingUserDraft replaces a queued draft before it is consumed.
func (a *MainAgent) UpdatePendingUserDraft(draftID string, parts []message.ContentPart) bool {
	if strings.TrimSpace(draftID) == "" || len(parts) == 0 {
		return false
	}
	if focused := a.validFocusedSubAgent(); focused != nil && focused.State() == SubAgentStateRunning {
		return focused.UpdatePendingUserDraft(draftID, parts)
	}
	a.sendEvent(Event{
		Type:    EventPendingDraftUpsert,
		Payload: pendingUserMessageFromDraft(draftID, parts),
	})
	return true
}

// RemovePendingUserDraft removes a queued draft before it is consumed.
func (a *MainAgent) RemovePendingUserDraft(draftID string) bool {
	if strings.TrimSpace(draftID) == "" {
		return false
	}
	if focused := a.validFocusedSubAgent(); focused != nil && focused.State() == SubAgentStateRunning {
		return focused.RemovePendingUserDraft(draftID)
	}
	a.sendEvent(Event{
		Type:    EventPendingDraftRemove,
		Payload: strings.TrimSpace(draftID),
	})
	return true
}

// AppendContextMessage appends a user-role message to the focused agent's
// conversation context without invoking the LLM (e.g. TUI !shell output).
func (a *MainAgent) AppendContextMessage(msg message.Message) {
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
		return
	}
	msg.Role = "user"
	if focused := a.validFocusedSubAgent(); focused != nil {
		if !a.enqueueRegisteredSubAgent(focused, func(sub *SubAgent) bool {
			return sub.TryEnqueueContextAppend(msg)
		}) {
			log.Warnf("subagent context append rejected agent_id=%v state=%v", focused.instanceID, focused.State())
		}
		return
	}
	if rec := a.focusedDurableTask(); rec != nil && rec.RuntimeParked {
		sub, _, err := a.rehydrateTask(rec)
		if err != nil {
			log.Warnf("parked subagent context append rehydrate failed task_id=%v error=%v", rec.TaskID, err)
			return
		}
		if !a.enqueueRegisteredSubAgent(sub, func(current *SubAgent) bool {
			return current.TryEnqueueContextAppend(msg)
		}) {
			log.Warnf("subagent context append rejected agent_id=%v state=%v", sub.instanceID, sub.State())
		}
		return
	}
	a.sendEvent(Event{Type: EventAppendContext, Payload: msg})
}

func (a *MainAgent) reactivateFocusedSubAgentForManualInput(sub *SubAgent) bool {
	if sub == nil || sub.State() == SubAgentStateRunning {
		return true
	}
	if err := a.acquireSubAgentSlot(sub); err != nil {
		a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: sub.instanceID})
		return false
	}
	a.markSubAgentReactivated(sub, "Resumed from user input")
	a.saveRecoverySnapshot()
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	return true
}

// Events returns a read-only channel of AgentEvents for the TUI to consume.
func (a *MainAgent) Events() <-chan AgentEvent {
	return a.outputCh
}

// PendingUserMessageCount returns the number of queued user messages waiting to
// be drained after the current turn ends.
func (a *MainAgent) PendingUserMessageCount() int {
	if a == nil {
		return 0
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return len(a.pendingUserMessages)
}

// SetAgentConfigs stores the pre-resolved agent configurations (built-in →
// global → project merged). If the current active role is present in configs,
// it is preserved; otherwise the active role defaults to "builder". The
// permission ruleset is rebuilt accordingly.
//
// Call this after NewMainAgent and before Run.
func (a *MainAgent) SetAgentConfigs(configs map[string]*config.AgentConfig) {
	a.stateMu.Lock()
	currentRole := ""
	if a.activeConfig != nil {
		currentRole = strings.TrimSpace(a.activeConfig.Name)
	}
	a.agentConfigs = configs
	selectedRole := ""
	if currentRole != "" {
		if cfg, ok := configs[currentRole]; ok && cfg != nil {
			a.activeConfig = cfg
			selectedRole = cfg.Name
		} else {
			a.activeConfig = nil
		}
	}
	if selectedRole == "" {
		if builderCfg, ok := configs["builder"]; ok && builderCfg != nil {
			a.activeConfig = builderCfg
			selectedRole = builderCfg.Name
		}
	}
	a.stateMu.Unlock()
	if len(configs) > 0 {
		names := make([]string, 0, len(configs))
		for name := range configs {
			names = append(names, name)
		}
		log.Debugf("agent configs installed count=%v names=%v", len(configs), names)
	}

	// Cache the available subagents using the initial role selection.
	// agentConfigs is immutable after this point; subsequent role changes call rebuildCachedSubAgents.
	a.rebuildCachedSubAgents()

	// Rebuild active-role state after configs install or refresh.
	if selectedRole != "" {
		a.rebuildRuleset()
		log.Debugf("set active role from agent configs role=%v total_rules=%v", selectedRole, len(a.effectiveRuleset()))

		// Rebuild system prompt to include the selected role instructions.
		a.refreshSystemPrompt()
	}
}
