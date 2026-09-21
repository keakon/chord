package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Wire names for the events tools hand to the agent's event loop through
// EventSender. The agent package aliases these in its own Event* constants, so
// the producer and the consumer cannot silently drift apart.
const (
	EventEscalate = "escalate"
	// EventAgentNotify is the non-blocking progress/notice update NotifyTool sends.
	EventAgentNotify = "agent_notify"
	// EventBackgroundObjectFinished delivers a finished background job's result.
	EventBackgroundObjectFinished = "background_object_finished"
)

type JobFinishedPayload struct {
	BackgroundID string
	AgentID      string
	SessionDir   string
	Status       string
	Message      string
	// UserStopped marks a job the operator stopped from the interface. The
	// result still reaches the model's transcript, but the completion toast is
	// suppressed: the operator already knows they stopped it.
	UserStopped bool
}

func (p *JobFinishedPayload) EffectiveID() string {
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

// Escalation kinds. kind is required and carries no default: the two states have
// different consequences (one parks the task for a reply, the other ends the
// attempt), so a missing kind silently meaning needs_repair would be exactly the
// kind of compatibility branch this project does not carry before 1.0.
const (
	// EscalateKindNeedsRepair parks the worker until its direct owner replies.
	EscalateKindNeedsRepair = "needs_repair"
	// EscalateKindBlocked ends the attempt: the worker has hit a dead end that
	// even its owner's reply cannot resolve.
	EscalateKindBlocked = "blocked"
)

type AgentRequestPayload struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// Validate reports whether the engine can route this escalation.
func (p AgentRequestPayload) Validate() error {
	switch strings.TrimSpace(p.Kind) {
	case EscalateKindNeedsRepair, EscalateKindBlocked:
	default:
		return fmt.Errorf("kind must be %q or %q", EscalateKindNeedsRepair, EscalateKindBlocked)
	}
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	return nil
}

func (EscalateTool) Name() string { return NameEscalate }

func (EscalateTool) Description() string {
	return "Request parent-agent intervention or escalation through the coordination chain. Use kind=needs_repair for a file conflict that needs coordination, " +
		"information from another task's output, a decision beyond your scope, or a task that should be reassigned or split; unlike complete, it does not end the task — " +
		"the worker parks until its direct owner replies. Use kind=blocked only for a dead end that even the owner's reply cannot resolve, which ends this attempt as failed."
}

func (EscalateTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{EscalateKindNeedsRepair, EscalateKindBlocked},
				"description": "needs_repair waits for the owner's reply; blocked ends the attempt because no reply can resolve it. Required; there is no default.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Why parent-agent intervention or escalation is needed. Be specific about what you need.",
			},
		},
		"required":             []string{"kind", "reason"},
		"additionalProperties": false,
	}
}

func (EscalateTool) IsReadOnly() bool { return false }

func (t *EscalateTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a AgentRequestPayload
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if err := a.Validate(); err != nil {
		return "", err
	}
	if t.sender == nil {
		return "", fmt.Errorf("event sender not available (no EventSender configured)")
	}

	agentID := AgentIDFromContext(ctx)
	t.sender.SendAgentEvent(EventEscalate, agentID, a)

	if strings.TrimSpace(a.Kind) == EscalateKindBlocked {
		return "The parent-agent coordination chain has been notified that this task is blocked.", nil
	}
	return "The parent-agent coordination chain has been notified. This task will wait for its direct owner's reply.", nil
}
