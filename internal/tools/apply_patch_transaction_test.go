package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestApplyPatchSequentialPartialFailureAppliesIndependentOps(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "plan.md")
	archive := filepath.Join(dir, "archive", "plan.md")
	if err := os.WriteFile(source, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: plan.md\n*** Move to: archive/plan.md\n@@\n-old\n+archived\n" +
		"*** Add File: plan.md\n+new\n" +
		"*** Update File: archive/plan.md\n@@\n-missing\n+never\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v, want late hunk failure", err)
	}
	// The move + add are independent of the failing update on archive/plan.md
	// (the failure is a content mismatch, not a dependency on a failed op), so
	// they must be committed.
	assertApplyPatchFile(t, source, "new\n")
	assertApplyPatchFile(t, archive, "archived\n")
	// The failed op and only the failed op must be reported as unapplied.
	if !strings.Contains(out, "\n\nNot applied:") || !strings.Contains(out, "- archive/plan.md:") {
		t.Fatalf("output must name the unapplied operation group: %q", out)
	}
	if strings.Contains(out, "*** Update File:") || strings.Contains(out, "*** Move to:") || strings.Contains(out, "*** Add File:") {
		t.Fatalf("failure output must not echo patch operations: %q", out)
	}
}

func TestRollbackMutationsRestoresFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX file modes")
	}
	dir := t.TempDir()
	deleted := filepath.Join(dir, "deleted.sh")
	source := filepath.Join(dir, "source.sh")
	target := filepath.Join(dir, "target.sh")

	if err := os.WriteFile(target, []byte("replacement\n"), 0755); err != nil {
		t.Fatal(err)
	}
	committed := []PlannedMutation{
		{
			Kind:        MutationDelete,
			SourcePath:  deleted,
			BeforeBytes: []byte("deleted original\n"),
			BeforeMode:  0700,
		},
		{
			Kind:               MutationMove,
			SourcePath:         source,
			TargetPath:         target,
			BeforeBytes:        []byte("source original\n"),
			BeforeMode:         0755,
			TargetBeforeExists: true,
			TargetBeforeBytes:  []byte("target original\n"),
			TargetBeforeMode:   0600,
		},
	}
	if err := rollbackMutations(committed); err != nil {
		t.Fatal(err)
	}

	assertContentAndMode := func(path, wantContent string, wantMode os.FileMode) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != wantContent {
			t.Fatalf("%s = %q, want %q", path, got, wantContent)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != wantMode {
			t.Fatalf("%s mode = %04o, want %04o", path, gotMode, wantMode)
		}
	}
	assertContentAndMode(deleted, "deleted original\n", 0700)
	assertContentAndMode(source, "source original\n", 0755)
	assertContentAndMode(target, "target original\n", 0600)
}

