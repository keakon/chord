package recovery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// CompactionHistoryStatusApplied is the status value the agent writes to
// history-N.status.json only after the pre-compress rename and the
// main.jsonl rewrite both completed.
const CompactionHistoryStatusApplied = "applied"

// CompactionHistorySnapshot identifies one applied pre-compaction snapshot of
// a session. The index is the file number assigned to the compaction attempt;
// indexes may have gaps and are unrelated to the successful-compaction count.
type CompactionHistorySnapshot struct {
	Index      int
	JSONLName  string
	StatusName string
}

// CompactionHistoryScanIssue records a directory entry that looks like a
// snapshot but whose status record could not be read or decoded. Discovery
// callers decide whether to surface or merely log it; the snapshot is not
// usable either way.
type CompactionHistoryScanIssue struct {
	Index      int
	StatusPath string
	Err        error
}

func compactionHistoryStatusName(index int) string {
	return fmt.Sprintf("history-%d.status.json", index)
}

func compactionPreCompressName(index int) string {
	return fmt.Sprintf("main.pre-compress-%d.jsonl", index)
}

// ScanCompactionHistory lists the applied pre-compaction snapshots of
// sessionDir, sorted by index. A snapshot counts only when its
// history-N.status.json reports "applied": a missing status means the apply
// never completed, and an unreadable/corrupt status is reported as an issue
// instead of silently reclassifying the directory as having no history.
func ScanCompactionHistory(sessionDir string) ([]CompactionHistorySnapshot, []CompactionHistoryScanIssue, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, nil, err
	}
	var snapshots []CompactionHistorySnapshot
	var issues []CompactionHistoryScanIssue
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "main.pre-compress-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "main.pre-compress-"), ".jsonl"))
		if err != nil || index < 1 {
			continue
		}
		statusName := compactionHistoryStatusName(index)
		applied, err := compactionHistoryAppliedFile(filepath.Join(sessionDir, statusName))
		if err != nil {
			issues = append(issues, CompactionHistoryScanIssue{
				Index:      index,
				StatusPath: filepath.Join(sessionDir, statusName),
				Err:        err,
			})
			continue
		}
		if !applied {
			continue
		}
		snapshots = append(snapshots, CompactionHistorySnapshot{
			Index:      index,
			JSONLName:  name,
			StatusName: statusName,
		})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Index < snapshots[j].Index })
	return snapshots, issues, nil
}

// compactionHistoryAppliedFile mirrors the fork boundary rule: a missing
// status file means the apply never completed, while any other read or decode
// failure is returned to the caller.
func compactionHistoryAppliedFile(statusPath string) (bool, error) {
	data, err := os.ReadFile(statusPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	var meta struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false, fmt.Errorf("parse compaction status: %w", err)
	}
	return meta.Status == CompactionHistoryStatusApplied, nil
}

// Sealed snapshot read failures. Callers use errors.Is to separate a missing
// snapshot from a damaged one and from an I/O or decode failure.
var (
	// ErrCompactionSnapshotUnavailable means the snapshot is not available:
	// no applied status, or the directory/snapshot/status no longer exists.
	ErrCompactionSnapshotUnavailable = errors.New("compaction snapshot unavailable")
	// ErrCompactionSnapshotInvalid means the path violates the sealed snapshot
	// boundary or the snapshot was replaced while being opened.
	ErrCompactionSnapshotInvalid = errors.New("compaction snapshot invalid")
	// ErrCompactionSnapshotEmpty means the snapshot file has no records.
	ErrCompactionSnapshotEmpty = errors.New("compaction snapshot empty")
	// ErrCompactionSnapshotRead means the snapshot could not be read.
	ErrCompactionSnapshotRead = errors.New("compaction snapshot read failed")
	// ErrCompactionSnapshotParse means a record could not be decoded.
	ErrCompactionSnapshotParse = errors.New("compaction snapshot parse failed")
)

// LoadAppliedCompactionSnapshot reads one sealed pre-compaction snapshot
// strictly read-only. It rejects session directories outside sessionsRoot and
// any symlink or non-regular snapshot/status file, decodes every record
// (accepting a valid final record without a trailing newline but rejecting a
// damaged one), and checks ctx between bounded reads. No session lock,
// metadata, attachment load, restore or cleanup is performed.
//
// sessionsRoot may be empty, which trusts sessionDir itself as the boundary;
// the file-level rules below still apply. When it is set, sessionDir must
// resolve inside it, so a caller cannot point the reader at an unrelated
// directory.
func LoadAppliedCompactionSnapshot(ctx context.Context, sessionsRoot, sessionDir string, index int) ([]message.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if index < 1 {
		return nil, fmt.Errorf("%w: invalid compaction index %d", ErrCompactionSnapshotInvalid, index)
	}
	root, err := openCompactionSessionRoot(sessionsRoot, sessionDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := verifyAppliedStatusStrict(ctx, root, compactionHistoryStatusName(index)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := openSealedRegularFile(root, compactionPreCompressName(index), "snapshot")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: stat snapshot %s: %w", ErrCompactionSnapshotRead, f.Name(), err)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("%w: %s is empty", ErrCompactionSnapshotEmpty, info.Name())
	}
	messages, err := decodeSealedSnapshot(ctx, f, info.Size())
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", compactionPreCompressName(index), err)
	}
	if err := verifyAppliedStatusStrict(ctx, root, compactionHistoryStatusName(index)); err != nil {
		return nil, err
	}
	return messages, nil
}

