package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadToolFileNotFoundSuggestsSiblingPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "responses.go"), []byte("package test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(`{"path":%q}`, filepath.Join(dir, "response.go")))
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want file-not-found error")
	}
	for _, want := range []string{
		"file not found:",
		"Did you mean:",
		filepath.Join(dir, "responses.go"),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ReadTool.Execute err = %v, want substring %q", err, want)
		}
	}
}

func TestReadToolFileNotFoundSuggestsWhitespaceRepair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "alpha", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(`{"path":"~/ alpha/file.txt"}`)
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want file-not-found error with whitespace repair suggestion")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Did you mean") {
		t.Fatalf("error missing \"Did you mean\": %s", msg)
	}
	if !strings.Contains(msg, filepath.ToSlash(filepath.Join("~", "alpha", "file.txt"))) {
		t.Fatalf("error missing home-relative repaired suggestion: %s", msg)
	}
}

func TestReadToolFileNotFoundSuggestsWhitespaceRepairFromBaseDir(t *testing.T) {
	base := t.TempDir()
	other := t.TempDir()
	target := filepath.Join(base, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(other); err != nil {
		t.Fatalf("Chdir other: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	raw := json.RawMessage(`{"path":"src /main.go"}`)
	_, err = (ReadTool{BaseDir: base}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want file-not-found error with base-dir whitespace repair suggestion")
	}
	msg := err.Error()
	for _, want := range []string{
		"file not found: src /main.go",
		"Did you mean",
		"- src/main.go",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("ReadTool.Execute err = %v, want substring %q", err, want)
		}
	}
	if strings.Contains(msg, base) || strings.Contains(msg, other) {
		t.Fatalf("ReadTool.Execute err = %v, want no absolute base/cwd paths", err)
	}
}

func TestSuggestWhitespacePathRepairHandlesVariousSpacePositions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "alpha", "beta", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	want := filepath.ToSlash(filepath.Join("~", "alpha", "beta", "file.txt"))
	cases := []string{
		"~/ alpha/beta/file.txt", // space right after ~/
		"~ /alpha/beta/file.txt", // space between ~ and separator
		"~/alpha /beta/file.txt", // space before a separator
		"~/alpha/ beta/file.txt", // space after a separator
		"~/alp ha/beta/file.txt", // space inside a token
	}
	for _, broken := range cases {
		got, ok := suggestWhitespacePathRepair(broken, PathTargetRegularFile)
		if !ok {
			t.Errorf("%q: want repair, got none", broken)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", broken, got, want)
		}
	}
}

func TestSuggestWhitespacePathRepairNoMatchWhenRepairedAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No space and no file: nothing to repair.
	if _, ok := suggestWhitespacePathRepair("~/alpha/beta/file.txt", PathTargetRegularFile); ok {
		t.Fatal("want no repair for path without spaces")
	}
	// Space present but the de-spaced path does not exist either: no false match.
	if got, ok := suggestWhitespacePathRepair("~/alp ha/beta/file.txt", PathTargetRegularFile); ok {
		t.Fatalf("want no repair when repaired path is absent, got %q", got)
	}
	// A legitimately spaced path that exists as typed is never reached by repair
	// (read succeeds), but if the spaced form is absent and the de-spaced form is
	// also absent, repair must stay silent rather than inventing a path.
	legit := filepath.Join(home, "with space", "file.txt")
	if err := os.MkdirAll(filepath.Dir(legit), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(legit, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Removing the space from the legitimate component yields a non-existent path.
	if got, ok := suggestWhitespacePathRepair("~/withspace/file.txt", PathTargetRegularFile); ok {
		t.Fatalf("want no repair to non-existent de-spaced path, got %q", got)
	}
}

func TestReadToolFileNotFoundDisplaysCurrentDirectoryRelativePaths(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	toolsDir := filepath.Join(dir, "internal", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "ignore.go"), []byte("package tools\n"), 0o644); err != nil {
		t.Fatalf("WriteFile ignore.go: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir temp dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	missingPath := filepath.Join(dir, "internal", "tools", "gitignore.go")
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q}`, missingPath))
	_, err = (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want file-not-found error")
	}
	msg := err.Error()
	for _, want := range []string{
		"file not found: " + filepath.Join("internal", "tools", "gitignore.go"),
		"Did you mean:",
		"- " + filepath.Join("internal", "tools", "ignore.go"),
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("ReadTool.Execute err = %v, want substring %q", err, want)
		}
	}
	for _, notWant := range []string{missingPath, filepath.Join(toolsDir, "ignore.go")} {
		if strings.Contains(msg, notWant) {
			t.Fatalf("ReadTool.Execute err = %v, want no absolute path %q", err, notWant)
		}
	}
}

func TestPathSuggestionsFindsDirectoryTypoWithinBoundedAncestor(t *testing.T) {
	dir := t.TempDir()
	goodDir := filepath.Join(dir, "internal", "llm")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	goodPath := filepath.Join(goodDir, "responses.go")
	if err := os.WriteFile(goodPath, []byte("package llm\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	missingPath := filepath.Join(dir, "internal", "lmm", "responses.go")
	got := suggestExistingToolPathsWithOptions(missingPath, PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) == 0 || got[0] != goodPath {
		t.Fatalf("suggestExistingToolPathsWithOptions() = %#v, want first %q", got, goodPath)
	}
}

func TestPathSuggestionsFindsTopLevelRelativeTypoFromProjectRoot(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	goodDir := filepath.Join(dir, "internal", "llm")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "responses.go"), []byte("package llm\n"), 0o644); err != nil {
		t.Fatalf("WriteFile responses.go: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir temp dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	got := suggestExistingToolPathsWithOptions("internl/llm/responses.go", PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) == 0 || got[0] != filepath.Join("internal", "llm", "responses.go") {
		t.Fatalf("suggestExistingToolPathsWithOptions() = %#v, want relative internal/llm/responses.go first", got)
	}
}

func TestPathSuggestionsFindsRelativeTypoFromBaseDir(t *testing.T) {
	base := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "go.mod"), []byte("module example.test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}
	goodDir := filepath.Join(base, "internal", "llm")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "responses.go"), []byte("package llm\n"), 0o644); err != nil {
		t.Fatalf("WriteFile responses.go: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(other); err != nil {
		t.Fatalf("Chdir other: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got := suggestExistingToolPathsWithOptionsInDir("internl/llm/responses.go", base, PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) == 0 || got[0] != "internal/llm/responses.go" {
		t.Fatalf("suggestExistingToolPathsWithOptionsInDir() = %#v, want relative internal/llm/responses.go first", got)
	}
}

func TestPathSuggestionsDoNotWalkPlainCurrentDirectory(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "llm"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "llm", "responses.go"), []byte("package llm\n"), 0o644); err != nil {
		t.Fatalf("WriteFile responses.go: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir temp dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	got := suggestExistingToolPathsWithOptions("internl/llm/responses.go", PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) != 0 {
		t.Fatalf("suggestExistingToolPathsWithOptions() = %#v, want no scan from unmarked cwd", got)
	}
}

func TestPathSuggestionsSkipIgnoredDirectories(t *testing.T) {
	dir := t.TempDir()
	ignoredDir := filepath.Join(dir, "node_modules")
	if err := os.MkdirAll(ignoredDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ignoredDir, "responses.go"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	missingPath := filepath.Join(dir, "internal", "llm", "responses.go")
	got := suggestExistingToolPathsWithOptions(missingPath, PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) != 0 {
		t.Fatalf("suggestExistingToolPathsWithOptions() = %#v, want no ignored-dir suggestions", got)
	}
}

func TestPathSuggestionsOmitLowConfidenceCandidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := suggestExistingToolPathsWithOptions(filepath.Join(dir, "responses.go"), PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) != 0 {
		t.Fatalf("suggestExistingToolPathsWithOptions() = %#v, want no low-confidence suggestions", got)
	}
}

func TestPathSuggestionsRejectDistantSimilarBasename(t *testing.T) {
	dir := t.TempDir()
	// A distant file whose stem matches but extension differs, with no shared
	// trailing parent segment: this is the home-wide walk noise pattern.
	farDir := filepath.Join(dir, "x", "y", "z")
	if err := os.MkdirAll(farDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(farDir, "data.bak"), []byte("data\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := suggestExistingToolPathsWithOptions(filepath.Join(dir, "a", "b", "data.txt"), PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 3,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) != 0 {
		t.Fatalf("suggestExistingToolPathsWithOptions() = %#v, want no distant basename-only suggestions", got)
	}
}

func TestPathSuggestionsRespectCandidateLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("response_%d.go", i)), []byte("package test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	got := suggestExistingToolPathsWithOptions(filepath.Join(dir, "responses.go"), PathTargetRegularFile, pathSuggestionOptions{
		Timeout:        time.Second,
		MaxVisited:     100,
		MaxCandidates:  100,
		MaxSuggestions: 2,
		MinScore:       pathSuggestionMinScore,
	})
	if len(got) != 2 {
		t.Fatalf("suggestExistingToolPathsWithOptions() len = %d, want 2; got %#v", len(got), got)
	}
}

func TestEditToolFileNotFoundSuggestsWhitespaceRepair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "alpha", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"path": "~/ alpha/file.txt", "old_string": "a", "new_string": "b"})
	_, err := (EditTool{}).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("EditTool.Execute err = nil, want file-not-found with whitespace repair suggestion")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Did you mean") {
		t.Fatalf("error missing \"Did you mean\": %s", msg)
	}
	if !strings.Contains(msg, filepath.ToSlash(filepath.Join("~", "alpha", "file.txt"))) {
		t.Fatalf("error missing home-relative repaired suggestion: %s", msg)
	}
}

func TestPatchPlanFileNotFoundSuggestsWhitespaceRepair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "alpha", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Minimal patch text: a single hunk. The target file does not exist as typed
	// (a misplaced space), so planning must fail with a repair suggestion rather
	// than a bare not-found.
	patchText := "@@\n content\n+changed\n"
	_, err := BuildPatchPlanInDirWithContext(context.Background(), "~/ alpha/file.txt", patchText, "")
	if err == nil {
		t.Fatal("BuildPatchPlanInDirWithContext err = nil, want file-not-found with whitespace repair suggestion")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Did you mean") {
		t.Fatalf("error missing \"Did you mean\": %s", msg)
	}
	if !strings.Contains(msg, "Use write to create files") {
		t.Fatalf("error missing patch guidance: %s", msg)
	}
	if !strings.Contains(msg, filepath.ToSlash(filepath.Join("~", "alpha", "file.txt"))) {
		t.Fatalf("error missing home-relative repaired suggestion: %s", msg)
	}
}
