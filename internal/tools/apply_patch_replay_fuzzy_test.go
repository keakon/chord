package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyPatchPartialPlanRebuildKeepsFuzzyCountSingle pins the fuzzy counter
// semantics of the tolerant plan builder: when a later group fails, the
// builder rolls the group back and replays the already-matched operations onto
// a clean virtual state. fuzzyHunks must be reset alongside
// punctuationHunks/cleanedInvisible on every replay — otherwise each rebuild
// doubles the fuzzy count that ends up in the result Note, telling the model
// fuzzy matching was used twice for one hunk.
func TestApplyPatchPartialPlanRebuildKeepsFuzzyCountSingle(t *testing.T) {
	dir := t.TempDir()
	writeEditFixture(t, dir, "a.txt", "before anchor\nfunc retryCount := 3 // number of attempts left\nafter anchor\n")
	writeEditFixture(t, dir, "b.txt", "first line\nsecond line\n")
	// File a.txt is updated through a unique fuzzy near-match (the claimed
	// removed line differs from the file's line in one rune of a long line,
	// well above the 0.9 similarity gate); file b.txt's hunk cannot match
	// anywhere and fails after the first file already succeeded, which is what
	// forces the partial-plan rebuild.
	patch := "*** Begin Patch\n" +
		"*** Update File: " + filepath.Join(dir, "a.txt") + "\n" +
		"@@\n" +
		" before anchor\n" +
		"-func retryCount := 2 // number of attempts left\n" +
		"+replacement\n" +
		" after anchor\n" +
		"*** Update File: " + filepath.Join(dir, "b.txt") + "\n" +
		"@@\n" +
		" missing context\n" +
		"-no such line\n" +
		"+replacement\n" +
		"*** End Patch\n"

	result, err := buildApplyPatchPlanWithOutcomes(context.Background(), patch, dir)
	if err != nil {
		t.Fatalf("build plan error = %v", err)
	}
	if !result.HasFailures() {
		t.Fatal("expected the second file's update to fail")
	}
	var fuzzy int
	for _, m := range result.Plan.Mutations {
		if strings.HasSuffix(m.SourcePath, "a.txt") {
			fuzzy += m.FuzzyHunks
		}
	}
	if fuzzy != 1 {
		t.Fatalf("fuzzy hunk count on the surviving file = %d, want 1: a rollback replay must rebuild the counter from zero, not double it", fuzzy)
	}
}

// TestApplyPatchFuzzyResultNoteShowsActualLine audits fuzzy replacements end to
// end: the tool result must quote the file's actual replaced line (not only
// the hunk's claimed line) so the model can see what a stale removed line
// really overwrote, and the file must hold the replacement after success.
func TestApplyPatchFuzzyResultNoteShowsActualLine(t *testing.T) {
	dir := t.TempDir()
	const actualLine = "func retryCount := 3 // number of attempts left"
	const claimedLine = "func retryCount := 2 // number of attempts left"
	writeEditFixture(t, dir, "a.txt", "before anchor\n"+actualLine+"\nafter anchor\n")
	patch := "*** Begin Patch\n" +
		"*** Update File: " + filepath.Join(dir, "a.txt") + "\n" +
		"@@\n" +
		" before anchor\n" +
		"-" + claimedLine + "\n" +
		"+replacement\n" +
		" after anchor\n" +
		"*** End Patch\n"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("Execute error = %v, want the fuzzy update to apply", err)
	}
	for _, want := range []string{
		"Note: used safe fuzzy matching for 1 hunk(s)",
		`Note: fuzzy hunk replaced the file's actual line "` + actualLine + `" with "replacement"; your hunk claimed "` + claimedLine + `"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("result = %q, want it to contain %q", out, want)
		}
	}
	content, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before anchor\nreplacement\nafter anchor\n" {
		t.Fatalf("a.txt = %q, want the replaced line written to disk", content)
	}
}
