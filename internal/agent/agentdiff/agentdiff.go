package agentdiff

import (
	"encoding/json"
	"errors"

	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

var errNotApplyPatchTool = errors.New("not an ApplyPatch tool call")

// Summary carries both the display diff text and the exact full change counts
// computed before any truncation is applied.
type Summary struct {
	Text    string
	Added   int
	Removed int
}

// CapturePreWriteState reads the current file content before a tool call
// so a before/after diff can be generated after execution.
// Returns the file path, existing content (empty string if file does not exist),
// and whether the file existed before execution.
func CapturePreWriteState(tc message.ToolCall) (filePath, content string, existed bool) {
	if tc.Name != tools.NameApplyPatch {
		return
	}
	plan, err := CaptureApplyPatchPlan(tc, "")
	if err == nil {
		return plan.Path, plan.Before, true
	}
	return
}

func CaptureApplyPatchPlan(tc message.ToolCall, baseDir string) (tools.ApplyPatchPlan, error) {
	switch tc.Name {
	case tools.NameApplyPatch:
		var args struct {
			Path  string `json:"path"`
			Patch string `json:"patch"`
		}
		rawArgs := llm.UnwrapToolArgs(tc.Args)
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return tools.ApplyPatchPlan{}, err
		}
		plan, err := tools.BuildApplyPatchPlanInDir(args.Path, args.Patch, baseDir)
		if err != nil {
			return tools.ApplyPatchPlan{}, err
		}
		return plan, nil
	default:
		return tools.ApplyPatchPlan{}, errNotApplyPatchTool
	}
}

// GenerateToolDiff builds a unified diff string for a completed file-editing tool call.
// preContent/preFilePath must have been captured before execution via
// CapturePreWriteState. Returns zero values for other tools or on any parse error.
func GenerateToolDiff(tc message.ToolCall, preContent, preFilePath string) Summary {
	switch tc.Name {
	case tools.NameApplyPatch:
		if preFilePath == "" {
			return Summary{}
		}
		decoded, err := tools.ReadDecodedTextFile(preFilePath)
		if err != nil {
			return Summary{}
		}
		s := tools.GenerateUnifiedDiffSummary(preContent, decoded.Text, preFilePath)
		return Summary{Text: s.Text, Added: s.Added, Removed: s.Removed}
	}
	return Summary{}
}
