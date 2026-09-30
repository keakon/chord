package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestEditBatchUsesOriginalText(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha middle alpha omega\n")
	result, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "omega", "replace_all": true},
			{"old_string": "omega", "new_string": "last"},
			{"old_string": "middle ", "new_string": ""},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "omega omega last\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(result, "4 replacements") {
		t.Fatalf("result = %s", result)
	}
}

func TestEditBatchRejectsWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edits []map[string]any
		want  string
	}{
		{"empty", []map[string]any{}, "at least one"},
		{"missing", []map[string]any{{"old_string": "alpha", "new_string": "changed"}, {"old_string": "absent", "new_string": "text"}}, "edits[1]"},
		{"dependent", []map[string]any{{"old_string": "alpha", "new_string": "changed"}, {"old_string": "changed", "new_string": "text"}}, "original file"},
		{"overlap", []map[string]any{{"old_string": "alpha", "new_string": "changed"}, {"old_string": "alpha beta", "new_string": "text"}}, "overlaps"},
		{"ambiguous", []map[string]any{{"old_string": "beta", "new_string": "changed"}}, "found 2 times"},
		{"missing replacement", []map[string]any{{"old_string": "alpha"}}, "new_string is required"},
		{"empty search", []map[string]any{{"old_string": "", "new_string": "text"}}, "old_string is required"},
		{"control", []map[string]any{{"old_string": "alpha", "new_string": "\x00"}}, "new_string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			const original = "alpha beta beta\n"
			path := writeEditFixture(t, dir, "sample.txt", original)
			_, err := runEdit(t, dir, map[string]any{"path": path, "edits": tc.edits})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "changes were written") {
				t.Fatalf("error = %v, want the no-writes statement", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Fatalf("failed batch changed file: %q", got)
			}
		})
	}
}

func TestEditBatchRejectsMixedFields(t *testing.T) {
	for _, field := range []map[string]any{
		{"old_string": "alpha"},
		{"new_string": "beta"},
		{"replace_all": true},
	} {
		dir := t.TempDir()
		path := writeEditFixture(t, dir, "sample.txt", "alpha")
		args := map[string]any{"path": path, "edits": []map[string]any{{"old_string": "alpha", "new_string": "beta"}}}
		maps.Copy(args, field)
		_, err := runEdit(t, dir, args)
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("%v: error = %v", field, err)
		}
	}
}

// Zero-valued single-replacement fields carry no request of their own, so a
// batch that echoes them still applies.
func TestEditBatchIgnoresZeroValuedTopLevelFields(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha")
	_, err := runEdit(t, dir, map[string]any{
		"path":        path,
		"old_string":  "",
		"new_string":  "",
		"replace_all": false,
		"edits":       []map[string]any{{"old_string": "alpha", "new_string": "beta"}},
	})
	if err != nil {
		t.Fatalf("batch with zero-valued top-level fields: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "beta" {
		t.Fatalf("file = %q, want %q", got, "beta")
	}
}

func TestEditBatchOverlapNamesEntriesInFileOrder(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta beta\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	_, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "beta beta", "new_string": "one"},
			{"old_string": "alpha beta", "new_string": "two"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "edits[1]: overlaps edits[0] in the original file") {
		t.Fatalf("error = %v, want the earlier match named first", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != original {
		t.Fatalf("failed batch changed file: %q (%v)", got, readErr)
	}
}

func TestEditBatchOverlapAtSameOffsetNamesEntriesInEditsOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha beta\n")
	_, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "alpha beta", "new_string": "one"},
			{"old_string": "alpha", "new_string": "two"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "edits[0]: overlaps edits[1] in the original file") {
		t.Fatalf("error = %v, want matches at one offset named in edits order", err)
	}
}