func TestCommitMutationPlanRejectsModeDrift(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX file modes")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(path, []byte("before\n"), 0755); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: script.sh\n@@\n-before\n+after\n*** End Patch"
	plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CommitMutationPlan(plan); err == nil || !strings.Contains(err.Error(), "source changed after planning") {
		t.Fatalf("err = %v, want mode-drift rejection", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before\n" {
		t.Fatalf("script.sh = %q, want unchanged content", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode, wantMode := info.Mode().Perm(), os.FileMode(0700); gotMode != wantMode {
		t.Fatalf("script.sh mode = %04o, want externally changed mode %04o", gotMode, wantMode)
	}
}

func TestApplyPatchFailedMoveRestoresTarget(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "source")
	targetDir := filepath.Join(dir, "target")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "old.txt")
	target := filepath.Join(targetDir, "new.txt")
	if err := os.WriteFile(source, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: source/old.txt\n*** Move to: target/new.txt\n@@\n-old\n+new\n*** End Patch"
	plan, err := BuildApplyPatchPlan(context.Background(), patch, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(sourceDir, 0755)

	err = CommitMutationPlan(plan)
	if err == nil {
		t.Skip("filesystem permits deletion from a read-only directory")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		got, _ := os.ReadFile(target)
		t.Fatalf("failed move left target behind: content=%q, commit error=%v", got, err)
	}
	got, readErr := os.ReadFile(source)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old\n" {
		t.Fatalf("source = %q, want original content", got)
	}
}

func TestApplyPatchPlanningFailureAppliesSuccessfulFilesAndReportsFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.txt"), []byte("good\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: good.txt\n@@\n-good\n+changed\n*** Update File: missing.txt\n@@\n-missing\n+created\n*** Add File: should-not-exist.txt\n+no\n*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "partially applied") {
		t.Fatalf("err = %v, want partial-apply error", err)
	}
	if !ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, want committed-changes marker", err)
	}
	// good.txt was an independent successful operation and must be committed.
	got, readErr := os.ReadFile(filepath.Join(dir, "good.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "changed\n" {
		t.Fatalf("good.txt = %q, want %q (independent successful op must be applied)", got, "changed\n")
	}
	// Add File after a failed op on an unrelated file still applies because it
	// touches a different file; only ops touching the failed file cascade-fail.
	if _, statErr := os.Stat(filepath.Join(dir, "should-not-exist.txt")); statErr != nil {
		t.Fatalf("should-not-exist.txt should have been applied (independent of missing.txt failure), err=%v", statErr)
	}
	// The model-facing output must name the committed files and the unapplied
	// groups with their causes; it must not echo the submitted patch back.
	if !strings.Contains(out, "Applied") || !strings.Contains(out, "good.txt") || !strings.Contains(out, "should-not-exist.txt") {
		t.Fatalf("output missing applied file list: %q", out)
	}
	if !strings.Contains(out, "\n\nNot applied:") || !strings.Contains(out, "- missing.txt:") {
		t.Fatalf("output missing unapplied file group: %q", out)
	}
	if !strings.Contains(out, "resolve each cause above and resubmit only the failed file groups rebuilt from current file contents") {
		t.Fatalf("partial failure must carry resubmission guidance: %q", out)
	}
	for _, leak := range []string{"*** Begin Patch", "*** Update File:", "+created"} {
		if strings.Contains(out, leak) {
			t.Fatalf("failure output must not echo patch content (%s): %q", leak, out)
		}
	}
	if strings.Contains(out, "Retry only") || strings.Contains(out, "must be retried") {
		t.Fatalf("output must not present stale operations as directly retryable: %q", out)
	}
	if strings.Contains(out, "update missing.txt: update missing.txt:") {
		t.Fatalf("output must not duplicate the operation path prefix: %q", out)
	}
}

func TestApplyPatchRepeatedUpdateFailureDoesNotModifyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: same.txt\n@@\n-before\n+after\n" +
		"*** Update File: same.txt\n@@\n-missing\n+never\n" +
		"*** End Patch"
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v, want later update failure", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "before\n" {
		t.Fatalf("file = %q, %v; want unchanged", got, readErr)
	}
}

func TestApplyPatchRejectsHierarchicalTargets(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Add File: generated\n+parent\n" +
		"*** Add File: generated/child.txt\n+child\n" +
		"*** End Patch"

	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "overlapping operations") {
		t.Fatalf("err = %v, want hierarchical-operation rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "generated")); !os.IsNotExist(statErr) {
		t.Fatalf("hierarchical target was created, stat err = %v", statErr)
	}
}

