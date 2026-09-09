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

// notifyResponseCorrelationHint is appended to response-delivery rejections
// (a missing correlation_id, or one that does not match a pending request) so
// the model can recover instead of retrying the same response with a guessed
// id: message_type=response answers exactly one pending request, and a plain
// follow-up is not a response.
const notifyResponseCorrelationHint = "message_type=response answers only a real pending request: " +
	"correlation_id must be the id that request actually carries, and must never be invented, guessed, or reused. " +
	"If you meant a plain follow-up to the target, call notify again without the message_type and correlation_id keys, " +
	"keeping target_task_id, message, and optionally kind. " +
	"If you do need to answer but cannot find the genuine correlation_id, stop this response and ask for coordination instead of substituting any other id."

func (t *NotifyTool) Description() string {
	const usageRule = "For a plain note, call notify with only target_task_id, message, and optionally kind — do not send message_type or correlation_id. " +
		"Use message_type=response only to answer a pending request that is genuinely waiting on you, passing exactly the correlation_id that request carries; never invent one. "
	switch {
	case t.allowOwner && t.allowTarget:
		return "Send a non-blocking update. Without target_task_id, notify your direct owner / coordination chain and continue working. " +
			"With target_task_id, deliver a clarification, correction, or follow-up to a specific delegated worker without escalating. " + usageRule + targetedNotifyResumeNote
	case t.allowTarget:
		return "Send a non-blocking clarification, decision, or correction to a delegated worker identified by target_task_id. " + usageRule + targetedNotifyResumeNote
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
			"description": "Owner notifications support progress/notice. A targeted response requires message_type=response and correlation_id, and accepts only message and kind. Omit message_type for a plain targeted message.",
		},
		"correlation_id": map[string]any{"type": "string", "description": "Required for message_type=response: use the exact correlation_id of the pending request being answered, never an invented, guessed, or reused one. Omit for plain targeted messages. Optional for owner-visible notices."},
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
	}
	params := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
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
				return "", fmt.Errorf("correlation_id is required for response: %s", notifyResponseCorrelationHint)
			}
			if a.Subtype != "" || len(a.Payload) != 0 {
				return "", fmt.Errorf("subtype and payload are unavailable for response")
			}
			handle, err := messenger.NotifySubAgentMessage(ctx, AgentResponseRequest{
				TargetTaskID: a.TargetTaskID, Message: a.Message, Kind: a.Kind, CorrelationID: a.CorrelationID,
			})
			if err != nil {
				if strings.Contains(err.Error(), "unknown pending request") {
					return "", fmt.Errorf("%w: %s", err, notifyResponseCorrelationHint)
				}
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
