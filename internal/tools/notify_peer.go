package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
)

// PeerSubAgentMessenger routes data messages between sibling tasks without
// granting either task control over the other's lifecycle or scope.
type PeerSubAgentMessenger interface {
	NotifyPeerMessage(ctx context.Context, request AgentPeerNoticeRequest) (TaskHandle, error)
}

type AgentPeerNoticeRequest struct {
	TargetTaskID string `json:"target_task_id"`
	Message      string `json:"message"`
	Kind         string `json:"kind,omitempty"`
}

type NotifyPeerTool struct {
	messenger PeerSubAgentMessenger
}

func NewNotifyPeerTool(messenger PeerSubAgentMessenger) *NotifyPeerTool {
	return &NotifyPeerTool{messenger: messenger}
}

func (NotifyPeerTool) Name() string { return NameNotifyPeer }

func (NotifyPeerTool) Description() string {
	return "Send a non-blocking notice to a live sibling task that has the same direct owner. " +
		"Use the owner-mediated Escalate/Notify path when a reply or decision is required. This does not grant control over the peer."
}

func (NotifyPeerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target_task_id": map[string]any{"type": "string", "description": "Durable task ID of a live sibling owned by the same direct owner."},
			"message":        map[string]any{"type": "string", "description": "Informational data to send without waiting for a reply."},
			"kind":           map[string]any{"type": "string", "description": "Optional display hint."},
		},
		"required":             []string{"target_task_id", "message"},
		"additionalProperties": false,
	}
}

func (NotifyPeerTool) IsReadOnly() bool { return false }

func (t *NotifyPeerTool) VisibleWithRuleset(ruleset permission.Ruleset) bool {
	return ruleset.Evaluate(NameNotifyPeer, "*") != permission.ActionDeny
}

func (t *NotifyPeerTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var request AgentPeerNoticeRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	request.TargetTaskID = strings.TrimSpace(request.TargetTaskID)
	request.Message = strings.TrimSpace(request.Message)
	request.Kind = strings.TrimSpace(request.Kind)
	if request.TargetTaskID == "" || request.Message == "" {
		return "", fmt.Errorf("target_task_id and message are required")
	}
	if t.messenger == nil {
		return "", fmt.Errorf("peer routing is unavailable")
	}
	handle, err := t.messenger.NotifyPeerMessage(ctx, request)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(handle)
	if err != nil {
		return "", fmt.Errorf("marshal peer notify handle: %w", err)
	}
	return string(out), nil
}
