package agent

import (
	"os"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// RebuildTouchedPathsFromMessages reconstructs the session-scoped touched-file set
// from persisted tool history. Successful Write/Edit add files; successful Delete
// removes files. Read-only tools are ignored.
func RebuildTouchedPathsFromMessages(msgs []message.Message) []string {
	type callInfo struct {
		name  string
		paths []string
	}
	calls := make(map[string]callInfo)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			name := tools.NormalizeName(tc.Name)
			if name != tools.NameWrite && name != tools.NameEdit && name != tools.NameDelete {
				continue
			}
			paths := extractHookFilePaths(tc.Args, os.Getenv("CHORD_PROJECT_ROOT"))
			if len(paths) == 0 {
				continue
			}
			calls[tc.ID] = callInfo{name: name, paths: paths}
		}
	}
	if len(calls) == 0 {
		return nil
	}
	paths := make(map[string]struct{})
	for _, msg := range msgs {
		if msg.Role != "tool" || !restoredToolResultSucceeded(msg.Content) {
			continue
		}
		info, ok := calls[msg.ToolCallID]
		if !ok {
			continue
		}
		switch info.name {
		case tools.NameWrite, tools.NameEdit:
			for _, path := range info.paths {
				paths[path] = struct{}{}
			}
		case tools.NameDelete:
			groups := tools.ParseDeleteResult(msg.Content)
			for _, path := range groups.Deleted {
				delete(paths, path)
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func restoredToolResultSucceeded(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	if lower == "cancelled" || strings.HasPrefix(lower, "cancelled\n") {
		return false
	}
	if strings.HasPrefix(trimmed, "Error: ") || strings.Contains(trimmed, "\n\nError: ") || strings.HasPrefix(trimmed, "Model stopped before completing this tool call") {
		return false
	}
	return true
}
