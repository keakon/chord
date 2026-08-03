package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEditFixture writes content to a file in dir and returns its path.
func writeEditFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runEdit(t *testing.T, dir string, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return (EditTool{BaseDir: dir}).Execute(context.Background(), raw)
}

// A1: when the model emits straight quotes but the file uses curly quotes
// (the dominant failure mode observed in real sessions), the quote-tolerant
// fallback should still apply the edit instead of erroring.
func TestEditToolQuoteTolerantMatchAppliesInsert(t *testing.T) {
	dir := t.TempDir()
	// File uses curly quotes around the phrase, as the real docs did.
	file := "RULE: 不得缺少“具体成员—候选—来源主张”映射。\n"
	path := writeEditFixture(t, dir, "config.md", file)

	// Model's old/new both use straight ASCII quotes for the same phrase,
	// and new inserts a gate sentence between the two paragraphs.
	oldText := "RULE: 不得缺少\"具体成员—候选—来源主张\"映射。\n"
	newText := "RULE: 不得缺少\"具体成员—候选—来源主张\"映射。\n门禁：覆盖率不足时返回 Medical。\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want quote-tolerant success", err)
	}
	if !strings.Contains(out, "quote-tolerant") {
		t.Fatalf("output = %q, want quote-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	want := "RULE: 不得缺少“具体成员—候选—来源主张”映射。\n门禁：覆盖率不足时返回 Medical。\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", string(got), want)
	}
	// Critical: the file's original curly quotes must be preserved for the
	// unchanged context, not flipped to straight quotes.
	if strings.Contains(string(got), "\"具体成员") {
		t.Fatalf("context quotes drifted to straight: %q", string(got))
	}
}

// A1: rewording where old/new share a quoted context. The shared context's
// curly quotes must be preserved from the file; only the small delta from
// new_string is inserted verbatim with the model's quote style.
func TestEditToolQuoteTolerantMatchPreservesUnchangedContext(t *testing.T) {
	dir := t.TempDir()
	// File uses curly quotes around q, in both a prefix and suffix context.
	file := "HEAD “q” TAIL\n"
	path := writeEditFixture(t, dir, "strategy.md", file)

	// Model's old/new use straight quotes for the same phrase; new appends a
	// delta (" plus") before the trailing newline.
	oldText := "HEAD \"q\" TAIL\n"
	newText := "HEAD \"q\" TAIL plus\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want quote-tolerant success", err)
	}
	if !strings.Contains(out, "quote-tolerant") {
		t.Fatalf("output = %q, want quote-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	// The shared context "HEAD "q" TAIL" keeps the file's curly quotes; only
	// the delta " plus" is inserted.
	want := "HEAD “q” TAIL plus\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q (curly context preserved)", string(got), want)
	}
	if strings.Contains(string(got), "\"q\"") {
		t.Fatalf("context quotes drifted to straight: %q", string(got))
	}
}

// A1: quote tolerance must NOT mask a genuine mismatch. When old_string
// differs by more than quotes (extra word), it must still error with the
// fresh-read hint so the model does not get a silent wrong edit.
func TestEditToolQuoteTolerantMatchDoesNotMaskRealMismatch(t *testing.T) {
	dir := t.TempDir()
	file := "line with “quotes” here\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "line with “quotes” totally-different\n" // diverges beyond quotes
	newText := "line with “quotes” here and now\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err == nil {
		t.Fatal("Execute err = nil, want mismatch error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "old_string not found") {
		t.Fatalf("err = %q, want old_string not found", msg)
	}
}

// A1: ambiguous normalized match (the same normalized old_string matches
// twice) must error asking for more context, mirroring exact-match semantics.
func TestEditToolQuoteTolerantMatchAmbiguousErrors(t *testing.T) {
	dir := t.TempDir()
	file := "first “x” line\nsecond “x” line\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "“x” line" // normalizes to a substring appearing twice
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": "“y” line",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want ambiguity error")
	}
	if !strings.Contains(err.Error(), "found 2 times") && !strings.Contains(err.Error(), "provide more context") {
		t.Fatalf("err = %q, want ambiguity hint", err)
	}
}

