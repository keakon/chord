package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// handleLocalOnlySlashCommands runs local-only slash commands that must never
// be appended to the conversation or sent to the model. Returns true if
// handled. busy reports whether an active turn is in flight (a.turn != nil)
// so handlers can avoid clearing turn state mid-retry. Runs even when the
// agent is busy (not queued), including when the submitted message carries
// image parts.
func (a *MainAgent) handleLocalOnlySlashCommands(content string, parts []message.ContentPart, busy bool) bool {
	return a.executeLocalOnlySlashCommand(content, parts, busy)
}

// IsTUILocalOnlySlashCommand reports whether content is a local-only slash
// command (/export, /models, /tier, /rename, /role, /compact, /yolo, /mcp) that must run on the main agent's event
// loop and must never be routed to a focused SubAgent. Predicate only —
// execution lives in executeLocalOnlySlashCommand, which the event-loop
// goroutine calls. The TUI also consults it to route the command without
// echoing a USER card.
func IsTUILocalOnlySlashCommand(content string) bool {
	c := strings.TrimSpace(content)
	switch {
	case c == "/export" || strings.HasPrefix(c, "/export "):
		return true
	case c == "/models" || strings.HasPrefix(c, "/models "):
		return true
	case c == "/role" || strings.HasPrefix(c, "/role "):
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
	case c == "/skill":
		// Bare /skill opens the selector; `/skill <name> [args]` is a normal
		// user message and must follow the focused agent instead.
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
	if a.controlActionCanSetBaseline() {
		// These commands may settle the runtime without starting a real turn.
		// Establish the silent baseline before their handler emits its own
		// follow-up events; a queued control command must not inherit the
		// preceding task's completion notification.
		a.markControlAction()
	}
	switch {
	case c == "/export" || strings.HasPrefix(c, "/export "):
		a.handleExportCommand(c, busy)
		return true
	case c == "/models" || strings.HasPrefix(c, "/models "):
		a.handleModelsCommand(c, busy)
		return true
	case c == "/role" || strings.HasPrefix(c, "/role "):
		a.handleRoleCommand(c, busy)
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
	case c == "/skill":
		a.emitToTUI(SkillSelectEvent{})
		if !busy {
			a.setIdleAndDrainPending()
		}
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
	if IsTUILocalOnlySlashCommand(content) {
		a.sendEvent(Event{Type: EventUserMessage, Payload: content})
		return
	}
	target, ok := a.resolveConversationTarget(conversation)
	if !ok {
		a.emitToTUI(ToastEvent{Message: "Conversation is no longer available; retry the message", Level: "warn", AgentID: conversation.AgentID})
		return
	}
	if target.settled {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Task %s has finished; delegate it again to send it a follow-up", target.task.TaskID), Level: "warn", AgentID: target.task.LatestInstanceID})
		return
	}
	if focused := target.sub; focused != nil {
		kind := "follow_up"
		if focused.State() == SubAgentStateWaitingMain {
			kind = "reply"
		} else if state := focused.State(); isTerminalSubAgentState(state) {
			// A finished worker still holding its runtime (settlement landed
			// before parking) must be resumed through the explicit new-attempt
			// machinery — a plain delivery may never revive the settled
			// attempt. Cancelled stays cancelled.
			if state == SubAgentStateCancelled {
				a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Task %s was cancelled; delegate the work again if it should still be done", focused.taskID), Level: "warn", AgentID: focused.instanceID})
				return
			}
			if err := a.beginNextTaskAttemptForLiveSub(focused); err != nil {
				a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: focused.instanceID})
				return
			}
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
		Payload: a.acceptRawUserMessage(content, nil),
	})
}

// acceptRawUserMessage stamps a main-agent user message with its arrival order.
// The stamp is taken before the message enters the event queues, so a cancel
// request that samples the counter can tell whether this message was already
// accepted, and the loop can tell whether a later message outranks it.
func (a *MainAgent) acceptRawUserMessage(content string, parts []message.ContentPart) acceptedUserMessage {
	return acceptedUserMessage{
		Content:       content,
		Parts:         parts,
		AcceptedOrder: a.acceptedRawMessages.Add(1),
	}
}

// SendUserMessageWithParts enqueues a multi-part user message (text + images).
func (a *MainAgent) SendUserMessageWithParts(parts []message.ContentPart) {
	var content strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			content.WriteString(part.Text)
		}
	}
	if IsTUILocalOnlySlashCommand(content.String()) {
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
	if rec := a.focusedDurableTask(); rec != nil && !rec.RuntimeParked && isTerminalSubAgentState(SubAgentState(strings.TrimSpace(rec.State))) {
		a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Task %s has finished; delegate it again to send it a follow-up", rec.TaskID), Level: "warn", AgentID: rec.LatestInstanceID})
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
		Payload: a.acceptRawUserMessage(content.String(), parts),
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
	if rec := a.focusedDurableTask(); rec != nil && !rec.RuntimeParked && isTerminalSubAgentState(SubAgentState(strings.TrimSpace(rec.State))) {
		// A settled task has no runtime and cannot be rehydrated; appending to
		// the main session would leak a worker-scoped message into the main
		// context, so the settled transcript stays strictly read-only.
		log.Warnf("subagent context append rejected: task %v has settled and is read-only", rec.TaskID)
		return
	}
	a.sendEvent(Event{Type: EventAppendContext, Payload: msg})
}

func (a *MainAgent) reactivateFocusedSubAgentForManualInput(sub *SubAgent) bool {
	if sub == nil || sub.State() == SubAgentStateRunning {
		return true
	}
	if state := sub.State(); isTerminalSubAgentState(state) {
		// A settled runtime can only accept input again through the explicit
		// new-attempt machinery (attempt bump plus reset to Idle), never by
		// flipping it straight back to Running. A cancelled task stays
		// cancelled: the user stopped it, so a follow-up must be a fresh
		// delegation instead.
		if state == SubAgentStateCancelled {
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Task %s was cancelled; delegate the work again if it should still be done", sub.taskID), Level: "warn", AgentID: sub.instanceID})
			return false
		}
		if err := a.beginNextTaskAttemptForLiveSub(sub); err != nil {
			a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: sub.instanceID})
			return false
		}
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

	// The cached sub-agent list derives only from agentConfigs, which is
	// replaced exclusively here. Per-role filtering (delegate permissions and
	// the active-role exclusion) happens lazily at read time against this
	// cache, so later role switches do not need to rebuild it.
	a.rebuildCachedSubAgents()

	// Rebuild active-role state after configs install or refresh.
	if selectedRole != "" {
		a.rebuildRuleset()
		log.Debugf("set active role from agent configs role=%v total_rules=%v", selectedRole, len(a.effectiveRuleset()))

		// Rebuild system prompt to include the selected role instructions.
		a.refreshSystemPrompt()
	}
}
