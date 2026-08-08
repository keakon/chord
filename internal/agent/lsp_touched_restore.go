package agent

import (
	"sort"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// RebuildTouchedPathsFromMessages reconstructs the session-scoped touched-file set
// from persisted tool history. Successful Write/Edit add files; successful Delete
// removes files. Read-only tools are ignored.
func RebuildTouchedPathsFromMessages(msgs []message.Message, projectRoot string) []string {
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
			if name != tools.NameWrite && name != tools.NameEdit && name != tools.NameApplyPatch && name != tools.NameDelete {
				continue
			}
			paths := extractHookToolFilePaths(name, tc.Args, projectRoot)
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
		if msg.Role != "tool" || !message.ToolResultSucceeded(msg.Content) {
			continue
		}
		info, ok := calls[msg.ToolCallID]
		if !ok {
			continue
		}
		if msg.FileState != nil {
			for _, write := range msg.FileState.Writes {
				if write.Exists {
					paths[write.Path] = struct{}{}
				}
			}
			for _, deleted := range msg.FileState.Deletes {
				if !deleted.Exists {
					delete(paths, deleted.Path)
				}
			}
			if len(msg.FileState.Writes) > 0 || len(msg.FileState.Deletes) > 0 {
				continue
			}
		}
		switch info.name {
		case tools.NameWrite, tools.NameEdit, tools.NameApplyPatch:
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
