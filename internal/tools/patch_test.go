package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchToolReportsPlanningProgressWithReporter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n one\n-two\n+TWO\n@@\n three\n-four\n+FOUR\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})

	recorder := &progressRecorder{}
	ctx := WithToolProgressReporter(context.Background(), recorder)
	if _, err := (PatchTool{BaseDir: dir}).Execute(ctx, args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}

	if len(recorder.snapshots) == 0 {
		t.Fatal("expected progress snapshots")
	}
	var gotPlanning bool
	for _, snap := range recorder.snapshots {
		if strings.Contains(snap.Text, "matching hunk 2/2") {
			gotPlanning = true
			break
		}
	}
	if !gotPlanning {
		t.Fatalf("progress snapshots = %#v, want planning progress for second hunk", recorder.snapshots)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "one\nTWO\nthree\nFOUR\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestPatchParserAcceptsSingleUpdate(t *testing.T) {
	parsed, err := ParsePatch("a/b.txt", "@@\n-old\n+new\n")
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if parsed.Path != filepath.Join("a", "b.txt") || len(parsed.Hunks) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserExplainsMissingContextMarkerWithoutLosingIndentation(t *testing.T) {
	_, err := ParsePatch("a.py", "@@\ndef target():\n-    old\n+    new\n")
	if err == nil {
		t.Fatal("ParsePatch unexpectedly succeeded")
	}
	for _, want := range []string{
		"every non-empty hunk line must start with a patch marker",
		"context marker space is separate from source indentation",
		"unchanged source line is `    value` with four leading spaces",
		"write it in the hunk as `     value`",
		"original four indentation spaces",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestPatchParserAllowsContextOnlyAnchorHunkWhenPatchHasChanges(t *testing.T) {
	parsed, err := ParsePatch("a.txt", "@@\n section header\n@@\n old\n-old value\n+new value\n")
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(parsed.Hunks))
	}
	if hunkHasChanges(parsed.Hunks[0]) {
		t.Fatalf("first hunk has changes: %+v", parsed.Hunks[0])
	}
	if !hunkHasChanges(parsed.Hunks[1]) {
		t.Fatalf("second hunk has no changes: %+v", parsed.Hunks[1])
	}
}

func TestPatchContextOnlyAnchorAdvancesSearchPosition(t *testing.T) {
	// "anchor" is a unique marker near the second block. A context-only anchor
	// hunk on "anchor" must advance the search position so the following change
	// hunk lands after it instead of matching the first "target" occurrence.
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	content := "target\nfirst\nanchor\ntarget\nsecond\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n anchor\n@@\n target\n-second\n+second\n+inserted by anchor\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	out, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(path)
	want := "target\nfirst\nanchor\ntarget\nsecond\ninserted by anchor\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
	plan, err := BuildPatchPlanInDirWithContext(context.Background(), "demo.txt", patch, dir)
	if err != nil {
		t.Fatalf("BuildPatchPlanInDirWithContext: %v", err)
	}
	if !strings.Contains(plan.ModelContextNote, "accepted 1 context-only hunk as no-op anchor") {
		t.Fatalf("plan note = %q, want compatibility note", plan.ModelContextNote)
	}
}

func TestPatchParserRejectsAllContextHunks(t *testing.T) {
	_, err := ParsePatch("a.txt", "@@\n section header\n@@\n old value\n")
	if err == nil {
		t.Fatal("ParsePatch unexpectedly succeeded")
	}
	for _, want := range []string{
		"patch contains no changes",
		"all 2 hunks only have unchanged context lines",
		"context-only hunk is allowed as a no-op anchor only when another hunk has +/- changes",
		"Add + lines for insertions or - lines for deletions",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestPatchParserRejectsUnsupportedOperations(t *testing.T) {
	tests := []struct {
		name       string
		patch      string
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name:  "add file",
			patch: "*** Add File: a.txt\n+new\n",
			wantSubstr: []string{
				"*** Add File: a.txt",
				"Use write to create files.",
				"No files were modified",
			},
			notSubstr: []string{"Use delete to remove whole files."},
		},
		{
			name:  "delete file",
			patch: "*** Delete File: a.txt\n",
			wantSubstr: []string{
				"*** Delete File: a.txt",
				"Use delete to remove whole files.",
				"No files were modified",
			},
			notSubstr: []string{"Use write to create files."},
		},
		{
			name:  "move file",
			patch: "*** Move to: b.txt\n",
			wantSubstr: []string{
				"*** Move to: b.txt",
				"Use separate read/write/delete steps for rename or move workflows.",
				"No files were modified",
			},
		},
		{
			name:  "update different file",
			patch: "*** Update File: b.txt\n@@\n-a\n+b\n",
			wantSubstr: []string{
				"*** Update File: b.txt",
				"Split multi-file update patches into separate patch calls, one file per call.",
				"No files were modified",
			},
			notSubstr: []string{"Use delete to remove whole files.", "Use write to create files."},
		},
		{
			name:  "update second file after hunk",
			patch: "@@\n-a\n+b\n*** Update File: b.txt\n@@\n-c\n+d\n",
			wantSubstr: []string{
				"*** Update File: b.txt",
				"Split multi-file update patches into separate patch calls, one file per call.",
				"No files were modified",
			},
			notSubstr: []string{"Use delete to remove whole files.", "Use write to create files."},
		},
		{
			name:  "update plus delete",
			patch: "*** Update File: b.txt\n@@\n-a\n+b\n*** Delete File: old.txt\n",
			wantSubstr: []string{
				"unsupported patch operations:",
				"*** Update File: b.txt",
				"*** Delete File: old.txt",
				"Mixed apply_patch-style operations were detected.",
				"Split multi-file update patches into separate patch calls, one file per call.",
				"Use delete to remove whole files.",
				"No files were modified",
			},
			notSubstr: []string{"Use write to create files."},
		},
		{
			name:  "add plus move",
			patch: "*** Add File: new.txt\n+new\n*** Move to: renamed.txt\n",
			wantSubstr: []string{
				"unsupported patch operations:",
				"*** Add File: new.txt",
				"*** Move to: renamed.txt",
				"Mixed apply_patch-style operations were detected.",
				"Use write to create files.",
				"Use separate read/write/delete steps for rename or move workflows.",
				"No files were modified",
			},
			notSubstr: []string{"Use delete to remove whole files.", "Split multi-file update patches into separate patch calls, one file per call."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePatch("a.txt", tt.patch)
			if err == nil {
				t.Fatalf("ParsePatch(%q) unexpectedly succeeded", tt.patch)
			}
			for _, want := range tt.wantSubstr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("ParsePatch(%q) err = %q, want substring %q", tt.patch, err.Error(), want)
				}
			}
			for _, notWant := range tt.notSubstr {
				if strings.Contains(err.Error(), notWant) {
					t.Fatalf("ParsePatch(%q) err = %q, should not mention %q", tt.patch, err.Error(), notWant)
				}
			}
		})
	}
}

