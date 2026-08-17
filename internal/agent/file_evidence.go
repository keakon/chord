package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

type fileEvidenceValidity string

const (
	fileEvidenceCurrent    fileEvidenceValidity = "current"
	fileEvidenceStale      fileEvidenceValidity = "stale"
	fileEvidenceSuperseded fileEvidenceValidity = "superseded"
)

type fileEvidenceObservation struct {
	// MessageIndex is the transcript index the observation came from; the
	// view is in-memory only, so no richer source reference is needed.
	MessageIndex     int
	Operation        string
	ObservedRevision string
	ReadStart        int
	ReadEnd          int
	ChangedStart     int
	ChangedEnd       int
	LineDelta        int
	Validity         fileEvidenceValidity
}

type fileEvidenceView map[string][]fileEvidenceObservation

type fileEvidenceStats struct {
	DurationUS   int64
	Files        int
	Observations int
	Current      int
	Stale        int
	Superseded   int
}

func (s *reductionHistoryScan) fileEvidence() fileEvidenceView {
	if !s.evidenceDone {
		started := time.Now()
		s.evidence = buildFileEvidenceViewWithMeta(s.messages, s.callMeta())
		s.evidenceStats = s.evidence.stats(time.Since(started))
		s.evidenceDone = true
	}
	return s.evidence
}

func (v fileEvidenceView) stats(duration time.Duration) fileEvidenceStats {
	stats := fileEvidenceStats{DurationUS: duration.Microseconds(), Files: len(v)}
	for _, observations := range v {
		stats.Observations += len(observations)
		for _, observation := range observations {
			switch observation.Validity {
			case fileEvidenceCurrent:
				stats.Current++
			case fileEvidenceStale:
				stats.Stale++
			case fileEvidenceSuperseded:
				stats.Superseded++
			}
		}
	}
	return stats
}

func (v fileEvidenceView) validityByMessage() map[int]readValidity {
	var result map[int]readValidity
	for _, observations := range v {
		for _, observation := range observations {
			if observation.Operation != "read" || observation.Validity == fileEvidenceCurrent {
				continue
			}
			if result == nil {
				result = make(map[int]readValidity)
			}
			result[observation.MessageIndex] = readValidity{
				Invalidated: observation.Validity == fileEvidenceStale,
				Superseded:  observation.Validity == fileEvidenceSuperseded,
			}
		}
	}
	return result
}

// buildFileEvidenceView derives file observations from the same tool metadata
// and read-validity analysis used by reduction. It is intentionally disposable:
// the messages and current filesystem remain the authorities.
func buildFileEvidenceView(messages []message.Message) fileEvidenceView {
	return buildFileEvidenceViewWithMeta(messages, buildToolCallMeta(messages))
}

func buildFileEvidenceViewWithMeta(messages []message.Message, meta map[string]toolCallMeta) fileEvidenceView {
	validity := analyzeReadValidity(messages, meta)
	view := make(fileEvidenceView)
	for index := range messages {
		msg := &messages[index]
		if msg.Role != message.RoleTool || (isToolResultUnsuccessfulStatus(msg.ToolStatus) && !msg.FileState.HasChanges()) {
			continue
		}
		call := meta[msg.ToolCallID]
		operation := evidenceOperation(call.Name)
		if operation == "" {
			continue
		}
		for path, observation := range evidenceObservations(msg, &call, operation) {
			if observation.Operation == "read" {
				if state, ok := validity[index]; ok {
					if state.Invalidated {
						observation.Validity = fileEvidenceStale
					} else if state.Superseded {
						observation.Validity = fileEvidenceSuperseded
					}
				}
			}
			observation.MessageIndex = index
			view[path] = append(view[path], observation)
		}
	}
	return view
}

func evidenceOperation(name string) string {
	switch strings.TrimSpace(name) {
	case tools.NameRead:
		return "read"
	case tools.NameEdit, tools.NameApplyPatch, tools.NameWrite:
		return "write"
	case tools.NameDelete:
		return "delete"
	default:
		return ""
	}
}

func evidenceObservations(msg *message.Message, call *toolCallMeta, operation string) map[string]fileEvidenceObservation {
	observations := make(map[string]fileEvidenceObservation)
	add := func(state message.TrackedFileState, start, end, delta int) {
		path := reductionNormalizePath(state.Path)
		if path == "" {
			return
		}
		observations[path] = fileEvidenceObservation{Operation: operation, ObservedRevision: state.SHA256, ReadStart: start, ReadEnd: end, ChangedStart: state.ChangedStart, ChangedEnd: state.ChangedEnd, LineDelta: delta, Validity: fileEvidenceCurrent}
	}
	if msg.FileState != nil {
		for _, state := range msg.FileState.Reads {
			start, end := evidenceReadRange(msg, call)
			add(state, start, end, 0)
		}
		for _, state := range msg.FileState.Writes {
			add(state, 0, 0, state.LineDelta)
		}
		for _, state := range msg.FileState.Deletes {
			add(state, 0, 0, 0)
		}
	}
	if len(observations) > 0 {
		return observations
	}
	var args struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(call.Args), &args); err != nil {
		return observations
	}
	paths := append([]string{args.Path}, args.Paths...)
	for _, path := range paths {
		state := message.TrackedFileState{Path: path, Exists: operation != "delete"}
		add(state, 0, 0, 0)
	}
	return observations
}

func evidenceReadRange(msg *message.Message, call *toolCallMeta) (int, int) {
	if parsed := parseDisplayedReadRange(msg.Content); parsed.OK {
		return parsed.Start, parsed.End
	}
	request := call.parsedReadRequest()
	start := request.Offset + 1
	limit := request.Limit
	if limit <= 0 {
		limit = tools.MaxOutputLines
	}
	return start, start + limit - 1
}
