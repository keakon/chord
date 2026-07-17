package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) recordTaskToolChanges(result *toolResult, isError bool) (files []string, incomplete bool) {
	if s == nil || result == nil {
		return nil, false
	}

	var exactPaths []string
	if result.FileState != nil {
		for _, state := range result.FileState.Writes {
			exactPaths = append(exactPaths, state.Path)
		}
		for _, state := range result.FileState.Deletes {
			exactPaths = append(exactPaths, state.Path)
		}
	}
	if len(exactPaths) == 0 && !isError {
		payload := &ToolResultPayload{
			CallID:      result.CallID,
			Name:        result.Name,
			ArgsJSON:    result.ArgsJSON,
			Audit:       result.Audit,
			Result:      result.Result,
			Diff:        result.Diff,
			DiffAdded:   result.DiffAdded,
			DiffRemoved: result.DiffRemoved,
		}
		if changed := changedFileSummary(payload); changed != nil {
			if paths, ok := changed["paths"].([]string); ok {
				exactPaths = append(exactPaths, paths...)
			}
		}
	}

	s.taskChangesMu.Lock()
	defer s.taskChangesMu.Unlock()
	if len(exactPaths) > 0 {
		if s.actualChangedFiles == nil {
			s.actualChangedFiles = make(map[string]struct{})
		}
		for _, path := range exactPaths {
			path = displayPathFromWorkDir(s.workDir, path)
			if path = strings.TrimSpace(path); path != "" {
				s.actualChangedFiles[path] = struct{}{}
				files = append(files, path)
			}
		}
		return normalizeStringList(files), false
	}

	name := tools.NormalizeName(result.Name)
	if isFileAttributionNeutralTool(name) ||
		tools.ConcurrencyClassForTool(s.tools, name, json.RawMessage(result.ArgsJSON)) == tools.ToolConcurrencyClassReadOnly {
		return nil, false
	}
	if isError && tools.IsFileMutation(name) {
		s.fileAttributionIncomplete = true
		return nil, true
	}
	if tool, ok := s.tools.Get(name); ok && !tool.IsReadOnly() {
		s.fileAttributionIncomplete = true
		return nil, true
	}
	return nil, false
}

func (s *SubAgent) restoreTaskToolChanges(msgs []message.Message) {
	if s == nil || len(msgs) == 0 {
		return
	}
	s.taskChangesMu.Lock()
	defer s.taskChangesMu.Unlock()
	for _, msg := range msgs {
		if msg.Role != "tool" {
			continue
		}
		paths := append([]string(nil), msg.ToolChangedPaths...)
		if len(paths) == 0 && msg.FileState != nil {
			for _, state := range msg.FileState.Writes {
				paths = append(paths, state.Path)
			}
			for _, state := range msg.FileState.Deletes {
				paths = append(paths, state.Path)
			}
		}
		for _, path := range paths {
			if len(msg.ToolChangedPaths) == 0 {
				path = displayPathFromWorkDir(s.workDir, path)
			}
			if path = strings.TrimSpace(path); path != "" {
				if s.actualChangedFiles == nil {
					s.actualChangedFiles = make(map[string]struct{})
				}
				s.actualChangedFiles[path] = struct{}{}
			}
		}
		if msg.FileAttributionIncomplete {
			s.fileAttributionIncomplete = true
		}
	}
}

func isFileAttributionNeutralTool(name string) bool {
	switch tools.NormalizeName(name) {
	case tools.NameComplete,
		tools.NameDelegate,
		tools.NameNotify,
		tools.NameEscalate,
		tools.NameCancel,
		tools.NameTodoWrite,
		tools.NameQuestion,
		tools.NameSkill,
		tools.NameHandoff,
		tools.NameDone,
		tools.NameSaveArtifact,
		tools.NameReadArtifact,
		tools.NameViewImage,
		tools.NameSpawnStatus,
		tools.NameSpawnStop:
		return true
	default:
		return false
	}
}

func (s *SubAgent) taskChangeSnapshot() (files []string, incomplete bool) {
	if s == nil {
		return nil, false
	}
	s.taskChangesMu.Lock()
	defer s.taskChangesMu.Unlock()
	files = make([]string, 0, len(s.actualChangedFiles))
	for path := range s.actualChangedFiles {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, s.fileAttributionIncomplete
}

func (s *SubAgent) enrichCompletionResult(result *AgentResult) *AgentResult {
	if result == nil {
		return nil
	}
	out := cloneAgentResult(result)
	if out.Envelope == nil {
		out.Envelope = &CompletionEnvelope{Summary: strings.TrimSpace(out.Summary)}
	}
	reported := normalizeStringList(out.Envelope.ReportedFilesChanged)
	if len(reported) == 0 {
		reported = normalizeStringList(out.Envelope.FilesChanged)
	}
	observed, incomplete := s.taskChangeSnapshot()
	actual := mergeStringLists(out.Envelope.ActualFilesChanged, observed)
	out.Envelope.ReportedFilesChanged = reported
	out.Envelope.ActualFilesChanged = actual
	out.Envelope.FilesChanged = mergeStringLists(reported, actual)
	out.Envelope.FileAttributionIncomplete = out.Envelope.FileAttributionIncomplete || incomplete
	out.Envelope = normalizeCompletionEnvelope(out.Envelope)
	return out
}

func mergeStringLists(groups ...[]string) []string {
	var all []string
	for _, group := range groups {
		all = append(all, group...)
	}
	return normalizeStringList(all)
}
