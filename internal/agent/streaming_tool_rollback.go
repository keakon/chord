package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type speculativeToolHooks struct {
	commit        func() error
	rollback      func() error
	captureAfter  func()
	fileState     func() *message.ToolFileState
	backupSources func() []fileBackupSource
	stale         bool
	paths         []string
}

type speculativeFileSnapshot struct {
	Path              string
	Existed           bool
	Data              []byte
	Mode              os.FileMode
	SymlinkTarget     string
	Hash              string
	PostExisted       bool
	PostMode          os.FileMode
	PostSymlinkTarget string
	PostHash          string
	lease             *filelock.WriteLease
}

type speculativeFileMutation struct {
	agentID string
	track   *filelock.FileTracker
	files   []speculativeFileSnapshot
	stale   bool
	paths   []string
	// observedOnCommit marks whole-file writes: the committed content is
	// exactly the bytes the model supplied, so committing records it as an
	// observation and a follow-up write/delete needs no redundant re-read.
	observedOnCommit bool
	commit           bool
	rolledBack       bool
}

func prepareSpeculativeToolCall(tc message.ToolCall, registry *tools.Registry, track *filelock.FileTracker, agentID, baseDir string) (*speculativeToolHooks, error) {
	switch strings.TrimSpace(tc.Name) {
	case tools.NameWrite:
		path, ok := singlePathToolPath(tc.Args, baseDir)
		if !ok {
			return nil, nil
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, []string{path}, "write")
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	case tools.NameEdit:
		path := trackedEditPathFromArgs(tc.Args, baseDir)
		if path == "" {
			return nil, nil
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, []string{path}, "")
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	case tools.NameApplyPatch:
		paths, err := applyPatchToolPaths(tc.Args, baseDir)
		if err != nil {
			return nil, err
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, paths, "")
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	case tools.NameDelete:
		paths, err := deleteToolPaths(tc.Args, baseDir)
		if err != nil {
			return nil, err
		}
		mutation, err := newSpeculativeFileMutation(track, agentID, paths, "delete")
		if err != nil {
			return nil, err
		}
		return mutation.hooks(), nil
	case tools.NameTodoWrite:
		if registry == nil {
			return nil, fmt.Errorf("tool registry not available")
		}
		tool, ok := registry.Get(tools.NameTodoWrite)
		if !ok {
			return nil, fmt.Errorf("tool not found: %s", tools.NameTodoWrite)
		}
		todoTool, ok := tool.(*tools.TodoWriteTool)
		if !ok {
			return nil, fmt.Errorf("tool %q is not a TodoWriteTool", tools.NameTodoWrite)
		}
		if _, err := todoTool.ParseTodos(llm.UnwrapToolArgs(tc.Args)); err != nil {
			return nil, err
		}
		commitArgs := append(json.RawMessage(nil), llm.UnwrapToolArgs(tc.Args)...)
		return &speculativeToolHooks{commit: func() error {
			_, err := todoTool.Execute(context.Background(), commitArgs)
			return err
		}}, nil
	default:
		return nil, nil
	}
}

func singlePathToolPath(args json.RawMessage, baseDir string) (string, bool) {
	var parsed struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(llm.UnwrapToolArgs(args), &parsed); err != nil {
		return "", false
	}
	path, err := tools.ResolveToolPathInDir(parsed.Path, baseDir)
	if err != nil {
		return "", false
	}
	path = normalizeSpeculativeMutationPath(path)
	if path == "" {
		return "", false
	}
	return path, true
}

func deleteToolPaths(args json.RawMessage, baseDir string) ([]string, error) {
	req, err := tools.DecodeDeleteRequestInDir(llm.UnwrapToolArgs(args), baseDir)
	if err != nil {
		return nil, err
	}
	return req.Paths, nil
}

func applyPatchToolPaths(args json.RawMessage, baseDir string) ([]string, error) {
	targets, err := tools.ApplyPatchTargets(llm.UnwrapToolArgs(args), baseDir)
	if err != nil {
		return nil, err
	}
	return tools.MutationTargetPaths(targets), nil
}

func newSpeculativeFileMutation(track *filelock.FileTracker, agentID string, paths []string, observationAction string) (*speculativeFileMutation, error) {
	paths = normalizeSpeculativeMutationPaths(paths)
	if len(paths) == 0 {
		return nil, nil
	}
	mutation := &speculativeFileMutation{agentID: agentID, track: track, files: make([]speculativeFileSnapshot, 0, len(paths)), observedOnCommit: observationAction == "write"}
	locked := make([]*filelock.WriteLease, 0, len(paths))
	for _, path := range paths {
		snap, err := captureSpeculativeFileSnapshot(path)
		if err != nil {
			for _, l := range slices.Backward(locked) {
				l.Abort()
			}
			return nil, err
		}
		if track != nil {
			status, lease, err := track.AcquireWriteLease(path, agentID, snap.Hash)
			if err != nil {
				for _, l := range slices.Backward(locked) {
					l.Abort()
				}
				return nil, err
			}
			snap.lease = lease
			if observationAction != "" && snap.Existed {
				if err := requireCurrentFileObservation(track, agentID, path, snap.Hash, observationAction, status.ExternalChanged); err != nil {
					lease.Abort()
					for _, l := range slices.Backward(locked) {
						l.Abort()
					}
					return nil, err
				}
			} else if status.ExternalChanged {
				mutation.stale = true
			}
			if lease != nil {
				locked = append(locked, lease)
			}
		}
		mutation.paths = append(mutation.paths, path)
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
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return snap, fmt.Errorf("capture speculative symlink target for %s: %w", path, err)
		}
		snap.SymlinkTarget = target
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, fmt.Errorf("capture speculative pre-state for %s: %w", path, err)
	}
	snap.Existed = true
	snap.Data = data
	snap.Mode = info.Mode()
	snap.Hash = hashBytesHex(data)
	return snap, nil
}

