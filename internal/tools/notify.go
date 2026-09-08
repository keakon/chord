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

// ScopeGrantingSubAgentMessenger widens a task's path authority before
// delivering a plain targeted message.
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
			"type":        "string",
			"description": "Owner notifications support progress/notice. A targeted response requires message_type=response and correlation_id, and accepts message/kind but not subtype, payload, or grant_write_scope. Omit message_type for a plain targeted message.",
		},
		"correlation_id": map[string]any{"type": "string", "description": "Required for message_type=response: use the pending request's correlation_id. Omit for plain targeted messages. Optional for owner-visible notices."},
	}
	messageTypes := []string{"response"}
	if t.allowOwner {
		messageTypes = []string{"progress", "notice"}
		if t.allowTarget {
			messageTypes = append(messageTypes, "response")
		}
		properties["subtype"] = map[string]any{"type": "string", "description": "Optional application-defined subtype for an owner notification, not a targeted message."}
		properties["payload"] = map[string]any{"type": "object", "description": "Optional JSON object for an owner notification, limited to 32 KiB. Not available for targeted messages."}
	}
	properties["message_type"].(map[string]any)["enum"] = messageTypes
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
			"type":                 "object",
			"description":          "Add paths to the target worker's expected_write_scope before delivering a plain targeted message. Requires target_task_id and cannot be combined with message_type=response. Paths are only added; read_only and verification_commands cannot change. Use this instead of cancelling and re-delegating work for a missing path.",
			"properties":           writeScopePathProperties(),
			"additionalProperties": false,
		}
	}
	params := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	if t.allowTarget {
		// A scope grant widens a plain targeted message and has no meaning on a
		// structured reply. JSON Schema has no positive spelling for "these two
		// fields must not appear together", so the constraint stays a "not" and
		// relies on provider schema conversion dropping keywords the target API
		// cannot represent (Gemini's Schema has no "not" field, and an
		// unconverted one fails the whole request). Execute rejects the same
		// combination at runtime, and both property descriptions state it, so a
		// provider that drops the clause loses nothing but the structural hint.
		// Roles without grant_write_scope omit it entirely: the property cannot
		// appear there, which would make the clause vacuous.
		params["not"] = map[string]any{
			"required": []string{"message_type", "grant_write_scope"},
			"properties": map[string]any{
				"message_type": map[string]any{"enum": []string{"response"}},
			},
		}
	}
	return params
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
	if a.MessageType == "response" && a.TargetTaskID == "" {
		return "", fmt.Errorf("message_type=response requires target_task_id and correlation_id")
	}
	if a.MessageType != "" && a.MessageType != "progress" && a.MessageType != "notice" && a.MessageType != "response" {
		return "", fmt.Errorf("message_type must be progress, notice, or response")
	}
	if a.GrantScope != nil {
		if a.TargetTaskID == "" {
			return "", fmt.Errorf("grant_write_scope requires target_task_id")
		}
		if a.MessageType == "response" {
			return "", fmt.Errorf("grant_write_scope cannot be combined with message_type=response; grant paths with a separate plain targeted notify")
		}
		a.GrantScope = new(a.GrantScope.Normalized())
		if a.GrantScope.ReadOnly || len(a.GrantScope.VerificationCommands) > 0 {
			return "", fmt.Errorf("grant_write_scope adds paths only; read_only and verification_commands are fixed when the task is delegated")
		}
		if a.GrantScope.Empty() {
			return "", fmt.Errorf("grant_write_scope must add at least one file, path_prefix, or module")
		}
	}

	if a.TargetTaskID != "" {
		if !t.allowTarget {
			return "", fmt.Errorf("target_task_id is not available in this role")
		}
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
			return "", fmt.Errorf("plain targeted notify accepts message and kind; structured replies require message_type=response and correlation_id, without subtype or payload")
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
			handle, err = granter.NotifySubAgentWithScopeGrant(ctx, a.TargetTaskID, a.Message, a.Kind, *a.GrantScope)
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
