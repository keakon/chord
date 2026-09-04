package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func applyPatchArgs(t *testing.T, patch string) []byte {
	t.Helper()
	args, err := json.Marshal(ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	return args
}

func TestApplyPatchDisplayTargetsPreserveModelFacingPaths(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: src/old.go\n" +
		"*** Move to: src/new.go\n" +
		"@@\n-old\n+new\n" +
		"*** Add File: docs/new.md\n+# New\n" +
		"*** Delete File: tmp/old.txt\n" +
		"*** End Patch"
	targets, err := ApplyPatchDisplayTargets(applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("ApplyPatchDisplayTargets error: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %#v, want three operations", targets)
	}
	if targets[0].Kind != MutationUpdate || targets[0].SourcePath != "src/old.go" || targets[0].TargetPath != "src/new.go" {
		t.Fatalf("move target = %#v", targets[0])
	}
	if targets[0].Added != 1 || targets[0].Removed != 1 {
		t.Fatalf("move stats = +%d -%d, want +1 -1", targets[0].Added, targets[0].Removed)
	}
	if targets[1].Kind != MutationAdd || targets[1].SourcePath != "docs/new.md" {
		t.Fatalf("add target = %#v", targets[1])
	}
	if targets[1].Added != 1 || targets[1].Removed != 0 {
		t.Fatalf("add stats = +%d -%d, want +1 -0", targets[1].Added, targets[1].Removed)
	}
	if targets[2].Kind != MutationDelete || targets[2].SourcePath != "tmp/old.txt" {
		t.Fatalf("delete target = %#v", targets[2])
	}
}

func TestApplyPatchDisplayTargetsAggregateRepeatedUpdates(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: src/demo.go\n@@\n-old\n+middle\n" +
		"*** Update File: src/./demo.go\n@@\n-middle\n+new\n" +
		"*** End Patch"
	targets, err := ApplyPatchDisplayTargets(applyPatchArgs(t, patch))
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %#v, want one file-level target", targets)
	}
	target := targets[0]
	if target.SourcePath != "src/demo.go" || target.Added != 2 || target.Removed != 2 {
		t.Fatalf("target = %#v, want first model path with aggregate operation stats", target)
	}
}

func TestApplyPatchDisplayTargetsRejectsNonCanonicalArgs(t *testing.T) {
	raw := json.RawMessage(`{"path":"src/demo.go","patch":"@@\n-old\n+new\n"}`)
	if _, err := ApplyPatchDisplayTargets(raw); err == nil {
		t.Fatal("ApplyPatchDisplayTargets unexpectedly accepted non-canonical args")
	}
}

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

func TestApplyPatchCodexParserAndHunkCompatibility(t *testing.T) {
	t.Run("first hunk omits marker", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: file.txt\n-old\n+new\n*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "new\n")
	})

	t.Run("pure addition precedes earlier replacement", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: file.txt\n@@\n+tail-one\n+tail-two\n@@\n line1\n-line2\n-line3\n+replacement\n" +
			"*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "line1\nreplacement\ntail-one\ntail-two\n")
	})

	t.Run("update adds final newline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "new\n")
	})

	t.Run("implicit hunk preserves bare empty context", func(t *testing.T) {
		doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: file.txt\n context before\n\n-context after\n+context updated\n*** End Patch")
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
			t.Fatalf("doc = %#v", doc)
		}
		lines := doc.Operations[0].Hunks[0].Lines
		if len(lines) != 4 || lines[1].Kind != ' ' || lines[1].Text != "" {
			t.Fatalf("lines = %#v, want bare empty context line", lines)
		}
	})
}