func TestPatchParserStripsEnvelopeMarkers(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch\n"
	parsed, err := ParsePatch("a.txt", patch)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserStripsTrailingEndPatch(t *testing.T) {
	patch := "@@\n-old\n+new\n*** End Patch"
	parsed, err := ParsePatch("a.txt", patch)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].Lines[1].Text != "new" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserRetriesWithoutTrailingMalformedEndPatch(t *testing.T) {
	for _, trailer := range []string{"*** End Patch?", "*** End Patch_PLACEHOLDER", "*** End Patch whatever"} {
		t.Run(trailer, func(t *testing.T) {
			patch := "@@\n-old\n+new\n" + trailer
			parsed, err := ParsePatch("a.txt", patch)
			if err != nil {
				t.Fatalf("ParsePatch: %v", err)
			}
			if len(parsed.Hunks) != 1 || parsed.Hunks[0].Lines[1].Text != "new" {
				t.Fatalf("parsed = %+v", parsed)
			}
		})
	}
}

func TestPatchParserDoesNotStripNonTrailingMalformedEndPatch(t *testing.T) {
	patch := "@@\n-old\n+new\n*** End Patch?\n@@\n-a\n+b"
	_, err := ParsePatch("a.txt", patch)
	if err == nil {
		t.Fatalf("ParsePatch unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "unsupported patch operation") || !strings.Contains(err.Error(), "*** End Patch?") {
		t.Fatalf("ParsePatch err = %q", err.Error())
	}
}

func TestPatchParserKeepsEnvelopeLikeHunkContext(t *testing.T) {
	patch := "@@\n *** Begin Patch\n-old\n+new\n *** End Patch\n*** End Patch"
	parsed, err := ParsePatch("a.txt", patch)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 4 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Hunks[0].Lines[0].Text != "*** Begin Patch" || parsed.Hunks[0].Lines[3].Text != "*** End Patch" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserKeepsMalformedEndPatchLikeHunkContext(t *testing.T) {
	patch := "@@\n *** End Patch?\n-old\n+new\n*** End Patch?"
	parsed, err := ParsePatch("a.txt", patch)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 3 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Hunks[0].Lines[0].Text != "*** End Patch?" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserKeepsTrailingMalformedEndPatchWhenStrictParseSucceeds(t *testing.T) {
	patch := "@@\n-old\n+new\n *** End Patch?"
	parsed, err := ParsePatch("a.txt", patch)
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 3 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Hunks[0].Lines[2].Text != "*** End Patch?" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserTreatsBlankLinesAsContext(t *testing.T) {
	parsed, err := ParsePatch("a.txt", "@@\n a\n\n-b\n+B\n")
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 4 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Hunks[0].Lines[1].Kind != ' ' || parsed.Hunks[0].Lines[1].Text != "" {
		t.Fatalf("blank line not parsed as empty context line: %+v", parsed.Hunks[0].Lines)
	}
}

func TestPatchToolMatchesBlankContextLine(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("blank.txt", []byte("a\n\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The blank line between "a" and "-b" is a bare empty context line.
	patch := "@@\n a\n\n-b\n+B\n"
	args, _ := json.Marshal(map[string]string{"path": "blank.txt", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("blank.txt")
	if string(got) != "a\n\nB\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestReplaceEditToolUsesBaseDirForRelativePath(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "demo.txt"), []byte("wrong\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldWD, _ := os.Getwd()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "old_string": "before", "new_string": "after"})
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "after\n" {
		t.Fatalf("base dir file = %q", got)
	}
	otherGot, _ := os.ReadFile(filepath.Join(otherDir, "demo.txt"))
	if string(otherGot) != "wrong\n" {
		t.Fatalf("cwd file was modified: %q", otherGot)
	}
}

func TestPatchParserRejectsInvalidPaths(t *testing.T) {
	for _, path := range []string{"", "."} {
		if _, err := ParsePatch(path, "@@\n-a\n+b\n"); err == nil {
			t.Fatalf("ParsePatch accepted invalid path %q", path)
		}
	}
}

func TestPatchParserAcceptsExternalPathForms(t *testing.T) {
	for _, path := range []string{filepath.Join("..", "a.txt"), filepath.Join("a", "..", "..", "b.txt"), filepath.Join(string(filepath.Separator), "tmp", "a.txt"), "~/a.txt"} {
		if _, err := ParsePatch(path, "@@\n-a\n+b\n"); err != nil {
			t.Fatalf("ParsePatch rejected external path form %q: %v", path, err)
		}
	}
}

func TestPatchToolReplacesInsertsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n one\n-two\n+TWO\n three\n@@\n three\n+four\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	out, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch") {
		t.Fatalf("output = %q", out)
	}
}

func TestPatchToolIgnoresUnifiedDiffLineRangeHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nTWO\nthree\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestPatchParserPreservesUnifiedDiffSectionHeader(t *testing.T) {
	parsed, err := ParsePatch("demo.go", "@@ -3,3 +3,4 @@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n")
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].Header != "func second() {" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestPatchParserPreservesUnifiedDiffSectionWhitespace(t *testing.T) {
	parsed, err := ParsePatch("demo.go", "@@ -3,3 +3,4 @@     func  second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n")
	if err != nil {
		t.Fatalf("ParsePatch: %v", err)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].Header != "    func  second() {" {
		t.Fatalf("header = %q", parsed.Hunks[0].Header)
	}
}

func TestPatchToolUsesUnifiedDiffSectionHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.go")
	content := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -6,3 +6,4 @@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.go", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n\tprintln(\"y\")\n}\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestPatchToolUsesIndentedUnifiedDiffSectionHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.py")
	content := "def first():\n    value = 1\n\ndef second():\n    value = 1\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -3,2 +3,3 @@     def second():\n     value = 1\n+    value = 2\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.py", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "def first():\n    value = 1\n\ndef second():\n    value = 1\n    value = 2\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestPatchToolSupportsMultipleUnifiedDiffRangeHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n@@ -3,2 +3,3 @@\n three\n four\n+five\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nTWO\nthree\nfour\nfive\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestPatchToolPatchDescriptionEmphasizesPreferredFormat(t *testing.T) {
	params := (PatchTool{}).Parameters()
	props := params["properties"].(map[string]any)
	patch := props["patch"].(map[string]any)
	desc := patch["description"].(string)
	for _, want := range []string{"Direct single-file @@ hunks for the JSON path", "not apply_patch format", "Top-level `*** ...` envelope lines are invalid", "Use verified @@ headers and nearby context", "Split unrelated or distant edits into separate patch calls.", "Example:"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q: %q", want, desc)
		}
	}
}

func TestPatchToolDescriptionExplainsSingleFileSubset(t *testing.T) {
	desc := (PatchTool{}).Description()
	for _, want := range []string{
		"Edit one existing file with direct @@ hunks for the JSON path.",
		"This is not apply_patch format",
		"top-level *** envelope lines are invalid",
		"Each patch must contain at least one +/- change",
		"context-only @@ hunk is allowed as a no-op anchor",
		"Do not use shell to run apply_patch.",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q: %q", want, desc)
		}
	}
}

func TestPatchToolPathDescriptionWarnsAgainstGuessing(t *testing.T) {
	params := (PatchTool{}).Parameters()
	props := params["properties"].(map[string]any)
	path := props["path"].(map[string]any)
	desc := path["description"].(string)
	for _, want := range []string{"verified existing file", "Do not guess paths"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("path description missing %q: %q", want, desc)
		}
	}
}

