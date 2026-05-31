package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchParserAcceptsSingleUpdate(t *testing.T) {
	parsed, err := ParseApplyPatch("*** Begin Patch\n*** Update File: a/b.txt\n@@\n-old\n+new\n*** End Patch\n")
	if err != nil {
		t.Fatalf("ParseApplyPatch: %v", err)
	}
	if parsed.Path != filepath.Join("a", "b.txt") || len(parsed.Hunks) != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestApplyPatchParserRejectsUnsupportedOperations(t *testing.T) {
	for _, patch := range []string{
		"*** Begin Patch\n*** Add File: a.txt\n+new\n*** End Patch\n",
		"*** Begin Patch\n*** Delete File: a.txt\n*** End Patch\n",
		"*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n*** End Patch\n",
		"*** Begin Patch\n*** Update File: a.txt\n@@\n-a\n+b\n*** Update File: b.txt\n@@\n-c\n+d\n*** End Patch\n",
	} {
		_, err := ParseApplyPatch(patch)
		if err == nil || !strings.Contains(err.Error(), "No files were modified") {
			t.Fatalf("ParseApplyPatch(%q) err = %v", patch, err)
		}
	}
}

func TestApplyPatchParserRejectsInvalidPaths(t *testing.T) {
	for _, path := range []string{"", "."} {
		patch := "*** Begin Patch\n*** Update File: " + path + "\n@@\n-a\n+b\n*** End Patch\n"
		if _, err := ParseApplyPatch(patch); err == nil {
			t.Fatalf("ParseApplyPatch accepted invalid path %q", path)
		}
	}
}

func TestApplyPatchParserAcceptsExternalPathForms(t *testing.T) {
	for _, path := range []string{filepath.Join("..", "a.txt"), filepath.Join("a", "..", "..", "b.txt"), filepath.Join(string(filepath.Separator), "tmp", "a.txt"), "~/a.txt"} {
		patch := "*** Begin Patch\n*** Update File: " + path + "\n@@\n-a\n+b\n*** End Patch\n"
		if _, err := ParseApplyPatch(patch); err != nil {
			t.Fatalf("ParseApplyPatch rejected external path form %q: %v", path, err)
		}
	}
}

func TestApplyPatchToolReplacesInsertsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n one\n-two\n+TWO\n three\n@@\n three\n+four\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ApplyPatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "one\nTWO\nthree\nfour\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch") {
		t.Fatalf("output = %q", out)
	}
}

func TestApplyPatchToolSupportsParentDirectoryPath(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "repo")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: ../outside.txt\n@@\n-old\n+new\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ApplyPatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "new\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Applied patch") {
		t.Fatalf("output = %q", out)
	}
}

func TestApplyPatchToolUsesHunkHeaderAsAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.go")
	content := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: demo.go\n@@ func second() {\n \tprintln(\"x\")\n+\tprintln(\"y\")\n }\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("ApplyPatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "package demo\n\nfunc first() {\n\tprintln(\"x\")\n}\n\nfunc second() {\n\tprintln(\"x\")\n\tprintln(\"y\")\n}\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestApplyPatchAmbiguousWeakContextAppliesFirstMatchWithHint(t *testing.T) {
	got, matches, err := applyParsedPatch("func a() {\n}\n\nfunc b() {\n}\n", parsedApplyPatch{
		Path: "demo.go",
		Hunks: []applyPatchHunk{{Lines: []applyPatchLine{
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

func TestApplyPatchToolWhitespaceAndUnicodeTolerance(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("alpha   \nquote “x”\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n alpha\n-quote \"x\"\n+quote \"y\"\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("ApplyPatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "alpha   \nquote \"y\"\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyPatchToolPreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("one\r\ntwo\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n-one\n+ONE\n two\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args); err != nil {
		t.Fatalf("ApplyPatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "ONE\r\ntwo\r\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyPatchToolAmbiguousHunkAppliesFirstMatchWithNote(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.WriteFile("demo.txt", []byte("same\nsame\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n-same\n+changed\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("ApplyPatchTool.Execute: %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "changed\nsame\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "Matched hunk near line(s): 1") || !strings.Contains(out, "matched multiple locations") || !strings.Contains(out, "Other candidate line(s): 2") {
		t.Fatalf("output = %q", out)
	}
}

func TestApplyPatchToolConcurrencyPolicyUsesPatchPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: demo.txt\n@@\n-old\n+new\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patch})

	policy := (ApplyPatchTool{BaseDir: dir}).ConcurrencyPolicy(args)
	if policy.Mode != ConcurrencyModeWrite || policy.Resource != "file:demo.txt" {
		t.Fatalf("policy = %+v, want write file:demo.txt", policy)
	}
}
