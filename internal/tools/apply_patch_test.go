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
		doc, err := ParseApplyPatch("*** Begin Patch\n*** Update File: file.txt\n context before\n\n context after\n*** End Patch")
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Operations) != 1 || len(doc.Operations[0].Hunks) != 1 {
			t.Fatalf("doc = %#v", doc)
		}
		lines := doc.Operations[0].Hunks[0].Lines
		if len(lines) != 3 || lines[1].Kind != ' ' || lines[1].Text != "" {
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
		if !strings.Contains(out, "used punctuation-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation-tolerant note", out)
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
		if err == nil || !strings.Contains(err.Error(), "punctuation-tolerant matching is ambiguous at lines 1, 3") {
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
		if !strings.Contains(out, "used punctuation-tolerant matching for 1 hunk") {
			t.Fatalf("output = %q, want punctuation-tolerant note", out)
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

	t.Run("rejects unsafe replacement after tolerant match", func(t *testing.T) {
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

		_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
		if err == nil || !strings.Contains(err.Error(), "cannot preserve unchanged punctuation safely") {
			t.Fatalf("error = %v, want unsafe tolerant replacement rejection", err)
		}
		assertApplyPatchFile(t, path, content)
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

func TestApplyPatchSequentialPlanningFailureIsAtomic(t *testing.T) {
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
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v, want late hunk failure", err)
	}
	assertApplyPatchFile(t, source, "old\n")
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("archive exists after planning failure, err=%v", err)
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

func TestApplyPatchPlanningFailureDoesNotModifyAnyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.txt"), []byte("good\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: good.txt\n@@\n-good\n+changed\n*** Update File: missing.txt\n@@\n-missing\n+created\n*** Add File: should-not-exist.txt\n+no\n*** End Patch"
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil || !strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("err=%v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "good.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "good\n" {
		t.Fatalf("good.txt changed to %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "should-not-exist.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected added file, err=%v", statErr)
	}
}

func TestParseApplyPatchRejectsNonCodexEnvelope(t *testing.T) {
	_, err := ParseApplyPatch("@@\n-old\n+new\n")
	if err == nil || !strings.Contains(err.Error(), "Begin Patch") {
		t.Fatalf("err=%v", err)
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

func TestApplyPatchLegacySingleFileArgsAreNormalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"path": "legacy.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "after\n" {
		t.Fatalf("legacy result = %q, %v; want after", got, err)
	}
}

// A valid envelope with leading whitespace must not be misrouted through the
// legacy single-file wrapping: ParseApplyPatch trims the text, so the prefix
// sniff in NormalizeApplyPatchArgs must trim too.
func TestApplyPatchEnvelopeWithLeadingWhitespaceIsNotTreatedAsLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lead.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "\n  \n*** Begin Patch\n*** Update File: lead.txt\n@@\n-before\n+after\n*** End Patch\n"
	for name, args := range map[string]map[string]any{
		"without path": {"patch": patch},
		"with path":    {"path": "lead.txt", "patch": patch},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(args)
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
		})
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
