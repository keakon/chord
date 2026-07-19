package agent

import (
	"encoding/json"
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

type deleteLockSet struct {
	paths  []string
	agent  string
	track  *filelock.FileTracker
	mode   deleteLockReleaseMode
	stale  bool
	result tools.DeleteResultGroups
}

func acquireDeleteLocks(tracker *filelock.FileTracker, agentID string, args json.RawMessage) (*deleteLockSet, error) {
	if tracker == nil {
		return nil, nil
	}
	req, err := tools.DecodeDeleteRequest(llm.UnwrapToolArgs(args))
	if err != nil {
		return nil, nil
	}
	if len(req.Paths) == 0 {
		return nil, nil
	}

	locked := make([]string, 0, len(req.Paths))
	stale := false
	for _, path := range req.Paths {
		currentHash := computeFileHash(path)
		if currentHash == "" {
			continue // already absent; DeleteTool treats this as warning, not blocker
		}
		status, err := tracker.AcquireWriteStatus(path, agentID, currentHash)
		if err != nil {
			for _, l := range slices.Backward(locked) {
				tracker.AbortWrite(l, agentID)
			}
			return nil, err
		}
		if status.ExternalChanged {
			stale = true
		}
		locked = append(locked, path)
	}
	if len(locked) == 0 {
		return nil, nil
	}
	return &deleteLockSet{paths: locked, agent: agentID, track: tracker, stale: stale}, nil
}

func (s *deleteLockSet) Release() {
	if s == nil || s.track == nil {
		return
	}
	for _, path := range slices.Backward(s.paths) {

		if s.mode == deleteLockReleaseCommitted {
			if containsDeleteResultPath(s.result.Deleted, path) {
				s.track.ReleaseWrite(path, s.agent, "")
				continue
			}
		}
		s.track.AbortWrite(path, s.agent)
	}
}

func (s *deleteLockSet) Commit(rawResult string) {
	if s == nil {
		return
	}
	s.result = tools.ParseDeleteResult(rawResult)
	s.mode = deleteLockReleaseCommitted
}

func containsDeleteResultPath(paths []string, target string) bool {
	return slices.Contains(paths, target)
}