func TestPatchToolSupportsParentDirectoryPath(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "repo")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-old\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "../outside.txt", "patch": patch})
	out, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch") {
		t.Fatalf("output = %q", out)
	}
}

func TestPatchToolUsesHunkHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.go")
	content := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.go", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n\tprintln(\"y\")\n}\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestPatchAmbiguousWeakContextAppliesFirstMatch(t *testing.T) {
	got, err := applyParsedPatch("func a() {\n}\n\nfunc b() {\n}\n", parsedPatch{
		Path: "demo.go",
		Hunks: []patchHunk{{Lines: []patchLine{
			{Kind: ' ', Text: "}"},
			{Kind: '+', Text: ""},
			{Kind: '+', Text: "func c() {"},
			{Kind: '+', Text: "}"},
		}}},
	})
	if err != nil {
		t.Fatalf("applyParsedPatch: %v", err)
	}
	if got != "func a() {\n}\n\nfunc c() {\n}\n\nfunc b() {\n}\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestPatchToolDiagnosesMissingHunkLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n 1\talpha\n-2\tbeta\n+gamma\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	_, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "tool-added read metadata, copied line numbers, or a tab separator") {
		t.Fatalf("err = %v", err)
	}
}

func TestPatchToolDiagnosesMissingHunkReadResultHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n READ_RESULT lines=1-2 total=2\n-alpha\n+gamma\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	_, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "remove the READ_RESULT line") {
		t.Fatalf("err = %v", err)
	}
}