func TestApplyPatchProsePunctuationTolerance(t *testing.T) {
	t.Run("preserves unchanged punctuation", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "注册表保持配置数组顺序；旧字段，结尾。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-注册表保持配置数组顺序;旧字段,结尾.\n" +
			"+注册表保持配置数组顺序;新字段,结尾.\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "used punctuation/whitespace-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant note", out)
		}
		assertApplyPatchFile(t, path, "注册表保持配置数组顺序；新字段，结尾。\n")
	})

	t.Run("rejects ambiguous normalized matches", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "相同；文本。\n中间\n相同；文本。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-相同;文本.\n" +
			"+不同;文本.\n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "punctuation/whitespace-tolerant matching is ambiguous at lines 1, 3") {
			t.Fatalf("error = %v, want ambiguous punctuation candidates", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	t.Run("applies to any decoded text file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env.example")
		content := "MESSAGE=\"old;value。\"\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: .env.example\n" +
			"@@\n" +
			"-MESSAGE=\"old;value.\"\n" +
			"+MESSAGE=\"new;value.\"\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "used punctuation/whitespace-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant note", out)
		}
		assertApplyPatchFile(t, path, "MESSAGE=\"new;value。\"\n")
	})

	t.Run("applies to extensionless text files", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "Makefile")
		content := "说明:old value。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: Makefile\n" +
			"@@\n" +
			"-说明:old value.\n" +
			"+说明:new value.\n" +
			"*** End Patch"

		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "说明:new value。\n")
	})

	// ApplyPatch shares the same separator-space folding as Edit: "：" and
	// ": " (and ":the" when the space is dropped) are treated as equivalent,
	// and unchanged context keeps the file's own bytes.
	t.Run("applies separator-space folding", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "说明：旧值。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-说明: 旧值.\n" +
			"+说明: 新值.\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "used punctuation/whitespace-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant note", out)
		}
		// The colon is unchanged context, so the file's full-width "：" and
		// "。" survive; only the delta "新" replaces "旧".
		assertApplyPatchFile(t, path, "说明：新值。\n")
	})

	t.Run("rejects ambiguous separator-space matches", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "a：b\na: b\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// "a:b" matches neither line exactly but normalizes to the same
		// "a:b" as both variants, so the match must be rejected as ambiguous.
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-a:b\n" +
			"+a:x\n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("error = %v, want ambiguous candidates", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	t.Run("applies model-intended punctuation normalization", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "说明：旧值。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// old/new differ in original bytes at the colon (full-width vs
		// half-width+space); that difference is the model's intended delta
		// and must come from newText verbatim.
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-说明：旧值。\n" +
			"+说明: 新值.\n" +
			"*** End Patch"

		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchFile(t, path, "说明: 新值.\n")
	})

	t.Run("reports text that is only part of a line", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "前缀；`pipeline_timeout_seconds` 是旧字段，后缀。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-`pipeline_timeout_seconds` 是旧字段\n" +
			"+`EXTERNAL_TIMEOUT_SECONDS` 是统一字段\n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "only part of current line 1") || !strings.Contains(err.Error(), "include that complete line") {
			t.Fatalf("error = %v, want complete-line guidance", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	t.Run("applies inter-word space tolerance", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "diff and count\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// The hunk drops the inter-word space ("diffand"); the tolerance
		// treats it as optional and preserves the file's own space in the
		// unchanged context.
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-diffand count\n" +
			"+diffand total\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant note", out)
		}
		assertApplyPatchFile(t, path, "diff and total\n")
	})

	t.Run("replaces a fully-variant line after tolerant match", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "旧值；结尾。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-旧值;结尾.\n" +
			"+完全不同\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatalf("error = %v, want a fully-variant tolerant replacement to apply", err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant note", out)
		}
		assertApplyPatchFile(t, path, "完全不同\n")
	})

	t.Run("does not hide shifted surrounding whitespace", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := " 相同；文本。\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-相同;文本. \n" +
			"+不同;文本. \n" +
			"*** End Patch"

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "hunk not found (1/1)") {
			t.Fatalf("error = %v, want surrounding-whitespace mismatch", err)
		}
		assertApplyPatchFile(t, path, content)
	})

	// An orphaned combining mark (U+0304) leaked into the hunk's removal
	// line — the "### ̄.2.1" shape — is folded by the shared tolerance
	// normalizer just like Edit's old_string, so a model copy that carries the
	// tokenizer artifact still replaces the file's clean heading.

	t.Run("folds orphaned combining mark", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "proposal.md")
		content := "### 3.2.1 Heading\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: proposal.md\n" +
			"@@\n" +
			"-### \u03043.2.1 Heading\n" +
			"+### 3.2.1 Renamed\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant note", out)
		}
		assertApplyPatchFile(t, path, "### 3.2.1 Renamed\n")
	})
}

