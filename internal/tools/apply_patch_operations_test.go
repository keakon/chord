package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyPatchCodexMultiFileOperations(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("update.txt", "one\ntwo\n")
	write("delete.txt", "obsolete\n")

	patch := "*** Begin Patch\n" +
		"*** Update File: update.txt\n" +
		"@@\n-two\n+TWO\n" +
		"*** Add File: added.txt\n" +
		"+created\n" +
		"*** Delete File: delete.txt\n" +
		"*** End Patch"
	tool := ApplyPatchTool{BaseDir: dir}
	if _, err := tool.Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	assertFile := func(name, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	assertFile("update.txt", "one\nTWO\n")
	assertFile("added.txt", "created\n")
	if _, err := os.Stat(filepath.Join(dir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete.txt exists, err=%v", err)
	}
}

func TestApplyPatchUpdatesExistingEmptyFileWithPureInsertion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: empty.txt\n@@\n+hello\n*** End Patch"

	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("empty.txt = %q, want %q", got, "hello\\n")
	}
}

func TestApplyPatchRejectsContextOnlyUpdate(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@\n" +
		" ## 5. Related docs\n" +
		"*** End Patch"
	_, err := ParseApplyPatch(patch)
	if err == nil || !strings.Contains(err.Error(), "at least one added or removed line is required") || !strings.Contains(err.Error(), "whitespace-only lines are not omission placeholders") {
		t.Fatalf("err = %v, want context-only hunk rejection", err)
	}
}

func TestApplyPatchAnchorsSplitHunkHeaderContext(t *testing.T) {
	// Models trained on the Codex spelling emit the two-line header form:
	// `@@`, one section-context line, then the hunk's own `@@`. Chord starts a
	// new hunk at every `@@`, so the naive read leaves the first hunk with
	// context only — which used to fail the whole patch and cost the model
	// several recovery round trips. The lone context line is now carried onto
	// the next hunk as its anchor, matching Codex's "one change" reading.
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	original := "package x\n\nfunc TestDegraded(t *testing.T) {\n\tif true {\n\t}\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	split := "*** Begin Patch\n" +
		"*** Update File: " + path + "\n" +
		"@@\n" +
		" func TestDegraded(t *testing.T) {\n" +
		"@@\n" +
		" \t}\n" +
		" }\n" +
		"+\n" +
		"+func TestAdded(t *testing.T) {}\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, split))
	if err != nil {
		t.Fatalf("split @@ header not tolerated: %v (output %q)", err, out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "package x\n\nfunc TestDegraded(t *testing.T) {\n\tif true {\n\t}\n}\n\nfunc TestAdded(t *testing.T) {}\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyPatchRejectsContextOnlyHunkAtOperationEnd(t *testing.T) {
	// The guardrail that survives the split-header tolerance: a context-only
	// hunk with no following `@@` is a navigation placeholder, not an anchor,
	// and stays an error so the model cannot believe a no-op succeeded.
	trailing := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@\n" +
		" ## 5. Related docs\n" +
		"*** End Patch"
	if _, err := ParseApplyPatch(trailing); err == nil || !strings.Contains(err.Error(), "at least one added or removed line is required") {
		t.Fatalf("err = %v, want the trailing context-only hunk rejected", err)
	}

	twoLines := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@\n" +
		" ## 5. Related docs\n" +
		" ## 6. Appendix\n" +
		"@@\n" +
		" old\n" +
		"+new\n" +
		"*** End Patch"
	if _, err := ParseApplyPatch(twoLines); err == nil || !strings.Contains(err.Error(), "at least one added or removed line is required") {
		t.Fatalf("err = %v, want an ambiguous multi-line context block rejected", err)
	}

	inline := "*** Begin Patch\n" +
		"*** Update File: unchanged.md\n" +
		"@@ ## 5. Related docs\n" +
		" old\n" +
		"+new\n" +
		"*** End Patch"
	if _, err := ParseApplyPatch(inline); err != nil {
		t.Fatalf("single-line @@ header rejected: %v", err)
	}
}

func TestApplyPatchErrorUsesRelativePathHint(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "internal", "agent", "missing.go")
	patch := "*** Begin Patch\n" +
		"*** Update File: " + missing + "\n" +
		"@@\n" +
		"-old\n" +
		"+new\n" +
		"*** End Patch"
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatal("expected the missing source to fail")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error repeated the absolute working directory: %v", err)
	}
	if want := filepath.ToSlash(filepath.Join("internal", "agent", "missing.go")); !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want the relative hint %q", err, want)
	}
}

func TestApplyPatchReportsModeOnlyUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable mode bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "run.sh")
	content := "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Delete File: run.sh\n" +
		"*** Add File: run.sh\n" +
		"+#!/bin/sh\n" +
		"+echo ok\n" +
		"*** End Patch"
	plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != MutationUpdate {
		t.Fatalf("mutations = %#v, want one update", plan.Mutations)
	}
	if plan.Mutations[0].BeforeMode.Perm() != 0o755 || plan.Mutations[0].AfterMode.Perm() != 0o644 {
		t.Fatalf("mode change = %#o -> %#o, want 0755 -> 0644", plan.Mutations[0].BeforeMode.Perm(), plan.Mutations[0].AfterMode.Perm())
	}

	result, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatal(err)
	}
	if result != "Applied patch:\nM run.sh" {
		t.Fatalf("result = %q, want mode-only update to be reported", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("run.sh mode = %#o, want 0644", info.Mode().Perm())
	}
}

func TestApplyPatchPureInsertionWithoutContextAppendsToNonEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("existing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: existing.txt\n@@\n+ambiguous\n*** End Patch"

	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing\nambiguous\n" {
		t.Fatalf("existing.txt = %q, want appended line", got)
	}
}

func TestApplyPatchMove(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0755); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt\n@@\n-old\n+new\n*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt still exists, err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("new.txt = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "new.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := info.Mode().Perm(), os.FileMode(0755); got != want {
			t.Fatalf("new.txt mode = %04o, want %04o", got, want)
		}
	}
}

// TestApplyPatchReportsOnePathSpelling pins that the summary lines use the same
// path spelling as every other apply_patch message: the model resubmits what it
// is shown, so a report mixing relative hints with resolved absolute paths
// teaches two spellings for one file.
func TestApplyPatchReportsOnePathSpelling(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: sub/new.txt\n@@\n-old\n+new\n*** End Patch"
	result, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatal(err)
	}
	if result != "Applied patch:\nR old.txt -> sub/new.txt" {
		t.Fatalf("result = %q, want both sides of the move spelled relative to the session directory", result)
	}

	// Outside the session directory both the summary and the error hint fall
	// back to the same spelling instead of one going absolute.
	root := t.TempDir()
	t.Chdir(root)
	outside := filepath.Join(root, "outside.txt")
	mutation := PlannedMutation{Kind: MutationUpdate, SourcePath: outside}
	summary := applyPatchMutationSummary(mutation, filepath.Join(root, "session"))
	if want := "M " + applyPatchPathHint(outside, filepath.Join(root, "session")); summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	if summary != "M outside.txt" {
		t.Fatalf("summary = %q, want the working-directory-relative spelling", summary)
	}
}

func TestApplyPatchCodexSequentialFileOperations(t *testing.T) {
	t.Run("move then recreate source", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "plan.md")
		archive := filepath.Join(dir, "archive", "plan.md")
		if err := os.WriteFile(path, []byte("# old\nbody\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: plan.md\n*** Move to: archive/plan.md\n@@\n-# old\n+# archived\n" +
			"*** Add File: plan.md\n+# new\n+replacement\n" +
			"*** End Patch"
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Mutations) != 2 {
			t.Fatalf("mutations = %#v, want source update plus archive add", plan.Mutations)
		}
		byPath := make(map[string]PlannedMutation, len(plan.Mutations))
		for _, mutation := range plan.Mutations {
			byPath[mutation.TargetPath] = mutation
		}
		if mutation := byPath[path]; mutation.Kind != MutationUpdate || string(mutation.BeforeBytes) != "# old\nbody\n" || string(mutation.AfterBytes) != "# new\nreplacement\n" || mutation.AfterMode.Perm() != 0o644 {
			t.Fatalf("source mutation = %#v", mutation)
		}
		if mutation := byPath[archive]; mutation.Kind != MutationAdd || string(mutation.AfterBytes) != "# archived\nbody\n" || mutation.AfterMode.Perm() != 0o600 {
			t.Fatalf("archive mutation = %#v", mutation)
		}
		if err := CommitMutationPlan(plan); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "# new\nreplacement\n")
		assertApplyPatchFile(t, archive, "# archived\nbody\n")
	})

	t.Run("add rejects existing file atomically", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("original\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Add File: file.txt\n+middle\n" +
			"*** Update File: file.txt\n-middle\n+final\n" +
			"*** End Patch"
		if _, err := BuildApplyPatchPlan(context.Background(), patch, dir); err == nil || !strings.Contains(err.Error(), "cannot add file that already exists") {
			t.Fatalf("error = %v, want existing-file rejection", err)
		}
		assertApplyPatchFile(t, path, "original\n")
	})

	t.Run("update then delete", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: file.txt\n@@\n-old\n+new\n" +
			"*** Delete File: file.txt\n" +
			"*** End Patch"
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != MutationDelete || string(plan.Mutations[0].BeforeBytes) != "old\n" {
			t.Fatalf("mutations = %#v, want deletion of original file", plan.Mutations)
		}
		if err := CommitMutationPlan(plan); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("file still exists, err=%v", err)
		}
	})

	t.Run("delete then add", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("old\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Delete File: file.txt\n" +
			"*** Add File: file.txt\n+new\n" +
			"*** End Patch"
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != MutationUpdate || string(plan.Mutations[0].AfterBytes) != "new\n" || plan.Mutations[0].AfterMode.Perm() != 0o644 {
			t.Fatalf("mutations = %#v, want recreated 0644 file", plan.Mutations)
		}
		if err := CommitMutationPlan(plan); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "new\n")
	})
}

