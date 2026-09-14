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

// CompletionReportStructure is the required section shape of the final Markdown
// completion report. Shared by the done tool description and the agent's
// loop-mode completion instructions so the two never drift apart.
const CompletionReportStructure = "- **Completion status**: one line summary (e.g., 'All requested work is finished')\n" +
	"- **What changed**: files modified, created, deleted or key actions taken\n" +
	"- **Verification**: tests run and their results\n" +
	"- **Remaining issues**: any limitations, unverified areas, or known issues"

func (DoneTool) Name() string { return NameDone }

// Description is written for the only situation in which the tool is mounted:
// an active loop, whose completion contract designates it as the required exit
// signal. It therefore states when exit is warranted instead of arguing
// against being called at all — the runtime keeps the tool off the surface
// outside a loop, so that argument no longer has an audience.
func (DoneTool) Description() string {
	return "Requests exit from the active loop workflow, which requires this tool as its completion signal.\n" +
		"First use any available tool that can make real progress; call `" + NameDone + "` only when the current objective is fully complete, no blocker or unresolved user decision remains, and no other tool call is necessary or appropriate. Required verification must be completed, or explicitly reported as not run with the reason it could not be run. Never call it for partial progress or while you still have necessary investigation, edits, runnable verification, or user questions. If you are unsure whether the task is truly complete, continue working instead of calling `" + NameDone + "`.\n" +
		"Provide a non-empty 'report' argument containing the complete final Markdown completion report; put the full completion summary in the report argument itself and do not rely on the surrounding assistant message to carry it.\n" +
		"The report must include:\n" + CompletionReportStructure
}

func (DoneTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"report": map[string]any{
				"type":        "string",
				"description": "The complete final Markdown report describing completion status, changes, verification, and remaining issues. Write it in the user's current language unless the user explicitly asked for a different language.",
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