// TestApplyPatchStripsOrphanCombiningMarkFromAddedLine guards the write path:
// a model that leaks an orphan U+0304 into an added (+) line must not have it
// written to the file. The tolerant matcher folds comb-marks out of the context
// lines, but the added line's mark is cleaned off the bytes that get written
// and reported in the cleaned-invisible note.
func TestApplyPatchStripsOrphanCombiningMarkFromAddedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "### 3.2.1 Heading\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Heading\n" +
		"+## \u03043.2.1 Renamed\n" +
		"*** End Patch"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	if !strings.Contains(out, "cleaned 1 invisible character") {
		t.Fatalf("output = %q, want cleaned-invisible note", out)
	}
	assertApplyPatchFile(t, path, "### 3.2.1 Heading\n## 3.2.1 Renamed\n")
}

// TestApplyPatchCleansInvisibleOnlyInDecodableText guards the encoding
// boundary: the invisible-character clean runs on decoded text and re-encodes
// with the file's own encoding. A UTF-16 file whose body bytes (or the added
// line) carry U+0304 must end up with the mark cleaned in UTF-16, not with
// every byte the UTF-8 strip could not decode rewritten as U+FFFD. GB18030
// likewise: GBK lead bytes 0xCC/0xCD decode as combining marks under UTF-8
// fallback, so a GBK body must never be run through a UTF-8 strip.
func TestApplyPatchCleansInvisibleOnlyInDecodableText(t *testing.T) {
	t.Run("utf16 cleans mark in decoded text", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "notes.md")
		content := "### 3.2.1 Heading\n"
		encoded := mustEncodeForTest(content, "utf-16le")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: notes.md\n" +
			"@@\n" +
			"-### 3.2.1 Heading\n" +
			"+### 3.2.1 Heading\n" +
			"+## \u03043.2.1 Renamed\n" +
			"*** End Patch"

		out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err != nil {
			t.Fatalf("Execute err = %v, want success", err)
		}
		if !strings.Contains(out, "cleaned 1 invisible character") {
			t.Fatalf("output = %q, want cleaned-invisible note", out)
		}
		want := mustEncodeForTest("### 3.2.1 Heading\n## 3.2.1 Renamed\n", "utf-16le")
		assertApplyPatchFileBytes(t, path, want)
	})

	t.Run("gbk file survives uncleaned body", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "gbk.txt")
		// GB18030 text whose lead/trail bytes (0xCC 0xAB "太", 0xCD 0xAC
		// "同", ...) decode as UTF-8 combining marks: a UTF-8 strip over the
		// raw bytes would fold every such pair into a phantom mark and
		// rewrite the file as garbage. The clean must decode first and, for
		// text with no leaked marks, leave the bytes untouched. The body is
		// long enough for the regional detector to resolve gb18030 over
		// big5 (it needs a clear simplified-Chinese majority).
		content := "这是一段简体中文的说明文字，用来测试编码检测。\n不同的图书馆已经停用同样的图书管理系统。\n国家规定的标准规范文件必须使用统一的格式。\n"
		encoded := mustEncodeForTest(content, "gb18030")
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		patch := "*** Begin Patch\n" +
			"*** Update File: gbk.txt\n" +
			"@@\n" +
			"-不同的图书馆已经停用同样的图书管理系统。\n" +
			"+不同的图书馆已经停用同样的图书管理制度。\n" +
			"*** End Patch"
		if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
			t.Fatalf("Execute err = %v, want success", err)
		}
		want := mustEncodeForTest("这是一段简体中文的说明文字，用来测试编码检测。\n不同的图书馆已经停用同样的图书管理制度。\n国家规定的标准规范文件必须使用统一的格式。\n", "gb18030")
		assertApplyPatchFileBytes(t, path, want)
	})
}

// TestApplyPatchKeepsCombiningMarkOnVisibleBaseInAddedLine guards the
// corrected orphan-mark boundary on the write path: a combining mark over a
// visible base (here a digit) in an added (+) line is legitimate content —
// Unicode lets marks attach to digits and symbols, e.g. a U+0305 overline —
// and must land in the file verbatim, not be dropped like a floating heading
// artifact (U+0304 after a space).
func TestApplyPatchKeepsCombiningMarkOnVisibleBaseInAddedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "### 3.2.1 Heading\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Heading\n" +
		"+value 3\u0305 stays\n" +
		"*** End Patch"

	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	if strings.Contains(out, "cleaned") {
		t.Fatalf("output = %q, want no cleaned-invisible note (digit mark is kept)", out)
	}
	want := "### 3.2.1 Heading\nvalue 3\u0305 stays\n"
	assertApplyPatchFile(t, path, want)
}