func TestApplyPatchCodexSequentialMoves(t *testing.T) {
	t.Run("move then update destination", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: old.txt\n*** Move to: middle.txt\n@@\n-old\n+middle\n" +
			"*** Update File: middle.txt\n@@\n-middle\n+final\n" +
			"*** End Patch"
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != MutationMove || plan.Mutations[0].SourcePath != filepath.Join(dir, "old.txt") || plan.Mutations[0].TargetPath != filepath.Join(dir, "middle.txt") || string(plan.Mutations[0].AfterBytes) != "final\n" {
			t.Fatalf("mutations = %#v, want one folded move", plan.Mutations)
		}
		if err := CommitMutationPlan(plan); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, filepath.Join(dir, "middle.txt"), "final\n")
	})

	t.Run("move chain folds original to final", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("old\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: old.txt\n*** Move to: middle.txt\n@@\n-old\n+middle\n" +
			"*** Update File: middle.txt\n*** Move to: final.txt\n@@\n-middle\n+final\n" +
			"*** End Patch"
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != MutationMove || plan.Mutations[0].SourcePath != filepath.Join(dir, "old.txt") || plan.Mutations[0].TargetPath != filepath.Join(dir, "final.txt") {
			t.Fatalf("mutations = %#v, want old-to-final move", plan.Mutations)
		}
		if err := CommitMutationPlan(plan); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, filepath.Join(dir, "final.txt"), "final\n")
		if _, err := os.Stat(filepath.Join(dir, "middle.txt")); !os.IsNotExist(err) {
			t.Fatalf("middle file exists, err=%v", err)
		}
	})

	t.Run("move overwrites target and keeps source mode", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "old.txt")
		target := filepath.Join(dir, "target.txt")
		if err := os.WriteFile(source, []byte("old\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: target.txt\n@@\n-old\n+new\n*** End Patch"
		plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != MutationMove || !plan.Mutations[0].TargetBeforeExists || plan.Mutations[0].AfterMode.Perm() != 0o700 {
			t.Fatalf("mutations = %#v, want overwrite move with source mode", plan.Mutations)
		}
		if err := CommitMutationPlan(plan); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, target, "new\n")
		if runtime.GOOS != "windows" {
			info, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Fatalf("target mode = %04o, want source mode 0700", got)
			}
		}
	})
}

func TestApplyPatchKeepsStarContextLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("intro\n*** heading\nold\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: notes.md\n" +
		"@@\n intro\n *** heading\n-old\n+new\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "intro\n*** heading\nnew\n" {
		t.Fatalf("notes.md = %q, want the *** context line kept in place", got)
	}
}

func TestApplyPatchEndOfFileMarkerStillPinsTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.txt")
	if err := os.WriteFile(path, []byte("dup\nmid\ndup\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: dup.txt\n" +
		"@@\n-dup\n+tail\n" +
		"*** End of File\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dup\nmid\ntail\n" {
		t.Fatalf("dup.txt = %q, want only the trailing dup replaced", got)
	}
}

func TestApplyPatchLiteralEndOfFileContextLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(path, []byte("alpha\n*** End of File\nomega\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: marker.txt\n" +
		"@@\n alpha\n *** End of File\n-omega\n+OMEGA\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\n*** End of File\nOMEGA\n" {
		t.Fatalf("marker.txt = %q, want the literal marker line kept as context", got)
	}
}

func TestApplyPatchRepeatedUpdatesAreAppliedInOperationOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("first\nmiddle\nlast\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: same.txt\n@@\n-last\n+LAST\n" +
		"*** Update File: same.txt\n@@\n-first\n+FIRST\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "FIRST\nmiddle\nLAST\n" {
		t.Fatalf("file = %q, %v; want both repeated updates", got, err)
	}
}

func TestApplyPatchRepeatedUpdateCanDependOnEarlierUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: same.txt\n@@\n-before\n+middle\n" +
		"*** Update File: same.txt\n@@\n-middle\n+after\n" +
		"*** End Patch"
	plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Mutations) != 1 {
		t.Fatalf("mutations = %#v, want one file-level mutation", plan.Mutations)
	}
	mutation := plan.Mutations[0]
	if string(mutation.BeforeBytes) != "before\n" || string(mutation.AfterBytes) != "after\n" || mutation.Added != 1 || mutation.Removed != 1 {
		t.Fatalf("mutation = %#v, want original-to-final update", mutation)
	}
	if err := CommitMutationPlan(plan); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("file = %q, %v; want dependent update result", got, err)
	}
}

