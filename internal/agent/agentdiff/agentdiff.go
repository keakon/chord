package agentdiff

import (
	"encoding/json"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Summary carries both the display diff text and the exact full change counts
// computed before any truncation is applied.
type Summary struct {
	Text    string
	Added   int
	Removed int
}

// CapturePreWriteState reads the current file content before a Write or Edit
// tool call so a before/after diff can be generated after execution.
// Returns the file path, existing content (empty string if file does not exist),
// and whether the file existed before execution.
func CapturePreWriteState(tc message.ToolCall) (filePath, content string, existed bool) {
	if tc.Name != "Write" && tc.Name != "Edit" {
		return
	}
	var args struct {
		FilePath string `json:"path"`
	}
	if json.Unmarshal(tc.Args, &args) != nil {
		return
	}
	filePath = args.FilePath
	decoded, err := tools.ReadDecodedTextFile(filePath)
	if err != nil {
		return filePath, "", false
	}
	return filePath, decoded.Text, true
}

// GenerateToolDiff builds a unified diff string for a completed Write or Edit
// tool call. preContent/preFilePath must have been captured before execution
// via CapturePreWriteState. Returns zero values for other tools or on any parse error.
func GenerateToolDiff(tc message.ToolCall, preContent, preFilePath string) Summary {
	switch tc.Name {
	case "Write":
		var args struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(tc.Args, &args) != nil {
			return Summary{}
		}
		decoded, err := tools.DecodeToolStringArgForAgent(args.Content)
		if err != nil {
			return Summary{}
		}
		s := tools.GenerateUnifiedDiffSummary(preContent, decoded, preFilePath)
		return Summary{Text: s.Text, Added: s.Added, Removed: s.Removed}

	case "Edit":
		var args struct {
			FilePath string `json:"path"`
		}
		if json.Unmarshal(tc.Args, &args) != nil {
			return Summary{}
		}
		decoded, err := tools.ReadDecodedTextFile(args.FilePath)
		if err != nil {
			return Summary{}
		}
		s := tools.GenerateUnifiedDiffSummary(preContent, decoded.Text, args.FilePath)
		return Summary{Text: s.Text, Added: s.Added, Removed: s.Removed}
	}
	return Summary{}
}