// TestApplyPatchLeavesUntouchedRegionRunesIntact guards the minimal-change
// boundary of the invisible-character clean: the clean runs only on the
// model-added (+) lines and add-file content, never on a mutation's whole
// after-bytes. File content the patch does not touch must survive byte for
// byte, including zero-width runes, orphan variation selectors, and
// combining marks the write-path strip would remove if they were model text.
func TestApplyPatchLeavesUntouchedRegionRunesIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	content := "keep \u200bzero-width here\nkeep \ufe0f selector here\nkeep 1\u0304 digit mark here\n### 3.2.1 Heading\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: proposal.md\n" +
		"@@\n" +
		"-### 3.2.1 Heading\n" +
		"+### 3.2.1 Renamed\n" +
		"*** End Patch"

	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("Execute err = %v, want success", err)
	}
	want := "keep \u200bzero-width here\nkeep \ufe0f selector here\nkeep 1\u0304 digit mark here\n### 3.2.1 Renamed\n"
	assertApplyPatchFile(t, path, want)
}

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
	err := applyPatchHunkNotFoundError(fileLines, []string{"foo bar", "unrelated"}, 0, 0, 1, false, []int{0, 1})
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous") {
		t.Fatalf("error = %q, want the ambiguity note", msg)
	}
	if strings.Contains(msg, "closest file line") {
		t.Fatalf("error = %q, must not emit a closest match when candidates are ambiguous", msg)
	}

	// Sanity: a single candidate is unambiguous and the closest path still
	// works when the hunk line is genuinely close to a file line.
	err = applyPatchHunkNotFoundError(fileLines, []string{"foobqr"}, 0, 0, 1, false, []int{0})
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

func assertApplyPatchFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertApplyPatchFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %X, want %X", path, got, want)
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

func TestParseApplyPatchRejectsNonCodexEnvelope(t *testing.T) {
	_, err := ParseApplyPatch("@@\n-old\n+new\n")
	if err == nil || !strings.Contains(err.Error(), "Begin Patch") {
		t.Fatalf("err=%v", err)
	}
}

func TestNormalizeApplyPatchArgsIgnoresLegacyPathField(t *testing.T) {
	// The legacy single-file {path, patch} wrapper is tolerated by ignoring
	// the `path` field: only `patch` participates, matching the generic
	// sanitization applied to tool arguments before Execute. The patch must
	// still satisfy the constraints (non-empty; the envelope is enforced by
	// ParseApplyPatch at execution time).
	raw := json.RawMessage(`{"path":"file.txt","patch":"*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"}`)
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	if string(normalized) != `{"patch":"*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch"}` {
		t.Fatalf("normalized = %s, want patch-only canonical args with path ignored", normalized)
	}
}

func TestNormalizeApplyPatchArgsLegacyPathWithoutPatchRejected(t *testing.T) {
	// A {path, patch} wrapper with an empty or missing patch is rejected even
	// though `path` is ignored: patch is the required field.
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"path":"file.txt"}`),
		json.RawMessage(`{"path":"file.txt","patch":""}`),
		json.RawMessage(`{"path":"file.txt","patch":"   "}`),
	} {
		if _, err := NormalizeApplyPatchArgs(raw); err == nil {
			t.Fatalf("NormalizeApplyPatchArgs(%s) succeeded, want patch-required error", raw)
		}
	}
}

func TestNormalizeApplyPatchArgsBareFreeformText(t *testing.T) {
	raw := json.RawMessage(`*** Begin Patch