func TestApplyPatchInteriorBlankHunkLineStaysContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.txt")
	if err := os.WriteFile(path, []byte("one\n\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: d.txt\n@@\n one\n\n-two\n+TWO\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "one\n\nTWO\n" {
		t.Fatalf("d.txt = %q, %v; want blank context preserved", got, err)
	}
}

func TestApplyPatchRenameOnlyUpdate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n*** Move to: new.txt\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt still exists, err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil || string(got) != "keep\n" {
		t.Fatalf("new.txt = %q, %v; want keep", got, err)
	}
}

func TestApplyPatchRenameOnlyPreservesMixedEOL(t *testing.T) {
	dir := t.TempDir()
	mixed := []byte("a\r\nb\nc\r\n")
	os.WriteFile(filepath.Join(dir, "old.txt"), mixed, 0644)
	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n*** Move to: new.txt\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if !bytes.Equal(got, mixed) {
		t.Fatalf("rename-only changed bytes: got %q, want %q", got, mixed)
	}
}

func TestApplyPatchRenameOnlyWorksForBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	bin := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0xFF, 0xFE}
	os.WriteFile(filepath.Join(dir, "icon.png"), bin, 0644)
	patch := "*** Begin Patch\n" +
		"*** Update File: icon.png\n*** Move to: logo.png\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "logo.png"))
	if !bytes.Equal(got, bin) {
		t.Fatalf("rename-only changed binary bytes: got %x, want %x", got, bin)
	}
}

// TestApplyPatchSingleFileMultiHunkPriorHunkWarning verifies that when a file
// has multiple hunks and a later hunk fails, the error notes which earlier
// hunks already matched in memory and directs the model to rebuild the complete
// atomic file operation.
func TestApplyPatchSingleFileMultiHunkPriorHunkWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: multi.txt\n" +
		"@@\n-alpha\n+ALPHA\n" +
		"@@\n-missing\n+NEVER\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v, want hunk-match failure", err)
	}
	if !strings.Contains(err.Error(), "hunks 1..1 matched successfully in memory but were not applied") {
		t.Fatalf("err = %v, want prior-hunks matched note", err)
	}
	if !strings.Contains(err.Error(), "keep all hunks 1..2 together when rebuilding this operation") || strings.Contains(err.Error(), "only hunks") {
		t.Fatalf("err = %v, want whole-operation rebuild guidance", err)
	}
	// The file must be unchanged because the whole file group was discarded.
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("multi.txt = %q, %v; want unchanged", got, readErr)
	}
	// The failure should name the failed file so the model can rebuild the
	// whole operation without losing the earlier matched hunk.
	if !strings.Contains(out, "- multi.txt:") {
		t.Fatalf("failure must name the failed file: %q", out)
	}
	if strings.Contains(out, "+ALPHA") || strings.Contains(out, "+NEVER") {
		t.Fatalf("failure output must not echo hunk content: %q", out)
	}
}

// TestApplyPatchRetryAfterFix walks the recovery loop the failure message
// tells the model to follow: a multi-hunk file fails on its last bare @@ hunk,
// the model rebuilds the operation from its own submitted patch with only that
// hunk corrected, and resubmitting must then succeed against the untouched
// file.
func TestApplyPatchRetryAfterFix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: multi.txt\n" +
		"@@\n-alpha\n+ALPHA\n" +
		"@@\n-gamma\n+GAMMA\n" +
		"@@\n-missing\n+NEVER\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatalf("out = %q, want hunk-match failure", out)
	}
	if !strings.Contains(out, "- multi.txt:") || !strings.Contains(out, "keep all hunks 1..3 together when rebuilding this operation") {
		t.Fatalf("failure must point at the failed group: %q", out)
	}
	fixed := strings.ReplaceAll(patch, "-missing", "-delta")
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, fixed)); err != nil {
		t.Fatalf("corrected patch must apply, got %v:\n%s", err, fixed)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "ALPHA\nbeta\nGAMMA\nNEVER\n" {
		t.Fatalf("multi.txt = %q, %v; want all three hunks applied", got, readErr)
	}
}
