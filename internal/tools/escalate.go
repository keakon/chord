package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type SpawnFinishedPayload struct {
	BackgroundID  string
	AgentID       string
	Kind          string
	Status        string
	Command       string
	Description   string
	MaxRuntimeSec int
	Message       string
	LogFile       string
}

func (p *SpawnFinishedPayload) EffectiveID() string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.BackgroundID)
}

// EventSender is the interface for sending events to MainAgent without
// importing the agent package (avoiding circular imports). NotifyTool and
// EscalateTool use this interface.
type EventSender interface {
	// SendAgentEvent sends a typed event from a SubAgent to the MainAgent.
	// eventType identifies the kind of event (e.g. "escalate", "agent_notify").
	// sourceID is the calling agent's instance ID.
	// payload carries event-specific data.
	SendAgentEvent(eventType, sourceID string, payload any)
}

// EscalateTool requests intervention from the direct owner / coordination chain.
// Only available to SubAgents. Use when the SubAgent is blocked and needs
// parent-agent coordination or escalation back to MainAgent.
type EscalateTool struct {
	sender EventSender
}

// NewEscalateTool creates an EscalateTool with the given EventSender.
func NewEscalateTool(sender EventSender) *EscalateTool {
	return &EscalateTool{sender: sender}
}

type escalateArgs struct {
	Reason string `json:"reason"`
}

func (EscalateTool) Name() string { return "Escalate" }

func (EscalateTool) Description() string {
	return "Request parent-agent intervention or escalation through the coordination chain. Use when: " +
		"(1) you encounter a file conflict that needs coordination, " +
		"(2) you need information from another task's output, " +
		"(3) you are blocked and need the task to be reassigned or split, " +
		"(4) you need a decision that is beyond your scope. " +
		"Unlike Complete, this does NOT end your task — you remain active."
}

func (EscalateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{
				"type":        "string",
				"description": "Why parent-agent intervention or escalation is needed. Be specific about what you need.",
			},
		},
		"required":             []string{"reason"},
		"additionalProperties": false,
	}
}

func (EscalateTool) IsReadOnly() bool { return false }

func (t *EscalateTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a escalateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Reason == "" {
		return "", fmt.Errorf("reason is required")
	}

	if t.sender == nil {
		return "", fmt.Errorf("event sender not available (no EventSender configured)")
	}

	agentID := AgentIDFromContext(ctx)
	t.sender.SendAgentEvent("escalate", agentID, a.Reason)

	return "The parent-agent coordination chain has been notified. Continue working or wait for instructions.", nil
}
