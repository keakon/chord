package tools

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- textclean.StripOrphanVariationSelectors unit tests ---

// --- Integration: edit tool strips orphans from arguments before matching ---

func TestEditToolStripsOrphanVariationSelectorsFromOldString(t *testing.T) {
	dir := t.TempDir()
	// File uses tab indentation (matches real Go code).
	content := "package main\n\nfunc foo() int {\n\treturn 0, false\n}\n"
	path := writeEditFixture(t, dir, "foo.go", content)

	// Model emits orphaned FE0F inside the old_string (the real bug report).
	// After stripping, oldStr must exactly match a substring of the file.
	oldStrWithOrphan := "return \ufe0f0, false\n}\n"
	newStr := "return 1, true\n}\n"

	result, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": oldStrWithOrphan,
		"new_string": newStr,
	})
	if err != nil {
		t.Fatalf("edit failed with orphaned FE0F in old_string: %v\nresult: %s", err, result)
	}
	got := readFileString(t, path)
	want := "package main\n\nfunc foo() int {\n\treturn 1, true\n}\n"
	if got != want {
		t.Errorf("file content =\n%s\nwant:\n%s", got, want)
	}
}

func TestEditToolStripsOrphanVariationSelectorsFromBothStrings(t *testing.T) {
	dir := t.TempDir()
	// File content uses simple text that matches what the stripped old_string will be.
	content := "\"at line  12\" for a single hit\"\n"
	path := writeEditFixture(t, dir, "notes.txt", content)

	// Both strings contain orphans. After stripping:
	//   old: '"at line  12" for a single hit"'  (matches line 1 of file)
	//   new: '"at lines  12,  45" for many"'
	oldStrWithOrphan := "\"at line  \ufe0f12\" for a single hit\""
	newStrWithOrphan := "\"at lines  \ufe0f12,  \ufe0f45\" for many\""

	result, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": oldStrWithOrphan,
		"new_string": newStrWithOrphan,
	})
	if err != nil {
		t.Fatalf("edit failed: %v\nresult: %s", err, result)
	}
	got := readFileString(t, path)
	want := "\"at lines  12,  45\" for many\"\n"
	if got != want {
		t.Errorf("file content =\n%s\nwant:\n%s", got, want)
	}
}

// readFileString reads a file's content as a string for test assertions.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := readFileForEdit(path, path, "", "test")
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data.Decoded.Text
}

// --- Integration: apply_patch strips orphans before writing added lines ---

func TestApplyPatchStripsOrphanSelectorsFromAddedLines(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: note.txt\n+count is \ufe0f42 done\n+warning \u26a0\ufe0f stays\n"
	if _, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch)); err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}
	got := readFileString(t, filepath.Join(dir, "note.txt"))
	want := "count is 42 done\nwarning \u26a0\ufe0f stays\n"
	if got != want {
		t.Errorf("file content = %q; want %q", got, want)
	}
}

// An old_string made only of orphaned selectors strips to the empty string,
// which would match everywhere: replace_all would splice new_string between
// every rune while reporting success, and plain replace reports a meaningless
// "found N times". The empty-after-strip guard must reject it and leave the
// file untouched.
func TestEditToolRejectsSelectorOnlyOldString(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "guard.txt", "hello world\n")
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"replace_all", map[string]any{"replace_all": true}},
		{"single", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{
				"path":       path,
				"old_string": "\ufe0f",
				"new_string": "X",
			}
			maps.Copy(args, tc.extra)
			_, err := runEdit(t, dir, args)
			if err == nil {
				t.Fatalf("edit with selector-only old_string must fail")
			}
			if !strings.Contains(err.Error(), "only invisible characters") {
				t.Fatalf("error = %v, want the only-invisible-characters message", err)
			}
			if got := readFileString(t, path); got != "hello world\n" {
				t.Fatalf("file was modified: %q", got)
			}
		})
	}
}

