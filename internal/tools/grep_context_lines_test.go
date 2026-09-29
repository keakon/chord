package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// writeGrepContextFixture writes a 12-line file whose hits sit at lines 4 and
// 8: far enough apart that a context_lines=2 window cannot merge them, and
// with a third hit at line 9 inside the trailing window of the hit at line 8
// so the merge path is exercised by the same fixture.
func writeGrepContextFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ctx.txt")
	lines := []string{
		"l1 alpha",
		"l2 alpha",
		"l3 alpha",
		"l4 needle one",
		"l5 alpha",
		"l6 alpha",
		"l7 alpha",
		"l8 needle two",
		"l9 needle three",
		"l10 alpha",
		"l11 alpha",
		"l12 alpha",
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func grepExec(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func TestGrepContextLinesDefaultKeepsMatchOnlyOutput(t *testing.T) {
	path := writeGrepContextFixture(t)
	out := grepExec(t, map[string]any{"pattern": "needle", "paths": []string{path}})
	for _, want := range []string{"ctx.txt:4:l4 needle one", "ctx.txt:8:l8 needle two", "ctx.txt:9:l9 needle three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("default output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "l5 alpha") || strings.Contains(out, "l3 alpha") {
		t.Fatalf("default output must not carry context lines; got:\n%s", out)
	}
}

func TestGrepContextLinesReturnsSurroundingLinesWithDistinctSeparator(t *testing.T) {
	path := writeGrepContextFixture(t)
	// BaseDir keeps the display path the bare fixture name, so assertions can
	// match whole output lines.
	out := grepExecIn(t, filepath.Dir(path), map[string]any{"pattern": "needle", "context_lines": 2})
	// Hit at line 4 keeps 2 lines before and 2 after; context uses "-" while
	// the hit keeps ":".
	for _, want := range []string{
		"| ctx.txt-2-l2 alpha",
		"| ctx.txt-3-l3 alpha",
		"ctx.txt:4:l4 needle one",
		"| ctx.txt-5-l5 alpha",
		"| ctx.txt-6-l6 alpha",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("context output missing %q; got:\n%s", want, out)
		}
	}
	// Lines outside every window stay out.
	for _, unwanted := range []string{"l1 alpha", "l12 alpha"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("context output must not include outside-window line %q; got:\n%s", unwanted, out)
		}
	}
}

func TestGrepContextLinesMergesAdjacentHitWindows(t *testing.T) {
	path := writeGrepContextFixture(t)
	out := grepExecIn(t, filepath.Dir(path), map[string]any{"pattern": "needle", "context_lines": 2})
	// The hits at lines 8 and 9 share their windows: the line between them is
	// emitted once, and the trailing window of line 9 runs to line 11.
	if n := strings.Count(out, "l10 alpha"); n != 1 {
		t.Fatalf("shared context line emitted %d times, want 1; got:\n%s", n, out)
	}
	if n := strings.Count(out, "l7 alpha"); n != 1 {
		t.Fatalf("leading context line emitted %d times, want 1; got:\n%s", n, out)
	}
	for _, want := range []string{"ctx.txt:8:l8 needle two", "ctx.txt:9:l9 needle three", "| ctx.txt-11-l11 alpha"} {
		if !strings.Contains(out, want) {
			t.Fatalf("merged window missing %q; got:\n%s", want, out)
		}
	}
	// Line 12 falls outside the last window.
	if strings.Contains(out, "l12 alpha") {
		t.Fatalf("trailing window must stop at the cap; got:\n%s", out)
	}
}

func TestGrepContextLinesOrderIsFileOrder(t *testing.T) {
	path := writeGrepContextFixture(t)
	out := grepExec(t, map[string]any{"pattern": "needle", "paths": []string{path}, "context_lines": 1})
	body := out
	if strings.Contains(body, "No matches found") {
		t.Fatalf("unexpected empty result: %s", body)
	}
	numbers := grepLineNumbers(t, body)
	want := []int{3, 4, 5, 7, 8, 9, 10}
	if len(numbers) != len(want) {
		t.Fatalf("emitted lines = %v, want %v (output:\n%s)", numbers, want, out)
	}
	for i := range want {
		if numbers[i] != want[i] {
			t.Fatalf("emitted lines = %v, want %v", numbers, want)
		}
	}
}

