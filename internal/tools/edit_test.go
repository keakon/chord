package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditParserAcceptsSingleUpdate(t *testing.T) {
	parsed, err := ParseEdit("a/b.txt", "@@\n-old\n+new\n")
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if parsed.Path != filepath.Join("a", "b.txt") || len(parsed.Hunks) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestEditParserRejectsUnsupportedOperations(t *testing.T) {
	for _, patch := range []string{
		"*** Add File: a.txt\n+new\n",
		"*** Delete File: a.txt\n",
		"*** Move to: b.txt\n",
		"*** Update File: b.txt\n@@\n-a\n+b\n",
		"@@\n-a\n+b\n*** Update File: b.txt\n@@\n-c\n+d\n",
	} {
		_, err := ParseEdit("a.txt", patch)
		if err == nil || !strings.Contains(err.Error(), "No files were modified") {
			t.Fatalf("ParseEdit(%q) err = %v", patch, err)
		}
	}
}

func TestEditParserStripsEnvelopeMarkers(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch\n"
	parsed, err := ParseEdit("a.txt", patch)
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestEditParserStripsTrailingEndPatch(t *testing.T) {
	patch := "@@\n-old\n+new\n*** End Patch"
	parsed, err := ParseEdit("a.txt", patch)
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].Lines[1].Text != "new" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestEditParserKeepsEnvelopeLikeHunkContext(t *testing.T) {
	patch := "@@\n *** Begin Patch\n-old\n+new\n *** End Patch\n*** End Patch"
	parsed, err := ParseEdit("a.txt", patch)
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 4 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Hunks[0].Lines[0].Text != "*** Begin Patch" || parsed.Hunks[0].Lines[3].Text != "*** End Patch" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestEditParserTreatsBlankLinesAsContext(t *testing.T) {
	parsed, err := ParseEdit("a.txt", "@@\n a\n\n-b\n+B\n")
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if len(parsed.Hunks) != 1 || len(parsed.Hunks[0].Lines) != 4 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if parsed.Hunks[0].Lines[1].Kind != ' ' || parsed.Hunks[0].Lines[1].Text != "" {
		t.Fatalf("blank line not parsed as empty context line: %+v", parsed.Hunks[0].Lines)
	}
}

func TestEditToolMatchesBlankContextLine(t *testing.T) {
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
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("blank.txt")
	if string(got) != "a\n\nB\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditParserRejectsInvalidPaths(t *testing.T) {
	for _, path := range []string{"", "."} {
		if _, err := ParseEdit(path, "@@\n-a\n+b\n"); err == nil {
			t.Fatalf("ParseEdit accepted invalid path %q", path)
		}
	}
}

func TestEditParserAcceptsExternalPathForms(t *testing.T) {
	for _, path := range []string{filepath.Join("..", "a.txt"), filepath.Join("a", "..", "..", "b.txt"), filepath.Join(string(filepath.Separator), "tmp", "a.txt"), "~/a.txt"} {
		if _, err := ParseEdit(path, "@@\n-a\n+b\n"); err != nil {
			t.Fatalf("ParseEdit rejected external path form %q: %v", path, err)
		}
	}
}

func TestEditToolReplacesInsertsAndDeletes(t *testing.T) {
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
	out, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch") {
		t.Fatalf("output = %q", out)
	}
}

func TestEditToolIgnoresUnifiedDiffLineRangeHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nTWO\nthree\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditParserPreservesUnifiedDiffSectionHeader(t *testing.T) {
	parsed, err := ParseEdit("demo.go", "@@ -3,3 +3,4 @@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n")
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].Header != "func second() {" {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestEditParserPreservesUnifiedDiffSectionWhitespace(t *testing.T) {
	parsed, err := ParseEdit("demo.go", "@@ -3,3 +3,4 @@     func  second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n")
	if err != nil {
		t.Fatalf("ParseEdit: %v", err)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].Header != "    func  second() {" {
		t.Fatalf("header = %q", parsed.Hunks[0].Header)
	}
}

func TestEditToolUsesUnifiedDiffSectionHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.go")
	content := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -6,3 +6,4 @@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.go", "patch": patch})
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n\tprintln(\"y\")\n}\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestEditToolUsesIndentedUnifiedDiffSectionHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.py")
	content := "def first():\n    value = 1\n\ndef second():\n    value = 1\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -3,2 +3,3 @@     def second():\n     value = 1\n+    value = 2\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.py", "patch": patch})
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "def first():\n    value = 1\n\ndef second():\n    value = 1\n    value = 2\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestEditToolSupportsMultipleUnifiedDiffRangeHeaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n@@ -3,2 +3,3 @@\n three\n four\n+five\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "one\nTWO\nthree\nfour\nfive\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditToolPatchDescriptionEmphasizesPreferredFormat(t *testing.T) {
	params := (EditTool{}).Parameters()
	props := params["properties"].(map[string]any)
	patch := props["patch"].(map[string]any)
	desc := patch["description"].(string)
	for _, want := range []string{"Use direct @@ or @@ <verified header>", "Do not rely on unified diff line numbers or apply_patch wrappers", "Example:"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q: %q", want, desc)
		}
	}
}