func (m *speculativeFileMutation) hooks() *speculativeToolHooks {
	if m == nil {
		return nil
	}
	return &speculativeToolHooks{
		commit: func() error {
			m.Commit()
			return nil
		},
		rollback: func() error {
			return m.Rollback()
		},
		captureAfter:  func() { m.CaptureAfter() },
		fileState:     func() *message.ToolFileState { return m.postState() },
		backupSources: func() []fileBackupSource { return m.backupSources() },
		stale:         m.stale,
		paths:         append([]string(nil), m.paths...),
	}
}

// backupSources returns the pre-execution content of every touched file that
// existed and was non-empty, so a stale multi-file mutation can be backed up
// in full rather than only through its first tracked path.
func (m *speculativeFileMutation) backupSources() []fileBackupSource {
	if m == nil {
		return nil
	}
	sources := make([]fileBackupSource, 0, len(m.files))
	for _, snap := range m.files {
		if !snap.Existed || len(snap.Data) == 0 {
			continue
		}
		sources = append(sources, fileBackupSource{Path: snap.Path, Data: snap.Data})
	}
	return sources
}

func (m *speculativeFileMutation) CaptureAfter() {
	if m == nil {
		return
	}
	for i := range m.files {
		captureSpeculativePostState(&m.files[i])
	}
}

func captureSpeculativePostState(snap *speculativeFileSnapshot) {
	if snap == nil {
		return
	}
	info, err := os.Lstat(snap.Path)
	if err != nil {
		return
	}
	snap.PostExisted = true
	snap.PostMode = info.Mode()
	if info.Mode()&os.ModeSymlink != 0 {
		snap.PostSymlinkTarget, _ = os.Readlink(snap.Path)
	}
	snap.PostHash = computeFileHash(snap.Path)
}

func (m *speculativeFileMutation) Commit() {
	if m == nil || m.commit {
		return
	}
	m.commit = true
	for _, snap := range m.files {
		if m.track != nil && snap.lease != nil {
			if !snap.PostExisted {
				snap.lease.CommitDelete()
			} else {
				m.track.ReleaseWrite(snap.Path, m.agentID, snap.PostHash)
			}
		}
		if m.track != nil && snap.PostExisted {
			m.track.TrackCommittedSnapshot(snap.Path, m.agentID, snap.PostHash)
		}
		if m.track != nil && m.observedOnCommit && snap.PostHash != "" {
			m.track.TrackObservedSnapshot(snap.Path, m.agentID, snap.PostHash)
		}
	}
}

func (m *speculativeFileMutation) Abort() {
	if m == nil || m.commit || m.rolledBack {
		return
	}
	m.rolledBack = true
	for _, snap := range slices.Backward(m.files) {
		if snap.lease != nil {
			snap.lease.Abort()
		}
	}
}

func (m *speculativeFileMutation) postState() *message.ToolFileState {
	if m == nil {
		return nil
	}
	state := &message.ToolFileState{}
	for _, snap := range m.files {
		if snap.PostExisted && snap.PostHash != "" {
			state.Writes = append(state.Writes, message.TrackedFileState{Path: snap.Path, SHA256: snap.PostHash, Exists: true})
		} else if snap.Existed {
			state.Deletes = append(state.Deletes, message.TrackedFileState{Path: snap.Path, Exists: false})
		}
	}
	if len(state.Writes) == 0 && len(state.Deletes) == 0 {
		return nil
	}
	return state
}

func (m *speculativeFileMutation) Rollback() error {
	if m == nil || m.commit || m.rolledBack {
		return nil
	}
	m.rolledBack = true
	var errs []error
	for _, snap := range slices.Backward(m.files) {

		if err := restoreSpeculativeFileSnapshot(snap); err != nil {
			errs = append(errs, err)
		}
	}
	for _, snap := range m.files {
		if snap.lease != nil {
			snap.lease.Abort()
		}
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
		if !matchesSpeculativePostState(snap) {
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
		if snap.Mode&os.ModeSymlink != 0 {
			if snap.PostExisted {
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("remove speculative-mutated path %s: %w", path, err)
				}
			}
			if err := os.Symlink(snap.SymlinkTarget, path); err != nil {
				return fmt.Errorf("restore symlink %s: %w", path, err)
			}
			return nil
		}
		if err := writeSpeculativeSnapshot(path, snap.Data, snap.Mode, !snap.PostExisted); err != nil {
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

func matchesSpeculativePostState(snap speculativeFileSnapshot) bool {
	info, err := os.Lstat(snap.Path)
	if err != nil || info.Mode() != snap.PostMode {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(snap.Path)
		if err != nil || target != snap.PostSymlinkTarget {
			return false
		}
	}
	return computeFileHash(snap.Path) == snap.PostHash
}

func writeSpeculativeSnapshot(path string, data []byte, mode os.FileMode, requireNew bool) error {
	flags := os.O_WRONLY
	if requireNew {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, mode.Perm())
	if err != nil {
		return err
	}
	if !requireNew {
		openedInfo, statErr := f.Stat()
		pathInfo, lstatErr := os.Lstat(path)
		if statErr != nil || lstatErr != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
			_ = f.Close()
			return fmt.Errorf("path changed before restore")
		}
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			return err
		}
	}
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
	case tools.EditTool:
		if t.LSP != nil {
			return t.LSP.CurrentReviewSnapshots(path)
		}
	}
	return nil
}
