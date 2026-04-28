package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// HandoffTool signals that plan generation is complete and hands off control
// to another agent for execution. Called by the MainAgent in planner role
// after writing the plan document. The target agent is chosen by the user
// in the TUI (default: builder).
type HandoffTool struct{}

type handoffArgs struct {
	PlanPath string `json:"plan_path"`
}

func (HandoffTool) Name() string { return "Handoff" }

func (HandoffTool) Description() string {
	return "Signal that planning is complete and hand off to another agent for execution. " +
		"Always write the plan to .chord/plans/plan-XXX.md before calling this tool — " +
		"it validates that the referenced file already exists. Call this after writing the plan document to .chord/plans/. " +
		"You MUST call this when planning is done — do not just stop with a text response."
}

func (HandoffTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan_path": map[string]any{
				"type":        "string",
				"description": "Path to the plan document (e.g. .chord/plans/plan-001.md). The file must exist before calling this tool.",
			},
		},
		"required":             []string{"plan_path"},
		"additionalProperties": false,
	}
}

func (HandoffTool) IsReadOnly() bool { return false }

func (HandoffTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a handoffArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	planPath := filepath.Clean(a.PlanPath)
	if planPath == "" || planPath == "." {
		return "", fmt.Errorf("plan_path is required")
	}

	info, err := os.Stat(planPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("plan file %s not found; write the plan before calling Handoff", planPath)
		}
		return "", fmt.Errorf("stat plan file %s: %w", planPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("plan_path %s is a directory; expected a file", planPath)
	}

	// Return structured JSON for handleToolResult to parse.
	result, _ := json.Marshal(map[string]string{
		"plan_path": planPath,
	})
	return string(result), nil
}