func TestApplyPatchRejectsTargetsAliasingSameFile(t *testing.T) {
	for name, patch := range map[string]string{
		"two updates": "*** Begin Patch\n" +
			"*** Update File: source.txt\n@@\n-before\n+first\n" +
			"*** Update File: alias.txt\n@@\n-before\n+second\n" +
			"*** End Patch",
		"move onto alias": "*** Begin Patch\n" +
			"*** Update File: source.txt\n*** Move to: alias.txt\n@@\n-before\n+after\n" +
			"*** End Patch",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.txt")
			alias := filepath.Join(dir, "alias.txt")
			if err := os.WriteFile(source, []byte("before\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(source, alias); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}

			_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
			if err == nil || !strings.Contains(err.Error(), "overlapping operations") {
				t.Fatalf("err = %v, want aliased-operation rejection", err)
			}
			got, readErr := os.ReadFile(source)
			if readErr != nil || string(got) != "before\n" {
				t.Fatalf("source = %q, %v; want unchanged", got, readErr)
			}
		})
	}
}

func TestCommitMutationDoesNotOverwriteUnexpectedNewTarget(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.txt")
		if err := os.WriteFile(target, []byte("external\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		dirty, err := commitMutation(PlannedMutation{Kind: MutationAdd, SourcePath: target, TargetPath: target, AfterBytes: []byte("patch\n")})
		if err == nil {
			t.Fatal("commitMutation succeeded, want exclusive-create failure")
		}
		if dirty {
			t.Fatal("failed exclusive create reported a dirty target")
		}
		got, readErr := os.ReadFile(target)
		if readErr != nil || string(got) != "external\n" {
			t.Fatalf("target = %q, %v; want external content preserved", got, readErr)
		}
	})

	t.Run("move", func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "source.txt")
		target := filepath.Join(dir, "target.txt")
		if err := os.WriteFile(source, []byte("source\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("external\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		dirty, err := commitMutation(PlannedMutation{
			Kind: MutationMove, SourcePath: source, TargetPath: target,
			BeforeBytes: []byte("source\n"), BeforeMode: 0o644, AfterBytes: []byte("moved\n"),
		})
		if err == nil {
			t.Fatal("commitMutation succeeded, want exclusive-create failure")
		}
		if dirty {
			t.Fatal("failed exclusive move reported a dirty target")
		}
		if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "source\n" {
			t.Fatalf("source = %q, %v; want source preserved", got, readErr)
		}
		if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "external\n" {
			t.Fatalf("target = %q, %v; want external content preserved", got, readErr)
		}
	})
}

func TestRollbackFailedMutationRestoresHalfWrittenFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX file modes")
	}
	dir := t.TempDir()

	updatePath := filepath.Join(dir, "update.sh")
	original := "#!/bin/sh\necho ok\n"
	if err := os.WriteFile(updatePath, []byte(original), 0755); err != nil {
		t.Fatal(err)
	}
	// Simulate commitMutation dying after O_TRUNC: partial bytes, wrong mode.
	if err := os.WriteFile(updatePath, []byte("#!/bi"), 0644); err != nil {
		t.Fatal(err)
	}
	update := PlannedMutation{
		Kind:        MutationUpdate,
		SourcePath:  updatePath,
		TargetPath:  updatePath,
		BeforeBytes: []byte(original),
		BeforeMode:  0755,
	}
	if err := rollbackFailedMutation(update); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(updatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("update.sh = %q, want restored original", got)
	}
	info, err := os.Stat(updatePath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0755 {
		t.Fatalf("update.sh mode = %04o, want 0755", mode)
	}

	addPath := filepath.Join(dir, "orphan.txt")
	if err := os.WriteFile(addPath, []byte("part"), 0644); err != nil {
		t.Fatal(err)
	}
	add := PlannedMutation{Kind: MutationAdd, SourcePath: addPath, TargetPath: addPath}
	if err := rollbackFailedMutation(add); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(addPath); !os.IsNotExist(err) {
		t.Fatalf("orphan add file still exists, err=%v", err)
	}

	// Move and Delete failures leave no half-written state of their own.
	untouched := PlannedMutation{Kind: MutationDelete, SourcePath: filepath.Join(dir, "absent.txt")}
	if err := rollbackFailedMutation(untouched); err != nil {
		t.Fatal(err)
	}
}

