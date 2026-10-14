package toolefficiency

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tooloutput"
)

func lookupCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{}`)}
}

func partialRead(path string, start, end, total int) ReadObservation {
	return ReadObservation{Path: path, Start: start, End: end, Total: total}
}

// dispatchSingleLookups simulates n dispatched rounds that each carry exactly
// one read call, consuming any produced note between rounds like the real
// dispatch→result loop does. Returns the last produced note.
func dispatchSingleLookups(t *testing.T, turn *State, n int, idPrefix string) string {
	t.Helper()
	note := ""
	for i := range n {
		call := lookupCall(fmt.Sprintf("%s-%d", idPrefix, i), toolname.Read)
		turn.Dispatch([]message.ToolCall{call}, false)
		note = turn.NoteResult(call.ID, false, ReadObservation{})
	}
	return note
}

func TestBatchingNoteFiresAfterStreak(t *testing.T) {
	turn := &State{}
	if note := dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm"); note != "" {
		t.Fatalf("streak below threshold produced note %q", note)
	}
	note := dispatchSingleLookups(t, turn, 1, "arm")
	if !strings.Contains(note, "Efficiency note") || !strings.Contains(note, "read-only lookup") {
		t.Fatalf("expected batching note at threshold, got %q", note)
	}
	if !strings.Contains(note, fmt.Sprintf("last %d responses", batchingNoteStreakThreshold)) {
		t.Fatalf("note should report the streak length, got %q", note)
	}
	if turn.consecutiveSingleLookupRounds != 0 {
		t.Fatalf("streak not reset after arming: %d", turn.consecutiveSingleLookupRounds)
	}
}

func TestBatchingStreakResetsOnBatchedOrNonLookupRounds(t *testing.T) {
	resets := [][]message.ToolCall{
		{lookupCall("a", toolname.Read), lookupCall("b", toolname.Grep)}, // batched round
		{lookupCall("c", toolname.Shell)},                                // mutating single call
		nil,                                                              // no tool calls
	}
	for i, calls := range resets {
		turn := &State{}
		dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
		turn.Dispatch(calls, false)
		if got := turn.consecutiveSingleLookupRounds; got != 0 {
			t.Fatalf("case %d: streak = %d, want 0", i, got)
		}
		if note := dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "again"); note != "" {
			t.Fatalf("case %d: streak should restart from zero, got note %q", i, note)
		}
	}
}

func TestForcedShapeRoundLeavesStreakUntouched(t *testing.T) {
	turn := &State{}
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.Dispatch([]message.ToolCall{lookupCall("forced", toolname.Read)}, true)
	if got := turn.consecutiveSingleLookupRounds; got != batchingNoteStreakThreshold-1 {
		t.Fatalf("forced round changed streak: %d", got)
	}
	if note := dispatchSingleLookups(t, turn, 1, "arm"); !strings.Contains(note, "Efficiency note") {
		t.Fatalf("expected note on next organic single round, got %q", note)
	}
}

func TestBatchingNoteConsumedOnceAndCapped(t *testing.T) {
	turn := &State{}
	call := lookupCall("armed", toolname.Read)
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.Dispatch([]message.ToolCall{call}, false)
	if note := turn.NoteResult(call.ID, false, ReadObservation{}); note == "" {
		t.Fatal("expected first batching note")
	}
	if note := turn.NoteResult(call.ID, false, ReadObservation{}); note != "" {
		t.Fatalf("armed note should be one-shot, got %q", note)
	}

	if note := dispatchSingleLookups(t, turn, batchingNoteStreakThreshold, "second"); !strings.Contains(note, "Efficiency note") {
		t.Fatalf("expected second batching note, got %q", note)
	}
	// maxBatchingNotesPerTurn reached: further streaks stay silent.
	if note := dispatchSingleLookups(t, turn, batchingNoteStreakThreshold*3, "third"); note != "" {
		t.Fatalf("batching notes should be capped per turn, got %q", note)
	}
}

func TestBatchingNoteSkippedOnFailedResult(t *testing.T) {
	turn := &State{}
	call := lookupCall("armed", toolname.Read)
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.Dispatch([]message.ToolCall{call}, false)
	if note := turn.NoteResult(call.ID, true, ReadObservation{}); note != "" {
		t.Fatalf("failed result should not carry a note, got %q", note)
	}
	if turn.armedBatchingCallID != "" {
		t.Fatal("failed result should still consume the armed call ID")
	}
	if turn.batchingNotesFired != 0 {
		t.Fatalf("skipped note must not count toward the cap: %d", turn.batchingNotesFired)
	}
}

func TestStaleArmClearedByNextDispatch(t *testing.T) {
	turn := &State{}
	call := lookupCall("cancelled", toolname.Read)
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.Dispatch([]message.ToolCall{call}, false)
	// The armed round's result never arrives (cancelled); a new round dispatches.
	turn.Dispatch([]message.ToolCall{lookupCall("x", toolname.Shell)}, false)
	if note := turn.NoteResult(call.ID, false, ReadObservation{}); note != "" {
		t.Fatalf("stale arm must not fire after a newer dispatch, got %q", note)
	}
}

func TestPartialReadNoteFiresOnThirdWindowOncePerPath(t *testing.T) {
	turn := &State{}
	args := "pkg/big.go"
	for i := 1; i < partialReadNoteThreshold; i++ {
		if note := turn.NoteResult(fmt.Sprintf("r%d", i), false, partialRead(args, 1, 200, 940)); note != "" {
			t.Fatalf("window %d fired early: %q", i, note)
		}
	}
	note := turn.NoteResult("r-final", false, partialRead(args, 201, 400, 940))
	if !strings.Contains(note, fmt.Sprintf("partial read #%d of pkg/big.go", partialReadNoteThreshold)) {
		t.Fatalf("expected full-read note on window %d, got %q", partialReadNoteThreshold, note)
	}
	if !strings.Contains(note, "940 lines") {
		t.Fatalf("note should report the total line count, got %q", note)
	}
	if again := turn.NoteResult("r-extra", false, partialRead(args, 401, 600, 940)); again != "" {
		t.Fatalf("path should be noted at most once per turn, got %q", again)
	}
}

func TestBatchingNoteTakesPrecedenceAndKeepsPathEligible(t *testing.T) {
	turn := &State{}
	args := "pkg/central.go"
	// Warm the streak so it reaches the batching threshold exactly when the
	// path reaches its window threshold: unrelated lookups first, then two
	// partial windows of the path across single-lookup rounds.
	for i := range batchingNoteStreakThreshold - partialReadNoteThreshold {
		turn.Dispatch([]message.ToolCall{lookupCall(fmt.Sprintf("g%d", i), toolname.Grep)}, false)
		turn.NoteResult(fmt.Sprintf("g%d", i), false, ReadObservation{})
	}
	for i := range partialReadNoteThreshold - 1 {
		turn.Dispatch([]message.ToolCall{lookupCall(fmt.Sprintf("w%d", i), toolname.Read)}, false)
		turn.NoteResult(fmt.Sprintf("w%d", i), false, partialRead(args, 1, 200, 940))
	}
	// The next single round both completes the batching streak and is the
	// threshold-crossing partial window: only the batching note is emitted.
	armed := lookupCall("both", toolname.Read)
	turn.Dispatch([]message.ToolCall{armed}, false)
	note := turn.NoteResult(armed.ID, false, partialRead(args, 201, 400, 940))
	if !strings.Contains(note, "read-only lookup") {
		t.Fatalf("expected batching note to win, got %q", note)
	}
	// The path was not marked as noted, so the next window still advises.
	next := turn.NoteResult("after", false, partialRead(args, 401, 600, 940))
	if !strings.Contains(next, "partial read #") {
		t.Fatalf("expected deferred full-read note, got %q", next)
	}
}

func TestEfficiencyNotesTotalCapPerTurn(t *testing.T) {
	turn := &State{}
	fired := 0
	// Distinct paths each reach the partial-read threshold in sequence.
	for p := range maxEfficiencyNotesPerTurn + 2 {
		args := fmt.Sprintf("pkg/file%d.go", p)
		for i := range partialReadNoteThreshold {
			if note := turn.NoteResult(fmt.Sprintf("p%d-%d", p, i), false, partialRead(args, 1, 200, 940)); note != "" {
				fired++
			}
		}
	}
	if fired != maxEfficiencyNotesPerTurn {
		t.Fatalf("total notes per turn = %d, want %d", fired, maxEfficiencyNotesPerTurn)
	}
	// The cap also blocks arming new batching notes.
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold, "capped")
	if turn.armedBatchingCallID != "" {
		t.Fatal("total cap should block further arming")
	}
}

func TestPartialReadObservationsDoNotCountIneligibleWindows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		read   ReadObservation
		failed bool
	}{
		{"full read", partialRead("a.go", 1, 940, 940), false},
		{"near full remainder", partialRead("a.go", 2, 941, 941), false},
		{"file exceeds one read", partialRead("a.go", 1, 200, tooloutput.MaxOutputLines+500), false},
		{"budget truncated", ReadObservation{Path: "a.go", Start: 1, End: 200, Total: 940, Truncated: true}, false},
		{"no observation", ReadObservation{}, false},
		{"failed read", partialRead("a.go", 1, 200, 940), true},
		{"missing path", partialRead("", 1, 200, 940), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var state State
			for i := range partialReadNoteThreshold * 2 {
				if note := state.NoteResult(fmt.Sprint(i), tc.failed, tc.read); note != "" {
					t.Fatalf("unexpected note: %q", note)
				}
			}
			if len(state.partialReadsByPath) != 0 {
				t.Fatalf("ineligible window counted: %v", state.partialReadsByPath)
			}
		})
	}
}

func TestConcurrentResultsShareTurnNoteBudget(t *testing.T) {
	var state State
	var notes atomic.Int32
	var workers sync.WaitGroup
	for pathIndex := range maxEfficiencyNotesPerTurn * 4 {
		workers.Go(func() {
			read := partialRead(fmt.Sprintf("pkg/file%d.go", pathIndex), 1, 200, 940)
			for i := range partialReadNoteThreshold {
				if state.NoteResult(fmt.Sprintf("%d-%d", pathIndex, i), false, read) != "" {
					notes.Add(1)
				}
			}
		})
	}
	workers.Wait()
	if notes.Load() != int32(maxEfficiencyNotesPerTurn) || state.notesFired != maxEfficiencyNotesPerTurn {
		t.Fatalf("notes=%d state=%d, want %d", notes.Load(), state.notesFired, maxEfficiencyNotesPerTurn)
	}
}