func TestEditToolSupportsParentDirectoryPath(t *testing.T) {
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
	out, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch") {
		t.Fatalf("output = %q", out)
	}
}

func TestEditToolUsesHunkHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.go")
	content := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.go", "patch": patch})
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n\tprintln(\"y\")\n}\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestEditAmbiguousWeakContextAppliesFirstMatch(t *testing.T) {
	got, err := applyParsedPatch("func a() {\n}\n\nfunc b() {\n}\n", parsedEdit{
		Path: "demo.go",
		Hunks: []editHunk{{Lines: []editLine{
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

func TestEditToolDiagnosesMissingHunkLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n 1\talpha\n-2\tbeta\n+gamma\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	_, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "tool-added read metadata, copied line numbers, or a tab separator") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditToolDiagnosesMissingHunkReadResultHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n READ_RESULT lines=1-2 total=2\n-alpha\n+gamma\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	_, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "remove the READ_RESULT line") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditToolDiagnosesMissingHunkWhitespace(t *testing.T) {
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

func TestEditToolDiagnosesMissingHunkStaleText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-gamma\n+delta\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})
	_, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "not found in the current file") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditToolDiagnosesMissingHunkHeaderAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo_test.go")
	if err := os.WriteFile(path, []byte("package demo\n\nfunc TestActual(t *testing.T) {\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@ func TestImagined(t *testing.T) {\n }\n+// added\n"
	args, _ := json.Marshal(map[string]string{"path": "demo_test.go", "patch": patch})
	_, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "@@ header anchor \"TestImagined\"") || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditToolWhitespaceAndUnicodeTolerance(t *testing.T) {
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
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "alpha   \nquote \"y\"\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditToolPreservesCRLF(t *testing.T) {
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
	if _, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "ONE\r\ntwo\r\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditToolAmbiguousHunkAppliesFirstMatchWithNote(t *testing.T) {
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
	out, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("EditTool.Execute: %v", err)
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

func TestEditToolErrorIncludesPatchExcerptForHunkMatchFailures(t *testing.T) {
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
	out, err := (EditTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"hunk not found", "Patch excerpt:", "```diff", "-missing", "+new"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want substring %q", out, want)
		}
	}
}

func TestEditToolConcurrencyPolicyUsesPatchPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "@@\n-old\n+new\n"
	args, _ := json.Marshal(map[string]string{"path": "demo.txt", "patch": patch})

	policy := (EditTool{BaseDir: dir}).ConcurrencyPolicy(args)
	if policy.Mode != ConcurrencyModeWrite || policy.Resource != "file:demo.txt" {
		t.Fatalf("policy = %+v, want write file:demo.txt", policy)
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
	}
	for _, tt := range tests {
		runLen, fileLine := longestContiguousRun(tt.fileLines, tt.oldSeq, tt.start)
		if runLen != tt.wantLen || fileLine != tt.wantLine {
			t.Errorf("longestContiguousRun(%v, %v, %d) = (%d, %d), want (%d, %d)",
				tt.fileLines, tt.oldSeq, tt.start, runLen, fileLine, tt.wantLen, tt.wantLine)
		}
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