// TestApplyPatchCrossFilePartialFailureCommitsIndependentOps verifies that
// when an operation on one file fails, all other independent file operations
// (before and after the failure) are still committed, and the operation
// reference contains only the unapplied operation.
func TestApplyPatchCrossFilePartialFailureCommitsIndependentOps(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("one.txt", "one\n")
	write("two.txt", "two\n")
	write("four.txt", "four\n")
	write("five.txt", "five\n")

	patch := "*** Begin Patch\n" +
		"*** Update File: one.txt\n@@\n-one\n+ONE\n" +
		"*** Update File: two.txt\n@@\n-two\n+TWO\n" +
		"*** Update File: three.txt\n@@\n-three\n+THREE\n" +
		"*** Update File: four.txt\n@@\n-four\n+FOUR\n" +
		"*** Update File: five.txt\n@@\n-five\n+FIVE\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "partially applied") || !strings.Contains(err.Error(), "1 file group not applied") {
		t.Fatalf("err = %v, want partial failure for one file", err)
	}
	if !ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, want committed-changes marker", err)
	}
	for _, name := range []string{"one.txt", "two.txt", "four.txt", "five.txt"} {
		got, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != strings.ToUpper(strings.TrimSuffix(name, ".txt"))+"\n" {
			t.Fatalf("%s = %q, want committed content", name, got)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "three.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("three.txt should not have been created, err=%v", statErr)
	}
	// Only the unapplied op on three.txt must be reported as failed.
	if !strings.Contains(out, "- three.txt:") {
		t.Fatalf("failure must name the unapplied op's file: %q", out)
	}
	for _, committed := range []string{"one.txt", "two.txt", "four.txt", "five.txt"} {
		if strings.Contains(out, "- "+committed+":") {
			t.Fatalf("failure must not report committed ops (%s): %q", committed, out)
		}
	}
}

// TestApplyPatchSameFileCascadeFailureIsAtomic verifies that when a file has
// two operations and the first succeeds but the second fails, the whole file
// is rolled back to its pre-group state and both operations are reported as
// failed.
func TestApplyPatchSameFileCascadeFailureIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("first\nlast\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: same.txt\n@@\n-first\n+FIRST\n" +
		"*** Update File: same.txt\n@@\n-missing\n+NEVER\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "1 file group not applied") {
		t.Fatalf("err = %v, want one-file failure", err)
	}
	if ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, do not want committed-changes marker", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "first\nlast\n" {
		t.Fatalf("same.txt = %q, %v; want unchanged because the whole file group was discarded", got, readErr)
	}
	// Both operations on the failed file must be reported: the first matched
	// in memory but was rolled back, so the model must redo it too.
	if !strings.Contains(out, "- same.txt:") || !strings.Contains(out, "keep it with the failed operation when revising the group") {
		t.Fatalf("failure must report the whole rolled-back group: %q", out)
	}
	if strings.Contains(out, "+FIRST") || strings.Contains(out, "+NEVER") {
		t.Fatalf("failure output must not echo hunk content: %q", out)
	}
}

// TestApplyPatchSameFileCascadeFailurePreservesPriorMove verifies that a
// successful move into a file does not get erased when a later failed update
// on that same file is rolled back: the moved-to file keeps its moved
// content, and only the failed update is retried.
func TestApplyPatchSameFileCascadeFailurePreservesPriorMove(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old.txt")
	archive := filepath.Join(dir, "archive.txt")
	if err := os.WriteFile(source, []byte("orig\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n*** Move to: archive.txt\n@@\n-orig\n+moved\n" +
		"*** Update File: archive.txt\n@@\n-missing\n+never\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "partially applied") {
		t.Fatalf("err = %v, want partial-apply error", err)
	}
	if !ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, want committed move marker", err)
	}
	// The successful move must be committed and must survive the failed
	// update's rollback.
	if _, statErr := os.Stat(source); !os.IsNotExist(statErr) {
		t.Fatalf("old.txt should have been moved away, err=%v", statErr)
	}
	got, readErr := os.ReadFile(archive)
	if readErr != nil || string(got) != "moved\n" {
		t.Fatalf("archive.txt = %q, %v; want the committed moved content", got, readErr)
	}
	// Failure must name only the failed update, not the applied move.
	if !strings.Contains(out, "- archive.txt:") {
		t.Fatalf("failure must name the failed update: %q", out)
	}
	if strings.Contains(out, "*** Update File:") || strings.Contains(out, "*** Move to:") {
		t.Fatalf("failure output must not echo patch operations: %q", out)
	}
}