func TestPatchToolDiagnosesMissingHunkWhitespace(t *testing.T) {
	got := diagnoseMissingHunk([]string{"\treturn nil"}, []string{"return nil"}, 0)
	if !strings.Contains(got, "exact indentation") {
		t.Fatalf("diagnosis = %q", got)
	}
}

func TestNearestLineDiagnosticReportsFirstDifferingColumn(t *testing.T) {
	// File line and hunk line share a long prefix/suffix but differ by one word
	// deep inside, the case where exact whole-line matching is unhelpful.
	fileLine := `return "the quick brown fox jumps over the lazy dog and keeps going"`
	hunkLine := `return "the quick brown cat jumps over the lazy dog and keeps going"`
	got := diagnoseMissingHunk([]string{fileLine}, []string{hunkLine}, 0)
	if !strings.Contains(got, "file line 1") {
		t.Fatalf("diagnosis = %q, want file line reference", got)
	}
	if !strings.Contains(got, "column 25") {
		t.Fatalf("diagnosis = %q, want first differing column 25", got)
	}
	if !strings.Contains(got, "fox") || !strings.Contains(got, "cat") {
		t.Fatalf("diagnosis = %q, want both file and hunk excerpts", got)
	}
}

func TestNearestLineDiagnosticReportsUnicodeColumn(t *testing.T) {
	fileLine := "你好abcdef"
	hunkLine := "你好abcxef"
	got := diagnoseMissingHunk([]string{fileLine}, []string{hunkLine}, 0)
	if !strings.Contains(got, "column 6") {
		t.Fatalf("diagnosis = %q, want first differing character column 6", got)
	}
	if strings.Contains(got, "column 10") {
		t.Fatalf("diagnosis = %q, should not report byte column 10", got)
	}
	if !strings.Contains(got, "def") || !strings.Contains(got, "xef") {
		t.Fatalf("diagnosis = %q, want UTF-8-safe excerpts from both sides", got)
	}
}