// A1: replace_all under quote tolerance replaces every normalized occurrence.
func TestEditToolQuoteTolerantMatchReplaceAll(t *testing.T) {
	dir := t.TempDir()
	// File uses curly quotes; old_string below uses straight quotes so exact
	// matching fails and the quote-tolerant fallback handles replace_all.
	file := "a “term” b\na “term” b\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "\"term\""
	newText := "\"TERM\""
	all := true
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText, "replace_all": all,
	})
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if !strings.Contains(out, "quote-tolerant") {
		t.Fatalf("output = %q, want quote-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	// Intent preservation: only the inner word changed (term -> TERM), so the
	// surrounding curly quotes are kept from the file rather than flipped to
	// the model's straight quotes.
	want := "a “TERM” b\na “TERM” b\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q (curly quotes preserved, word replaced)", string(got), want)
	}
}

// A1: replace_all under quote tolerance collects non-overlapping matches
// left-to-right, mirroring strings.ReplaceAll. Overlapping normalized
// matches (curly “““ matching straight "") must not panic on slice bounds
// and must consume only the leftmost non-overlapping occurrence.
func TestEditToolQuoteTolerantMatchReplaceAllNonOverlapping(t *testing.T) {
	dir := t.TempDir()
	// Three curly quotes; straight old_string normalizes to a two-rune
	// window that overlaps itself at adjacent positions.
	file := "“““\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := `""`
	newText := `"x`
	all := true
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText, "replace_all": all,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want quote-tolerant success", err)
	}
	if !strings.Contains(out, "quote-tolerant") {
		t.Fatalf("output = %q, want quote-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	// Leftmost non-overlapping match consumes the first two curly quotes
	// (file bytes preserved for the shared prefix); only the model's delta
	// "x" is inserted, and the third curly quote stays as trailing context.
	want := "“x“\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q (non-overlapping leftmost replacement)", string(got), want)
	}
}

// A3: the deprecated "filePath" field name is accepted as an alias for "path"
// at both validation and execution time.
func TestEditToolAcceptsFilePathAlias(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "demo.txt", "hello\n")
	// Schema validation via ValidateToolArgs.
	args, _ := json.Marshal(map[string]string{"filePath": "demo.txt", "old_string": "hello", "new_string": "world"})
	if err := ValidateToolArgs(EditTool{}, args); err != nil {
		t.Fatalf("ValidateToolArgs(filePath) err = %v, want nil", err)
	}
	policy := (EditTool{BaseDir: dir}).ConcurrencyPolicy(args)
	if policy.Resource != "file:"+path || policy.Mode != ConcurrencyModeWrite {
		t.Fatalf("ConcurrencyPolicy(filePath) = %#v, want write lock for %q", policy, path)
	}
	if got := ExtractEditPathFromArgs(args); got != "demo.txt" {
		t.Fatalf("ExtractEditPathFromArgs(filePath) = %q, want demo.txt", got)
	}
	// Execution honors the alias too.
	out, err := runEdit(t, dir, map[string]any{
		"filePath": "demo.txt", "old_string": "hello", "new_string": "world",
	})
	if err != nil {
		t.Fatalf("Execute(filePath) err = %v, want nil", err)
	}
	if !strings.Contains(out, "Replaced") {
		t.Fatalf("output = %q, want Replaced marker", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "world\n" {
		t.Fatalf("file = %q, want world\\n", string(got))
	}
}

// A3: when both "path" and "filePath" are present, "path" wins (current field).
func TestEditToolPathWinsOverFilePathAlias(t *testing.T) {
	dir := t.TempDir()
	realPath := writeEditFixture(t, dir, "real.txt", "hello\n")
	_ = writeEditFixture(t, dir, "other.txt", "hello\n")
	out, err := runEdit(t, dir, map[string]any{
		"path": "real.txt", "filePath": "other.txt", "old_string": "hello", "new_string": "world",
	})
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	_ = out
	got, _ := os.ReadFile(realPath)
	if string(got) != "world\n" {
		t.Fatalf("real.txt = %q, want world\\n (path must win over filePath)", string(got))
	}
	other, _ := os.ReadFile(filepath.Join(dir, "other.txt"))
	if string(other) != "hello\n" {
		t.Fatalf("other.txt = %q, want unchanged hello\\n (filePath must lose)", string(other))
	}
}
