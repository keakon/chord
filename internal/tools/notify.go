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

type StructuredSubAgentMessenger interface {
	NotifySubAgentMessage(ctx context.Context, request AgentResponseRequest) (TaskHandle, error)
}

// ScopeGrantingSubAgentMessenger delivers a targeted message together with a
// widening of the target task's write scope. A running task's scope is fixed at
// delegation time, so without this the only way to hand a worker one more file
// is to cancel it and re-delegate the whole task.
type ScopeGrantingSubAgentMessenger interface {
	NotifySubAgentWithScopeGrant(ctx context.Context, taskID, message, kind string, grant WriteScope) (TaskHandle, error)
}

type AgentResponseRequest struct {
	TargetTaskID  string
	Message       string
	Kind          string
	CorrelationID string
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
	Message       string          `json:"message"`
	TargetTaskID  string          `json:"target_task_id,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	MessageType   string          `json:"message_type,omitempty"`
	Subtype       string          `json:"subtype,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	GrantScope    *WriteScope     `json:"grant_write_scope,omitempty"`
}

// AgentNotifyPayload is the structured owner-update payload emitted by Notify.
type AgentNotifyPayload struct {
	Message       string          `json:"message"`
	Kind          string          `json:"kind,omitempty"`
	MessageType   string          `json:"message_type,omitempty"`
	Subtype       string          `json:"subtype,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

func (NotifyTool) Name() string { return NameNotify }

// targetedNotifyResumeNote states which workers a targeted message can still
// reach. A worker that finished or failed is restarted with its full transcript,
// which is what makes "tell the worker what to fix" cheaper than delegating the
// same work to someone who has to rediscover the context.
const targetedNotifyResumeNote = "A worker that already finished or failed is resumed with its own history, " +
	"so send it the correction rather than delegating the same work to a fresh worker. " +
	"A cancelled task is not resumable; delegate again if the work should still happen."

func (t *NotifyTool) Description() string {
	switch {
	case t.allowOwner && t.allowTarget:
		return "Send a non-blocking update. Without target_task_id, notify your direct owner / coordination chain and continue working. " +
			"With target_task_id, deliver a clarification, correction, or follow-up to a specific delegated worker without escalating. " + targetedNotifyResumeNote
	case t.allowTarget:
		return "Send a non-blocking clarification, decision, or correction to a delegated worker identified by target_task_id. " + targetedNotifyResumeNote
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
		"message_type": map[string]any{
			"type": "string", "enum": []string{"progress", "notice", "response"},
			"description": "Optional communication category. Owner notifications support progress/notice; a targeted response uses message, kind, target_task_id, and correlation_id only.",
		},
		"subtype":        map[string]any{"type": "string", "description": "Optional application-defined subtype. Runtime does not interpret it."},
		"correlation_id": map[string]any{"type": "string", "description": "Optional application correlation ID for owner-visible notices."},
		"payload":        map[string]any{"type": "object", "description": "Optional JSON object payload, limited to 32 KiB. Runtime does not interpret business fields."},
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
		properties["grant_write_scope"] = map[string]any{
			"type":        "object",
			"description": "Optional. Add paths to the target worker's expected_write_scope before delivering the message, for when it turns out to need a file you did not declare. Paths are only ever added. Use this instead of cancelling and re-delegating a worker that is already most of the way through its task.",
			"properties": map[string]any{
				"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"path_prefix": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"modules":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"additionalProperties": false,
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
	notifyVisible := !ruleset.IsDisabled(NameNotify)
	if !notifyVisible {
		return false
	}
	if t.allowOwner {
		return true
	}
	return t.allowTarget && !ruleset.IsDisabled(NameDelegate)
}

func (t *NotifyTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a notifyArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.Message = strings.TrimSpace(a.Message)
	a.TargetTaskID = strings.TrimSpace(a.TargetTaskID)
	a.Kind = strings.TrimSpace(a.Kind)
	a.MessageType = strings.TrimSpace(a.MessageType)
	a.Subtype = strings.TrimSpace(a.Subtype)
	a.CorrelationID = strings.TrimSpace(a.CorrelationID)
	if len(a.Subtype) > 256 || len(a.CorrelationID) > 256 || len(a.Payload) > 32*1024 {
		return "", fmt.Errorf("structured message metadata exceeds size limit")
	}
	if len(a.Payload) > 0 {
		trimmed := []byte(strings.TrimSpace(string(a.Payload)))
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return "", fmt.Errorf("payload must be a JSON object")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
			return "", fmt.Errorf("payload must be a JSON object")
		}
		a.Payload = append(json.RawMessage(nil), trimmed...)
	}

	if a.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	if a.TargetTaskID != "" {
		if a.MessageType == "response" {
			messenger, ok := t.messenger.(StructuredSubAgentMessenger)
			if !ok {
				return "", fmt.Errorf("structured response delivery is unavailable")
			}
			if a.CorrelationID == "" {
				return "", fmt.Errorf("correlation_id is required for response")
			}
			if a.Subtype != "" || len(a.Payload) != 0 {
				return "", fmt.Errorf("subtype and payload are unavailable for response")
			}
			handle, err := messenger.NotifySubAgentMessage(ctx, AgentResponseRequest{
				TargetTaskID: a.TargetTaskID, Message: a.Message, Kind: a.Kind, CorrelationID: a.CorrelationID,
			})
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(handle)
			if err != nil {
				return "", fmt.Errorf("marshal notify handle: %w", err)
			}
			return string(out), nil
		}
		if a.MessageType != "" || a.Subtype != "" || a.CorrelationID != "" || len(a.Payload) != 0 {
			return "", fmt.Errorf("structured message fields are unavailable with target_task_id until durable owner-to-child delivery is implemented")
		}
		if !t.allowTarget {
			return "", fmt.Errorf("target_task_id is not available in this role")
		}
		if t.messenger == nil {
			return "", fmt.Errorf("targeted notify is not available")
		}
		var handle TaskHandle
		var err error
		if a.GrantScope != nil {
			granter, ok := t.messenger.(ScopeGrantingSubAgentMessenger)
			if !ok {
				return "", fmt.Errorf("grant_write_scope is unavailable")
			}
			grant := a.GrantScope.Normalized()
			if grant.ReadOnly || len(grant.VerificationCommands) > 0 {
				return "", fmt.Errorf("grant_write_scope adds paths only; read_only and verification_commands are fixed when the task is delegated")
			}
			handle, err = granter.NotifySubAgentWithScopeGrant(ctx, a.TargetTaskID, a.Message, a.Kind, grant)
		} else {
			handle, err = t.messenger.NotifySubAgent(ctx, a.TargetTaskID, a.Message, a.Kind)
		}
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
	if a.MessageType == "" {
		a.MessageType = "progress"
	}
	if a.MessageType != "progress" && a.MessageType != "notice" {
		return "", fmt.Errorf("message_type must be progress or notice")
	}
	agentID := AgentIDFromContext(ctx)
	t.sender.SendAgentEvent("agent_notify", agentID, AgentNotifyPayload{
		Message: a.Message, Kind: a.Kind, MessageType: a.MessageType, Subtype: a.Subtype,
		CorrelationID: a.CorrelationID, Payload: a.Payload,
	})
	return "Owner coordination chain has been notified. Continue working.", nil
}