func TestNearestLineDiagnosticStaysSilentForDissimilarLines(t *testing.T) {
	got := diagnoseMissingHunk([]string{"completely different content here"}, []string{"xyz"}, 0)
	if strings.Contains(got, "almost identical") {
		t.Fatalf("diagnosis = %q, should not claim near-match for dissimilar lines", got)
	}
}

func TestPatchToolDiagnosesMissingHunkStaleText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-gamma\n+delta\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	_, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "not found in the current file") {
		t.Fatalf("err = %v", err)
	}
}

func TestPatchToolDiagnosesMissingHunkHeaderAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo_test.go")
	if err := os.WriteFile(path, []byte("package demo\n\nfunc TestActual(t *testing.T) {\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ func TestImagined(t *testing.T) {\n }\n+// added\n"
	args, _ := json.Marshal(map[string]string{"path": "demo_test.go", "patch": patch})
	_, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "@@ header anchor \"TestImagined\"") || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestPatchToolWhitespaceAndUnicodeTolerance(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("alpha   \nquote “x”\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n alpha\n-quote \"x\"\n+quote \"y\"\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "alpha   \nquote \"y\"\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestPatchToolPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("one\r\ntwo\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-one\n+ONE\n two\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	if _, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "ONE\r\ntwo\r\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestPatchToolAmbiguousHunkAppliesFirstMatchWithNote(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("same\nsame\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-same\n+changed\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	out, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("PatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "changed\nsame\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch to demo.txt (+1 -1)") {
		t.Fatalf("output = %q", out)
	}
	if strings.Contains(out, "Matched hunk near line(s):") || strings.Contains(out, "matched multiple locations") {
		t.Fatalf("output should not include low-level match notes by default, got %q", out)
	}
}

func TestPatchToolSoftAnchorIgnoresEarlierMatchesBeforeSearchStart(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)

	content := strings.Join([]string{
		"func first() {",
		"    fmt.Println(\"same\")",
		"    fmt.Println(\"keep\")",
		"}",
		"",
		"func second() {",
		"    fmt.Println(\"same\")",
		"    fmt.Println(\"keep\")",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile("demo.go", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	patch := strings.Join([]string{
		"@@ func first() {",
		"     fmt.Println(\"same\")",
		"     fmt.Println(\"keep\")",
		" }",
		"+// first done",
		"@@ missing header",
		"     fmt.Println(\"same\")",
		"-    fmt.Println(\"keep\")",
		"+    fmt.Println(\"second keep\")",
		" }",
	}, "\n") + "\n"

	args, _ := json.Marshal(map[string]string{"path": "demo.go", "patch": patch})
	out, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("PatchTool.Execute: %v\noutput=%s", err, out)
	}

	got, err := os.ReadFile("demo.go")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"func first() {",
		"    fmt.Println(\"same\")",
		"    fmt.Println(\"keep\")",
		"}",
		"// first done",
		"",
		"func second() {",
		"    fmt.Println(\"same\")",
		"    fmt.Println(\"second keep\")",
		"}",
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("file = %q, want %q", string(got), want)
	}
	if strings.Contains(out, "ambiguous") {
		t.Fatalf("output should not report ambiguity after earlier matches are skipped, got %q", out)
	}
}

func TestPatchToolErrorIncludesPatchExcerptForHunkMatchFailures(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-missing\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	out, err := (PatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"hunk not found", "Patch excerpt:", "```diff", "-missing", "+new"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want substring %q", out, want)
		}
	}
}

func TestPatchToolConcurrencyPolicyUsesPatchPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-old\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})

	policy := (PatchTool{BaseDir: dir}).ConcurrencyPolicy(args)
	wantResource := "file:" + path
	if policy.Mode != ConcurrencyModeWrite || policy.Resource != wantResource {
		t.Fatalf("policy = %+v, want write %s", policy, wantResource)
	}
}

