package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/worktree"
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
			path = displayPathFromWorkDir(s.effectiveToolBaseDir(), path)
			if path = strings.TrimSpace(path); path != "" {
				s.actualChangedFiles[path] = struct{}{}
				files = append(files, path)
			}
		}
		return normalizeStringList(files), false
	}

	name := tools.NormalizeName(result.Name)
	if isFileAttributionNeutralTool(name, json.RawMessage(result.ArgsJSON)) ||
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
				path = displayPathFromWorkDir(s.effectiveToolBaseDir(), path)
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

// isFileAttributionNeutralTool reports whether a tool cannot change the files
// its owner tracks, so its call must not put the completion's file list under
// suspicion. WorktreeEnter and WorktreeExit{keep} only move the agent between
// checkouts; WorktreeExit{remove} deletes a checkout — including the
// gitignored files copied into it — without recording any path, so it stays
// flagged.
func isFileAttributionNeutralTool(name string, args json.RawMessage) bool {
	switch tools.NormalizeName(name) {
	case tools.NameWorktreeEnter:
		return true
	case tools.NameWorktreeExit:
		return !worktreeExitRemovesCheckout(args)
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
		tools.NameJobOutput,
		tools.NameJobList,
		tools.NameJobKill:
		return true
	default:
		return false
	}
}

// worktreeExitRemovesCheckout mirrors the WorktreeExit action switch: only an
// explicit "remove" deletes the checkout, and unparseable args are treated as
// a removal so the conservative flag survives.
func worktreeExitRemovesCheckout(args json.RawMessage) bool {
	var req struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return true
	}
	return strings.ToLower(strings.TrimSpace(req.Action)) == "remove"
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
	if wt := s.completionWorktreeSnapshot(); wt != nil {
		out.Envelope.Worktree = wt
	}
	out.Envelope = normalizeCompletionEnvelope(out.Envelope)
	return out
}

// completionWorktreeSnapshot describes the worktree the worker is reporting
// from, including the base commit and a diff stat, so the owner can tell which
// copy of the work to pick up. It returns nil when the worker is not in a
// worktree.
func (s *SubAgent) completionWorktreeSnapshot() *CompletionWorktree {
	if s == nil {
		return nil
	}
	state := s.workDirState.load()
	if strings.TrimSpace(state.WorktreeID) == "" || strings.TrimSpace(state.Path) == "" {
		return nil
	}
	wt := &CompletionWorktree{
		Name:       state.WorktreeID,
		Branch:     state.Branch,
		Path:       state.Path,
		Base:       state.BaseSHA,
		Generation: state.Generation,
	}
	ctx := context.Background()
	if s.turn != nil && s.turn.Ctx != nil {
		ctx = s.turn.Ctx
	} else if s.parentCtx != nil {
		ctx = s.parentCtx
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stat, err := worktree.DiffStat(ctx, state.Path, state.BaseSHA)
	if err != nil {
		wt.DiffError = err.Error()
		return wt
	}
	wt.DiffStat = stat
	return wt
}

func mergeStringLists(groups ...[]string) []string {
	var all []string
	for _, group := range groups {
		all = append(all, group...)
	}
	return normalizeStringList(all)
}
