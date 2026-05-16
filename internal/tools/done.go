package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type DoneArgs struct {
	Report string `json:"report"`
}

// DoneTool requests loop/session exit handling from the MainAgent runtime.
// MainAgent intercepts its tool result and decides whether exit is allowed.
type DoneTool struct{}

func NewDoneTool() DoneTool { return DoneTool{} }

func (DoneTool) Name() string { return NameDone }

func (DoneTool) Description() string {
	return "Exit-control signal for requesting task completion or loop exit, not a generic conversation ending. " +
		"Use this only when the current objective is fully complete and no unresolved user decision, error, or verification remains. " +
		"Never call it for partial progress or while you still need to investigate, edit, test, or ask the user. " +
		"In loop mode, it requests loop exit; outside loop mode, it requests final completion and user approval. " +
		"This tool REQUIRES a non-empty 'report' argument containing the complete final Markdown completion report. " +
		"You must put the full completion summary in the report argument itself; do not rely on the surrounding assistant message to carry the report. " +
		"Write the report in the user's current language unless the user explicitly asked for a different language. " +
		"The report must include: " +
		"- **Completion status**: one line summary (e.g., 'All requested work is finished') " +
		"- **What changed**: files modified, created, deleted or key actions taken " +
		"- **Verification**: tests run and their results " +
		"- **Remaining issues**: any limitations, unverified areas, or known issues " +
		"The report argument is shown in the Done confirmation UI and is required for approval. " +
		"If you are unsure whether the task is truly complete, continue working instead of calling Done."
}

func (DoneTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"report": map[string]any{
				"type":        "string",
				"description": "Required final Markdown completion report describing completion status, changes, verification, and remaining issues. Write it in the user's current language unless the user explicitly asked for a different language.",
				"minLength":   1,
			},
		},
		"required":             []string{"report"},
		"additionalProperties": false,
	}
}

func (DoneTool) IsReadOnly() bool { return true }

func ParseDoneArgs(raw json.RawMessage) (DoneArgs, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return DoneArgs{}, fmt.Errorf("missing required argument: report")
	}
	var args DoneArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return DoneArgs{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.Report = strings.TrimSpace(args.Report)
	if args.Report == "" {
		return DoneArgs{}, fmt.Errorf("missing required argument: report")
	}
	return args, nil
}

func (DoneTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	args, err := ParseDoneArgs(raw)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Done requested: report received (%d chars)", len(args.Report)), nil
}
