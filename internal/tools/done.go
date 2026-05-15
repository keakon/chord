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
	return "Exit-control signal for requesting task completion or loop exit, not a generic conversation ending. " +
		"Use this only when the current objective is fully complete and no unresolved user decision, error, or verification remains. " +
		"Never call it for partial progress or while you still need to investigate, edit, test, or ask the user. " +
		"In loop mode, it requests loop exit; outside loop mode, it requests final completion and user approval. " +
		"Before calling this tool, you MUST provide a final assistant message in Markdown format with the following structure: " +
		"- **Completion status**: one line summary (e.g., 'All requested work is finished') " +
		"- **What changed**: files modified, created, deleted or key actions taken " +
		"- **Verification**: tests run and their results " +
		"- **Remaining issues**: any limitations, unverified areas, or known issues " +
		"If you are unsure whether the task is truly complete, continue working instead of calling Done."
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