// A batch entry that misses only by a trailing newline is not proof the text is
// absent: the same entry succeeds on its own, because a single edit tolerates
// that difference. The batch error has to state that fact as a diagnostic clue
// without telling the model to resend only that entry — the rest of the batch
// still has to be resubmitted.
func TestEditBatchNotFoundNamesTheSingleEditFallback(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	entry := map[string]any{"old_string": "alpha beta\n\n", "new_string": "alpha omega\n\n"}
	_, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{entry}})
	if err == nil {
		t.Fatal("expected the batch to reject a near-miss entry")
	}
	if !strings.Contains(err.Error(), "No changes were written (batch edits are atomic).") ||
		!strings.Contains(err.Error(), "edits[0]: old_string not found in the original file") {
		t.Fatalf("error = %v, want it to state that nothing was written and name the missing entry", err)
	}
	if !strings.Contains(err.Error(), "single edit") || !strings.Contains(err.Error(), tolerantMatchNote) {
		t.Fatalf("error = %v, want it to name the single-edit tolerance and %q", err, tolerantMatchNote)
	}
	if strings.Contains(err.Error(), "retry this entry") || strings.Contains(err.Error(), "retry it on its own") {
		t.Fatalf("error = %v, want no instruction to resend only the failing entry", err)
	}
	if !strings.Contains(err.Error(), "resubmit the complete batch") {
		t.Fatalf("error = %v, want the complete-batch resubmit guidance", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != original {
		t.Fatalf("failed batch changed file: %q (%v)", got, readErr)
	}
	// The claim in that message: the same entry is accepted as a single edit.
	if _, err := runEdit(t, dir, map[string]any{"path": path, "old_string": "alpha beta\n\n", "new_string": "alpha omega\n\n"}); err != nil {
		t.Fatalf("single edit of the same entry failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha omega\n" {
		t.Fatalf("single edit content = %q", got)
	}
}

func TestEditBatchSchema(t *testing.T) {
	for _, raw := range []string{
		`{"path":"sample.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`,
		`{"path":"sample.txt","old_string":"alpha","new_string":"beta"}`,
	} {
		if err := ValidateToolArgs(EditTool{}, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateToolArgs(EditTool{}, json.RawMessage(`{"path":"sample.txt","edits":[{"old_string":"alpha"}]}`)); err == nil {
		t.Fatal("missing new_string accepted")
	}
}

func TestEditBatchAmbiguityReportsOriginalLines(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("newline-%q", newline), func(t *testing.T) {
			dir := t.TempDir()
			original := strings.Join([]string{"first", "target", "middle", "target", ""}, newline)
			path := writeEditFixture(t, dir, "sample.txt", original)
			_, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{
				{"old_string": "first", "new_string": "first\nextra"},
				{"old_string": "target", "new_string": "changed"},
			}})
			if err == nil || !strings.Contains(err.Error(), "edits[1]: old_string found 2 times at lines 2, 4 in the original file; provide unique context or set replace_all") {
				t.Fatalf("unexpected error: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != original {
				t.Fatalf("batch wrote changes: %q, %v", got, err)
			}
		})
	}
}

func TestMatchLineNumbersPreservesNonOverlappingLocations(t *testing.T) {
	for _, tc := range []struct{ content, needle string }{
		{"", ""}, {"abc", ""}, {"aaaaa", "aa"},
		{"a\nb\na\nb\n", "a\nb\n"}, {"\n\nx\n\nx", "\n\nx"},
		{"x\r\nx\r\n", "x"}, {"abc", "missing"},
	} {
		var want []int
		if tc.needle != "" {
			for offset := 0; offset < len(tc.content); {
				at := strings.Index(tc.content[offset:], tc.needle)
				if at < 0 {
					break
				}
				start := offset + at
				want = append(want, 1+strings.Count(tc.content[:start], "\n"))
				offset = start + len(tc.needle)
			}
		}
		if got := matchLineNumbers(tc.content, tc.needle, maxMatchLinesShown); !slices.Equal(got, want) {
			t.Fatalf("content=%q needle=%q: got %v, want %v", tc.content, tc.needle, got, want)
		}
	}
}

func TestMatchLineNumbersBoundsCollectionAndReportsTotal(t *testing.T) {
	const matched = maxMatchLinesShown + 8
	content := strings.Repeat("x\n", matched)
	lines := matchLineNumbers(content, "x", maxMatchLinesShown)
	if len(lines) != maxMatchLinesShown {
		t.Fatalf("matchLineNumbers collected %d lines, want %d", len(lines), maxMatchLinesShown)
	}
	rendered := formatMatchLines(lines, matched)
	if !strings.Contains(rendered, fmt.Sprintf("(+%d more)", matched-len(lines))) {
		t.Fatalf("formatMatchLines(%v, %d) = %q, want the omitted count", lines, matched, rendered)
	}
}

// A batch entry without replace_all stops collecting matches at the listing
// cap but still reports the true total.
func TestPlanExactReplacementsReportsTotalBeyondLineCap(t *testing.T) {
	const matched = maxMatchLinesShown + 8
	_, err := planExactReplacements(strings.Repeat("x\n", matched), []textReplacement{{OldString: "x", NewString: new("y")}})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("found %d times at lines 1, 2,", matched)) || !strings.Contains(err.Error(), "(+8 more)") {
		t.Fatalf("err = %v, want the full count and the first lines", err)
	}
}

func BenchmarkEditBatchAmbiguousOriginal(b *testing.B) {
	content := strings.Repeat("section\ntarget\n", 10000)
	edits := []textReplacement{{OldString: "target", NewString: new("changed")}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := planExactReplacements(content, edits); err == nil {
			b.Fatal("expected ambiguity")
		}
	}
}

func TestEditBatchNotFoundShowsClosestMatchWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	original := "header\nresult = transform(value, enabled=True)\nfooter\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	_, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{{"old_string": "header", "new_string": "changed"}, {"old_string": "result = transform(values, enabled=True)", "new_string": "result = transform(value)"}}})
	if err == nil || !strings.Contains(err.Error(), "edits[1]") || !strings.Contains(err.Error(), "Closest match is at line 2") {
		t.Fatalf("missing diagnostic: %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != original {
		t.Fatalf("failed batch changed file: %q %v", got, readErr)
	}
}

// entryDetailLines returns the indented detail lines of one entry's problem
// block in a batch failure report.
func entryDetailLines(message string, entry int) []string {
	header := fmt.Sprintf("edits[%d]:", entry)
	var detail []string
	inBlock := false
	for line := range strings.SplitSeq(message, "\n") {
		switch {
		case strings.HasPrefix(line, "edits["):
			inBlock = strings.HasPrefix(line, header)
		case inBlock && strings.HasPrefix(line, "  "):
			detail = append(detail, line)
		}
	}
	return detail
}

// A batch with several failure kinds reports them all in one result, states
// that nothing was written, and asks for the complete corrected batch — never
// for a single resend of the failing entry.
func TestEditBatchReportsEveryFailureInOneResult(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta beta\nomega\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	_, err := runEdit(t, dir, map[string]any{
		"path": path,
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "ALPHA"},
			{"old_string": "alpha beta z", "new_string": "text"},
			{"old_string": "beta", "new_string": "BETA"},
			{"old_string": "omega", "new_string": "omega"},
			{"old_string": "", "new_string": "text"},
			{"old_string": "gamma"},
		},
	})
	if err == nil {
		t.Fatal("expected the batch to reject the failing entries")
	}
	message := err.Error()
	for _, want := range []string{
		"No changes were written (batch edits are atomic).",
		"edits[1]: old_string not found in the original file",
		"edits[2]: old_string found 2 times at lines 1, 1 in the original file; provide unique context or set replace_all",
		"edits[4]: old_string is required",
		"edits[5]: new_string is required; use an empty string for deletion",
		"Skipped edits[3]: old_string and new_string are identical, so no change was requested.",
		"Remaining entries (edits[0]) passed per-entry matching but were not applied.",
		"Fix the failing entries above and resubmit the complete batch.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("report missing %q:\n%s", want, message)
		}
	}
	for _, unwanted := range []string{"retry this entry", "retry it on its own", "Applied "} {
		if strings.Contains(message, unwanted) {
			t.Fatalf("report contains %q:\n%s", unwanted, message)
		}
	}
	if lines := strings.Split(message, "\n"); len(lines) > batchReportLineBudget {
		t.Fatalf("report has %d lines, want at most %d:\n%s", len(lines), batchReportLineBudget, message)
	}
	// Entries are named in index order, and the missing entry carries its
	// closest-match detail within the per-entry bound.
	at := 0
	for _, token := range []string{"edits[1]:", "edits[2]:", "edits[4]:", "edits[5]:"} {
		next := strings.Index(message[at:], token)
		if next < 0 {
			t.Fatalf("report lacks %q in order:\n%s", token, message)
		}
		at += next
	}
	if detail := entryDetailLines(message, 1); len(detail) == 0 || len(detail) > maxBatchEntryDetailLines {
		t.Fatalf("entry detail = %q, want 1..%d lines", detail, maxBatchEntryDetailLines)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != original {
		t.Fatalf("failed batch changed file: %q (%v)", got, readErr)
	}
}

