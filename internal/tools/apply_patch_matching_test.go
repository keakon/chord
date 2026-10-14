package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchHunkFailureReportsEarlierContextOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "first\nmiddle\nlast\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n-last\n+LAST\n" +
		"@@\n-first\n+FIRST\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found (2/2)") || !strings.Contains(err.Error(), "matching context exists earlier at line 1") || !strings.Contains(err.Error(), "hunks must follow file order") {
		t.Fatalf("error = %v, want hunk-order guidance", err)
	}
	assertApplyPatchFile(t, path, content)
}

func TestApplyPatchHunkFailureSaysLineMissingFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "first\nmiddle\nlast\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// The hunk's first expected line does not exist in the file at all
	// (not even as a punctuation/whitespace variant): the hint must say the
	// line is missing from the current file and ask for a re-read, rather
	// than blaming read history.
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-invented content line\n" +
		"+replacement\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk mismatch")
	}
	if !strings.Contains(err.Error(), "does not exist in the current file") {
		t.Fatalf("error = %q, want missing-from-file guidance", err)
	}
	if strings.Contains(err.Error(), "read") && !strings.Contains(err.Error(), "since it was last read") {
		t.Fatalf("error = %q, hint must not blame read history", err)
	}
	assertApplyPatchFile(t, path, content)
}

func TestApplyPatchHunkFailurePointsAtClosestFileLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "first line\nmiddle line\nlast line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// The hunk shares no complete line with the file, but its first line is
	// close to "middle line" (a typo'd variant). The error must point at the
	// closest file line with both the actual and expected text so the model
	// can rebuild the hunk without a blind re-read.
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-muddle line\n" +
		"+replacement\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk not found with closest-line hint")
	}
	msg := err.Error()
	if !strings.Contains(msg, "hunk not found (1/1)") {
		t.Fatalf("error = %q, want hunk not found", msg)
	}
	if !strings.Contains(msg, "closest file line is 2") {
		t.Fatalf("error = %q, want closest line 2 (middle line)", msg)
	}
	if !strings.Contains(msg, "middle line") || !strings.Contains(msg, "muddle line") {
		t.Fatalf("error = %q, want both the file line and the expected line", msg)
	}
	assertApplyPatchFile(t, path, content)
}

func TestApplyPatchHunkFailureExplainsWhitespaceOnlyContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "first line\nmiddle line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		" \t\n" +
		"-missing line\n" +
		"+replacement\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk mismatch")
	}
	msg := err.Error()
	if !strings.Contains(msg, "whitespace-only context lines are literal source lines") {
		t.Fatalf("error = %q, want whitespace-placeholder guidance", msg)
	}
	assertApplyPatchFile(t, path, content)
}

// TestApplyPatchEOFClosestMatchOnlyScansTail guards the EOF-hunk window: when
// the closest similar line sits at the top of the file but the hunk is
// End-Of-File, the suggestion must not point at the unreachable top line.
// The EOF window starts at the suffix position, so only the tail lines are
// candidates and the generic missing-line hint applies when nothing there is
// close enough.
func TestApplyPatchEOFClosestMatchOnlyScansTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tail.md")
	content := "similar signature\n\n\n\ntail marker line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// An End-Of-File hunk whose expected line is typo-close to the file's
	// FIRST line. Without the EOF window the closest scan would point at
	// line 1, which a retry cannot use for a tail hunk.
	patch := "*** Begin Patch\n" +
		"*** Update File: tail.md\n" +
		"@@\n" +
		"-similr signature\n" +
		"+replacement\n" +
		"*** End of File\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk not found")
	}
	msg := err.Error()
	if strings.Contains(msg, "closest file line is 1") {
		t.Fatalf("error = %q, EOF hunk must not point at line 1 (outside its tail window)", msg)
	}
	// The tail window holds no close line (blank lines normalize to empty,
	// "tail marker line" is unrelated), so the generic missing-line hint
	// applies instead of a wrong suggestion.
	if !strings.Contains(msg, "expected line does not exist") {
		t.Fatalf("error = %q, want the generic missing-line hint for the EOF window", msg)
	}
	assertApplyPatchFile(t, path, content)
}