func TestExtractEditPathFromArgsFallsBackToLegacyEnvelope(t *testing.T) {
	dir := t.TempDir()
	args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: demo.txt\n@@\n-old\n+new\n*** End Patch\n"}`)

	got := ExtractEditPathFromArgsInDir(args, dir)
	want := filepath.Join(dir, "demo.txt")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestDiagnoseContiguousMatchPartialLinesMissing(t *testing.T) {
	// Some hunk lines are completely absent from the file → "no longer exist"
	fileLines := []string{"alpha", "beta", "delta"}
	oldSeq := []string{"alpha", "gamma", "delta"} // "gamma" is not in the file
	got := diagnoseMissingHunk(fileLines, oldSeq, 0)
	if !strings.Contains(got, "no longer exist") {
		t.Fatalf("diagnosis = %q, want mention of lines no longer existing", got)
	}
	if !strings.Contains(got, "2 of 3") {
		t.Fatalf("diagnosis = %q, want '2 of 3 lines found'", got)
	}
	if !strings.Contains(got, "likely changed") {
		t.Fatalf("diagnosis = %q, want mention of file likely changed", got)
	}
}

func TestDiagnoseContiguousMatchAllExistNotContiguous(t *testing.T) {
	// All lines exist individually but not contiguous → "not as one contiguous block"
	fileLines := []string{"alpha", "inserted", "beta", "gamma"}
	oldSeq := []string{"alpha", "beta", "gamma"} // "inserted" breaks contiguity
	got := diagnoseMissingHunk(fileLines, oldSeq, 0)
	if !strings.Contains(got, "not as one contiguous block") {
		t.Fatalf("diagnosis = %q, want 'not as one contiguous block'", got)
	}
	if !strings.Contains(got, "longest adjacent match") {
		t.Fatalf("diagnosis = %q, want longest adjacent match info", got)
	}
}

func TestDiagnoseContiguousMatchAllExistLongestRun(t *testing.T) {
	// All lines exist, longest run is 2 of 3 at a known position.
	fileLines := []string{"alpha", "beta", "extra", "gamma"}
	oldSeq := []string{"alpha", "beta", "gamma"}
	got := diagnoseMissingHunk(fileLines, oldSeq, 0)
	if !strings.Contains(got, "2 of 3") {
		t.Fatalf("diagnosis = %q, want '2 of 3 lines'", got)
	}
	if !strings.Contains(got, "line 1") {
		t.Fatalf("diagnosis = %q, want 'starting at line 1'", got)
	}
}

func TestLongestContiguousRun(t *testing.T) {
	tests := []struct {
		fileLines []string
		oldSeq    []string
		start     int
		wantLen   int
		wantLine  int
	}{
		{
			fileLines: []string{"a", "b", "c"},
			oldSeq:    []string{"a", "b", "c"},
			start:     0,
			wantLen:   3, wantLine: 0,
		},
		{
			fileLines: []string{"a", "x", "b", "c"},
			oldSeq:    []string{"a", "b", "c"},
			start:     0,
			wantLen:   2, wantLine: 2,
		},
		{
			fileLines: []string{"x", "a", "b", "y", "c"},
			oldSeq:    []string{"a", "b", "c"},
			start:     0,
			wantLen:   2, wantLine: 1,
		},
		{
			// The first occurrence of "a" (line 0) cannot extend, but a later
			// occurrence (line 2) continues into "b": must find the longer run
			// rather than stopping at the first match.
			fileLines: []string{"a", "x", "a", "b"},
			oldSeq:    []string{"a", "b"},
			start:     0,
			wantLen:   2, wantLine: 2,
		},
		{
			fileLines: []string{"a", "b"},
			oldSeq:    []string{"c", "d"},
			start:     0,
			wantLen:   0, wantLine: -1,
		},
		{
			// The fast 500-line window ends between the two matching lines. The
			// fallback full scan should still recover the true 2-line run.
			fileLines: func() []string {
				lines := make([]string, 0, 501)
				for i := 0; i < 499; i++ {
					lines = append(lines, "x")
				}
				lines = append(lines, "a")
				lines = append(lines, "b")
				return lines
			}(),
			oldSeq:   []string{"a", "b"},
			start:    0,
			wantLen:  2,
			wantLine: 499,
		},
	}
	for _, tt := range tests {
		runLen, fileLine := longestContiguousRun(tt.fileLines, tt.oldSeq, tt.start)
		if runLen != tt.wantLen || fileLine != tt.wantLine {
			t.Errorf("longestContiguousRun(%v, %v, %d) = (%d, %d), want (%d, %d)",
				tt.fileLines, tt.oldSeq, tt.start, runLen, fileLine, tt.wantLen, tt.wantLine)
		}
	}
}

func TestNearestLineDiagnosticReportsIncompleteWindow(t *testing.T) {
	fileLines := make([]string, 0, 120)
	for i := 0; i < 105; i++ {
		fileLines = append(fileLines, "filler")
	}
	fileLines = append(fileLines, "prefix MISMATCH suffix")
	for i := len(fileLines); i < 120; i++ {
		fileLines = append(fileLines, "tail")
	}

	hint, complete := nearestLineDiagnostic(fileLines, []string{"prefix match suffix"}, 0, 100)
	if hint != "" {
		t.Fatalf("hint = %q, want empty because the best match is outside the fast window", hint)
	}
	if complete {
		t.Fatal("complete = true, want false for truncated fast window")
	}
}

func TestDiagnoseContiguousMatchNoExactMatches(t *testing.T) {
	// No exact line matches → analyzeContiguousMatch returns empty string,
	// falling through to hasTrimmedLineMatches.
	fileLines := []string{"  alpha  ", "  beta  "}
	oldSeq := []string{"gamma", "delta"}
	got := diagnoseMissingHunk(fileLines, oldSeq, 0)
	// Should fall through to the trimmed or generic message.
	if strings.Contains(got, "no longer exist") {
		t.Fatalf("diagnosis = %q, should not mention 'no longer exist' when no exact matches exist", got)
	}
}