// A new_string made only of orphaned selectors would turn the replacement
// into a deletion on unknowable intent. A genuinely empty new_string is a
// legitimate deletion and must stay allowed.
func TestEditToolRejectsSelectorOnlyNewString(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "guard.txt", "hello world\n")
	_, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "hello",
		"new_string": "\ufe0f",
	})
	if err == nil {
		t.Fatalf("edit with selector-only new_string must fail")
	}
	if !strings.Contains(err.Error(), "only invisible characters") {
		t.Fatalf("error = %v, want the only-invisible-characters message", err)
	}
	if got := readFileString(t, path); got != "hello world\n" {
		t.Fatalf("file was modified: %q", got)
	}
	// Deletion via an empty new_string keeps working.
	if _, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "hello ",
		"new_string": "",
	}); err != nil {
		t.Fatalf("empty new_string deletion failed: %v", err)
	}
	if got := readFileString(t, path); got != "world\n" {
		t.Fatalf("file = %q, want %q", got, "world\n")
	}
}

// Control characters in new_string would land in the file verbatim; the tool
// must reject them and point at the shell/script channel instead.
func TestEditToolRejectsControlCharactersInNewString(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "ctrl.txt", "hello world\n")
	_, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "hello",
		"new_string": "he\x00llo",
	})
	if err == nil {
		t.Fatalf("edit with a NUL byte in new_string must fail")
	}
	if !strings.Contains(err.Error(), "control character") || !strings.Contains(err.Error(), "shell command") {
		t.Fatalf("error = %v, want the control-character guidance", err)
	}
	if got := readFileString(t, path); got != "hello world\n" {
		t.Fatalf("file was modified: %q", got)
	}
}

// Both sides are cleaned and the success message reports the total so the
// model learns its output channel leaks invisible characters.
func TestEditToolCleansSelectorsOnBothSidesAndReports(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "both.txt", "hello world\n")
	out, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "hello \ufe0fworld\n",
		"new_string": "big \ufe0fworld\n",
	})
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	if !strings.Contains(out, "cleaned 2 invisible character") {
		t.Fatalf("output = %q, want the both-sides cleaned count", out)
	}
	if got := readFileString(t, path); got != "big world\n" {
		t.Fatalf("file = %q, want %q", got, "big world\n")
	}
}

// --- Integration: write cleans content and rejects what it cannot clean ---

func runWrite(t *testing.T, dir string, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return (WriteTool{BaseDir: dir}).Execute(context.Background(), raw)
}

func TestWriteToolCleansSelectorsAndReports(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	out, err := runWrite(t, dir, map[string]any{"path": path, "content": "count is \ufe0f42 done\n"})
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !strings.Contains(out, "cleaned 1 invisible character") {
		t.Fatalf("output = %q, want the cleaned-invisible-character note", out)
	}
	if !strings.Contains(out, "U+FE0F×1") {
		t.Fatalf("output = %q, want the per-character type report U+FE0F×1", out)
	}
	if got := readFileString(t, path); got != "count is 42 done\n" {
		t.Fatalf("file = %q, want %q", got, "count is 42 done\n")
	}
}

// Content that strips to empty had no visible content to begin with; writing
// it would truncate the target file on unknowable intent. Genuinely empty
// content stays a legitimate truncation.
func TestWriteToolRejectsSelectorOnlyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	if _, err := runWrite(t, dir, map[string]any{"path": path, "content": "\ufe0f\ufe0e"}); err == nil {
		t.Fatalf("write with selector-only content must fail")
	} else if !strings.Contains(err.Error(), "only invisible characters") {
		t.Fatalf("error = %v, want the only-invisible-characters message", err)
	} else if !strings.Contains(err.Error(), "truncate") {
		t.Fatalf("error = %v, want the truncation warning", err)
	}
	// The failed write must not have touched the target.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed write created the target file")
	}
	// Genuinely empty content still truncates.
	if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runWrite(t, dir, map[string]any{"path": path, "content": ""}); err != nil {
		t.Fatalf("empty-content write failed: %v", err)
	}
	if got := readFileString(t, path); got != "" {
		t.Fatalf("file = %q, want empty", got)
	}
}

// Control characters are rejected with guidance toward the shell/script
// channel, and the target file stays untouched.
func TestWriteToolRejectsControlCharacters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ctrl.txt")
	if err := os.WriteFile(path, []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"nul":    "a\x00b",
		"bell":   "ring \x07 bell",
		"escape": "\x1b[31mred\x1b[0m",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runWrite(t, dir, map[string]any{"path": path, "content": content})
			if err == nil {
				t.Fatalf("write with control character must fail")
			}
			if !strings.Contains(err.Error(), "control character") || !strings.Contains(err.Error(), "shell command") {
				t.Fatalf("error = %v, want the control-character guidance", err)
			}
			if got := readFileString(t, path); got != "seed\n" {
				t.Fatalf("file was modified: %q", got)
			}
		})
	}
}

