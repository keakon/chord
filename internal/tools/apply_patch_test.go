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

func TestApplyPatchParserRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"", "/tmp/a.txt", "~/a.txt", "../a.txt", "a/../../b.txt"} {
		patch := "*** Begin Patch\n*** Update File: " + path + "\n@@\n-a\n+b\n*** End Patch\n"
		if _, err := ParseApplyPatch(patch); err == nil {
			t.Fatalf("ParseApplyPatch accepted unsafe path %q", path)
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

func TestApplyPatchToolAmbiguousHunkDoesNotWrite(t *testing.T) {
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
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "not unique") || !strings.Contains(err.Error(), "line(s): 1, 2") || !strings.Contains(err.Error(), "same @@ hunk") || !strings.Contains(err.Error(), "separate earlier @@ hunk does not anchor") {
		t.Fatalf("err = %v", err)
	}
	got, _ := os.ReadFile("demo.txt")
	if string(got) != "same\nsame\n" {
		t.Fatalf("file modified: %q", got)
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
