package requesttrim

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tooloutput"
	"github.com/keakon/chord/internal/tools"
)

type fileEvidenceValidity string

const (
	fileEvidenceCurrent    fileEvidenceValidity = "current"
	fileEvidenceStale      fileEvidenceValidity = "stale"
	fileEvidenceSuperseded fileEvidenceValidity = "superseded"
)

// fileEvidenceObservation records a file read or mutation and its derived validity in one history.
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
	// PriorContentLost carries ReadValidity.PriorContentLost for a stale read:
	// the observed bytes exist nowhere else in the transcript. The reduction
	// pass reads it back through ValidityByMessage, so it must survive this
	// round-trip.
	PriorContentLost bool
}

// fileEvidenceView owns the observations derived for one request; it is not durable state.
type fileEvidenceView map[string][]fileEvidenceObservation

// EvidenceStats describes the cost and contents of a derived evidence view.
type EvidenceStats struct {
	DurationUS   int64
	Files        int
	Observations int
	Current      int
	Stale        int
	Superseded   int
}

func (s *Scan) fileEvidence() fileEvidenceView {
	if !s.evidenceDone {
		started := time.Now()
		s.evidence = BuildFileEvidence(s.messages, s.CallMeta())
		s.evidenceStats = s.evidence.Stats(time.Since(started))
		s.evidenceDone = true
	}
	return s.evidence
}

// Stats counts the observations and records the caller's measurement duration.
func (v fileEvidenceView) Stats(duration time.Duration) EvidenceStats {
	Stats := EvidenceStats{DurationUS: duration.Microseconds(), Files: len(v)}
	for _, observations := range v {
		Stats.Observations += len(observations)
		for _, observation := range observations {
			switch observation.Validity {
			case fileEvidenceCurrent:
				Stats.Current++
			case fileEvidenceStale:
				Stats.Stale++
			case fileEvidenceSuperseded:
				Stats.Superseded++
			}
		}
	}
	return Stats
}

// ValidityByMessage returns a new map of invalidated or superseded read results.
func (v fileEvidenceView) ValidityByMessage() map[int]ReadValidity {
	var result map[int]ReadValidity
	for _, observations := range v {
		for _, observation := range observations {
			if observation.Operation != "read" || observation.Validity == fileEvidenceCurrent {
				continue
			}
			if result == nil {
				result = make(map[int]ReadValidity)
			}
			result[observation.MessageIndex] = ReadValidity{
				Invalidated:      observation.Validity == fileEvidenceStale,
				Superseded:       observation.Validity == fileEvidenceSuperseded,
				PriorContentLost: observation.PriorContentLost,
			}
		}
	}
	return result
}

// BuildFileEvidence derives file observations from the same tool metadata
// and read-validity analysis used by reduction. It is intentionally disposable:
// the messages and current filesystem remain the authorities.
func BuildFileEvidence(messages []message.Message, meta map[string]ToolCall) fileEvidenceView {
	validity := analyzeReadValidity(messages, meta)
	view := make(fileEvidenceView)
	for index := range messages {
		msg := &messages[index]
		if msg.Role != message.RoleTool || (message.IsToolResultUnsuccessfulStatus(msg.ToolStatus) && !msg.FileState.HasChanges()) {
			continue
		}
		call := meta[msg.ToolCallID]
		operation := EvidenceOperation(call.Name)
		if operation == "" {
			continue
		}
		for path, observation := range evidenceObservations(msg, &call, operation) {
			if observation.Operation == "read" {
				if state, ok := validity[index]; ok {
					if state.Invalidated {
						observation.Validity = fileEvidenceStale
						observation.PriorContentLost = state.PriorContentLost
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

// EvidenceOperation maps a tool name to its file-observation operation.
func EvidenceOperation(name string) string {
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

func evidenceObservations(msg *message.Message, call *ToolCall, operation string) map[string]fileEvidenceObservation {
	observations := make(map[string]fileEvidenceObservation)
	add := func(state message.TrackedFileState, start, end, delta int) {
		path := NormalizePath(state.Path)
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

func evidenceReadRange(msg *message.Message, call *ToolCall) (int, int) {
	if parsed := parseDisplayedReadRange(msg.Content); parsed.OK {
		return parsed.Start, parsed.End
	}
	request := call.ParsedReadRequest()
	start := max(request.Offset, 1)
	limit := request.Limit
	if limit <= 0 {
		limit = tooloutput.MaxOutputLines
	}
	return start, start + limit - 1
}
