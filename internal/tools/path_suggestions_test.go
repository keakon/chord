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
