package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
)

// SubAgentMessenger is the interface used by NotifyTool to continue or reply to
// an existing worker without importing the agent package.
type SubAgentMessenger interface {
	NotifySubAgent(ctx context.Context, taskID, message, kind string) (TaskHandle, error)
}

type NotifyTool struct {
	sender      EventSender
	messenger   SubAgentMessenger
	allowOwner  bool
	allowTarget bool
}

func NewNotifyTool(sender EventSender, messenger SubAgentMessenger, allowOwner, allowTarget bool) *NotifyTool {
	return &NotifyTool{
		sender:      sender,
		messenger:   messenger,
		allowOwner:  allowOwner,
		allowTarget: allowTarget,
	}
}

type notifyArgs struct {
	Message      string `json:"message"`
	TargetTaskID string `json:"target_task_id,omitempty"`
	Kind         string `json:"kind,omitempty"`
}

func (NotifyTool) Name() string { return "Notify" }

func (t *NotifyTool) Description() string {
	switch {
	case t.allowOwner && t.allowTarget:
		return "Send a non-blocking update. Without target_task_id, notify your direct owner / coordination chain and continue working. " +
			"With target_task_id, deliver a clarification, correction, or follow-up to a specific delegated worker without escalating."
	case t.allowTarget:
		return "Send a non-blocking clarification, decision, or correction to a delegated worker identified by target_task_id."
	default:
		return "Send a non-blocking progress update or intermediate result to your direct owner / coordination chain and continue working."
	}
}

func (t *NotifyTool) Parameters() map[string]any {
	properties := map[string]any{
		"message": map[string]any{
			"type":        "string",
			"description": "The update, clarification, or intermediate result to deliver.",
		},
		"kind": map[string]any{
			"type":        "string",
			"description": "Optional message kind hint such as progress, clarification, correction, or constraint_update.",
		},
	}
	required := []string{"message"}
	if t.allowTarget {
		properties["target_task_id"] = map[string]any{
			"type":        "string",
			"description": "Optional durable task handle for the specific delegated worker to notify. Required when used from MainAgent or when notifying a specific delegate.",
		}
		if !t.allowOwner {
			required = append(required, "target_task_id")
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func (NotifyTool) IsReadOnly() bool { return false }

func (t *NotifyTool) IsAvailable() bool {
	return t.allowOwner || t.allowTarget
}

func (t *NotifyTool) VisibleWithRuleset(ruleset permission.Ruleset) bool {
	notifyVisible := !ruleset.IsDisabled("Notify")
	if !notifyVisible {
		return false
	}
	if t.allowOwner {
		return true
	}
	return t.allowTarget && !ruleset.IsDisabled("Delegate")
}

func (t *NotifyTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a notifyArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.Message = strings.TrimSpace(a.Message)
	a.TargetTaskID = strings.TrimSpace(a.TargetTaskID)
	a.Kind = strings.TrimSpace(a.Kind)

	if a.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	if a.TargetTaskID != "" {
		if !t.allowTarget {
			return "", fmt.Errorf("target_task_id is not available in this role")
		}
		if t.messenger == nil {
			return "", fmt.Errorf("targeted notify is not available")
		}
		handle, err := t.messenger.NotifySubAgent(ctx, a.TargetTaskID, a.Message, a.Kind)
		if err != nil {
			return "", err
		}
		out, err := json.Marshal(handle)
		if err != nil {
			return "", fmt.Errorf("marshal notify handle: %w", err)
		}
		return string(out), nil
	}

	if !t.allowOwner {
		return "", fmt.Errorf("target_task_id is required in this context")
	}
	if t.sender == nil {
		return "", fmt.Errorf("event sender not available (no EventSender configured)")
	}
	agentID := AgentIDFromContext(ctx)
	t.sender.SendAgentEvent("agent_notify", agentID, a.Message)
	return "Owner coordination chain has been notified. Continue working.", nil
}
