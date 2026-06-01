package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type restoreTrackedFileStateResult struct {
	RestoredUsable int
	RestoredStale  int
	Skipped        int

	SkippedNonNativeProvenance int
	SkippedNonSuccessResult    int
	SkippedMissingArgs         int
	SkippedInvalidPath         int
	SkippedStateMismatch       int
	SkippedDeleteState         int
	SkippedMissingDurableState int
}

func (r *restoreTrackedFileStateResult) skipNonNativeProvenance() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedNonNativeProvenance++
}

func (r *restoreTrackedFileStateResult) skipNonSuccessResult() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedNonSuccessResult++
}

func (r *restoreTrackedFileStateResult) skipMissingArgs() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedMissingArgs++
}

func (r *restoreTrackedFileStateResult) skipInvalidPath() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedInvalidPath++
}

func (r *restoreTrackedFileStateResult) skipStateMismatch() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedStateMismatch++
}

func (r *restoreTrackedFileStateResult) skipDeleteState() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedDeleteState++
}

func (r *restoreTrackedFileStateResult) skipMissingDurableState() {
	if r == nil {
		return
	}
	r.Skipped++
	r.SkippedMissingDurableState++
}

type restoreToolCall struct {
	name string
	args json.RawMessage
}

type restoreReadCandidate struct {
	path    string
	hash    string
	durable bool
}

// restoreTrackedFileStateFromMessages rebuilds the tracked-read safety
// sentinel from durable tool metadata persisted in prior session messages.
// Restored reads are attached to the current runtime agentID rather than the
// historical main/subagent IDs that originally produced them, so a restored
// session regains the conversation-level edit precondition in the current
// runtime without replaying the old execution topology.
func restoreTrackedFileStateFromMessages(tracker *filelock.FileTracker, agentID string, messages []message.Message) restoreTrackedFileStateResult {
	var result restoreTrackedFileStateResult
	if tracker == nil || strings.TrimSpace(agentID) == "" || len(messages) == 0 {
		return result
	}

	calls := make(map[string]restoreToolCall)
	candidates := make(map[string]restoreReadCandidate)

	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			if !isNativeChordProvenance(msg.Provenance) {
				continue
			}
			for _, tc := range msg.ToolCalls {
				id := strings.TrimSpace(tc.ID)
				name := strings.TrimSpace(tc.Name)
				if id == "" || !isTrackedRestoreTool(name) {
					continue
				}
				calls[id] = restoreToolCall{name: name, args: append(json.RawMessage(nil), tc.Args...)}
			}

		case "tool":
			callID := strings.TrimSpace(msg.ToolCallID)
			if callID == "" {
				continue
			}
			call, ok := calls[callID]
			if !ok {
				continue
			}
			if !isNativeChordProvenance(msg.Provenance) {
				result.skipNonNativeProvenance()
				continue
			}
			if !restoreToolResultSucceeded(msg) {
				result.skipNonSuccessResult()
				continue
			}

			args, ok := restoreEffectiveArgs(msg, call)
			if !ok {
				result.skipMissingArgs()
				continue
			}

			switch call.name {
			case tools.NameRead:
				path, key, ok := restoreSinglePathAndKey(args, call.name)
				if !ok {
					result.skipInvalidPath()
					continue
				}
				hash, durable := restoreReadStateHashForPath(msg.FileState, path)
				if msg.FileState != nil && len(msg.FileState.Reads) > 0 && !durable {
					result.skipStateMismatch()
					continue
				}
				candidates[key] = restoreReadCandidate{path: key, hash: hash, durable: durable}

			case tools.NameEdit:
				path, key, ok := restoreSinglePathAndKey(args, call.name)
				if !ok {
					result.skipInvalidPath()
					continue
				}
				if statePath, durable := restoreEditWriteStatePath(msg.FileState, key); durable {
					path = statePath
					key = restoreNormalizeTrackedPath(path)
				}
				candidate, exists := candidates[key]
				if !exists {
					continue
				}
				hash, durable := restoreWriteStateHashForPath(msg.FileState, path)
				if msg.FileState != nil && len(msg.FileState.Writes) > 0 && !durable {
					delete(candidates, key)
					result.skipStateMismatch()
					continue
				}
				candidate.hash = hash
				candidate.durable = durable
				candidates[key] = candidate

			case tools.NameWrite:
				path, key, ok := restoreSinglePathAndKey(args, call.name)
				if !ok {
					result.skipInvalidPath()
					continue
				}
				candidate, exists := candidates[key]
				if !exists {
					continue
				}
				hash, durable := restoreWriteStateHashForPath(msg.FileState, path)
				if msg.FileState != nil && len(msg.FileState.Writes) > 0 && !durable {
					delete(candidates, key)
					result.skipStateMismatch()
					continue
				}
				candidate.hash = hash
				candidate.durable = durable
				candidates[key] = candidate

			case tools.NameDelete:
				paths := restoreDeletePaths(msg, args)
				if len(paths) == 0 {
					result.skipDeleteState()
					continue
				}
				for _, path := range paths {
					key := restoreNormalizeTrackedPath(path)
					if key == "" {
						continue
					}
					delete(candidates, key)
				}
			}
		}
	}

	for _, candidate := range candidates {
		if candidate.path == "" {
			result.skipInvalidPath()
			continue
		}
		if !candidate.durable || strings.TrimSpace(candidate.hash) == "" {
			result.skipMissingDurableState()
			continue
		}
		current := computeFileHash(candidate.path)
		tracker.TrackRead(candidate.path, agentID, candidate.hash)
		if current != "" && current == candidate.hash {
			result.RestoredUsable++
		} else {
			result.RestoredStale++
		}
	}

	return result
}