// TestApplyPatchAmbiguousCandidatesSkipClosest guards that an ambiguous
// tolerant match (multiple equivalent normalized candidates) does not also
// emit a single closest-match suggestion: the ambiguity note is already
// present, and a "100% similar" line would masquerade as the unique answer.
func TestApplyPatchAmbiguousCandidatesSkipClosest(t *testing.T) {
	// Direct unit test of the diagnostic builder: with multiple tolerant
	// candidates the hunk is ambiguous, so the single closest-match line must
	// not be emitted (it would masquerade as the unique suggestion).
	fileLines := []string{"foo bar", "foobar"}
	err := applyPatchHunkNotFoundErrorWithHints(fileLines, []string{"foo bar", "unrelated"}, 0, 0, 1, false, []int{0, 1}, nil, false)
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous") {
		t.Fatalf("error = %q, want the ambiguity note", msg)
	}
	if strings.Contains(msg, "closest file line") {
		t.Fatalf("error = %q, must not emit a closest match when candidates are ambiguous", msg)
	}

	// Sanity: a single candidate is unambiguous and the closest path still
	// works when the hunk line is genuinely close to a file line.
	err = applyPatchHunkNotFoundErrorWithHints(fileLines, []string{"foobqr"}, 0, 0, 1, false, []int{0}, nil, false)
	msg = err.Error()
	if !strings.Contains(msg, "closest file line") {
		t.Fatalf("error = %q, want closest-match for a unique candidate", msg)
	}
}

// TestApplyPatchClosestMatchBudgetDegradesToGenericHint guards the failure
// path against pathological inputs: a large file with long lines must not
// burn unbounded CPU in the closest-line scan. Past the work budget the
// suggestion degrades to the generic missing-line hint instead of stalling.
func TestApplyPatchClosestMatchBudgetDegradesToGenericHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.md")
	var b strings.Builder
	long := strings.Repeat("x", 4000) // long line: 4000 runes
	for range 500 {
		b.WriteString(long)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// The hunk's expected line is a typo of the long line; the closest scan
	// would need a 4000×4000 Levenshtein per line (500 lines). The budget
	// must stop it and fall back to the generic hint rather than hang.
	patch := "*** Begin Patch\n" +
		"*** Update File: big.md\n" +
		"@@\n" +
		"-" + strings.Repeat("y", 3990) + "\n" +
		"+replacement\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk not found")
	}
	msg := err.Error()
	if !strings.Contains(msg, "hunk not found (1/1)") {
		t.Fatalf("error = %q, want hunk not found", msg)
	}
	assertApplyPatchFile(t, path, b.String())
}

func TestApplyPatchHunkFailurePinpointsDivergingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "first line\nmiddle line\nlast line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// The hunk's first two context lines exist, but the third expected line
	// is wrong ("lastt line" vs "last line") — a content-level mismatch the
	// tolerance cannot bridge. The error must pinpoint line 3 as the first
	// diverging line with both the expected and found text.
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		" first line\n" +
		" middle line\n" +
		"-lastt line\n" +
		"+new last line\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk mismatch")
	}
	if !strings.Contains(err.Error(), "the first 2 line(s) of the hunk match at line 1") {
		t.Fatalf("error = %q, want pinpointed matching prefix", err)
	}
	if !strings.Contains(err.Error(), "expected \"lastt line\"") || !strings.Contains(err.Error(), "found \"last line\"") {
		t.Fatalf("error = %q, want expected/found divergence detail", err)
	}
	assertApplyPatchFile(t, path, content)
}

