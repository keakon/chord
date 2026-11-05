package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ScanAppliedCompactionHistory discovers sealed snapshots within the same
// boundary enforced by LoadAppliedCompactionSnapshot. It checks cancellation
// between directory batches and files, and reports invalid entries separately
// from missing or unapplied snapshots.
func ScanAppliedCompactionHistory(ctx context.Context, sessionsRoot, sessionDir string) ([]CompactionHistorySnapshot, []CompactionHistoryScanIssue, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	root, err := openCompactionSessionRoot(sessionsRoot, sessionDir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open snapshot directory: %w", ErrCompactionSnapshotRead, err)
	}
	defer dir.Close()
	var snapshots []CompactionHistorySnapshot
	var issues []CompactionHistoryScanIssue
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, fmt.Errorf("%w: enumerate snapshots: %w", ErrCompactionSnapshotRead, err)
		}
		if len(entries) == 0 {
			break
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "main.pre-compress-") || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "main.pre-compress-"), ".jsonl"))
			if err != nil || index < 1 || name != compactionPreCompressName(index) {
				continue
			}
			statusName := compactionHistoryStatusName(index)
			err = verifyAppliedStatusStrict(ctx, root, statusName)
			if err == nil {
				f, openErr := openSealedRegularFile(root, name, "snapshot")
				err = openErr
				if f != nil {
					_ = f.Close()
				}
			}
			if cancelErr := ctx.Err(); cancelErr != nil {
				return nil, nil, cancelErr
			}
			if errors.Is(err, ErrCompactionSnapshotUnavailable) {
				continue
			}
			if err != nil {
				issues = append(issues, CompactionHistoryScanIssue{
					Index: index, StatusPath: filepath.Join(sessionDir, statusName), Err: err,
				})
				continue
			}
			snapshots = append(snapshots, CompactionHistorySnapshot{
				Index: index, JSONLName: name, StatusName: statusName,
			})
		}
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Index < snapshots[j].Index })
	sort.Slice(issues, func(i, j int) bool { return issues[i].Index < issues[j].Index })
	return snapshots, issues, nil
}
