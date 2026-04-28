package agent

import (
	"encoding/json"

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
	for _, path := range req.Paths {
		currentHash := computeFileHash(path)
		if currentHash == "" {
			continue // already absent; DeleteTool treats this as warning, not blocker
		}
		if err := tracker.AcquireWrite(path, agentID, currentHash); err != nil {
			for i := len(locked) - 1; i >= 0; i-- {
				tracker.AbortWrite(locked[i], agentID)
			}
			return nil, err
		}
		locked = append(locked, path)
	}
	if len(locked) == 0 {
		return nil, nil
	}
	return &deleteLockSet{paths: locked, agent: agentID, track: tracker}, nil
}

func (s *deleteLockSet) Release() {
	if s == nil || s.track == nil {
		return
	}
	for i := len(s.paths) - 1; i >= 0; i-- {
		path := s.paths[i]
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
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}
