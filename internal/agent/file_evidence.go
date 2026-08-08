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
	fileEvidenceUnknown    fileEvidenceValidity = "unknown"
)

type fileEvidenceObservation struct {
	SourceRef        checkpointSourceRef
	Operation        string
	ObservedRevision string
	ReadStart        int
	ReadEnd          int
	ChangedStart     int
	ChangedEnd       int
	LineDelta        int
	Validity         fileEvidenceValidity
	InvalidatedBy    int
}

type fileEvidenceView map[string][]fileEvidenceObservation

type fileEvidenceStats struct {
	DurationUS   int64
	Files        int
	Observations int
	Current      int
	Stale        int
	Superseded   int
	Unknown      int
}

func (s *reductionHistoryScan) fileEvidence() fileEvidenceView {
	if !s.evidenceDone {
		started := time.Now()
		s.evidence = buildFileEvidenceViewWithMeta(s.messages, "", "current", s.callMeta())
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
			case fileEvidenceUnknown:
				stats.Unknown++
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
			result[observation.SourceRef.LegacyOrdinal] = readValidity{
				Invalidated: observation.Validity == fileEvidenceStale || observation.Validity == fileEvidenceUnknown,
				Superseded:  observation.Validity == fileEvidenceSuperseded,
			}
		}
	}
	return result
}

// buildFileEvidenceView derives file observations from the same tool metadata
// and read-validity analysis used by reduction. It is intentionally disposable:
// the messages and current filesystem remain the authorities.
func buildFileEvidenceView(messages []message.Message, sessionID, generation string) fileEvidenceView {
	return buildFileEvidenceViewWithMeta(messages, sessionID, generation, buildToolCallMeta(messages))
}

func buildFileEvidenceViewWithMeta(messages []message.Message, sessionID, generation string, meta map[string]toolCallMeta) fileEvidenceView {
	validity := analyzeReadValidity(messages, meta)
	view := make(fileEvidenceView)
	for index := range messages {
		msg := &messages[index]
		if msg.Role != message.RoleTool || isToolResultUnsuccessfulStatus(msg.ToolStatus) {
			continue
		}
		call := meta[msg.ToolCallID]
		operation := evidenceOperation(call.Name)
		if operation == "" {
			continue
		}
		ref := checkpointSourceRef{SessionID: sessionID, TranscriptGeneration: generation, SegmentKind: "transcript", LegacyOrdinal: index, Role: string(msg.Role), ToolCallID: msg.ToolCallID}
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
			observation.SourceRef = ref
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