func TestApplyPatchHunkFailureLabelsTruncatedExpectedLineAsPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	if err := os.WriteFile(path, []byte("current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected := strings.Repeat("x", 160)
	patch := "*** Begin Patch\n*** Update File: long.txt\n@@\n-" + expected + "\n+replacement\n*** End Patch"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk mismatch")
	}
	if !strings.Contains(out, "first expected line prefix: \"") {
		t.Fatalf("output must label a truncated diagnostic as a prefix: %q", out)
	}
	if strings.Contains(out, "expected complete line") || strings.Contains(out, expected) {
		t.Fatalf("failure must not claim or print the unavailable complete line: %q", out)
	}
	if strings.Contains(out, "Changes under \"Applied patch\"") {
		t.Fatalf("all-failed output must not refer to a nonexistent committed section: %q", out)
	}
	assertApplyPatchFile(t, path, "current\n")
}

// Tolerance is one normalizer (normalizePatchTolerantLine) rather than a layer
// of the exact cascade, so a line that differs in trailing whitespace *and*
// punctuation still matches.
func TestApplyPatchTolerantMatchCoversWhitespaceAndPunctuation(t *testing.T) {
	file := "alpha\nit’s here\nbeta\n"
	hunks := updateHunksFor(t, "*** Begin Patch\n*** Update File: f\n@@\n-alpha\n it's here  \n+new\n*** End Patch")
	got, _, _, _, err := applyApplyPatchHunks(context.Background(), file, hunks)
	if err != nil {
		t.Fatalf("applyApplyPatchHunks error = %v, want tolerance to cover trailing whitespace plus punctuation", err)
	}
	if !strings.Contains(got, "new") {
		t.Fatalf("result = %q, want the replacement applied", got)
	}
}

func TestApplyPatchTolerantMatchCoversFullWidthPunctuation(t *testing.T) {
	file := "alpha\n注意：这里\nbeta\n"
	hunks := updateHunksFor(t, "*** Begin Patch\n*** Update File: f\n@@\n alpha\n 注意: 这里\n-beta\n+new\n*** End Patch")
	got, _, _, _, err := applyApplyPatchHunks(context.Background(), file, hunks)
	if err != nil {
		t.Fatalf("applyApplyPatchHunks error = %v, want full-width punctuation tolerance", err)
	}
	if !strings.Contains(got, "注意：这里") {
		t.Fatalf("result = %q, want the file's original full-width text preserved", got)
	}
}

