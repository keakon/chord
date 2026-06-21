package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestGlobParametersDeclareRequiredPatterns(t *testing.T) {
	params := GlobTool{}.Parameters()
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("required has type %T, want []string", params["required"])
	}
	found := false
	for _, name := range required {
		if name == "patterns" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("glob schema must declare patterns as required, got %v", required)
	}
}

func TestValidateGlobArgsRejectsEmptyArgs(t *testing.T) {
	if err := ValidateToolArgs(GlobTool{}, json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "patterns is required") {
		t.Fatalf("ValidateToolArgs on empty args should report patterns required, got %v", err)
	}
}

func TestValidateGlobArgsAcceptsScalarPatternsViaSchemaCoerce(t *testing.T) {
	if err := ValidateToolArgs(GlobTool{}, json.RawMessage(`{"patterns":"**/*.go"}`)); err != nil {
		t.Fatalf("schema-level coerceFromString should accept scalar patterns, got %v", err)
	}
}

func TestValidateGlobArgsAcceptsLegacyPatternField(t *testing.T) {
	if err := ValidateToolArgs(GlobTool{}, json.RawMessage(`{"pattern":"**/*.go"}`)); err != nil {
		t.Fatalf("legacy singular pattern field should validate via alias, got %v", err)
	}
}

func TestGlobExecutesWithLegacyPatternField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"pattern":"**/*.go","path":"` + dir + `"}`)
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute with legacy pattern field: %v", err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("expected a.go in results, got:\n%s", out)
	}
}

func TestGlobAcceptsScalarPatternsWithCoerceNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"patterns":"**/*.go","path":"` + dir + `"}`)
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "patterns was a single string") {
		t.Fatalf("scalar coerce note missing:\n%s", out)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("expected a.go in results, got:\n%s", out)
	}
}

func TestGlobArrayPatternsDoNotEmitCoerceNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"**/*.go"}, "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "single string") {
		t.Fatalf("array form must not emit coerce note:\n%s", out)
	}
}

func TestGlobMultiplePatternsDeduplicateResults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"**/*.go", "**/a.*"}, "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Count(out, "a.go") != 1 {
		t.Fatalf("expected single a.go entry across overlapping patterns, got:\n%s", out)
	}
}

// TestGlobExactPatternFastPath avoids walking a directory when the pattern is a
// plain relative file path. The sibling must not appear in results.
func TestGlobExactPatternFastPath(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "src", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "target.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "decoy.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{"patterns": []string{"src/deep/target.go"}, "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "src/deep/target.go") {
		t.Fatalf("expected fast-path match, got:\n%s", out)
	}
	if strings.Contains(out, "decoy.go") {
		t.Fatalf("fast path must not walk siblings, got:\n%s", out)
	}
}

// TestGlobExactPatternFastPathMissesGracefully returns "no files matched" when
// the named relative path does not exist, without walking the directory.
func TestGlobExactPatternFastPathMissesGracefully(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "there.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"missing.go"}, "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "No files matched the pattern") {
		t.Fatalf("expected no-match message, got:\n%s", out)
	}
}

// TestGlobBroadSearchGuardAborts scans a temp root (which isBroadSearchRoot
// treats as broad) with a tight visited threshold so the guard fires without
// creating a huge directory tree.
func TestGlobBroadSearchGuardAborts(t *testing.T) {
	dir := t.TempDir()
	// Populate enough entries to exceed the tightened threshold. Only empty
	// directories exist, so the file-matching candidates stay 0.
	for i := range 32 {
		if err := os.MkdirAll(filepath.Join(dir, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prev := broadSearchLimits
	broadSearchLimits.visitedEntries = 8
	broadSearchLimits.minCandidates = 1_000
	defer func() { broadSearchLimits = prev }()

	raw, _ := json.Marshal(map[string]any{"patterns": []string{"**/*"}, "path": dir})
	_, err := GlobTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected broad-search abort error")
	}
	for _, want := range []string{"search aborted", "Glob", "narrow the search path"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestGlobBroadSearchGuardAbortsSparseMatches covers patterns that match few or
// no files. doublestar.GlobWalk only calls its callback for matches, so the
// guard must count real traversal entries instead of matched entries.
func TestGlobBroadSearchGuardAbortsSparseMatches(t *testing.T) {
	dir := t.TempDir()
	for i := range 32 {
		if err := os.MkdirAll(filepath.Join(dir, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "d"+strconv.Itoa(i), "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	prev := broadSearchLimits
	broadSearchLimits.visitedEntries = 8
	broadSearchLimits.minCandidates = 1_000
	defer func() { broadSearchLimits = prev }()

	raw, _ := json.Marshal(map[string]any{"patterns": []string{"**/*.html"}, "path": dir})
	_, err := GlobTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected broad-search abort error")
	}
	for _, want := range []string{"search aborted", "Glob", "**/*.html", "narrow the search path"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestGrepBroadSearchGuardAborts mirrors the glob guard test for Grep, using an
// exact include on a broad temp root with a tight visited threshold.
func TestGrepBroadSearchGuardAborts(t *testing.T) {
	dir := t.TempDir()
	for i := range 32 {
		if err := os.MkdirAll(filepath.Join(dir, "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prev := broadSearchLimits
	broadSearchLimits.visitedEntries = 8
	broadSearchLimits.minCandidates = 1_000
	defer func() { broadSearchLimits = prev }()

	// Use a glob include so the exact fast path does not kick in; the walk then
	// runs and the guard fires.
	raw, _ := json.Marshal(map[string]any{
		"pattern":  "needle",
		"paths":    []string{dir},
		"includes": []string{"**/architecture-review-20260621-064007.html"},
	})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected broad-search abort error")
	}
	for _, want := range []string{"search aborted", "Grep", "narrow the search path"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestGrepExactIncludeFastPathDoesNotTriggerGuard ensures the fast path runs
// even inside a broad root (temp dir) and returns immediately, instead of
// walking into the guard.
func TestGrepExactIncludeFastPathDoesNotTriggerGuard(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.html"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prev := broadSearchLimits
	broadSearchLimits.visitedEntries = 2
	broadSearchLimits.minCandidates = 1
	defer func() { broadSearchLimits = prev }()

	raw, _ := json.Marshal(map[string]any{
		"pattern":  "needle",
		"paths":    []string{dir},
		"includes": []string{"target.html"},
	})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("fast path must not trigger guard, got: %v", err)
	}
	if !strings.Contains(out, "target.html") {
		t.Fatalf("expected fast-path match, got:\n%s", out)
	}
}