func (a *MainAgent) restoreMainTrackedFileState(messages []message.Message) restoreTrackedFileStateResult {
	if a == nil {
		return restoreTrackedFileStateResult{}
	}
	if a.fileTrack == nil {
		a.fileTrack = filelock.NewFileTracker()
	}
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, messages)
	log.Debugf("restored tracked file state from session restored_usable=%d restored_stale=%d skipped=%d skipped_non_native=%d skipped_non_success=%d skipped_missing_args=%d skipped_invalid_path=%d skipped_state_mismatch=%d skipped_delete_state=%d skipped_missing_durable_state=%d",
		result.RestoredUsable,
		result.RestoredStale,
		result.Skipped,
		result.SkippedNonNativeProvenance,
		result.SkippedNonSuccessResult,
		result.SkippedMissingArgs,
		result.SkippedInvalidPath,
		result.SkippedStateMismatch,
		result.SkippedDeleteState,
		result.SkippedMissingDurableState,
	)
	return result
}

func isTrackedRestoreTool(name string) bool {
	switch strings.TrimSpace(name) {
	case tools.NameRead, tools.NameEdit, tools.NameWrite, tools.NameDelete:
		return true
	default:
		return false
	}
}

func isNativeChordProvenance(prov *message.MessageProvenance) bool {
	if prov == nil {
		return true
	}
	if prov.Imported {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(prov.Source))
	return source == "" || source == "chord"
}

func restoreToolResultSucceeded(msg message.Message) bool {
	return strings.EqualFold(strings.TrimSpace(msg.ToolStatus), "success")
}

func restoreEffectiveArgs(msg message.Message, call restoreToolCall) (json.RawMessage, bool) {
	if msg.Audit != nil {
		if effective := strings.TrimSpace(msg.Audit.EffectiveArgsJSON); effective != "" {
			return json.RawMessage(effective), true
		}
	}
	if len(call.args) == 0 {
		return nil, false
	}
	return append(json.RawMessage(nil), call.args...), true
}

func restoreSinglePathAndKey(args json.RawMessage, toolName string) (string, string, bool) {
	path, ok := parseRestoreSinglePath(args)
	if !ok && toolName == tools.NameEdit {
		path = tools.ExtractEditPathFromArgsInDir(args, os.Getenv("CHORD_PROJECT_ROOT"))
		ok = path != ""
	}
	if !ok {
		return "", "", false
	}
	key := restoreNormalizeTrackedPath(path)
	if key == "" {
		return "", "", false
	}
	return path, key, true
}

func parseRestoreSinglePath(args json.RawMessage) (string, bool) {
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(llm.UnwrapToolArgs(args), &parsed); err != nil {
		return "", false
	}
	path := strings.TrimSpace(parsed.Path)
	return path, path != ""
}

func restoreDeletePaths(msg message.Message, args json.RawMessage) []string {
	if msg.FileState != nil && len(msg.FileState.Deletes) > 0 {
		paths := make([]string, 0, len(msg.FileState.Deletes))
		for _, state := range msg.FileState.Deletes {
			path := strings.TrimSpace(state.Path)
			if path != "" && !state.Exists {
				paths = append(paths, path)
			}
		}
		if len(paths) > 0 {
			return paths
		}
	}
	if groups := tools.ParseDeleteResult(msg.Content); len(groups.Deleted) > 0 {
		return groups.Deleted
	}
	if req, err := tools.DecodeDeleteRequest(llm.UnwrapToolArgs(args)); err == nil {
		return req.Paths
	}
	var parsed struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(llm.UnwrapToolArgs(args), &parsed); err != nil {
		return nil
	}
	return tools.NormalizeDeletePaths(parsed.Paths)
}

func restoreWriteStatePathForKey(state *message.ToolFileState, key string) (string, bool) {
	if state == nil || len(state.Writes) == 0 || key == "" {
		return "", false
	}
	for _, st := range state.Writes {
		if !st.Exists || strings.TrimSpace(st.SHA256) == "" {
			continue
		}
		if restoreNormalizeTrackedPath(st.Path) == key {
			return strings.TrimSpace(st.Path), true
		}
	}
	return "", false
}

func restoreEditWriteStatePath(state *message.ToolFileState, key string) (string, bool) {
	if path, ok := restoreWriteStatePathForKey(state, key); ok {
		return path, true
	}
	if state == nil || len(state.Writes) != 1 {
		return "", false
	}
	st := state.Writes[0]
	if !st.Exists || strings.TrimSpace(st.SHA256) == "" || strings.TrimSpace(st.Path) == "" {
		return "", false
	}
	return strings.TrimSpace(st.Path), true
}

func restoreReadStateHashForPath(state *message.ToolFileState, path string) (string, bool) {
	if state == nil {
		return "", false
	}
	return restoreStateHashForPath(state.Reads, path)
}

func restoreWriteStateHashForPath(state *message.ToolFileState, path string) (string, bool) {
	if state == nil {
		return "", false
	}
	return restoreStateHashForPath(state.Writes, path)
}

func restoreStateHashForPath(states []message.TrackedFileState, path string) (string, bool) {
	if len(states) == 0 {
		return "", false
	}
	key := restoreNormalizeTrackedPath(path)
	for _, state := range states {
		if !state.Exists || strings.TrimSpace(state.SHA256) == "" {
			continue
		}
		if restoreNormalizeTrackedPath(state.Path) == key {
			return strings.TrimSpace(state.SHA256), true
		}
	}
	return "", false
}

func restoreNormalizeTrackedPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		path = eval
	}
	path = filepath.Clean(path)
	if path == "." {
		return ""
	}
	return path
}
