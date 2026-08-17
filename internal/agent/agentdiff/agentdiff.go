package agentdiff

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/llm"
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

// CapturePreWriteState reads the current file content before a tool call
// so a before/after diff can be generated after execution.
// Returns the file path, existing content (empty string if file does not exist),
// and whether the file existed before execution.
func CapturePreWriteState(tc message.ToolCall, baseDir string) (filePath, content string, existed bool) {
	// NameEdit and NameWrite need path extraction and reading
	if tc.Name == tools.NameEdit || tc.Name == tools.NameWrite {
		rawArgs := llm.UnwrapToolArgs(tc.Args)
		var path string
		var err error
		if tc.Name == tools.NameEdit {
			path = tools.ExtractEditPathFromArgsInDir(rawArgs, baseDir)
			if path == "" {
				return
			}
		} else {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(rawArgs, &args); err != nil {
				return
			}
			path, err = tools.ResolveToolPathInDir(args.Path, baseDir)
		}
		if err != nil {
			return
		}
		decoded, err := tools.ReadDecodedTextFile(path)
		if err == nil {
			return path, decoded.Text, true
		}
		// For Write tool, return path with empty content and existed=false for new files
		if tc.Name == tools.NameWrite {
			return path, "", false
		}
	}
	// For all other tools (Delete, Read, etc.), return empty values
	return
}

// GenerateToolDiff builds a unified diff string for a completed file-editing tool call.
// preContent/preFilePath must have been captured before execution via
// CapturePreWriteState. preExisted indicates whether the file existed before the write.
// Returns zero values for other tools or on any parse error.
func GenerateToolDiff(tc message.ToolCall, preContent, preFilePath string, preExisted bool) Summary {
	switch tc.Name {
	case tools.NameEdit:
		if preFilePath == "" {
			return Summary{}
		}
		decoded, err := tools.ReadDecodedTextFile(preFilePath)
		if err != nil {
			return Summary{}
		}
		s := tools.GenerateUnifiedDiffSummary(preContent, decoded.Text, preFilePath)
		return Summary{Text: s.Text, Added: s.Added, Removed: s.Removed}
	case tools.NameWrite:
		if preFilePath == "" {
			return Summary{}
		}
		decoded, err := tools.ReadDecodedTextFile(preFilePath)
		if err != nil {
			return Summary{}
		}
		// If file existed before, compute full diff
		if preExisted {
			s := tools.GenerateUnifiedDiffSummary(preContent, decoded.Text, preFilePath)
			return Summary{Text: s.Text, Added: s.Added, Removed: s.Removed}
		}
		// New file: count all lines as added
		lines := strings.Split(decoded.Text, "\n")
		added := len(lines)
		if added > 0 && lines[added-1] == "" {
			added-- // Don't count trailing empty line from final newline
		}
		return Summary{Added: added}
	}
	return Summary{}
}
