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
	Summary              string          `json:"summary"`
	BlockedReason        string          `json:"blocked_reason,omitempty"`
	FilesChanged         []string        `json:"files_changed,omitempty"`
	RemainingLimitations []string        `json:"remaining_limitations,omitempty"`
	KnownRisks           []string        `json:"known_risks,omitempty"`
	FollowUpRecommended  []string        `json:"follow_up_recommended,omitempty"`
	Artifacts            []ArtifactRef   `json:"artifacts,omitempty"`
	ResultType           string          `json:"result_type,omitempty"`
	Result               json.RawMessage `json:"result,omitempty"`
	ResultRef            *ResultRef      `json:"result_ref,omitempty"`
}

func (CompleteTool) Name() string { return NameComplete }

func (CompleteTool) Description() string {
	return "Mark the current delegated task as complete. Call this only when the assigned task is finished. " +
		"Provide a concise summary plus structured completion details when available. For generic machine-readable output, provide result_type together with exactly one of result (a small JSON object) or result_ref (an existing immutable result reference); otherwise omit all three fields. " +
		"If a true blocker prevents completion, use the blocker route in the SubAgent Coordination section when your role provides one; otherwise call this tool with blocked_reason to end the task as failed, not completed. A blocked task need not deliver the result required for successful completion. This tool is the only way to signal completion; plain assistant text does not complete the task."
}

// Parameters states the result_type/result/result_ref pairing in the field
// descriptions instead of a top-level combinator: strict Anthropic endpoints
// reject a tool input_schema carrying anyOf, oneOf, or allOf at the top level,
// which would fail the whole request. The completion path
// (validateCompleteTypedResult) enforces the pairing at runtime.
func (CompleteTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{
				"type":        "string",
				"description": "Summary of what was done and the result.",
			},
			"blocked_reason": map[string]any{
				"type":        "string",
				"description": "A true blocker preventing the assigned task from finishing. A non-empty value ends the task as failed instead of completed; omit for successful completion with non-blocking limitations.",
			},
			"files_changed": map[string]any{
				"type":        "array",
				"description": "Actual files changed by this task. List only files that were actually modified.",
				"items":       map[string]any{"type": "string"},
			},
			"remaining_limitations": map[string]any{
				"type":        "array",
				"description": "Non-blocking limitations, caveats, or unverified items. For true blockers, use the SubAgent Coordination section's blocker route or blocked_reason.",
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
			"result_type": map[string]any{"type": "string", "description": "Application-defined type for a generic machine-readable result. Required whenever result or result_ref is supplied: provide result_type together with exactly one of result or result_ref."},
			"result":      map[string]any{"type": "object", "description": "Small JSON-object result. Only accepted when result_type is also provided (they must be supplied together). Runtime persists an immutable ResultRef automatically."},
			"result_ref": map[string]any{
				"type": "object", "description": "Existing immutable ResultRef for the machine-readable result. Only accepted when result_type is also provided (they must be supplied together).",
				"properties": map[string]any{
					"id": map[string]any{"type": "string"}, "result_type": map[string]any{"type": "string"},
					"rel_path": map[string]any{"type": "string"}, "sha256": map[string]any{"type": "string"}, "size_bytes": map[string]any{"type": "integer"},
				},
				"required": []string{"id", "result_type", "rel_path", "sha256", "size_bytes"}, "additionalProperties": false,
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
	if len(a.Result) > 0 && a.ResultRef != nil {
		return "", fmt.Errorf("provide exactly one of result or result_ref, not both")
	}
	if reason := strings.TrimSpace(a.BlockedReason); reason != "" {
		return "Marked as blocked: " + reason, nil
	}
	return "Marked as complete: " + strings.TrimSpace(a.Summary), nil
}