*** Update File: file.txt
@@
-old
+new
*** End Patch`)
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		t.Fatalf("normalized = %s, want canonical object: %v", normalized, err)
	}
	if !strings.Contains(args.Patch, "*** Update File: file.txt") {
		t.Fatalf("patch = %q, want the bare freeform text preserved", args.Patch)
	}
}

func TestNormalizeApplyPatchArgsJSONStringWrapped(t *testing.T) {
	raw := json.RawMessage(`"*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch"`)
	normalized, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		t.Fatalf("NormalizeApplyPatchArgs error = %v", err)
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		t.Fatalf("normalized = %s, want canonical object: %v", normalized, err)
	}
	if !strings.Contains(args.Patch, "*** Delete File: gone.txt") {
		t.Fatalf("patch = %q, want the unwrapped text", args.Patch)
	}
}

func TestNormalizeApplyPatchArgsGatewayLoweringDiagnostic(t *testing.T) {
	// A gateway that received a custom tool but lowered it to a function call
	// emits {"input": "..."} instead of {"patch": "..."}. The error must name
	// the actionable compat knob.
	raw := json.RawMessage(`{"input":"*** Begin Patch\n*** End Patch"}`)
	_, err := NormalizeApplyPatchArgs(raw)
	if err == nil {
		t.Fatal("NormalizeApplyPatchArgs error = nil, want lowering diagnostic")
	}
	if !strings.Contains(err.Error(), "freeform: false") {
		t.Fatalf("error = %q, want hint to set compat.apply_patch.freeform: false", err)
	}
}

func TestNormalizeApplyPatchArgsEmptyObjectStillRequiresPatch(t *testing.T) {
	if _, err := NormalizeApplyPatchArgs(json.RawMessage(`{}`)); err == nil {
		t.Fatal("NormalizeApplyPatchArgs({}) error = nil, want patch is required")
	}
}

func TestParseApplyPatchRecoversMissingBeginMarker(t *testing.T) {
	doc, err := ParseApplyPatch("*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Kind != MutationUpdate || doc.Operations[0].Path != "file.txt" {
		t.Fatalf("doc = %#v, want single update op", doc)
	}
}

func TestParseApplyPatchRecoversMissingEndMarker(t *testing.T) {
	doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
		t.Fatalf("doc = %#v, want single update hunk", doc)
	}
}

func TestParseApplyPatchRecoversMissingEndMarkerForDelete(t *testing.T) {
	doc, err := ParseApplyPatch("*** Begin Patch\n*** Delete File: file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Kind != MutationDelete || doc.Operations[0].Path != "file.txt" {
		t.Fatalf("doc = %#v, want single delete op", doc)
	}
}

func TestParseApplyPatchRecoversMissingEndMarkerForMoveOnlyUpdate(t *testing.T) {
	doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].Kind != MutationUpdate || doc.Operations[0].Path != "old.txt" || doc.Operations[0].MovePath != "new.txt" {
		t.Fatalf("doc = %#v, want single move-only update op", doc)
	}
}

func TestParseApplyPatchRejectsLeadingProseBeforeOperation(t *testing.T) {
	_, err := ParseApplyPatch("Applying patch now\n*** Update File: file.txt\n@@\n-old\n+new\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "Begin Patch") {
		t.Fatalf("err = %v, want begin-marker validation", err)
	}
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

func TestApplyPatchStarLinesNextToRealMarkers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first.txt"), []byte("*** old banner\nbody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: first.txt\n" +
		"@@\n-*** old banner\n+*** new banner\n *** more stars\n" +
		"*** Add File: second.txt\n" +
		"+*** generated\n+content\n" +
		"*** End Patch"
	// The hunk references a context line missing from the file; only the
	// parse/boundary behavior matters here, so apply must fail on matching,
	// not on protocol parsing.
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v, want hunk-match failure instead of a parse error", err)
	}
	doc, err := ParseApplyPatch(strings.ReplaceAll(patch, " *** more stars\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 2 || doc.Operations[1].Content != "*** generated\ncontent\n" {
		t.Fatalf("doc = %+v, want update plus add with literal *** content", doc)
	}
}

func TestApplyPatchHeaderRepeatedAsFirstContextLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	content := "func alpha() {\n\ta := 1\n}\n\nfunc target() {\n\told := 1\n\t_ = old\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	// Models often repeat the @@ header text as the hunk's first context line;
	// matching must then anchor at the header itself, not strictly after it.
	patch := "*** Begin Patch\n" +
		"*** Update File: code.go\n" +
		"@@ func target() {\n" +
		" func target() {\n" +
		"-\told := 1\n" +
		"+\tnew := 2\n" +
		"-\t_ = old\n" +
		"+\t_ = new\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "func alpha() {\n\ta := 1\n}\n\nfunc target() {\n\tnew := 2\n\t_ = new\n}\n"
	if string(got) != want {
		t.Fatalf("code.go = %q, want the hunk applied at the header line", got)
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

// A valid envelope with leading whitespace must be preserved as the current
// patch argument.
func TestApplyPatchEnvelopeWithLeadingWhitespacePreservesCurrentArgs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lead.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "\n  \n*** Begin Patch\n*** Update File: lead.txt\n@@\n-before\n+after\n*** End Patch\n"
	raw, err := json.Marshal(map[string]any{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), raw); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("result = %q, %v; want after", got, err)
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

func TestApplyPatchBlankLinesBetweenOperationsAreSeparators(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"a.txt": "one\n", "b.txt": "two\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: a.txt\n@@\n-one\n+ONE\n\n" +
		"*** Update File: b.txt\n@@\n-two\n+TWO\n\n\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "ONE\n", "b.txt": "TWO\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestApplyPatchBlankLineBeforeNextHunkIsSeparator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(path, []byte("one\nmid\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n" +
		"*** Update File: c.txt\n@@\n-one\n+ONE\n\n@@\n-two\n+TWO\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "ONE\nmid\nTWO\n" {
		t.Fatalf("c.txt = %q, %v; want ONE/mid/TWO", got, err)
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

func TestApplyPatchAddFileTrailingBlankLinesAreSeparators(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Add File: fresh.txt\n+hello\n\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "fresh.txt"))
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("fresh.txt = %q, %v; want hello", got, err)
	}
}

func TestApplyPatchAddFileInteriorBlankLineStillRejected(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Add File: bad.txt\n+hello\n\n+world\n" +
		"*** End Patch"
	if _, err := ParseApplyPatch(patch); err == nil || !strings.Contains(err.Error(), "must start with +") {
		t.Fatalf("err = %v, want add-file line error", err)
	}
	_ = dir
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

func TestApplyPatchTargetsPreserveRepeatedUpdateOperations(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n" +
		"*** Update File: dup.txt\n@@\n-a\n+b\n" +
		"*** Update File: ./dup.txt\n@@\n-b\n+c\n" +
		"*** End Patch"
	targets, err := ApplyPatchTargets(applyPatchArgs(t, patch), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Kind != MutationUpdate || targets[1].Kind != MutationUpdate || targets[0].SourcePath != filepath.Join(dir, "dup.txt") || targets[1].SourcePath != targets[0].SourcePath {
		t.Fatalf("targets = %#v, want two operations on one normalized path", targets)
	}
	paths := MutationTargetPaths(targets)
	if len(paths) != 1 || paths[0] != filepath.Join(dir, "dup.txt") {
		t.Fatalf("paths = %#v, want one lock and permission path", paths)
	}
}

func TestApplyPatchBlankLineAfterEndOfFileIsSeparator(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("a\n"), 0644)
	os.WriteFile(filepath.Join(dir, "y.txt"), []byte("c\n"), 0644)
	patch := "*** Begin Patch\n" +
		"*** Update File: x.txt\n@@\n-a\n+b\n*** End of File\n\n" +
		"*** Update File: y.txt\n@@\n-c\n+d\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("blank after End of File should be tolerated: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "x.txt"))
	if string(got) != "b\n" {
		t.Fatalf("x.txt = %q, want b", got)
	}
}

func TestApplyPatchBlankLineBetweenHeaderAndFirstHunk(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old\n"), 0644)
	patch := "*** Begin Patch\n" +
		"*** Update File: a.txt\n\n@@\n-old\n+new\n" +
		"*** End Patch"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("blank between update header and first @@ should be tolerated: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "new\n" {
		t.Fatalf("a.txt = %q, want new", got)
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

// TestParseApplyPatchKeepsBareHunksAndTrailingBlankLines verifies that
// adjacent bare @@ hunks stay separate hunks within one operation and that
// trailing blank lines of added files survive parsing.
func TestParseApplyPatchKeepsBareHunksAndTrailingBlankLines(t *testing.T) {
	patch := "*** Begin Patch\n" +
		// Multiple bare @@ hunks in one file: boundaries must survive.
		"*** Update File: multi.txt\n" +
		"@@\n-alpha\n+ALPHA\n" +
		"@@\n-gamma\n+GAMMA\n" +
		"@@\n-missing\n+NEVER\n" +
		// Added file with intentional blank lines at EOF.
		"*** Add File: fresh.txt\n+line\n+\n+\n" +
		"*** End Patch"
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(doc.Operations[0].Hunks); got != 3 {
		t.Fatalf("hunk count = %d, want 3 separate bare hunks", got)
	}
	if got := doc.Operations[1].Content; got != "line\n\n\n" {
		t.Fatalf("added content = %q, want trailing blank lines preserved by the parser", got)
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

// updateHunksFor parses a single-file update patch and returns its hunks, so
// matching behaviour can be exercised without touching the filesystem.
func updateHunksFor(t *testing.T, patch string) []applyPatchHunk {
	t.Helper()
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
		t.Fatalf("doc = %#v, want a single update hunk", doc)
	}
	return doc.Operations[0].Hunks
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
