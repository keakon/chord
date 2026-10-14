// Package toolefficiency tracks bounded, one-shot advice for inefficient tool usage.
package toolefficiency

import (
	"fmt"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tooloutput"
)

// Efficiency notes are one-shot runtime hints appended to a tool result's
// model-facing content when the turn's tool usage falls into a known slow
// pattern: long streaks of responses that each issue exactly one read-only
// lookup, or repeated small-window reads of a file that fits in a single read.
// Static prompt guidance covers both, but long turns drift away from it; the
// note re-surfaces the guidance at the moment the pattern is happening.
//
// Notes ride inside the tool result rather than a turn
// overlay: appended tail content keeps provider prefix caches and Responses
// incremental transport intact, while an overlay change would invalidate them.

const (
	// batchingNoteStreakThreshold is how many consecutive single-lookup
	// responses arm a batching note. Below it, short serial chains are usually
	// legitimate dependent lookups.
	batchingNoteStreakThreshold = 4
	maxBatchingNotesPerTurn     = 2
	// partialReadNoteThreshold is the partial-read count per path that triggers
	// a full-read note. The recommended grep→read-narrow-range flow legitimately
	// touches the same file twice; a third window signals paging.
	partialReadNoteThreshold = 3
	// partialReadMinRemainderLines keeps reads that stopped just short of the
	// end (for example offset=2 skipping the package line; offsets are 1-based)
	// from counting as partial: below this remainder another targeted read is
	// not worth advising.
	partialReadMinRemainderLines = 40
	maxEfficiencyNotesPerTurn    = 4
)

// ReadObservation is parsed read evidence supplied by the execution adapter.
// Missing or invalid headers and non-read tools use the zero value. The path
// retains the identity reported by the tool arguments, without normalization.
type ReadObservation struct {
	Path              string
	Start, End, Total int
	Truncated         bool
}

// ShouldTrack reports whether this read window can contribute to a partial-read
// pattern. It does not inspect Path, so execution adapters can reject irrelevant
// windows before decoding the tool arguments needed to identify the file.
func (r ReadObservation) ShouldTrack() bool {
	return r.Total > 0 && r.Total <= tooloutput.MaxOutputLines && !r.Truncated &&
		r.Total-(r.End-r.Start+1) >= partialReadMinRemainderLines
}

// State tracks per-turn tool usage patterns for efficiency
// notes. The zero value is ready for one turn. It must not be copied after use.
// Note consumption runs on the tool-execution goroutine that finalizes
// the result (it must precede the sync append hook), dispatch classification
// runs on the event loop, so access is mutex-guarded.
type State struct {
	mu                            sync.Mutex
	consecutiveSingleLookupRounds int
	armedBatchingCallID           string
	armedStreakLen                int
	batchingNotesFired            int
	partialReadsByPath            map[string]int
	notedReadPaths                map[string]struct{}
	notesFired                    int
}

// isBatchableLookupTool reports whether independent calls of this tool can run
// in parallel within one response, making one-per-response streaks wasteful.
func isBatchableLookupTool(name string) bool {
	switch toolname.Normalize(name) {
	case toolname.Read, toolname.Grep, toolname.Glob:
		return true
	default:
		return false
	}
}

// Dispatch classifies one dispatched LLM round. forcedShape
// marks rounds whose call count was imposed by the runtime (length recovery),
// which say nothing about the model's own batching behavior and leave the
// streak untouched.
func (s *State) Dispatch(calls []message.ToolCall, forcedShape bool) {
	if s == nil || forcedShape {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A new dispatch invalidates any arm left unconsumed by a cancelled round.
	s.armedBatchingCallID = ""
	if len(calls) != 1 || !isBatchableLookupTool(calls[0].Name) {
		s.consecutiveSingleLookupRounds = 0
		return
	}
	s.consecutiveSingleLookupRounds++
	if s.consecutiveSingleLookupRounds < batchingNoteStreakThreshold ||
		s.batchingNotesFired >= maxBatchingNotesPerTurn ||
		s.notesFired >= maxEfficiencyNotesPerTurn {
		return
	}
	s.armedBatchingCallID = calls[0].ID
	s.armedStreakLen = s.consecutiveSingleLookupRounds
	s.consecutiveSingleLookupRounds = 0
}

// NoteResult returns a one-shot efficiency note to append to
// this tool result's model-facing content, or "" when no advisory applies.
// Read observations describe successfully parsed read results. A zero observation
// carries no read evidence; failed results still consume an armed batching call.
func (s *State) NoteResult(callID string, failed bool, read ReadObservation) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	armed := callID != "" && callID == s.armedBatchingCallID
	if armed {
		s.armedBatchingCallID = ""
	}
	path, count, total, readNoteEligible := s.recordPartialRead(read, failed)
	if armed && !failed {
		s.batchingNotesFired++
		s.notesFired++
		return fmt.Sprintf(
			"Efficiency note: each of the last %d responses issued exactly one read-only lookup (%s). When your next lookups are independent of each other (different files, symbols, or ranges), issue them together in one response — they run in parallel and each avoided response saves a full model round trip. Keep one lookup per response only when it depends on the previous result.",
			s.armedStreakLen,
			strings.Join([]string{toolname.Read, toolname.Grep, toolname.Glob}, ", "),
		)
	}
	if readNoteEligible {
		if s.notedReadPaths == nil {
			s.notedReadPaths = make(map[string]struct{})
		}
		s.notedReadPaths[path] = struct{}{}
		s.notesFired++
		return fmt.Sprintf(
			"Efficiency note: this is partial read #%d of %s in this turn and the whole file is %d lines, within a single read's default %d-line range; if you expect to consult more of it, read the rest (or the whole file) in one call instead of more small windows.",
			count, path, total, tooloutput.MaxOutputLines,
		)
	}
	return ""
}

// recordPartialRead counts a successful partial-window read of a fully
// readable file and reports whether a full-read note may be emitted for it.
// Marking the path as noted is left to the caller so a colliding batching
// note (only one note per result) keeps the path eligible for the next read.
func (s *State) recordPartialRead(read ReadObservation, failed bool) (path string, count, total int, eligible bool) {
	if failed || !read.ShouldTrack() {
		return "", 0, 0, false
	}
	path = read.Path
	if path == "" {
		return "", 0, 0, false
	}
	if s.partialReadsByPath == nil {
		s.partialReadsByPath = make(map[string]int)
	}
	s.partialReadsByPath[path]++
	count = s.partialReadsByPath[path]
	_, noted := s.notedReadPaths[path]
	eligible = count >= partialReadNoteThreshold && !noted && s.notesFired < maxEfficiencyNotesPerTurn
	return path, count, read.Total, eligible
}
