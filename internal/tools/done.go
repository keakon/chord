package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DoneTool requests loop/session exit handling from the MainAgent runtime.
// MainAgent intercepts its tool result and decides whether exit is allowed.
type DoneTool struct{}

func NewDoneTool() DoneTool { return DoneTool{} }

func (DoneTool) Name() string { return "Done" }

func (DoneTool) Description() string {
	return "Request to stop the current loop or hand control back to the user. " +
		"Use this only when you believe the current loop goal is complete or should stop now. " +
		"Put your full final report in the assistant message immediately before calling this tool, using concise Markdown with completion status, what changed, verification results, and any remaining limitations. " +
		"The runtime will decide whether stopping is allowed; if not, it will return a rejection result and you must continue."
}

func (DoneTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (DoneTool) IsReadOnly() bool { return true }

func (DoneTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return "Done requested", nil
	}
	return "", fmt.Errorf("invalid arguments: Done does not accept arguments")
}
