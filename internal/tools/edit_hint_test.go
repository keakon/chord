package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditToolDescriptionGuidesReadBeforeRetryAndSmallUniqueBlocks(t *testing.T) {
	desc := (EditTool{}).Description()
	for _, want := range []string{
		"Read the file first",
		"re-read it before retrying after any mismatch or other change",
		"quote characters",
		"smallest unique 2-4 line block",
		"replace_all",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
}

func TestEditToolNotFoundErrorUsesSnakeCaseAndIndentHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	content := "\tif true {\n\t\tfmt.Println(\"hi\")\n\t}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"path":       path,
		"old_string": "  if true {\n\t\tfmt.Println(\"hi\")\n\t}",
		"new_string": "  if false {\n\t\tfmt.Println(\"hi\")\n\t}",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	_, err = (EditTool{}).Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "old_string not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(msg, "oldString") {
		t.Fatalf("error should not use camelCase parameter names: %v", err)
	}
	if !strings.Contains(msg, "Indentation mismatch") {
		t.Fatalf("expected indentation hint, got: %v", err)
	}
}

func TestEditToolErrorsUseSnakeCaseParameterNames(t *testing.T) {
	t.Run("identical old and new", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "same.txt")
		if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		args, err := json.Marshal(map[string]any{
			"path":       path,
			"old_string": "hello",
			"new_string": "hello",
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		_, err = (EditTool{}).Execute(context.Background(), args)
		if err == nil {
			t.Fatal("expected error")
		}
		want := "old_string and new_string are identical, no change needed"
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("multiple matches mention replace_all", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multi.txt")
		if err := os.WriteFile(path, []byte("dup\ndup\n"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		args, err := json.Marshal(map[string]any{
			"path":       path,
			"old_string": "dup",
			"new_string": "done",
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		_, err = (EditTool{}).Execute(context.Background(), args)
		if err == nil {
			t.Fatal("expected error")
		}
		want := "old_string found 2 times, provide more context or set replace_all"
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})
}

func TestBuildEditOldStringNotFoundHintMultilineIndentationWithTrailingNewline(t *testing.T) {
	fileText := "\tif true {\n\t\tfmt.Println(\"hi\")\n\t}\n"
	oldText := "  if true {\n\t\tfmt.Println(\"hi\")\n\t}\n"

	hint := buildEditOldStringNotFoundHint(fileText, oldText)
	if !strings.Contains(hint, "Indentation mismatch") {
		t.Fatalf("hint = %q, want indentation mismatch", hint)
	}
}

func TestBuildEditOldStringNotFoundHintReadGutter(t *testing.T) {
	fileText := "alpha\nbeta\n"
	oldText := "     1\talpha\n     2\tbeta"

	hint := buildEditOldStringNotFoundHint(fileText, oldText)
	if !strings.Contains(hint, "Read output gutter included") {
		t.Fatalf("hint = %q, want gutter mismatch", hint)
	}
}

func TestBuildEditOldStringNotFoundHintReadGutterWithTrailingNewline(t *testing.T) {
	fileText := "alpha\nbeta\n"
	oldText := "     1\talpha\n     2\tbeta\n"

	hint := buildEditOldStringNotFoundHint(fileText, oldText)
	if !strings.Contains(hint, "Read output gutter included") {
		t.Fatalf("hint = %q, want gutter mismatch", hint)
	}
}

func TestBuildEditOldStringNotFoundHintLineEndingMismatch(t *testing.T) {
	fileText := "alpha\r\nbeta\r\n"
	oldText := "alpha\nbeta\n"

	hint := buildEditOldStringNotFoundHint(fileText, oldText)
	if !strings.Contains(hint, "Line-ending mismatch") {
		t.Fatalf("hint = %q, want line-ending mismatch", hint)
	}
}

func TestBuildEditOldStringNotFoundHintQuoteMismatch(t *testing.T) {
	fileText := "say \"hello\"\n"
	oldText := "say “hello”\n"

	hint := buildEditOldStringNotFoundHint(fileText, oldText)
	if !strings.Contains(hint, "Quote mismatch") {
		t.Fatalf("hint = %q, want quote mismatch", hint)
	}
}

func TestBuildEditOldStringNotFoundHintTrimmedUniqueMatch(t *testing.T) {
	fileText := "alpha\n"
	oldText := "\n  alpha\t\n"

	hint := buildEditOldStringNotFoundHint(fileText, oldText)
	if !strings.Contains(hint, "Leading/trailing whitespace mismatch") {
		t.Fatalf("hint = %q, want trim mismatch", hint)
	}
}

func TestBuildEditOldStringNotFoundHintGenericFallback(t *testing.T) {
	hint := buildEditOldStringNotFoundHint("alpha\nbeta\n", "missing")
	for _, want := range []string{
		"Ensure old_string matches raw file text exactly",
		"do not include the displayed line-number gutter",
		"Re-read the smallest unique block before retrying",
		"set replace_all",
	} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint = %q, want substring %q", hint, want)
		}
	}
}

func TestBuildEditOldStringNotFoundHintTrimMismatchRequiresUniqueMatch(t *testing.T) {
	hint := buildEditOldStringNotFoundHint("alpha\nalpha\n", "  alpha\n")
	if strings.Contains(hint, "Leading/trailing whitespace mismatch") {
		t.Fatalf("hint should stay conservative for non-unique trim matches: %q", hint)
	}
	if !strings.Contains(hint, "Ensure old_string matches raw file text exactly") {
		t.Fatalf("hint = %q, want generic fallback", hint)
	}
}