// openCompactionSessionRoot resolves and validates the session directory
// against the sessions root, then opens it as an os.Root so every subsequent
// file open is confined to the same directory even if path components change.
//
// A session directory lives one level below its project sessions directory, so
// the containment check accepts any depth below the root but rejects the root
// itself, an absolute relative path, and any path that escapes upwards.
func openCompactionSessionRoot(sessionsRoot, sessionDir string) (*os.Root, error) {
	if strings.TrimSpace(sessionDir) == "" {
		return nil, fmt.Errorf("%w: missing session dir", ErrCompactionSnapshotInvalid)
	}
	dirResolved, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: session dir no longer exists: %w", ErrCompactionSnapshotUnavailable, err)
		}
		return nil, fmt.Errorf("%w: resolve session dir: %w", ErrCompactionSnapshotInvalid, err)
	}
	if strings.TrimSpace(sessionsRoot) != "" {
		rootResolved, err := filepath.EvalSymlinks(sessionsRoot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: sessions root no longer exists: %w", ErrCompactionSnapshotUnavailable, err)
			}
			return nil, fmt.Errorf("%w: resolve sessions root: %w", ErrCompactionSnapshotInvalid, err)
		}
		rel, err := filepath.Rel(rootResolved, dirResolved)
		if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%w: session dir %s is outside the sessions root", ErrCompactionSnapshotInvalid, sessionDir)
		}
		parent, err := os.OpenRoot(rootResolved)
		if err != nil {
			return nil, fmt.Errorf("%w: open sessions root: %w", ErrCompactionSnapshotRead, err)
		}
		defer parent.Close()
		root, err := parent.OpenRoot(rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: session dir no longer exists: %w", ErrCompactionSnapshotUnavailable, err)
			}
			return nil, fmt.Errorf("%w: open session dir: %w", ErrCompactionSnapshotInvalid, err)
		}
		return root, nil
	}
	root, err := os.OpenRoot(dirResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: session dir no longer exists: %w", ErrCompactionSnapshotUnavailable, err)
		}
		return nil, fmt.Errorf("%w: open session dir: %w", ErrCompactionSnapshotRead, err)
	}
	return root, nil
}

// verifyAppliedStatusStrict opens and decodes the status file inside the
// session root. Missing status or a non-applied status is unavailable; a
// symlink, non-regular file, or damaged status is invalid.
func verifyAppliedStatusStrict(ctx context.Context, root *os.Root, name string) error {
	f, err := openSealedRegularFile(root, name, "status")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	const maxStatusBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, r: f}, maxStatusBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read status %s: %w", ErrCompactionSnapshotRead, name, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > maxStatusBytes {
		return fmt.Errorf("%w: status %s exceeds %d bytes", ErrCompactionSnapshotInvalid, name, maxStatusBytes)
	}
	var meta struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("%w: parse status %s: %w", ErrCompactionSnapshotInvalid, name, err)
	}
	if meta.Status != CompactionHistoryStatusApplied {
		return fmt.Errorf("%w: status %s is %q, not %q", ErrCompactionSnapshotUnavailable, name, meta.Status, CompactionHistoryStatusApplied)
	}
	return nil
}

// openSealedRegularFile rejects symlinks and non-regular files before opening,
// then confirms the opened object is the same regular file that was checked.
func openSealedRegularFile(root *os.Root, name, kind string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s %s no longer exists: %w", ErrCompactionSnapshotUnavailable, kind, name, err)
		}
		return nil, fmt.Errorf("%w: stat %s %s: %w", ErrCompactionSnapshotInvalid, kind, name, err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s %s is a symbolic link", ErrCompactionSnapshotInvalid, kind, name)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s %s is not a regular file", ErrCompactionSnapshotInvalid, kind, name)
	}
	f, err := root.Open(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s %s no longer exists: %w", ErrCompactionSnapshotUnavailable, kind, name, err)
		}
		return nil, fmt.Errorf("%w: open %s %s: %w", ErrCompactionSnapshotRead, kind, name, err)
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: stat opened %s %s: %w", ErrCompactionSnapshotRead, kind, name, err)
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s %s changed while opening", ErrCompactionSnapshotInvalid, kind, name)
	}
	return f, nil
}

// decodeSealedSnapshot decodes every record of a sealed snapshot. A final
// record without a newline is accepted when it decodes; a truncated or
// malformed record fails the whole snapshot instead of returning a prefix.
func decodeSealedSnapshot(ctx context.Context, r io.Reader, size int64) ([]message.Message, error) {
	reader := bufio.NewReaderSize(&contextReader{ctx: ctx, r: r}, 64<<10)
	messages := make([]message.Message, 0, min(int(size/512)+1, 4096))
	record := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: read record %d: %w", ErrCompactionSnapshotRead, record+1, readErr)
		}
		terminated := readErr == nil
		if len(line) > 0 {
			if n := len(line); n > 0 && line[n-1] == '\n' {
				line = line[:n-1]
			}
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
		}
		if len(line) == 0 {
			if !terminated && readErr == io.EOF {
				if record == 0 {
					return nil, fmt.Errorf("%w: no records", ErrCompactionSnapshotEmpty)
				}
				return messages, nil
			}
			return nil, fmt.Errorf("%w: record %d is empty", ErrCompactionSnapshotParse, record+1)
		}
		record++
		var msg message.Message
		if err := recoverySessionJSON.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("%w: record %d: %w", ErrCompactionSnapshotParse, record, err)
		}
		messages = append(messages, msg)
		if readErr == io.EOF {
			return messages, nil
		}
	}
}

// contextReader checks cancellation on every underlying read so a huge line
// cannot postpone a cancelled load until it finishes.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