// A tolerant match that lands in several places must be rejected instead of
// silently taking the first one: the tolerant path runs through
// findUniqueApplyPatchSequence, which is what makes ambiguity visible.
func TestApplyPatchTolerantMatchRejectsAmbiguousCandidates(t *testing.T) {
	file := "it’s here\nx\nit’s here\nx\nit’s here\n"
	hunks := updateHunksFor(t, "*** Begin Patch\n*** Update File: f\n@@\n it's here\n+y\n*** End Patch")
	_, _, _, _, err := applyApplyPatchHunks(context.Background(), file, hunks)
	if err == nil {
		t.Fatal("applyApplyPatchHunks error = nil, want the ambiguous tolerant match rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous") || !strings.Contains(msg, "1, 3, 5") {
		t.Fatalf("err = %q, want an ambiguity notice naming all three lines", msg)
	}
}

func TestApplyPatchFuzzyMatchReplacesUniqueNearMatch(t *testing.T) {
	file := "before anchor\nfunc retryCount := 3 // number of attempts left\nafter anchor\n"
	hunks := updateHunksFor(t, "*** Begin Patch\n*** Update File: f\n@@\n before anchor\n-func retryCount := 2 // number of attempts left\n+replacement\n after anchor\n*** End Patch")
	got, _, fuzzy, _, err := applyApplyPatchHunks(context.Background(), file, hunks)
	if err != nil {
		t.Fatalf("applyApplyPatchHunks error = %v, want unique fuzzy match to apply", err)
	}
	if fuzzy != 1 {
		t.Fatalf("fuzzy hunk count = %d, want 1", fuzzy)
	}
	if got != "before anchor\nreplacement\nafter anchor\n" {
		t.Fatalf("result = %q, want current context preserved around replacement", got)
	}
}

func TestApplyPatchFuzzyMatchRejectsAmbiguousNearMatches(t *testing.T) {
	file := "before anchor\nfunc retryCount := 3 // number of attempts left\nafter anchor\nbefore anchor\nfunc retryCount := 3 // number of attempts left\nafter anchor\n"
	hunks := updateHunksFor(t, "*** Begin Patch\n*** Update File: f\n@@\n before anchor\n-func retryCount := 2 // number of attempts left\n+replacement\n after anchor\n*** End Patch")
	_, _, _, _, err := applyApplyPatchHunks(context.Background(), file, hunks)
	if err == nil {
		t.Fatal("applyApplyPatchHunks error = nil, want ambiguous fuzzy match rejected")
	}
	if !strings.Contains(err.Error(), "safe fuzzy matching is ambiguous at lines") {
		t.Fatalf("error = %q, want fuzzy ambiguity guidance", err)
	}
}

func TestApplyPatchHunkMismatchLineRespectsSearchStart(t *testing.T) {
	fileLines := []string{"alpha", "beta", "gamma", "delta"}
	// The full sequence occurs at index 0, but the hunk's legal window starts
	// at index 1: the suggestion must not point before it.
	if line, matched := applyPatchHunkMismatchLine(fileLines, []string{"alpha", "beta"}, 1, false); line != -1 || matched != 0 {
		t.Fatalf("got line=%d matched=%d, want no in-window prefix match", line, matched)
	}
	// A partial in-window match is still pinpointed.
	if line, matched := applyPatchHunkMismatchLine(fileLines, []string{"gamma", "deltax"}, 2, false); line != 2 || matched != 1 {
		t.Fatalf("got line=%d matched=%d, want the in-window prefix match at index 2", line, matched)
	}
}

func TestApplyPatchHunkMismatchLineRespectsEOFWindow(t *testing.T) {
	fileLines := []string{"alpha", "beta", "gamma", "delta"}
	// EOF window for a 2-line suffix starts at len(fileLines)-2 = 2. The
	// sequence's first line "alpha" matches at index 0, outside that window,
	// so no in-window prefix match exists and the diagnostic must stay
	// silent — pointing at line 1 would mislead a retry that must match the
	// file tail.
	if line, matched := applyPatchHunkMismatchLine(fileLines, []string{"alpha", "x"}, 0, true); line != -1 || matched != 0 {
		t.Fatalf("got line=%d matched=%d, want no in-window prefix match for EOF hunk", line, matched)
	}
	// A partial match inside the EOF tail window is still pinpointed.
	if line, matched := applyPatchHunkMismatchLine(fileLines, []string{"gamma", "deltax"}, 0, true); line != 2 || matched != 1 {
		t.Fatalf("got line=%d matched=%d, want the in-window prefix match at index 2", line, matched)
	}
}

// A multi-line hunk that breaks mid-sequence must pinpoint the first
// diverging line by code point (same visibility as edit), and a whole-line
// drift must be reported as a line-count difference instead of leaving the
// model staring at two position-shifted lines.
func TestApplyPatchHunkMismatchErrorPinpointsDivergence(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "demo.txt", "a\nb\n")
	// Hunk context: the first line "a" matches at line 1, the second expected
	// line "c" does not exist right after it — so the hunk's first line
	// matches but the sequence breaks at the second line.
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(),
		applyPatchArgs(t, "*** Begin Patch\n*** Update File: "+path+"\n@@\n a\n c\n-d\n+e\n*** End Patch\n"))
	if err == nil {
		t.Fatal("Execute error = nil, want hunk-not-found failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "hunk not found (1/1)") {
		t.Fatalf("error = %q, want hunk not found", msg)
	}
	// The hunk's first line matches; the divergence is reported with the
	// exact rune and the line-count difference.
	if !strings.Contains(msg, "first mismatch at rune") || !strings.Contains(msg, "U+0063") {
		t.Fatalf("error = %q, want rune-level first-mismatch with U+0063", msg)
	}
	if !strings.Contains(msg, "line-count difference") {
		t.Fatalf("error = %q, want a line-count difference notice", msg)
	}
}

