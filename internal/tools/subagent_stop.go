package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
)

// SubAgentStopper is the interface used by CancelTool to stop an
// existing worker without importing the agent package.
type SubAgentStopper interface {
	CancelSubAgent(ctx context.Context, taskID, reason string) (TaskHandle, error)
}

type CancelTool struct {
	stopper SubAgentStopper
}

func NewCancelTool(stopper SubAgentStopper) *CancelTool {
	return &CancelTool{stopper: stopper}
}

type cancelArgs struct {
	TargetTaskID string `json:"target_task_id"`
	Reason       string `json:"reason,omitempty"`
}

func (CancelTool) Name() string { return "Cancel" }

func (CancelTool) Description() string {
	return "Cancel a delegated worker identified by target_task_id. " +
		"Use this when delegated work should be abandoned, superseded, or explicitly cancelled instead of continuing in the background."
}

func (CancelTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target_task_id": map[string]any{
				"type":        "string",
				"description": "Stable durable task handle returned by Delegate, for example 'adhoc-3' or a plan task ID.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional reason to record in the worker state and UI.",
			},
		},
		"required":             []string{"target_task_id"},
		"additionalProperties": false,
	}
}

func (CancelTool) IsReadOnly() bool { return false }

func (CancelTool) VisibleWithRuleset(ruleset permission.Ruleset) bool {
	if ruleset.IsDisabled("Cancel") {
		return false
	}
	return !ruleset.IsDisabled("Delegate")
}

func (t *CancelTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a cancelArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.TargetTaskID = strings.TrimSpace(a.TargetTaskID)
	a.Reason = strings.TrimSpace(a.Reason)
	if a.TargetTaskID == "" {
		return "", fmt.Errorf("target_task_id is required")
	}
	if t.stopper == nil {
		return "", fmt.Errorf("delegate cancel not available")
	}
	handle, err := t.stopper.CancelSubAgent(ctx, a.TargetTaskID, a.Reason)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(handle)
	if err != nil {
		return "", fmt.Errorf("marshal cancel handle: %w", err)
	}
	return string(out), nil
}