// A batch member whose old and new text are identical requests no change: it
// is reported as skipped, not applied, and the rest of the batch still runs.
func TestEditBatchSkipsIdenticalEntries(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "alpha beta\n")
	result, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{
		{"old_string": "alpha", "new_string": "alpha"},
		{"old_string": "beta", "new_string": "gamma"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Applied 1 edits") || !strings.Contains(result, "Skipped edits[0]: old_string and new_string are identical") {
		t.Fatalf("result = %q, want one applied edit and the skipped entry", result)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "alpha gamma\n" {
		t.Fatalf("file = %q (%v), want the skipped entry untouched", got, readErr)
	}
}

// A batch that requests no text change at all reports no changes and takes no
// write path: the result must not contain the write summary the applied path
// always emits.
func TestEditBatchAllIdenticalReportsNoChanges(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	result, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{
		{"old_string": "alpha", "new_string": "alpha"},
		{"old_string": "beta", "new_string": "beta"},
	}})
	if err != nil {
		t.Fatalf("all-identical batch: %v", err)
	}
	if !strings.Contains(result, "No changes") || !strings.Contains(result, "identical old_string and new_string") {
		t.Fatalf("result = %q, want the no-change report", result)
	}
	if strings.Contains(result, "bytes ->") {
		t.Fatalf("result = %q, want no write summary", result)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != original {
		t.Fatalf("file = %q (%v), want it untouched", got, readErr)
	}
}