// Orphan variation selectors in a hunk line must be escaped in the expected
// line description, so an invisible U+FE0F renders as \ufe0f instead of
// appearing identical to the clean file line.
func TestApplyPatchExpectedLineEscapesVariationSelector(t *testing.T) {
	got := applyPatchExpectedLineDescription([]string{"\ufe0f0"})
	if !strings.Contains(got, `\ufe0f`) {
		t.Fatalf("description = %q, want escaped \\ufe0f", got)
	}
	if strings.Contains(got, "\ufe0f") {
		t.Fatalf("description = %q, must not contain a raw variation selector", got)
	}
}

func TestApplyPatchSubstringLineRespectsEOFWindow(t *testing.T) {
	fileLines := []string{"football team", "filler", "tail"}
	// "football" is a substring of the file's first line, but the EOF window
	// for a single-line hunk starts at the last line, so the diagnostic must
	// stay silent — the complete line it would name is not the file tail.
	if line := findApplyPatchSubstringLine(fileLines, []string{"football"}, 0, true); line != -1 {
		t.Fatalf("got line=%d, want no in-window substring hit for EOF hunk", line)
	}
	// A substring hit inside the EOF tail window is still reported.
	fileLines = []string{"football team", "filler", "tail football"}
	if line := findApplyPatchSubstringLine(fileLines, []string{"football"}, 0, true); line != 2 {
		t.Fatalf("got line=%d, want the in-window substring hit at index 2", line)
	}
}

func TestApplyPatchHunkClosestLineRespectsSearchStartOnEOF(t *testing.T) {
	fileLines := []string{"filler one", "filler two", "needle here", "unrelated line"}
	// EOF window for a 2-line suffix with searchStart 3: the similar line at
	// index 2 sits before the window and must not be suggested.
	if line, _ := applyPatchHunkClosestLine(fileLines, []string{"needle here", "tail"}, 3, true); line < 3 {
		t.Fatalf("got line=%d, want a suggestion at or after index 3", line)
	}
}

// The fuzzy layer is the only one that writes the model's line over a file
// line the model quoted wrongly, so its refusals need regression coverage of
// their own: without it the guard can be loosened (or its threshold nudged)
// without a single test turning red.
//
// Two runes of drift on a short line clears the 0.9 similarity ratio (0.933
// here) while being a real value change — "100" is not "250". It must be
// refused, and the refusal must name the exact rune that differs so the model
// can fix the hunk without re-reading the whole file.
func TestApplyPatchFuzzyRejectsTwoRuneDrift(t *testing.T) {
	dir := t.TempDir()
	const actualLine = "limit := 100 // maximum retries"
	const claimedLine = "limit := 250 // maximum retries"
	file := "before anchor\n" + actualLine + "\nafter anchor\n"
	writeEditFixture(t, dir, "a.txt", file)
	patch := "*** Begin Patch\n" +
		"*** Update File: " + filepath.Join(dir, "a.txt") + "\n" +
		"@@\n" +
		" before anchor\n" +
		"-" + claimedLine + "\n" +
		"+limit := 250 // maximum retries, doubled\n" +
		" after anchor\n" +
		"*** End Patch\n"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want the two-rune drift refused")
	}
	for _, want := range []string{
		"hunk not found",
		"first mismatch at rune 10 (1-based): your line has U+0032, file has U+0031",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want substring %q", err, want)
		}
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "a.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != file {
		t.Fatalf("a.txt = %q, want unchanged %q", got, file)
	}
}

