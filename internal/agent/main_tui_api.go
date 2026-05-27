package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// isTUILocalOnlySlashCommand reports whether content is a local-only slash
// command (/export, /models, /tier, /compact) that must run on the main agent's event
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

func (a *MainAgent) focusedSubAgentControlContext(focused *SubAgent) context.Context {
	if a == nil {
		return context.Background()
	}
	ctx := a.parentCtx
	if focused == nil {
		return ctx
	}
	if agentID := focused.OwnerAgentID(); agentID != "" {
		ctx = tools.WithAgentID(ctx, agentID)
	}
	if taskID := focused.OwnerTaskID(); taskID != "" {
		ctx = tools.WithTaskID(ctx, taskID)
	}
	return ctx
}

// SendUserMessage enqueues a user message for processing. It is safe to call
// from any goroutine (typically the TUI input handler).
//
// If a SubAgent is currently focused (via Tab), the message is routed directly
// to that SubAgent instead of the MainAgent's event loop. Local-only slash
// commands bypass SubAgent routing because they belong to the main agent —
// they're sent to the main event loop unchanged.
func (a *MainAgent) SendUserMessage(content string) {
	if isTUILocalOnlySlashCommand(content) {
		a.sendEvent(Event{Type: EventUserMessage, Payload: content})
		return
	}
	// Route to focused SubAgent if one is active.
	if focused := a.validFocusedSubAgent(); focused != nil {
		switch focused.State() {
		case SubAgentStateRunning:
			focused.InjectUserMessage(content)
			return
		case SubAgentStateWaitingMain:
			taskID := strings.TrimSpace(focused.taskID)
			if taskID == "" {
				a.emitToTUI(ToastEvent{Message: fmt.Sprintf("SubAgent %s is %s; direct input is disabled", focused.instanceID, focused.State()), Level: "warn", AgentID: focused.instanceID})
				return
			}
			if _, err := a.NotifySubAgent(a.focusedSubAgentControlContext(focused), taskID, content, "reply"); err != nil {
				a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: focused.instanceID})
			}
			return
		case SubAgentStateCompleted, SubAgentStateIdle:
			taskID := strings.TrimSpace(focused.taskID)
			if taskID == "" {
				a.emitToTUI(ToastEvent{Message: fmt.Sprintf("SubAgent %s is %s; direct input is disabled", focused.instanceID, focused.State()), Level: "warn", AgentID: focused.instanceID})
				return
			}
			if _, err := a.NotifySubAgent(a.focusedSubAgentControlContext(focused), taskID, content, "follow_up"); err != nil {
				a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: focused.instanceID})
			}
			return
		}
	}
	a.sendEvent(Event{
		Type:    EventUserMessage,
		Payload: content,
	})
}

// SendUserMessageWithParts enqueues a multi-part user message (text + images).
func (a *MainAgent) SendUserMessageWithParts(parts []message.ContentPart) {
	var content string
	for _, part := range parts {
		if part.Type == "text" {
			content += part.Text
		}
	}
	if isTUILocalOnlySlashCommand(content) {
		a.sendEvent(Event{Type: EventUserMessage, Payload: parts})
		return
	}
	if focused := a.validFocusedSubAgent(); focused != nil {
		if focused.State() != SubAgentStateRunning {
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("SubAgent %s is %s; direct input is disabled", focused.instanceID, focused.State()), Level: "warn", AgentID: focused.instanceID})
			return
		}
		focused.InjectUserMessageWithParts(parts)
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
		if focused.State() != SubAgentStateRunning {
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("SubAgent %s is %s; direct input is disabled", focused.instanceID, focused.State()), Level: "warn", AgentID: focused.instanceID})
			return
		}
		if !focused.TryEnqueueContextAppend(msg) {
			log.Warnf("subagent context append rejected agent_id=%v state=%v", focused.instanceID, focused.State())
		}
		return
	}
	a.sendEvent(Event{Type: EventAppendContext, Payload: msg})
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
