package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type speculativeToolHooks struct {
	commit       func()
	rollback     func() error
	captureAfter func()
	stale        bool
	paths        []string
}

type speculativeFileSnapshot struct {
	Path        string
	Existed     bool
	Data        []byte
	Hash        string
	PostExisted bool
	PostHash    string
}

type speculativeFileMutation struct {
	agentID    string
	track      *filelock.FileTracker
	files      []speculativeFileSnapshot
	stale      bool
	paths      []string
	commit     bool
	rolledBack bool
}

func prepareSpeculativeToolCall(tc message.ToolCall, track *filelock.FileTracker, agentID, projectRoot string) (*speculativeToolHooks, error) {
	switch strings.TrimSpace(tc.Name) {
	case tools.NameWrite:
		path, ok := singlePathToolPath(tc.Args)
		if !ok {
			return nil, nil
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, []string{path})
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	case tools.NameApplyPatch:
		path := tools.ExtractApplyPatchPathFromArgsInDir(tc.Args, projectRoot)
		if path == "" {
			return nil, nil
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, []string{path})
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	case tools.NameDelete:
		paths, err := deleteToolPaths(tc.Args)
		if err != nil {
			return nil, err
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, paths)
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	default:
		return nil, nil
	}
}

func singlePathToolPath(args json.RawMessage) (string, bool) {
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(llm.UnwrapToolArgs(args), &parsed); err != nil {
		return "", false
	}
	path := normalizeSpeculativeMutationPath(parsed.Path)
	if path == "" {
		return "", false
	}
	return path, true
}

func deleteToolPaths(args json.RawMessage) ([]string, error) {
	req, err := tools.DecodeDeleteRequest(llm.UnwrapToolArgs(args))
	if err != nil {
		return nil, err
	}
	return req.Paths, nil
}

func newSpeculativeFileMutation(track *filelock.FileTracker, agentID string, paths []string) (*speculativeFileMutation, error) {
	paths = normalizeSpeculativeMutationPaths(paths)
	if len(paths) == 0 {
		return nil, nil
	}
	mutation := &speculativeFileMutation{agentID: agentID, track: track, files: make([]speculativeFileSnapshot, 0, len(paths))}
	locked := make([]string, 0, len(paths))
	for _, path := range paths {
		snap, err := captureSpeculativeFileSnapshot(path)
		if err != nil {
			for i := len(locked) - 1; i >= 0; i-- {
				if track != nil {
					track.AbortWrite(locked[i], agentID)
				}
			}
			return nil, err
		}
		if track != nil && snap.Existed {
			status, err := track.AcquireWriteStatus(path, agentID, snap.Hash)
			if err != nil {
				for i := len(locked) - 1; i >= 0; i-- {
					track.AbortWrite(locked[i], agentID)
				}
				return nil, err
			}
			if status.ExternalChanged {
				mutation.stale = true
			}
			locked = append(locked, path)
			mutation.paths = append(mutation.paths, path)
		}
		mutation.files = append(mutation.files, snap)
	}
	return mutation, nil
}

func normalizeSpeculativeMutationPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := normalizeSpeculativeMutationPath(raw)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// normalizeSpeculativeMutationPath returns an absolute, cleaned path so that
// relative and absolute spellings of the same file collapse to the same
// conflict / rollback key. Falls back to the cleaned relative form if Abs
// fails (e.g., cwd unreadable).
func normalizeSpeculativeMutationPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	if path == "." {
		return ""
	}
	return path
}

func captureSpeculativeFileSnapshot(path string) (speculativeFileSnapshot, error) {
	snap := speculativeFileSnapshot{Path: path}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return snap, fmt.Errorf("capture speculative pre-state for %s: %w", path, err)
	}
	if info.IsDir() {
		return snap, fmt.Errorf("capture speculative pre-state for %s: path is a directory", path)
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return snap, fmt.Errorf("capture speculative pre-state for %s: unsupported file type", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, fmt.Errorf("capture speculative pre-state for %s: %w", path, err)
	}
	snap.Existed = true
	snap.Data = data
	snap.Hash = hashBytesHex(data)
	return snap, nil
}

func (m *speculativeFileMutation) hooks() *speculativeToolHooks {
	if m == nil {
		return nil
	}
	return &speculativeToolHooks{
		commit: func() { m.Commit() },
		rollback: func() error {
			return m.Rollback()
		},
		captureAfter: func() { m.CaptureAfter() },
		stale:        m.stale,
		paths:        append([]string(nil), m.paths...),
	}
}

func (m *speculativeFileMutation) CaptureAfter() {
	if m == nil {
		return
	}
	for i := range m.files {
		path := m.files[i].Path
		m.files[i].PostHash = computeFileHash(path)
		m.files[i].PostExisted = m.files[i].PostHash != ""
	}
}

func (m *speculativeFileMutation) Commit() {
	if m == nil || m.commit {
		return
	}
	m.commit = true
	for _, snap := range m.files {
		if m.track == nil || !snap.Existed {
			continue
		}
		m.track.ReleaseWrite(snap.Path, m.agentID, snap.PostHash)
	}
}

func (m *speculativeFileMutation) Rollback() error {
	if m == nil || m.commit || m.rolledBack {
		return nil
	}
	m.rolledBack = true
	var errs []error
	for i := len(m.files) - 1; i >= 0; i-- {
		snap := m.files[i]
		if err := restoreSpeculativeFileSnapshot(snap); err != nil {
			errs = append(errs, err)
		}
	}
	for _, snap := range m.files {
		if m.track == nil || !snap.Existed {
			continue
		}
		m.track.AbortWrite(snap.Path, m.agentID)
	}
	if len(errs) > 0 {
		return fmt.Errorf("rollback speculative file mutation: %w", errors.Join(errs...))
	}
	return nil
}

func restoreSpeculativeFileSnapshot(snap speculativeFileSnapshot) error {
	path := strings.TrimSpace(snap.Path)
	if path == "" {
		return nil
	}
	if snap.PostExisted {
		if current := computeFileHash(path); current != "" && current != snap.PostHash {
			return fmt.Errorf("%s changed after speculative execution; refusing to overwrite external changes", path)
		}
	} else {
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			return fmt.Errorf("%s was recreated after speculative execution; refusing to remove external changes", path)
		case !os.IsNotExist(err):
			return fmt.Errorf("stat speculative-mutated path %s: %w", path, err)
		}
	}
	if snap.Existed {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("restore parent directory for %s: %w", path, err)
		}
		if err := os.WriteFile(path, snap.Data, 0o644); err != nil {
			return fmt.Errorf("restore %s: %w", path, err)
		}
		return nil
	}
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat speculative-created path %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove speculative-created path %s: %w", path, err)
	}
	return nil
}

func hashBytesHex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func rollbackSpeculativeToolHooks(result ToolExecutionResult) {
	if result.speculativeHooks == nil || result.speculativeHooks.rollback == nil {
		return
	}
	if err := result.speculativeHooks.rollback(); err != nil {
		log.Warnf("speculative tool rollback failed error=%v", err)
	}
}

func speculativeWriteToolLSPReviews(registry *tools.Registry, toolName, path string) []message.LSPReview {
	if registry == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	tool, ok := registry.Get(toolName)
	if !ok {
		return nil
	}
	switch t := tool.(type) {
	case tools.WriteTool:
		if t.LSP != nil {
			return t.LSP.CurrentReviewSnapshots(path)
		}
	case tools.ApplyPatchTool:
		if t.LSP != nil {
			return t.LSP.CurrentReviewSnapshots(path)
		}
	}
	return nil
}
