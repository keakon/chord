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

func TestEditAmbiguousWeakContextAppliesFirstMatchWithHint(t *testing.T) {
	got, matches, err := applyParsedPatch("func a() {\n}\n\nfunc b() {\n}\n", parsedEdit{
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
	if len(matches) != 1 || matches[0].Line != 2 || !matches[0].WeakContext || len(matches[0].CandidateLines) != 2 {
		t.Fatalf("matches = %+v", matches)
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
	if err == nil || !strings.Contains(err.Error(), "line numbers or the tab separator") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditToolDiagnosesMissingHunkWhitespace(t *testing.T) {
	got := diagnoseMissingHunk([]string{"\treturn nil"}, []string{"return nil"}, 0)
	if !strings.Contains(got, "exact indentation") {
		t.Fatalf("diagnosis = %q", got)
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
	if !strings.Contains(out, "Matched hunk near line(s): 1") || !strings.Contains(out, "matched multiple locations") || !strings.Contains(out, "Other candidate line(s): 2") {
		t.Fatalf("output = %q", out)
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
