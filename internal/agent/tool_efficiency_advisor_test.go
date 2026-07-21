package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func lookupCall(id, name string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Args: json.RawMessage(`{}`)}
}

func readArgs(path string) string {
	return fmt.Sprintf(`{"path":%q,"offset":1,"limit":200}`, path)
}

func partialReadResult(start, end, total int) string {
	return fmt.Sprintf("READ_RESULT lines=%d-%d total=%d\nsample line\n", start, end, total)
}

// dispatchSingleLookups simulates n dispatched rounds that each carry exactly
// one read call, consuming any produced note between rounds like the real
// dispatch→result loop does. Returns the last produced note.
func dispatchSingleLookups(t *testing.T, turn *Turn, n int, idPrefix string) string {
	t.Helper()
	note := ""
	for i := range n {
		call := lookupCall(fmt.Sprintf("%s-%d", idPrefix, i), tools.NameRead)
		turn.noteDispatchedToolRound([]message.ToolCall{call}, false)
		note = turn.efficiencyNoteForToolResult(call.ID, call.Name, `{"path":"pkg/sample.go"}`, "READ_RESULT lines=1-10 total=10\nok\n", false)
	}
	return note
}

func TestBatchingNoteFiresAfterStreak(t *testing.T) {
	turn := &Turn{}
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
	if turn.Efficiency.consecutiveSingleLookupRounds != 0 {
		t.Fatalf("streak not reset after arming: %d", turn.Efficiency.consecutiveSingleLookupRounds)
	}
}

func TestBatchingStreakResetsOnBatchedOrNonLookupRounds(t *testing.T) {
	resets := [][]message.ToolCall{
		{lookupCall("a", tools.NameRead), lookupCall("b", tools.NameGrep)}, // batched round
		{lookupCall("c", tools.NameShell)},                                 // mutating single call
		nil,                                                                // no tool calls
	}
	for i, calls := range resets {
		turn := &Turn{}
		dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
		turn.noteDispatchedToolRound(calls, false)
		if got := turn.Efficiency.consecutiveSingleLookupRounds; got != 0 {
			t.Fatalf("case %d: streak = %d, want 0", i, got)
		}
		if note := dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "again"); note != "" {
			t.Fatalf("case %d: streak should restart from zero, got note %q", i, note)
		}
	}
}

func TestForcedShapeRoundLeavesStreakUntouched(t *testing.T) {
	turn := &Turn{}
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.noteDispatchedToolRound([]message.ToolCall{lookupCall("forced", tools.NameRead)}, true)
	if got := turn.Efficiency.consecutiveSingleLookupRounds; got != batchingNoteStreakThreshold-1 {
		t.Fatalf("forced round changed streak: %d", got)
	}
	if note := dispatchSingleLookups(t, turn, 1, "arm"); !strings.Contains(note, "Efficiency note") {
		t.Fatalf("expected note on next organic single round, got %q", note)
	}
}

func TestBatchingNoteConsumedOnceAndCapped(t *testing.T) {
	turn := &Turn{}
	call := lookupCall("armed", tools.NameRead)
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.noteDispatchedToolRound([]message.ToolCall{call}, false)
	if note := turn.efficiencyNoteForToolResult(call.ID, call.Name, `{"path":"pkg/sample.go"}`, "READ_RESULT lines=1-10 total=10\nok\n", false); note == "" {
		t.Fatal("expected first batching note")
	}
	if note := turn.efficiencyNoteForToolResult(call.ID, call.Name, `{"path":"pkg/sample.go"}`, "READ_RESULT lines=1-10 total=10\nok\n", false); note != "" {
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
	turn := &Turn{}
	call := lookupCall("armed", tools.NameRead)
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.noteDispatchedToolRound([]message.ToolCall{call}, false)
	if note := turn.efficiencyNoteForToolResult(call.ID, call.Name, `{}`, "boom", true); note != "" {
		t.Fatalf("failed result should not carry a note, got %q", note)
	}
	if turn.Efficiency.armedBatchingCallID != "" {
		t.Fatal("failed result should still consume the armed call ID")
	}
	if turn.Efficiency.batchingNotesFired != 0 {
		t.Fatalf("skipped note must not count toward the cap: %d", turn.Efficiency.batchingNotesFired)
	}
}

func TestStaleArmClearedByNextDispatch(t *testing.T) {
	turn := &Turn{}
	call := lookupCall("cancelled", tools.NameRead)
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold-1, "warm")
	turn.noteDispatchedToolRound([]message.ToolCall{call}, false)
	// The armed round's result never arrives (cancelled); a new round dispatches.
	turn.noteDispatchedToolRound([]message.ToolCall{lookupCall("x", tools.NameShell)}, false)
	if note := turn.efficiencyNoteForToolResult(call.ID, call.Name, `{}`, "late", false); note != "" {
		t.Fatalf("stale arm must not fire after a newer dispatch, got %q", note)
	}
}