// grepLineNumbers extracts the line number of every emitted output line for a
// single-file search.
func grepLineNumbers(t *testing.T, out string) []int {
	t.Helper()
	var nums []int
	for line := range strings.SplitSeq(out, "\n") {
		_, isContext, ok := ParseGrepOutputLine(line)
		if !ok {
			continue
		}
		sep := ":"
		if isContext {
			sep = "-"
		}
		fields := strings.SplitN(strings.TrimPrefix(line, GrepContextLinePrefix), sep, 3)
		if len(fields) < 3 {
			continue
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		nums = append(nums, n)
	}
	return nums
}

func TestGrepContextLinesRejectsNonIntegerValues(t *testing.T) {
	path := writeGrepContextFixture(t)
	for _, value := range []any{-1, 1.5, "invalid", "2", true} {
		raw, err := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{path}, "context_lines": value})
		if err != nil {
			t.Fatal(err)
		}
		_, err = GrepTool{}.Execute(context.Background(), raw)
		if err == nil {
			t.Fatalf("context_lines=%v should be rejected", value)
		}
		if !strings.Contains(err.Error(), "context_lines") {
			t.Fatalf("error should name the argument; got %v", err)
		}
	}
}

// The executor must accept exactly what the integer schema admits, so a call
// that passes validation is never rejected by decoding and vice versa.
func TestGrepContextLinesDecodingMatchesSchemaIntegerRule(t *testing.T) {
	path := writeGrepContextFixture(t)
	pathJSON, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value  string
		accept bool
	}{
		{value: "2", accept: true},
		{value: "2.0", accept: true},
		{value: "null", accept: true},
		{value: `"2"`, accept: false},
		{value: "2.5", accept: false},
	} {
		raw := []byte(`{"pattern":"needle","paths":[` + string(pathJSON) + `],"context_lines":` + tc.value + `}`)
		validateErr := ValidateToolArgs(GrepTool{}, raw)
		out, execErr := GrepTool{BaseDir: filepath.Dir(path)}.Execute(context.Background(), raw)
		if (validateErr == nil) != tc.accept || (execErr == nil) != tc.accept {
			t.Fatalf("context_lines=%s: validate=%v execute=%v, want accept=%v", tc.value, validateErr, execErr, tc.accept)
		}
		if tc.accept && tc.value != "null" && !strings.Contains(out, "| ctx.txt-5-l5 alpha") {
			t.Fatalf("context_lines=%s should return context; got:\n%s", tc.value, out)
		}
	}
}

