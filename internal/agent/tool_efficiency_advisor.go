package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// Efficiency notes are one-shot runtime hints appended to a tool result's
// model-facing content when the turn's tool usage falls into a known slow
// pattern: long streaks of responses that each issue exactly one read-only
// lookup, or repeated small-window reads of a file that fits in a single read.
// Static prompt guidance covers both, but long turns drift away from it; the
// note re-surfaces the guidance at the moment the pattern is happening.
//
// Notes ride inside the tool result (like ModelContextNote) rather than a turn
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
	// end (for example offset=1 skipping the package line) from counting as
	// partial: below this remainder another targeted read is not worth advising.
	partialReadMinRemainderLines = 40
	maxEfficiencyNotesPerTurn    = 4
)

// toolEfficiencyState tracks per-turn tool usage patterns for efficiency
// notes. Owned by the agent's event-loop goroutine, like Turn.MalformedCount;
// no synchronization needed.
type toolEfficiencyState struct {
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
	switch tools.NormalizeName(name) {
	case tools.NameRead, tools.NameGrep, tools.NameGlob, tools.NameLsp:
		return true
	default:
		return false
	}
}

// noteDispatchedToolRound classifies one dispatched LLM round. forcedShape
// marks rounds whose call count was imposed by the runtime (length recovery),
// which say nothing about the model's own batching behavior and leave the
// streak untouched.
func (t *Turn) noteDispatchedToolRound(calls []message.ToolCall, forcedShape bool) {
	if t == nil || forcedShape {
		return
	}
	s := &t.Efficiency
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

// efficiencyNoteForToolResult returns a one-shot efficiency note to append to
// this tool result's model-facing content, or "" when no advisory applies.
// rawResult must be the unaudited tool output so the READ_RESULT header is
// still the first line.
func (t *Turn) efficiencyNoteForToolResult(callID, toolName, argsJSON, rawResult string, failed bool) string {
	if t == nil {
		return ""
	}
	s := &t.Efficiency
	armed := callID != "" && callID == s.armedBatchingCallID
	if armed {
		s.armedBatchingCallID = ""
	}
	path, count, total, readNoteEligible := s.recordPartialRead(toolName, argsJSON, rawResult, failed)
	if armed && !failed {
		s.batchingNotesFired++
		s.notesFired++
		return fmt.Sprintf(
			"Efficiency note: each of the last %d responses issued exactly one read-only lookup (%s). When your next lookups are independent of each other (different files, symbols, or ranges), issue them together in one response — they run in parallel and each avoided response saves a full model round trip. Keep one lookup per response only when it depends on the previous result.",
			s.armedStreakLen,
			strings.Join([]string{tools.NameRead, tools.NameGrep, tools.NameGlob, tools.NameLsp}, ", "),
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
			count, path, total, tools.MaxOutputLines,
		)
	}
	return ""
}

// recordPartialRead counts a successful partial-window read of a fully
// readable file and reports whether a full-read note may be emitted for it.
// Marking the path as noted is left to the caller so a colliding batching
// note (only one note per result) keeps the path eligible for the next read.
func (s *toolEfficiencyState) recordPartialRead(toolName, argsJSON, rawResult string, failed bool) (path string, count, total int, eligible bool) {
	if failed || tools.NormalizeName(toolName) != tools.NameRead {
		return "", 0, 0, false
	}
	header, _, _ := strings.Cut(rawResult, "\n")
	r := parseReadResultHeaderRange(header)
	if !r.OK || r.Total <= 0 || r.Total > tools.MaxOutputLines {
		return "", 0, 0, false
	}
	// A budget-truncated read could not have returned more lines anyway.
	if strings.Contains(header, "truncated=") {
		return "", 0, 0, false
	}
	if r.Total-(r.End-r.Start+1) < partialReadMinRemainderLines {
		return "", 0, 0, false
	}
	path = toolPathFromArgs(json.RawMessage(argsJSON))
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
	return path, count, r.Total, eligible
}