func TestPartialReadNoteFiresOnThirdWindowOncePerPath(t *testing.T) {
	turn := &Turn{}
	args := readArgs("pkg/big.go")
	for i := 1; i < partialReadNoteThreshold; i++ {
		if note := turn.efficiencyNoteForToolResult(fmt.Sprintf("r%d", i), tools.NameRead, args, partialReadResult(1, 200, 940), false); note != "" {
			t.Fatalf("window %d fired early: %q", i, note)
		}
	}
	note := turn.efficiencyNoteForToolResult("r-final", tools.NameRead, args, partialReadResult(201, 400, 940), false)
	if !strings.Contains(note, fmt.Sprintf("partial read #%d of pkg/big.go", partialReadNoteThreshold)) {
		t.Fatalf("expected full-read note on window %d, got %q", partialReadNoteThreshold, note)
	}
	if !strings.Contains(note, "940 lines") {
		t.Fatalf("note should report the total line count, got %q", note)
	}
	if again := turn.efficiencyNoteForToolResult("r-extra", tools.NameRead, args, partialReadResult(401, 600, 940), false); again != "" {
		t.Fatalf("path should be noted at most once per turn, got %q", again)
	}
}

func TestPartialReadCountingGates(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		args      string
		rawResult string
		failed    bool
	}{
		{"full read", tools.NameRead, readArgs("a.go"), partialReadResult(1, 940, 940), false},
		{"near-full remainder", tools.NameRead, readArgs("a.go"), partialReadResult(2, 941, 941), false},
		{"file exceeds one read", tools.NameRead, readArgs("a.go"), partialReadResult(1, 200, tools.MaxOutputLines+500), false},
		{"budget truncated", tools.NameRead, readArgs("a.go"), "READ_RESULT lines=1-200 total=940 truncated=budget requested_lines=1-900\nsample\n", false},
		{"no range header", tools.NameRead, readArgs("a.go"), "READ_RESULT lines=none total=940\n", false},
		{"failed read", tools.NameRead, readArgs("a.go"), partialReadResult(1, 200, 940), true},
		{"not a read", tools.NameGrep, readArgs("a.go"), partialReadResult(1, 200, 940), false},
		{"missing path", tools.NameRead, `{}`, partialReadResult(1, 200, 940), false},
	}
	for _, tc := range cases {
		turn := &Turn{}
		for i := range partialReadNoteThreshold * 2 {
			if note := turn.efficiencyNoteForToolResult(fmt.Sprintf("c%d", i), tc.toolName, tc.args, tc.rawResult, tc.failed); note != "" {
				t.Fatalf("%s: unexpected note %q", tc.name, note)
			}
		}
		if len(turn.Efficiency.partialReadsByPath) != 0 {
			t.Fatalf("%s: result should not be counted, got %v", tc.name, turn.Efficiency.partialReadsByPath)
		}
	}
}

func TestBatchingNoteTakesPrecedenceAndKeepsPathEligible(t *testing.T) {
	turn := &Turn{}
	args := readArgs("pkg/central.go")
	// Warm the streak so it reaches the batching threshold exactly when the
	// path reaches its window threshold: unrelated lookups first, then two
	// partial windows of the path across single-lookup rounds.
	for i := range batchingNoteStreakThreshold - partialReadNoteThreshold {
		turn.noteDispatchedToolRound([]message.ToolCall{lookupCall(fmt.Sprintf("g%d", i), tools.NameGrep)}, false)
		turn.efficiencyNoteForToolResult(fmt.Sprintf("g%d", i), tools.NameGrep, `{}`, "no matches", false)
	}
	for i := range partialReadNoteThreshold - 1 {
		turn.noteDispatchedToolRound([]message.ToolCall{lookupCall(fmt.Sprintf("w%d", i), tools.NameRead)}, false)
		turn.efficiencyNoteForToolResult(fmt.Sprintf("w%d", i), tools.NameRead, args, partialReadResult(1, 200, 940), false)
	}
	// The next single round both completes the batching streak and is the
	// threshold-crossing partial window: only the batching note is emitted.
	armed := lookupCall("both", tools.NameRead)
	turn.noteDispatchedToolRound([]message.ToolCall{armed}, false)
	note := turn.efficiencyNoteForToolResult(armed.ID, armed.Name, args, partialReadResult(201, 400, 940), false)
	if !strings.Contains(note, "read-only lookup") {
		t.Fatalf("expected batching note to win, got %q", note)
	}
	// The path was not marked as noted, so the next window still advises.
	next := turn.efficiencyNoteForToolResult("after", tools.NameRead, args, partialReadResult(401, 600, 940), false)
	if !strings.Contains(next, "partial read #") {
		t.Fatalf("expected deferred full-read note, got %q", next)
	}
}

func TestEfficiencyNotesTotalCapPerTurn(t *testing.T) {
	turn := &Turn{}
	fired := 0
	// Distinct paths each reach the partial-read threshold in sequence.
	for p := range maxEfficiencyNotesPerTurn + 2 {
		args := readArgs(fmt.Sprintf("pkg/file%d.go", p))
		for i := range partialReadNoteThreshold {
			if note := turn.efficiencyNoteForToolResult(fmt.Sprintf("p%d-%d", p, i), tools.NameRead, args, partialReadResult(1, 200, 940), false); note != "" {
				fired++
			}
		}
	}
	if fired != maxEfficiencyNotesPerTurn {
		t.Fatalf("total notes per turn = %d, want %d", fired, maxEfficiencyNotesPerTurn)
	}
	// The cap also blocks arming new batching notes.
	dispatchSingleLookups(t, turn, batchingNoteStreakThreshold, "capped")
	if turn.Efficiency.armedBatchingCallID != "" {
		t.Fatal("total cap should block further arming")
	}
}