func TestApplyPatchFailedSourceGroupRollsBackMoveTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old.txt")
	target := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(source, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n*** Move to: new.txt\n@@\n-before\n+after\n" +
		"*** Update File: old.txt\n@@\n-missing\n+never\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "1 file group not applied") {
		t.Fatalf("err = %v, want one-file failure", err)
	}
	if ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, move group should have been fully rolled back", err)
	}
	if got, readErr := os.ReadFile(source); readErr != nil || string(got) != "before\n" {
		t.Fatalf("old.txt = %q, %v; want original content", got, readErr)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("new.txt should not survive the failed source group, err=%v", statErr)
	}
	if !strings.Contains(out, "- old.txt:") {
		t.Fatalf("failure must name the whole rolled-back group: %q", out)
	}
	if strings.Contains(out, "*** Move to:") || strings.Contains(out, "*** Update File:") {
		t.Fatalf("failure output must not echo patch operations: %q", out)
	}
}

func TestApplyPatchFailedMoveBlocksLaterTargetOperation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old.txt")
	target := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(source, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n*** Move to: new.txt\n@@\n-missing\n+moved\n" +
		"*** Update File: new.txt\n@@\n-target\n+updated\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "2 file groups not applied") {
		t.Fatalf("err = %v, want failed move plus dependent target group", err)
	}
	if ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, dependent target update must not commit", err)
	}
	assertApplyPatchFile(t, source, "source\n")
	assertApplyPatchFile(t, target, "target\n")
	if !strings.Contains(out, "- old.txt:") || !strings.Contains(out, "- new.txt: skipped: a prior operation touching ") || !strings.Contains(out, "must remain with that dependency when revised") {
		t.Fatalf("failure must include the failed move and dependent target update: %q", out)
	}
	if strings.Contains(out, "*** Move to:") || strings.Contains(out, "*** Update File:") {
		t.Fatalf("failure output must not echo patch operations: %q", out)
	}
}

func TestApplyPatchLateSourceFailureRollsBackDependentTargetOperation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "old.txt")
	target := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(source, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: old.txt\n*** Move to: new.txt\n@@\n-source\n+moved\n" +
		"*** Update File: new.txt\n*** Move to: final.txt\n@@\n-moved\n+updated\n" +
		"*** Add File: new.txt\n+replacement\n" +
		"*** Update File: new.txt\n*** Move to: old.txt\n" +
		"*** Update File: old.txt\n@@\n-missing\n+never\n" +
		"*** End Patch"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "2 file groups not applied") {
		t.Fatalf("err = %v, want failed source group plus dependent target group", err)
	}
	if ErrorHasCommittedChanges(err) {
		t.Fatalf("err = %v, dependent target update must be rolled back", err)
	}
	assertApplyPatchFile(t, source, "source\n")
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("new.txt should be rolled back with its prerequisite move, err=%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "final.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("final.txt should be rolled back with the dependent move, err=%v", statErr)
	}
	if !strings.Contains(out, "- old.txt:") || !strings.Contains(out, "- new.txt: rolled back: an earlier operation group for ") || !strings.Contains(out, "keep both groups together when revising") {
		t.Fatalf("failure must include the failed source group and dependent groups: %q", out)
	}
	if strings.Contains(out, "*** Move to:") || strings.Contains(out, "+updated") {
		t.Fatalf("failure output must not echo patch operations: %q", out)
	}
}