// A patch made only of orphaned selectors had no visible content; an explicit
// early rejection reads better than the generic parse failure.
func TestApplyPatchRejectsSelectorOnlyPatch(t *testing.T) {
	dir := t.TempDir()
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, "\ufe0f\ufe0f"))
	if err == nil {
		t.Fatalf("selector-only patch must fail")
	}
	if !strings.Contains(err.Error(), "only invisible characters") {
		t.Fatalf("error = %v, want the only-invisible-characters message", err)
	}
}

func TestApplyPatchRejectsControlCharactersInPatch(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: bad.txt\n+a \x00 b\n"
	_, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err == nil {
		t.Fatalf("patch with a NUL byte must fail")
	}
	if !strings.Contains(err.Error(), "control character") || !strings.Contains(err.Error(), "shell command") {
		t.Fatalf("error = %v, want the control-character guidance", err)
	}
}

// The success result reports cleaned selectors so the model learns its patch
// channel leaks invisible characters.
func TestApplyPatchNotesCleanedSelectors(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Add File: note.txt\n+count is \ufe0f7\n"
	out, err := (ApplyPatchTool{BaseDir: dir}).Execute(context.Background(), applyPatchArgs(t, patch))
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}
	if !strings.Contains(out, "cleaned 1 invisible character") {
		t.Fatalf("output = %q, want the cleaned-invisible-character note", out)
	}
	if !strings.Contains(out, "U+FE0F×1") {
		t.Fatalf("output = %q, want the per-character type report U+FE0F×1", out)
	}
}

// --- textclean.StripZeroWidthFormat unit tests ---

// TestDescribeInvisibleCountsSorted guards that the per-rune report lists
// code points in ascending order regardless of input order, so successive
// failures present the same stable text to the model.
func TestDescribeInvisibleCountsSorted(t *testing.T) {
	counts := CountStrippedInvisible("\ufeffa\u200bb\ufe0f\u200b", "ab") // FE0F before 200B in input
	want := "U+200B×2, U+FE0F×1, U+FEFF×1 (invisible formatting characters that carry no content; do not include them in tool arguments)"
	if got := describeInvisibleCounts(counts); got != want {
		t.Fatalf("describeInvisibleCounts = %q, want %q", got, want)
	}
	// A rune the strip preserves (ZWJ joining two emoji, leading BOM,
	// base-character variation selector) is never reported as cleaned.
	if preserved := CountStrippedInvisible("\U0001f468\u200d\U0001f469", "\U0001f468\u200d\U0001f469"); len(preserved) != 0 {
		t.Fatalf("CountStrippedInvisible counted preserved runes: %v", preserved)
	}
}

// TestEditToolStripsZeroWidthInNewString guards the full Edit flow: a ZWSP
// leaked inside new_string (an operator or identifier) must be stripped so the
// file receives clean visible text, and the success message reports it.
func TestEditToolStripsZeroWidthInNewString(t *testing.T) {
	dir := t.TempDir()
	file := "func foo() int {\n\treturn 0, false\n}\n"
	path := writeEditFixture(t, dir, "demo.go", file)
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": "return 0, false", "new_string": "return \u200b1, true",
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want success with stripped ZWSP", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "func foo() int {\n\treturn 1, true\n}\n" {
		t.Fatalf("file = %q, want ZWSP stripped from new_string", string(got))
	}
}

// TestWriteToolStripsZeroWidthInContent guards the Write flow end to end.
func TestWriteToolStripsZeroWidthInContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.md")
	_, err := (&WriteTool{BaseDir: dir}).Execute(context.Background(), mustJSON(t, map[string]any{
		"path": path, "content": "ab\u200bcd",
	}))
	if err != nil {
		t.Fatalf("Write err = %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "abcd" {
		t.Fatalf("file = %q, want ZWSP stripped from content", string(got))
	}
}