// A similarity ratio grows its allowance with line length, which is exactly
// backwards for this guard: five changed runes on a 67-rune line still scores
// 0.925 and would sail past a ratio-only check. The absolute rune-distance cap
// is what refuses it.
func TestApplyPatchFuzzyRejectsLongLineDrift(t *testing.T) {
	dir := t.TempDir()
	const actualLine = "const defaultRetryBudgetForBackgroundCompactionRuns = 12345 // tuned"
	const claimedLine = "const defaultRetryBudgetForBackgroundCompactionRuns = 67890 // tuned"
	file := "package config\n" + actualLine + "\nvar enabled = true\n"
	writeEditFixture(t, dir, "a.go", file)
	patch := "*** Begin Patch\n" +
		"*** Update File: " + filepath.Join(dir, "a.go") + "\n" +
		"@@\n" +
		" package config\n" +
		"-" + claimedLine + "\n" +
		"+const defaultRetryBudgetForBackgroundCompactionRuns = 99999 // tuned\n" +
		" var enabled = true\n" +
		"*** End Patch\n"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("Execute error = nil, want the five-rune drift refused")
	}
	if !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %q, want a hunk-not-found refusal", err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "a.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != file {
		t.Fatalf("a.go = %q, want unchanged %q", got, file)
	}
}

// Repeated whitespace is pure typesetting, so a hunk that re-spaces an aligned
// assignment must match in the tolerant layer — which splices the file's own
// bytes back for everything the hunk did not change — instead of falling
// through to the fuzzy layer, which would overwrite the file's alignment with
// the model's spacing.
func TestApplyPatchTolerantMatchFoldsRepeatedWhitespace(t *testing.T) {
	dir := t.TempDir()
	file := "var (\n\tcount   = 1\n\tenabled = true\n)\n"
	writeEditFixture(t, dir, "a.go", file)
	patch := "*** Begin Patch\n" +
		"*** Update File: " + filepath.Join(dir, "a.go") + "\n" +
		"@@\n" +
		" var (\n" +
		"-\tcount = 1\n" +
		"+\tcount = 2\n" +
		" \tenabled = true\n" +
		"*** End Patch\n"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("Execute error = %v, want the re-spaced hunk to match tolerantly", err)
	}
	if !strings.Contains(out, tolerantMatchNote) {
		t.Fatalf("result = %q, want the %s note", out, tolerantMatchNote)
	}
	if strings.Contains(out, "safe fuzzy matching") {
		t.Fatalf("result = %q, must not reach the fuzzy layer for a pure re-spacing", out)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "a.go"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	// The file's own alignment survives: only the value the hunk actually
	// changed comes from the model's line.
	if want := "var (\n\tcount   = 2\n\tenabled = true\n)\n"; string(got) != want {
		t.Fatalf("a.go = %q, want %q", got, want)
	}
}

// Distinctive context is a per-line property. Summing the context lines'
// lengths let "}" plus "});" clear the floor together, anchoring a fuzzy
// rewrite on boilerplate that repeats throughout a real file.
func TestApplyPatchFuzzyRejectsBoilerplateContext(t *testing.T) {
	dir := t.TempDir()
	file := "}\n\tretries := 3\n});\n"
	writeEditFixture(t, dir, "a.js", file)
	patch := "*** Begin Patch\n" +
		"*** Update File: " + filepath.Join(dir, "a.js") + "\n" +
		"@@\n" +
		" }\n" +
		"-\tretries := 2\n" +
		"+\tretries := 9\n" +
		" });\n" +
		"*** End Patch\n"

	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err == nil {
		t.Fatal("Execute error = nil, want the boilerplate-anchored fuzzy hunk refused")
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != file {
		t.Fatalf("a.js = %q, want unchanged %q", got, file)
	}
}
