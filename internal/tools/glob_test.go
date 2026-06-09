package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
