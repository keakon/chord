package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type doneArgs struct {
	Reason string `json:"reason,omitempty"`
}

// DoneTool requests loop/session exit handling from the MainAgent runtime.
// MainAgent intercepts its tool result and decides whether exit is allowed.
type DoneTool struct{}

func NewDoneTool() DoneTool { return DoneTool{} }

func (DoneTool) Name() string { return "Done" }

func (DoneTool) Description() string {
	return "Request to stop the current loop or hand control back to the user. " +
		"Use this only when you believe the current loop goal is complete or should stop now. " +
		"Put your full final report in reason, using concise Markdown with completion status, what changed, verification results, and any remaining limitations. " +
		"The runtime will decide whether stopping is allowed; if not, it will return a rejection result and you must continue."
}

func (DoneTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason": map[string]any{
				"type":        "string",
				"description": "Detailed final report in Markdown: completion status, summary of work, verification status, and remaining limitations.",
			},
		},
		"additionalProperties": false,
	}
}

func (DoneTool) IsReadOnly() bool { return true }

func (DoneTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "Done requested", nil
	}
	var args doneArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Reason) == "" {
		return "Done requested", nil
	}
	return strings.TrimSpace(args.Reason), nil
}
