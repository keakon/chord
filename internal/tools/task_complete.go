package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// CompleteTool marks the current task as complete. It is only available to
// SubAgents. In normal flow, this tool is intercepted by
// SubAgent.handleLLMResponse (which parses the summary and sends
// EventAgentDone); Execute is a fallback that returns a placeholder string.
type CompleteTool struct{}

type completeArgs struct {
	Summary string `json:"summary"`
}

func (CompleteTool) Name() string { return "Complete" }

func (CompleteTool) Description() string {
	return "Mark the current delegated task as complete. Call this after all work is finished, " +
		"providing a summary of what was accomplished. This is the ONLY way to signal " +
		"completion — do NOT simply stop responding. " +
		"Include key outcomes, files modified, and any important notes for the owner agent."
}

func (CompleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "Summary of what was done and the result. Include files modified, tests run, and any important decisions made.",
			},
		},
		"required":             []string{"summary"},
		"additionalProperties": false,
	}
}

func (CompleteTool) IsReadOnly() bool { return false }

func (CompleteTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	// This tool is normally intercepted in SubAgent.handleLLMResponse before
	// reaching Execute. If we get here, validate args and return a placeholder.
	var a completeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Summary == "" {
		return "", fmt.Errorf("summary is required")
	}
	return "Marked as complete: " + a.Summary, nil
}