// A batch entry whose block drifted too far cannot be rebuilt from the shown
// lines: its bounded detail sends the model to a fresh read instead of inviting
// a memory retype.
func TestEditBatchEntryDetailStaysBounded(t *testing.T) {
	dir := t.TempDir()
	var file, old strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&file, "item %d y\n", i)
		fmt.Fprintf(&old, "item %d x", i)
		if i < 8 {
			old.WriteString("\n")
		}
	}
	path := writeEditFixture(t, dir, "sample.txt", file.String())
	_, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{{"old_string": old.String(), "new_string": "replacement"}}})
	if err == nil {
		t.Fatal("expected the batch to reject the missing block")
	}
	detail := entryDetailLines(err.Error(), 0)
	if len(detail) != maxBatchEntryDetailLines {
		t.Fatalf("detail = %q, want exactly %d lines", detail, maxBatchEntryDetailLines)
	}
	if !strings.Contains(strings.Join(detail, "\n"), "read the file with offset=1 limit=8") {
		t.Fatalf("detail = %q, want the fresh-read guidance", detail)
	}
	if lines := strings.Split(err.Error(), "\n"); len(lines) > batchReportLineBudget {
		t.Fatalf("report has %d lines, want at most %d:\n%s", len(lines), batchReportLineBudget, err.Error())
	}
}

// The identical check runs before matching, so a batch treats it as a no-op
// skip; a single edit keeps reporting it as an error.
func TestEditToolIdenticalSingleEditRemainsAnError(t *testing.T) {
	dir := t.TempDir()
	const original = "alpha beta\n"
	path := writeEditFixture(t, dir, "sample.txt", original)
	_, err := runEdit(t, dir, map[string]any{"path": path, "old_string": "alpha", "new_string": "alpha"})
	if err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("error = %v, want the identical-arguments rejection", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != original {
		t.Fatalf("file = %q (%v), want it untouched", got, readErr)
	}
}
