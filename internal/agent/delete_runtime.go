package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

type deleteLockReleaseMode int

const (
	deleteLockReleaseAbort deleteLockReleaseMode = iota
	deleteLockReleaseCommitted
)

type deleteLockedPath struct {
	path    string
	lease   *filelock.WriteLease
	symlink bool
	stale   bool
}

type deleteLockSet struct {
	paths  []string
	locked []deleteLockedPath
	mode   deleteLockReleaseMode
	audit  tools.DeleteAudit
}

func (s *deleteLockSet) hasStalePath() bool {
	if s == nil {
		return false
	}
	for _, path := range s.locked {
		if path.stale {
			return true
		}
	}
	return false
}

func acquireDeleteLocks(tracker *filelock.FileTracker, agentID string, args json.RawMessage, baseDir string) (*deleteLockSet, error) {
	if tracker == nil {
		return nil, nil
	}
	req, err := tools.DecodeDeleteRequestInDir(llm.UnwrapToolArgs(args), baseDir)
	if err != nil {
		return nil, nil
	}
	if len(req.Paths) == 0 {
		return nil, nil
	}

	locked := make([]deleteLockedPath, 0, len(req.Paths))
	for _, path := range req.Paths {
		info, statErr := os.Lstat(path)
		isSymlink := statErr == nil && info.Mode()&os.ModeSymlink != 0

		// Symlinks have no content of their own — deleting one removes the
		// directory entry, not the target. Use an empty hash to skip content
		// verification and the target-readability check.
		var currentHash string
		var exists bool
		var verifyErr error
		if isSymlink {
			currentHash, exists, verifyErr = "", true, nil
		} else {
			currentHash, exists, verifyErr = verifiedCurrentFileHash(path)
		}
		if verifyErr != nil {
			for _, l := range slices.Backward(locked) {
				l.lease.Abort()
			}
			return nil, fmt.Errorf("refusing to delete file %s because its current state cannot be verified: %w", path, verifyErr)
		}
		if !exists {
			continue // already absent; DeleteTool treats this as warning, not blocker
		}
		status, lease, err := tracker.AcquireWriteLease(path, agentID, currentHash)
		if err != nil {
			for _, l := range slices.Backward(locked) {
				l.lease.Abort()
			}
			return nil, err
		}
		// Deletion is path-authorized, not content-authorized. The hash above
		// verifies the current target for locking and backup; a missing or stale
		// model observation must not turn an explicit delete into a refusal.
		locked = append(locked, deleteLockedPath{path: path, lease: lease, symlink: isSymlink, stale: status.ExternalChanged})
	}
	if len(locked) == 0 {
		return nil, nil
	}
	paths := make([]string, len(locked))
	for i, lockedPath := range locked {
		paths[i] = lockedPath.path
	}
	return &deleteLockSet{paths: paths, locked: locked}, nil
}

func (s *deleteLockSet) Release() {
	if s == nil {
		return
	}
	for _, locked := range slices.Backward(s.locked) {
		if s.mode == deleteLockReleaseCommitted && containsDeleteResultPath(s.audit.Deleted, locked.path) && !locked.symlink {
			locked.lease.CommitDelete()
			continue
		}
		locked.lease.Abort()
	}
}

func (s *deleteLockSet) Commit(rawResult string) {
	if s == nil {
		return
	}
	legacy := tools.ParseDeleteResult(rawResult)
	s.CommitAudit(tools.DeleteAudit(legacy))
}

func (s *deleteLockSet) CommitAudit(audit tools.DeleteAudit) {
	if s == nil {
		return
	}
	s.audit = audit
	s.mode = deleteLockReleaseCommitted
}

func containsDeleteResultPath(paths []string, target string) bool {
	return slices.Contains(paths, target)
}
