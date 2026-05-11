package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func buildReadFileState(path string) *message.ToolFileState {
	state := trackedExistingFileState(path)
	if state == nil {
		return nil
	}
	return &message.ToolFileState{Reads: []message.TrackedFileState{*state}}
}

func buildWriteFileState(path string) *message.ToolFileState {
	state := trackedExistingFileState(path)
	if state == nil {
		return nil
	}
	return &message.ToolFileState{Writes: []message.TrackedFileState{*state}}
}

func buildDeleteFileStateFromResult(rawResult string) *message.ToolFileState {
	groups := tools.ParseDeleteResult(rawResult)
	return buildDeleteFileState(groups.Deleted)
}

func buildDeleteFileState(paths []string) *message.ToolFileState {
	if len(paths) == 0 {
		return nil
	}
	deletes := make([]message.TrackedFileState, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		deletes = append(deletes, message.TrackedFileState{Path: path, Exists: false})
	}
	if len(deletes) == 0 {
		return nil
	}
	return &message.ToolFileState{Deletes: deletes}
}

func trackedExistingFileState(path string) *message.TrackedFileState {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	hash := computeFileHash(path)
	if hash == "" {
		return nil
	}
	return &message.TrackedFileState{Path: path, SHA256: hash, Exists: true}
}

func firstReadHashForPath(state *message.ToolFileState, path string) string {
	if state == nil {
		return ""
	}
	key := restoreNormalizeTrackedPath(path)
	if key == "" {
		return ""
	}
	for _, read := range state.Reads {
		if restoreNormalizeTrackedPath(read.Path) == key && read.Exists && strings.TrimSpace(read.SHA256) != "" {
			return strings.TrimSpace(read.SHA256)
		}
	}
	return ""
}
