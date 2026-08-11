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

func (DoneTool) Description() string {
	return "Exceptional tool-based completion signal, not the default way to end a conversation.\n" +
		"Call this tool only when an explicit workflow instruction in the current conversation (for example a loop anchor stating that Done is required to exit) designates it as the required completion signal; otherwise DO NOT call it and return the final answer directly as assistant text. Tool availability, completed work, or this tool's required report argument do not by themselves require a Done call.\n" +
		"When such an instruction is active, first use any available tool that can make real progress; call Done only when the current objective is fully complete, no unresolved user decision, error, or verification remains, and no other tool call is necessary or appropriate. Never call it for partial progress or while you still need to investigate, edit, test, or ask the user. If you are unsure whether the task is truly complete, continue working instead of calling Done.\n" +
		"When calling Done, provide a non-empty 'report' argument containing the complete final Markdown completion report; put the full completion summary in the report argument itself and do not rely on the surrounding assistant message to carry it.\n" +
		"The report must include:\n" + CompletionReportStructure
}

func (DoneTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"report": map[string]any{
				"type":        "string",
				"description": "When the runtime explicitly requires this exceptional completion tool, provide the complete final Markdown report describing completion status, changes, verification, and remaining issues. Otherwise, do not call Done; return the result directly as assistant text. Write the report in the user's current language unless the user explicitly asked for a different language.",
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
