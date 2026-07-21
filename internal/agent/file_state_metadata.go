package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func computeFileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func buildReadFileState(path string) *message.ToolFileState {
	state := trackedExistingFileState(path)
	if state == nil {
		return nil
	}
	return &message.ToolFileState{Reads: []message.TrackedFileState{*state}}
}

func buildObservedReadFileState(observation tools.ReadObservation) *message.ToolFileState {
	path := strings.TrimSpace(observation.Path)
	hash := strings.TrimSpace(observation.SHA256)
	if path == "" || hash == "" {
		return nil
	}
	return &message.ToolFileState{Reads: []message.TrackedFileState{{Path: path, SHA256: hash, Exists: true}}}
}

func buildWriteFileState(path string) *message.ToolFileState {
	state := trackedExistingFileState(path)
	if state == nil {
		return nil
	}
	return &message.ToolFileState{Writes: []message.TrackedFileState{*state}}
}

func buildLocalizedWriteFileState(path, before string) *message.ToolFileState {
	state := trackedExistingFileState(path)
	if state == nil {
		return nil
	}
	after, err := tools.ReadDecodedTextFile(path)
	if err != nil {
		return &message.ToolFileState{Writes: []message.TrackedFileState{*state}}
	}
	state.ChangedStart, state.ChangedEnd, state.LineDelta = changedPreEditLineRange(before, after.Text)
	return &message.ToolFileState{Writes: []message.TrackedFileState{*state}}
}

func changedPreEditLineRange(before, after string) (int, int, int) {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	prefix := 0
	for prefix < len(beforeLines) && prefix < len(afterLines) && beforeLines[prefix] == afterLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(beforeLines)-prefix && suffix < len(afterLines)-prefix &&
		beforeLines[len(beforeLines)-1-suffix] == afterLines[len(afterLines)-1-suffix] {
		suffix++
	}
	start := prefix + 1
	// A pure insertion affects the boundary at which it occurred.
	end := max(len(beforeLines)-suffix, start)
	return start, end, len(afterLines) - len(beforeLines)
}

func buildDeleteFileStateFromResultInDir(rawResult, baseDir string) *message.ToolFileState {
	groups := tools.ParseDeleteResult(rawResult)
	return buildDeleteFileState(tools.NormalizeDeletePathsInDir(groups.Deleted, baseDir))
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

func firstWriteHashForPath(state *message.ToolFileState, path string) string {
	if state == nil {
		return ""
	}
	key := restoreNormalizeTrackedPath(path)
	if key == "" {
		return ""
	}
	for _, write := range state.Writes {
		if restoreNormalizeTrackedPath(write.Path) == key && write.Exists && strings.TrimSpace(write.SHA256) != "" {
			return strings.TrimSpace(write.SHA256)
		}
	}
	return ""
}
