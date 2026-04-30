package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CompleteTool marks the current task as complete. It is only available to
// SubAgents. In normal flow, this tool is intercepted by
// SubAgent.handleLLMResponse (which parses the summary and sends
// EventAgentDone); Execute is a fallback that returns a placeholder string.
type CompleteTool struct{}

type completeArgs struct {
	Summary              string        `json:"summary"`
	FilesChanged         []string      `json:"files_changed,omitempty"`
	VerificationRun      []string      `json:"verification_run,omitempty"`
	RemainingLimitations []string      `json:"remaining_limitations,omitempty"`
	KnownRisks           []string      `json:"known_risks,omitempty"`
	FollowUpRecommended  []string      `json:"follow_up_recommended,omitempty"`
	Artifacts            []ArtifactRef `json:"artifacts,omitempty"`
}

func (CompleteTool) Name() string { return "Complete" }

func (CompleteTool) Description() string {
	return "Mark the current delegated task as complete. Call this only after all non-blocked work is finished. " +
		"Provide a concise summary plus structured completion details when available: actual files changed, verification run, non-blocking limitations/risks, recommended follow-up, and artifact references. " +
		"If a true blocker prevents completion, use Escalate/Notify/blocked flow instead of Complete. This is the ONLY way to signal completion — do NOT simply stop responding."
}

func (CompleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "Summary of what was done and the result.",
			},
			"files_changed": map[string]any{
				"type":        "array",
				"description": "Actual files changed by this task. Do not list expected scope unless it was actually changed.",
				"items":       map[string]any{"type": "string"},
			},
			"verification_run": map[string]any{
				"type":        "array",
				"description": "Verification commands or checks actually run. Leave empty and explain in remaining_limitations if not run.",
				"items":       map[string]any{"type": "string"},
			},
			"remaining_limitations": map[string]any{
				"type":        "array",
				"description": "Non-blocking limitations, caveats, or unverified items. True blockers should use Escalate/Notify instead of Complete.",
				"items":       map[string]any{"type": "string"},
			},
			"known_risks": map[string]any{
				"type":        "array",
				"description": "Known non-blocking risks for owner acceptance review.",
				"items":       map[string]any{"type": "string"},
			},
			"follow_up_recommended": map[string]any{
				"type":        "array",
				"description": "Recommended follow-up actions, if any.",
				"items":       map[string]any{"type": "string"},
			},
			"artifacts": map[string]any{
				"type":        "array",
				"description": "Runtime artifact references, such as research reports or verification logs, when available.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "string"},
						"type":        map[string]any{"type": "string"},
						"rel_path":    map[string]any{"type": "string"},
						"path":        map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
						"mime_type":   map[string]any{"type": "string"},
						"size_bytes":  map[string]any{"type": "integer"},
					},
					"additionalProperties": false,
				},
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
	if strings.TrimSpace(a.Summary) == "" {
		return "", fmt.Errorf("summary is required")
	}
	return "Marked as complete: " + strings.TrimSpace(a.Summary), nil
}