func TestGrepContextLinesDoNotConsumeTheMatchCap(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	// Ten hits, separated by more than the window so each one carries a full
	// 3-line leading and trailing context: the 60 context lines must not
	// consume the 4-hit budget.
	for range 10 {
		b.WriteString("needle\n")
		for range 8 {
			b.WriteString("filler\n")
		}
	}
	path := filepath.Join(dir, "many.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// A generous byte cap isolates the hit budget; the default 12 KiB would
	// bind first in this fixture.
	const capBytes = 1 << 20
	scan := scanGrepFile(context.Background(), path, dir, regexp.MustCompile("needle"), 4, capBytes, 3)
	if scan.err != nil {
		t.Fatalf("scanGrepFile: %v", scan.err)
	}
	hits := 0
	for _, m := range scan.matches {
		if !m.isContext() {
			hits++
		}
	}
	if hits != 4 {
		t.Fatalf("hits = %d, want 4 (the hit cap); entries = %d", hits, len(scan.matches))
	}
	if len(scan.matches) <= hits {
		t.Fatalf("scan should carry context entries beyond the hits; entries = %d", len(scan.matches))
	}
	formatted, appended := appendBudgetedGrepMatches(nil, scan, 4, capBytes, false)
	if appended.hits != 4 {
		t.Fatalf("budgeted hits = %d, want 4", appended.hits)
	}
	if len(formatted) <= appended.hits {
		t.Fatalf("budgeted output should include context lines; got %d lines", len(formatted))
	}
}

// Reaching the hit cap still completes the last hit's trailing window, so the
// final match is shown with the same context as every earlier one.
func TestGrepContextLinesHitCapCompletesLastWindow(t *testing.T) {
	path := writeGrepContextFixture(t)
	dir := filepath.Dir(path)
	re := regexp.MustCompile("needle")
	// Hits sit at lines 4, 8 and 9; a cap of 2 stops after line 8, whose
	// trailing window (lines 9 and 10) must still be read — line 9 is a hit
	// beyond the cap, so the window ends there.
	scan := scanGrepFile(context.Background(), path, dir, re, 1, 1<<20, 2)
	if !scan.hitCaps {
		t.Fatal("hit cap must be reported")
	}
	lines, appended := appendBudgetedGrepMatches(nil, scan, 1, 1<<20, false)
	want := []string{"| ctx.txt-2-l2 alpha", "| ctx.txt-3-l3 alpha", "ctx.txt:4:l4 needle one", "| ctx.txt-5-l5 alpha", "| ctx.txt-6-l6 alpha"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") || !appended.truncated || appended.hits != 1 {
		t.Fatalf("lines=%q appended=%+v, want %q", lines, appended, want)
	}
	// A merge budget smaller than the scan's applies the same rule.
	full := scanGrepFile(context.Background(), path, dir, re, 120, 1<<20, 2)
	merged, mergedAppend := appendBudgetedGrepMatches(nil, full, 1, 1<<20, false)
	if strings.Join(merged, "\n") != strings.Join(want, "\n") || !mergedAppend.truncated {
		t.Fatalf("merge under a smaller hit cap = %q (%+v), want %q", merged, mergedAppend, want)
	}
}

// writeGrepWindowFixture writes hits every 10 lines, each surrounded by
// distinct filler lines, so every hit carries a full context window. Filler
// lines are much longer than hits, so a budget too small for more context
// usually still has room for bare hits.
func writeGrepWindowFixture(t *testing.T, hits int) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for i := range hits * 10 {
		if i%10 == 5 {
			fmt.Fprintf(&b, "needle %d\n", i)
			continue
		}
		fmt.Fprintf(&b, "filler line %d %s\n", i, strings.Repeat("x", 40))
	}
	path := filepath.Join(dir, "windows.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertCompleteGrepWindows checks that every hit in lines is either bare or
// carries its full leading and trailing window, that no context line is
// orphaned from its hit, and that once a hit is bare every later hit is too.
// Only the last window with context may end early: its trailing lines are
// where the budget ran out.
func assertCompleteGrepWindows(t *testing.T, lines []string, contextLines int) (withContext, bare int) {
	t.Helper()
	type entry struct {
		num       int
		isContext bool
	}
	entries := make([]entry, 0, len(lines))
	for _, line := range lines {
		_, isContext, ok := ParseGrepOutputLine(line)
		if !ok {
			t.Fatalf("unexpected output line %q", line)
		}
		nums := grepLineNumbers(t, line)
		entries = append(entries, entry{num: nums[0], isContext: isContext})
	}
	cutShort := false
	for i, e := range entries {
		if e.isContext {
			continue
		}
		before, after := 0, 0
		for j := i - 1; j >= 0 && entries[j].isContext && entries[j].num == e.num-(i-j); j-- {
			before++
		}
		for j := i + 1; j < len(entries) && entries[j].isContext && entries[j].num == e.num+(j-i); j++ {
			after++
		}
		if before > 0 && (bare > 0 || cutShort) {
			t.Fatalf("hit at line %d carries context after context stopped: %q", e.num, lines)
		}
		switch {
		case before == 0 && after == 0:
			bare++
		case before == contextLines && after == contextLines:
			withContext++
		case before == contextLines && after < contextLines:
			cutShort = true
			withContext++
		default:
			t.Fatalf("hit at line %d has a partial window (%d before, %d after): %q", e.num, before, after, lines)
		}
	}
	return withContext, bare
}

// When the byte budget runs out for context, windows already emitted stay
// whole, the hit that no longer fits with its leading lines is listed bare,
// and every later hit is listed bare too instead of context being dropped
// from earlier windows.
func TestGrepContextBudgetOverflowKeepsEarlierWindowsWhole(t *testing.T) {
	path := writeGrepWindowFixture(t, 40)
	dir := filepath.Dir(path)
	re := regexp.MustCompile("needle")
	totalBare := 0
	for budget := 400; budget <= 1600; budget += 50 {
		scan := scanGrepFile(context.Background(), path, dir, re, 120, budget, 2)
		lines, appended := appendBudgetedGrepMatches(nil, scan, 120, budget, false)
		withContext, bare := assertCompleteGrepWindows(t, lines, 2)
		if withContext == 0 {
			t.Fatalf("budget %d: earlier windows must keep their context: %q", budget, lines)
		}
		totalBare += bare
		if !appended.contextOff {
			t.Fatalf("budget %d: omitted context must be reported", budget)
		}
		if body := strings.Join(lines, "\n"); appended.bytes != len(body) || appended.bytes > budget {
			t.Fatalf("budget %d: reported=%d actual=%d", budget, appended.bytes, len(body))
		}
		// The merge layer, given a smaller budget than the worker scanned
		// with, must produce exactly what a scan under that budget yields.
		wide := scanGrepFile(context.Background(), path, dir, re, 120, 1<<20, 2)
		merged, mergedAppend := appendBudgetedGrepMatches(nil, wide, 120, budget, false)
		if strings.Join(merged, "\n") != strings.Join(lines, "\n") || mergedAppend != appended {
			t.Fatalf("budget %d: merge of a wider scan diverged:\n%q\n%+v\nvs\n%q\n%+v", budget, merged, mergedAppend, lines, appended)
		}
	}
	if totalBare == 0 {
		t.Fatal("hits after the context cutoff must still be listed bare")
	}
}

// A search whose matches all fit but whose context did not reports only the
// omitted context, not a match truncation it never performed.
func TestGrepContextOmittedFooterIsSeparateFromMatchTruncation(t *testing.T) {
	tail := func(out string) string { return out[max(0, len(out)-400):] }

	// The first hit's full 20-line window nearly fills the budget; the second
	// hit no longer fits with its leading lines and is listed bare.
	dir := t.TempDir()
	filler := strings.Repeat("f", 300)
	var b strings.Builder
	for i := range 100 {
		switch i {
		case 25:
			b.WriteString("needle one\n")
		case 76:
			b.WriteString("needle two\n")
		default:
			b.WriteString(filler)
			b.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := grepExecIn(t, dir, map[string]any{"pattern": "needle", "context_lines": 20})
	if strings.Contains(out, "(showing first") {
		t.Fatalf("every match was listed, so no match truncation may be reported; got tail:\n%s", tail(out))
	}
	if !strings.Contains(out, "two.txt:26:needle one") || !strings.Contains(out, "two.txt:77:needle two") {
		t.Fatalf("both matches must be listed; got tail:\n%s", tail(out))
	}
	if !strings.Contains(out, "| two.txt-46-") || strings.Contains(out, "| two.txt-57-") {
		t.Fatalf("first window must be whole and the second hit bare; got tail:\n%s", tail(out))
	}
	if !strings.Contains(out, "\n\n"+GrepContextOmittedFooterPrefix) {
		t.Fatalf("context omission must be disclosed; got tail:\n%s", tail(out))
	}

	// Many hits: context runs out first, then the byte budget cuts matches.
	path := writeGrepWindowFixture(t, 300)
	out = grepExecIn(t, filepath.Dir(path), map[string]any{"pattern": "needle", "context_lines": 3})
	if !strings.Contains(out, "\n\n(showing first ") || !strings.Contains(out, ")\n"+GrepContextOmittedFooterPrefix) {
		t.Fatalf("both footers must be reported; got tail:\n%s", tail(out))
	}

	path = writeGrepWindowFixture(t, 3)
	out = grepExecIn(t, filepath.Dir(path), map[string]any{"pattern": "needle", "context_lines": 3})
	if strings.Contains(out, "(showing first") || strings.Contains(out, GrepContextOmittedFooterPrefix) {
		t.Fatalf("a search within budget must not carry footers; got:\n%s", out)
	}
}

func grepExecIn(t *testing.T, dir string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out, err := GrepTool{BaseDir: dir}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func TestGrepContextLinesClampsWithNotice(t *testing.T) {
	path := writeGrepContextFixture(t)
	for _, value := range []int{8, 20, 50} {
		out := grepExec(t, map[string]any{"pattern": "needle", "paths": []string{path}, "context_lines": value})
		if !strings.Contains(out, "ctx.txt:4:l4 needle one") {
			t.Fatalf("missing hit for %v: %s", value, out)
		}
		notice := fmt.Sprintf("Note: context_lines 50 exceeds the maximum of %d; using %d.", maxGrepContextLines, maxGrepContextLines)
		if strings.Contains(out, notice) != (value == 50) {
			t.Fatalf("wrong clamp notice for %v: %s", value, out)
		}
	}
	out := grepExec(t, map[string]any{"pattern": "absent", "paths": []string{path}, "context_lines": 50})
	if !strings.Contains(out, "Note: context_lines 50") || !strings.Contains(out, "No matches found") {
		t.Fatalf("empty search must retain clamp notice: %s", out)
	}
	// The schema must admit values the executor clamps.
	prop := (GrepTool{}).Parameters()["properties"].(map[string]any)["context_lines"].(map[string]any)
	if _, capped := prop["maximum"]; capped {
		t.Fatal("schema must not reject values before runtime clamping")
	}
}

// A request beyond the int range must be quoted in the clamp note as the
// number the caller sent, not the internal bound that keeps the int
// conversion defined.
func TestGrepContextLinesClampNoteQuotesSentValue(t *testing.T) {
	path := writeGrepContextFixture(t)
	pathJSON, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"pattern":"needle","paths":[` + string(pathJSON) + `],"context_lines":1e20}`)
	out, err := GrepTool{BaseDir: filepath.Dir(path)}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "ctx.txt:4:l4 needle one") {
		t.Fatalf("clamped search must still run; got:\n%s", out)
	}
	if !strings.Contains(out, "Note: context_lines 1e+20 exceeds the maximum of 20; using 20.") {
		t.Fatalf("clamp note must quote the value the caller sent; got:\n%s", out)
	}
	if strings.Contains(out, "2147483647") {
		t.Fatalf("clamp note must not report the internal conversion bound; got:\n%s", out)
	}
}

func TestGrepContextBudgetPreservesHits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	for _, content := range []string{
		strings.Repeat("x", 200) + "\nneedle\n",
		"before\nneedle " + strings.Repeat("x", 200) + "\n",
		"needle\n" + strings.Repeat("x", 200) + "\nneedle\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, scanBudget := range []int{64, 1024} {
			scan := scanGrepFile(context.Background(), path, dir, regexp.MustCompile("needle"), 120, scanBudget, 20)
			if scan.err != nil {
				t.Fatal(scan.err)
			}
			lines, appended := appendBudgetedGrepMatches(nil, scan, 120, 64, false)
			body := strings.Join(lines, "\n")
			if appended.hits == 0 || !strings.Contains(body, "needle") {
				t.Fatalf("context hid hit: %q", body)
			}
			if appended.bytes != len(body) || appended.bytes > 64 {
				t.Fatalf("budget mismatch: reported=%d actual=%d", appended.bytes, len(body))
			}
			if !appended.contextOff && !appended.truncated {
				t.Fatalf("omitted context must be disclosed: %q", body)
			}
			if strings.Count(content, "needle") == 2 && appended.hits != 2 {
				t.Fatalf("trailing context hid later hit: %q", body)
			}
		}
	}
}

// The defensive over-report truncation must cut at a hit boundary so a kept
// context line never loses the hit it was emitted for.
func TestGrepSafetyNetCutKeepsContextAttached(t *testing.T) {
	lines := []string{
		"| ctx.txt-2-l2 alpha",
		"ctx.txt:4:l4 needle one",
		"| ctx.txt-5-l5 alpha",
		"| ctx.txt-7-l7 alpha",
		"ctx.txt:8:l8 needle two",
		"| ctx.txt-9-l9 alpha",
	}
	join := func(in []string) string { return strings.Join(in, "\n") }
	// A naive cut at 4 would orphan the leading context of the hit at 8; the
	// cut moves back to the last hit.
	if got := grepCutAtHitBoundary(lines, 4); join(got) != join(lines[:2]) {
		t.Fatalf("cut at 4 = %q, want %q", got, lines[:2])
	}
	// A cut inside trailing context moves back to its hit as well.
	if got := grepCutAtHitBoundary(lines, 3); join(got) != join(lines[:2]) {
		t.Fatalf("cut at 3 = %q, want %q", got, lines[:2])
	}
	// A cut that lands on a hit keeps everything through it.
	if got := grepCutAtHitBoundary(lines, 5); join(got) != join(lines[:5]) {
		t.Fatalf("cut at 5 = %q, want %q", got, lines[:5])
	}
	// Within the bound nothing is cut.
	if got := grepCutAtHitBoundary(lines, len(lines)); join(got) != join(lines) {
		t.Fatalf("no-cut = %q, want %q", got, lines)
	}
	// A bound inside a leading block drops the block instead of keeping
	// context whose hit was cut.
	if got := grepCutAtHitBoundary(lines, 1); len(got) != 0 {
		t.Fatalf("cut at 1 = %q, want empty", got)
	}
}

func TestGrepContextLongLinesAreShortened(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	long := strings.Repeat("ab", maxGrepContextLineBytes)
	if err := os.WriteFile(path, []byte(long+"\nneedle\n"+long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scan := scanGrepFile(context.Background(), path, dir, regexp.MustCompile("needle"), 120, 1<<20, 1)
	if len(scan.matches) != 3 {
		t.Fatalf("entries = %+v, want leading, hit, trailing", scan.matches)
	}
	for _, m := range scan.matches {
		if !m.isContext() {
			continue
		}
		if want := long[:maxGrepContextLineBytes] + "..."; m.text != want {
			t.Fatalf("context line %d = %d bytes, want %d-byte prefix plus marker", m.num, len(m.text), maxGrepContextLineBytes)
		}
	}
}

func TestGrepContextMultipleFilesKeepSharedBudget(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("before\nneedle\nafter\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise both exact-include and parallel directory traversal paths.
	for _, includes := range [][]string{nil, {"a.txt", "b.txt"}} {
		root, err := grepSearchRoot(context.Background(), dir, dir, info,
			regexp.MustCompile("needle"), includes, dir, 120, 60, 20, "")
		if err != nil {
			t.Fatal(err)
		}
		body := strings.Join(root.lines, "\n")
		if root.hits != 2 || root.bytes != len(body) || root.bytes > 60 {
			t.Fatalf("hits=%d bytes=%d output=%q", root.hits, root.bytes, body)
		}
		// Once the first file's window used the room, the second file is
		// listed bare rather than resuming context.
		if !root.contextOmitted || strings.Contains(body, "b.txt-") {
			t.Fatalf("context must stay off across files: omitted=%v output=%q", root.contextOmitted, body)
		}
	}
}

func TestParseGrepOutputLine(t *testing.T) {
	for _, tc := range []struct {
		line      string
		path      string
		isContext bool
		ok        bool
	}{
		{line: "a.go:12:func main() {", path: "a.go", ok: true},
		{line: "| a.go-11-// comment: see 3:4:5", isContext: true, ok: true},
		{line: "notes/2026-09-29-plan.md:3:first item", path: "notes/2026-09-29-plan.md", ok: true},
		{line: "| notes/2026-09-29-plan.md-4-second item", isContext: true, ok: true},
		{line: "| a.go-5-\tx := m[a:1:2]", isContext: true, ok: true},
		{line: `C:\src\a.go:7:x`, path: `C:\src\a.go`, ok: true},
		{line: "(showing first 120 matches within 12 KiB; narrow paths/includes/pattern for more precise results)"},
		{line: "Note: context_lines 50 exceeds the maximum of 20; using 20."},
		{line: "No matches found."},
	} {
		path, isContext, ok := ParseGrepOutputLine(tc.line)
		if path != tc.path || isContext != tc.isContext || ok != tc.ok {
			t.Fatalf("ParseGrepOutputLine(%q) = (%q, %v, %v), want (%q, %v, %v)", tc.line, path, isContext, ok, tc.path, tc.isContext, tc.ok)
		}
	}
}

func TestGrepAmbiguousPathsAndExcerptContents(t *testing.T) {
	for _, name := range []string{"my-2026-09-note.md", "my file.md", "part:12:name.txt", "| sample.txt", "\"sample.txt"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, name), []byte("x[1:2:3]\nneedle\nnext\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out := grepExecIn(t, dir, map[string]any{"pattern": "needle", "context_lines": 1})
			hits, excerpts := 0, 0
			for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
				path, context, ok := ParseGrepOutputLine(line)
				if !ok {
					continue
				}
				if context {
					excerpts++
					if _, _, _, match := ParseGrepMatchLine(line); match {
						t.Fatalf("excerpt parsed as hit: %q", line)
					}
				} else {
					hits++
					if path != name {
						t.Fatalf("path = %q, want %q", path, name)
					}
				}
			}
			if hits != 1 || excerpts != 2 {
				t.Fatalf("hits=%d excerpts=%d: %s", hits, excerpts, out)
			}
		})
	}
}
