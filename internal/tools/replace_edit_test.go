package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
// (the dominant failure mode observed in real sessions), the punctuation-
// tolerant fallback should still apply the edit instead of erroring.
func TestEditToolPunctuationTolerantMatchAppliesInsert(t *testing.T) {
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
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
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
func TestEditToolPunctuationTolerantMatchPreservesUnchangedContext(t *testing.T) {
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
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
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
func TestEditToolPunctuationTolerantMatchDoesNotMaskRealMismatch(t *testing.T) {
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
func TestEditToolPunctuationTolerantMatchAmbiguousErrors(t *testing.T) {
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

// When every matching attempt (exact, trailing-newline, punctuation
// tolerance) fails on a character-level difference (dropped rune, extra
// word), the error locates the closest matching block with the exact file
// line and the difference, so the model can rebuild old_string without a
// re-read.
func TestEditToolClosestMatchPinpointsDifference(t *testing.T) {
	dir := t.TempDir()
	// The model's old_string drops the ")" — a missing character, which is
	// beyond 1:1 punctuation tolerance.
	file := "const scheduler = new DailyScheduler(async (day) => {\n    await stats.rebuildDay(day);\n    // recompute only this day's statistics.\n  });\n"
	path := writeEditFixture(t, dir, "main.ts", file)
	oldText := "const scheduler = new DailyScheduler(async (day) => {\n    await stats.rebuildDay(day;\n    // recompute only this day's statistics.\n  });\n"
	newText := "const scheduler = new DailyScheduler(async (day) => {\n    await stats.rebuildDay(day);\n  });\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Closest match is at line 1") {
		t.Fatalf("err = %q, want closest-match location", msg)
	}
	// The file's actual line 2 (rebuildDay with ")") must be shown so the
	// model can copy it exactly.
	if !strings.Contains(msg, "file line 2:") || !strings.Contains(msg, "rebuildDay(day);") {
		t.Fatalf("err = %q, want the exact file line 2 content", msg)
	}
	if !strings.Contains(msg, "your line 2:") || !strings.Contains(msg, "rebuildDay(day;") {
		t.Fatalf("err = %q, want the model's differing line 2", msg)
	}
}

// TestEditToolClosestMatchRelativeLineNumber guards the "your line" semantics:
// with a multi-line old_string whose window starts deep in the file, the
// differing line must be reported relative to the block ("your line 2"),
// not as an absolute file line number that would mislead the model.
func TestEditToolClosestMatchRelativeLineNumber(t *testing.T) {
	dir := t.TempDir()
	// Window starts at file line 11; the divergence sits at the window's
	// second line (file line 12).
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "padding line %d\n", i)
	}
	b.WriteString("alpha beta\n")
	b.WriteString("gamma delta\n")
	file := b.String()
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "alpha beta\ngammax delta\n"
	newText := "alpha beta\ngamma epsilon\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "file line 12: \"gamma delta\"") {
		t.Fatalf("err = %q, want absolute file line 12", msg)
	}
	if !strings.Contains(msg, "your line 2: \"gammax delta\"") {
		t.Fatalf("err = %q, want block-relative 'your line 2' (not 12)", msg)
	}
}

// When no window is close enough (the old_string targets something
// fundamentally different), the generic re-read hint still applies.
func TestEditToolClosestMatchFallsBackToGenericHint(t *testing.T) {
	dir := t.TempDir()
	file := "completely unrelated content here\nsecond line\nthird line\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "the quick brown fox jumps over the lazy dog\nand then some more text that is nowhere\nin this file at all\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": "replacement\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want generic error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "old_string not found in file, even after punctuation/whitespace tolerance") {
		t.Fatalf("err = %q, want generic old_string not found", msg)
	}
	if strings.Contains(msg, "Closest match") {
		t.Fatalf("err = %q, must not claim a closest match for unrelated content", msg)
	}
}

// A1: replace_all under punctuation tolerance replaces every normalized
// occurrence.
func TestEditToolPunctuationTolerantMatchReplaceAll(t *testing.T) {
	dir := t.TempDir()
	// File uses curly quotes; old_string below uses straight quotes so exact
	// matching fails and the punctuation/whitespace-tolerant fallback handles replace_all.
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
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
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

// A1: replace_all under punctuation tolerance collects non-overlapping
// matches left-to-right, mirroring strings.ReplaceAll. Overlapping normalized
// matches (curly “““ matching straight "") must not panic on slice bounds
// and must consume only the leftmost non-overlapping occurrence.
func TestEditToolPunctuationTolerantMatchReplaceAllNonOverlapping(t *testing.T) {
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
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
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

// A1: full-width CJK punctuation (e.g. "," vs ",") is tolerated the same way
// as curly quotes. The shared prefix/suffix keep the file's original full-
// width bytes; only the model's delta is written verbatim.
func TestEditToolPunctuationTolerantFullWidthPunct(t *testing.T) {
	dir := t.TempDir()
	// File uses a full-width comma; the model's old/new both use half-width.
	file := "keep，this\n"
	path := writeEditFixture(t, dir, "proposal.md", file)
	oldText := "keep,this\n"
	newText := "keep,that\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	// The comma is shared context, so the file's full-width "," survives;
	// only the delta "that" replaces "this".
	want := "keep，that\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q (full-width comma preserved)", string(got), want)
	}
	if strings.Contains(string(got), "keep,") {
		t.Fatalf("context comma drifted to half-width: %q", string(got))
	}
}

// U+FF0E FULLWIDTH FULL STOP (the CJK-IME period) folds to "." just like the
// ideographic full stop "。". A model that re-emits ". " as "．" must still
// match.
func TestEditToolPunctuationTolerantFullwidthFullStop(t *testing.T) {
	dir := t.TempDir()
	file := "完成。继续。\n"
	path := writeEditFixture(t, dir, "proposal.md", file)
	// Model re-emits the sentence period as U+FF0E.
	oldText := "完成．继续．\n"
	newText := "完成．继续下一项．\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
	}
	got, _ := os.ReadFile(path)
	// The file's original "。" survives as unchanged context.
	if want := "完成。继续下一项。\n"; string(got) != want {
		t.Fatalf("file = %q, want %q", string(got), want)
	}
}

// A1: whitespace stays significant. A space/newline difference must NOT be
// masked by punctuation tolerance, otherwise indentation or line-structure
// changes would silently apply wrong edits.
func TestEditToolPunctuationTolerantKeepsWhitespaceSignificant(t *testing.T) {
	dir := t.TempDir()
	file := "line one\n  line two\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	// old_string collapses the indentation of the second line.
	oldText := "line one\nline two\n"
	newText := "line one\nline three\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err == nil {
		t.Fatal("Execute err = nil, want mismatch error (whitespace must stay significant)")
	}
	if !strings.Contains(err.Error(), "old_string not found") {
		t.Fatalf("err = %q, want old_string not found", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != file {
		t.Fatalf("file = %q, want unchanged %q", string(got), file)
	}
}

// A1: unrelated punctuation classes are NOT equivalent. A half-width
// semicolon must not match a full-width period (which normalizes to ".").
func TestEditToolPunctuationTolerantDoesNotMergeUnrelatedPunct(t *testing.T) {
	dir := t.TempDir()
	file := "alpha。beta\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "alpha;beta\n" // ";" vs normalized "." must not match
	newText := "alpha;gamma\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err == nil {
		t.Fatal("Execute err = nil, want mismatch error (unrelated punctuation)")
	}
	if !strings.Contains(err.Error(), "old_string not found") {
		t.Fatalf("err = %q, want old_string not found", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != file {
		t.Fatalf("file = %q, want unchanged %q", string(got), file)
	}
}

// A1: "：" and ": " are equivalent. Models often tokenize ": " as a single
// token and re-emit it as "：" or drop the space (e.g. "refusal:the caller"
// in real sessions). The file's own punctuation survives unchanged context.
func TestEditToolPunctuationTolerantColonSpace(t *testing.T) {
	t.Run("file full-width colon, model half-width colon plus space", func(t *testing.T) {
		dir := t.TempDir()
		file := "说明：这是标题\n"
		path := writeEditFixture(t, dir, "note.md", file)
		oldText := "说明: 这是标题\n"
		newText := "说明: 新标题\n"
		out, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": oldText, "new_string": newText,
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
		}
		got, _ := os.ReadFile(path)
		// The colon is shared context, so the file's full-width "：" survives;
		// only the delta "新" replaces "这".
		want := "说明：新标题\n"
		if string(got) != want {
			t.Fatalf("file = %q, want %q (full-width colon preserved)", string(got), want)
		}
		if strings.Contains(string(got), "说明:") {
			t.Fatalf("context colon drifted to half-width: %q", string(got))
		}
	})

	t.Run("file half-width colon plus space, model full-width colon", func(t *testing.T) {
		dir := t.TempDir()
		file := "说明: 这是标题\n"
		path := writeEditFixture(t, dir, "note.md", file)
		oldText := "说明：这是标题\n"
		newText := "说明：新标题\n"
		_, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": oldText, "new_string": newText,
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
		}
		got, _ := os.ReadFile(path)
		want := "说明: 新标题\n"
		if string(got) != want {
			t.Fatalf("file = %q, want %q (half-width colon plus space preserved)", string(got), want)
		}
	})

	t.Run("model intends to normalize the colon itself", func(t *testing.T) {
		dir := t.TempDir()
		file := "说明：这是标题\n"
		path := writeEditFixture(t, dir, "note.md", file)
		// old/new differ in original bytes at the colon (full-width vs
		// half-width+space); that difference is the model's intended delta
		// and must come from newText verbatim.
		oldText := "说明：这是标题\n"
		newText := "说明: 这是标题\n"
		_, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": oldText, "new_string": newText,
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
		}
		got, _ := os.ReadFile(path)
		want := "说明: 这是标题\n"
		if string(got) != want {
			t.Fatalf("file = %q, want %q (model's colon delta applied)", string(got), want)
		}
	})
}

// A1: comma and paren spaces follow the same separator rule, so the
// normalized space folding applies consistently.
func TestEditToolPunctuationTolerantSeparatorSpace(t *testing.T) {
	t.Run("comma", func(t *testing.T) {
		dir := t.TempDir()
		file := "列表,项目\n"
		path := writeEditFixture(t, dir, "list.md", file)
		oldText := "列表, 项目\n"
		newText := "列表, 事项\n"
		if _, err := runEdit(t, dir, map[string]any{"path": path, "old_string": oldText, "new_string": newText}); err != nil {
			t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "列表,事项\n" {
			t.Fatalf("file = %q, want %q (full-width comma preserved)", string(got), "列表,事项\n")
		}
	})
	t.Run("parens", func(t *testing.T) {
		dir := t.TempDir()
		file := "(内容)\n"
		path := writeEditFixture(t, dir, "paren.md", file)
		oldText := "( 内容 )\n"
		newText := "( 新内容 )\n"
		if _, err := runEdit(t, dir, map[string]any{"path": path, "old_string": oldText, "new_string": newText}); err != nil {
			t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
		}
		got, _ := os.ReadFile(path)
		// Both parens plus their absorbed spaces come from the file; only
		// "新" is the delta.
		if string(got) != "(新内容)\n" {
			t.Fatalf("file = %q, want %q (full-width parens preserved)", string(got), "(新内容)\n")
		}
	})
}

// A1: the normalized match must stay unique. When a file mixes "：" and ": "
// variants of the same text, the same old_string matches both and must error
// like the exact path instead of silently picking one.
func TestEditToolPunctuationTolerantColonSpaceAmbiguous(t *testing.T) {
	dir := t.TempDir()
	file := "a：b\na: b\n"
	path := writeEditFixture(t, dir, "mixed.md", file)
	// "a:b" (no space) matches neither line exactly, but normalizes to the
	// same "a:b" as both "a：b" and "a: b" — that must error as ambiguous.
	oldText := "a:b\n"
	newText := "a:x\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err == nil || !strings.Contains(err.Error(), "found 2 times") {
		t.Fatalf("err = %v, want found-2-times ambiguity error", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != file {
		t.Fatalf("file = %q, want unchanged %q", string(got), file)
	}
}

// A1: the space folding is deliberately narrow. Double spaces, spaces after
// quotes/dashes, word-boundary spaces, and spaces on the non-absorbing side
// of a paren all stay significant, so a real mismatch still fails with the
// fresh-read hint instead of a wrong edit.
func TestEditToolPunctuationTolerantSpaceFoldingIsNarrow(t *testing.T) {
	cases := []struct {
		name string
		file string
		old  string
		new  string
	}{
		{"double space after separator", "a:  b\n", "a：b\n", "a：x\n"},   // only one space is optional
		{"space inside quotes", "\" foo\"\n", "\"foo\"\n", "\"fox\"\n"}, // quote space is content
		{"space after dash", "- foo\n", "-foo\n", "-fox\n"},             // list-item space is syntax
		{"extra space before opening paren", "(x\n", " (x\n", " (y\n"},  // model's leading space is not in the file
		{"extra space after closing paren", "x)\n", "x) \n", "y) \n"},   // ")" only absorbs a leading space
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeEditFixture(t, dir, "demo.md", tc.file)
			_, err := runEdit(t, dir, map[string]any{
				"path": path, "old_string": tc.old, "new_string": tc.new,
			})
			if err == nil {
				t.Fatalf("Execute err = nil, want mismatch error (old=%q file=%q)", tc.old, tc.file)
			}
			if !strings.Contains(err.Error(), "old_string not found") {
				t.Fatalf("err = %q, want old_string not found", err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != tc.file {
				t.Fatalf("file = %q, want unchanged %q", string(got), tc.file)
			}
		})
	}
}

// A1: an inter-word space (exactly one space between two word characters) is
// treated as optional, covering models that drop or insert a word-boundary
// space. The file's own bytes survive unchanged context; only the delta from
// new_string is applied.
func TestEditToolPunctuationTolerantInterWordSpace(t *testing.T) {
	t.Run("file has space, old drops it", func(t *testing.T) {
		dir := t.TempDir()
		file := "content and\n"
		path := writeEditFixture(t, dir, "demo.md", file)
		oldText := "contentand\n"
		newText := "contentax\n"
		out, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": oldText, "new_string": newText,
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want tolerant success", err)
		}
		if !strings.Contains(out, "punctuation/whitespace-tolerant") {
			t.Fatalf("output = %q, want tolerant marker", out)
		}
		got, _ := os.ReadFile(path)
		// The space is shared context, so the file's "and" space survives.
		if string(got) != "content ax\n" {
			t.Fatalf("file = %q, want %q", string(got), "content ax\n")
		}
	})
	t.Run("file has no space, old inserts one", func(t *testing.T) {
		dir := t.TempDir()
		file := "contentand\n"
		path := writeEditFixture(t, dir, "demo.md", file)
		// old/new differ at the inter-word space AND at the word ("and" vs
		// "ax"), so the delta covers both; the file's own bytes (no space)
		// are preserved outside the delta.
		oldText := "content and\n"
		newText := "content ax\n"
		_, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": oldText, "new_string": newText,
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want tolerant success", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "contentax\n" {
			t.Fatalf("file = %q, want %q", string(got), "contentax\n")
		}
	})
	t.Run("model intends to add the space", func(t *testing.T) {
		dir := t.TempDir()
		file := "contentand\n"
		path := writeEditFixture(t, dir, "demo.md", file)
		oldText := "contentand\n"
		newText := "content and\n"
		_, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": oldText, "new_string": newText,
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want tolerant success", err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "content and\n" {
			t.Fatalf("file = %q, want %q (model's space delta applied)", string(got), "content and\n")
		}
	})
}

// Invisible format runes next to a word-boundary space are stripped before
// matching (StripZeroWidthFormat), so the model's leaked rune never keeps a
// space significant on the model side while the file's clean text folds it,
// and never gets spliced into the file. The success message reports the
// cleaned characters by code point.
func TestEditToolStripsZeroWidthBesideInterWordSpace(t *testing.T) {
	const file = "func foo() int {\n\treturn 0, false\n}\n"
	const want = "func foo() int {\n\treturn 1, true\n}\n"
	t.Run("zero-width space after the space", func(t *testing.T) {
		dir := t.TempDir()
		path := writeEditFixture(t, dir, "demo.go", file)
		out, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": "return \u200b0, false\n}\n", "new_string": "return 1, true\n}\n",
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want success after stripping the ZWSP", err)
		}
		if !strings.Contains(out, "cleaned 1 invisible character") || !strings.Contains(out, "U+200B×1") {
			t.Fatalf("output = %q, want the cleaned-invisible-character report U+200B×1", out)
		}
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Fatalf("file = %q, want %q", string(got), want)
		}
	})
	t.Run("zero-width space before the space", func(t *testing.T) {
		dir := t.TempDir()
		path := writeEditFixture(t, dir, "demo.go", file)
		out, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": "return\u200b 0, false\n}\n", "new_string": "return 1, true\n}\n",
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want success after stripping the ZWSP", err)
		}
		if !strings.Contains(out, "cleaned 1 invisible character") || !strings.Contains(out, "U+200B×1") {
			t.Fatalf("output = %q, want the cleaned-invisible-character report U+200B×1", out)
		}
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Fatalf("file = %q, want %q", string(got), want)
		}
	})
	t.Run("invisible runes on both sides of the space", func(t *testing.T) {
		dir := t.TempDir()
		path := writeEditFixture(t, dir, "demo.go", file)
		out, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": "return\u200b \u200b0, false\n}\n", "new_string": "return 1, true\n}\n",
		})
		if err != nil {
			t.Fatalf("Execute err = %v, want success after stripping the ZWSPs", err)
		}
		if !strings.Contains(out, "cleaned 2 invisible character") || !strings.Contains(out, "U+200B×2") {
			t.Fatalf("output = %q, want the cleaned-invisible-character report U+200B×2", out)
		}
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Fatalf("file = %q, want %q", string(got), want)
		}
	})
	t.Run("double spaces stay significant", func(t *testing.T) {
		dir := t.TempDir()
		path := writeEditFixture(t, dir, "demo.go", "alpha  beta\n")
		if _, err := runEdit(t, dir, map[string]any{
			"path": path, "old_string": "alpha\u200b beta\n", "new_string": "alpha gamma\n",
		}); err == nil {
			t.Fatalf("edit succeeded; an invisible rune must not make a double space foldable")
		}
	})
}

// A1: the space folding is deliberately narrow. Double spaces, spaces after
// quotes/dashes, spaces next to punctuation, and spaces on the non-absorbing
// side of a paren all stay significant, so a real mismatch still fails with
// the fresh-read hint instead of a wrong edit.
func TestEditToolNotFoundSaysToleranceAlreadyTried(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "demo.md", "real content line\n")
	// A content-level mismatch: the tolerance fallback (punctuation, inter-
	// word space) has already been tried and cannot bridge it. The error
	// must say so, and the closest-match path pinpoints the exact file
	// line so the model can rebuild old_string without a full re-read.
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": "real contant line\n", "new_string": "replacement\n",
	})
	if err == nil {
		t.Fatal("Execute error = nil, want old_string not found")
	}
	if !strings.Contains(err.Error(), "even after punctuation/whitespace tolerance") {
		t.Fatalf("err = %q, want tolerance-already-tried note", err)
	}
	if !strings.Contains(err.Error(), "Closest match is at line 1") {
		t.Fatalf("err = %q, want closest-match location", err)
	}
	if !strings.Contains(err.Error(), "file line 1: \"real content line\"") {
		t.Fatalf("err = %q, want the exact file line verbatim (not normalized): %q", err, "file line 1: \"real content line\"")
	}
	if !strings.Contains(err.Error(), "your line 1: \"real contant line\"") {
		t.Fatalf("err = %q, want the model's differing line verbatim (not normalized): %q", err, "your line 1: \"real contant line\"")
	}
}

// When whole lines drifted past what the differing-lines display can
// reconstruct, the error must not send the model back to its (stale) memory:
// it names the drift and hands over executable read coordinates for the
// closest-match range, plus the small-anchor alternative.
func TestEditToolClosestMatchLargeDriftDirectsFreshRead(t *testing.T) {
	dir := t.TempDir()
	var file strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&file, "context line %d\n", i)
	}
	path := writeEditFixture(t, dir, "demo.md", file.String())
	// An 8-line block anchored at file line 3 with typos on four of its
	// lines: more differing lines than the error display can show, so the
	// shown lines cannot reconstruct the block. The error must hand over
	// read coordinates for the closest-match range instead of telling the
	// model to copy from the truncated display (or from stale memory).
	var old strings.Builder
	for i := 3; i <= 10; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			fmt.Fprintf(&old, "context line %d typox\n", i)
		} else {
			fmt.Fprintf(&old, "context line %d\n", i)
		}
	}
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": old.String(), "new_string": "replaced block\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "offset=3 limit=8") {
		t.Fatalf("err = %q, want read offset=3 limit=8 for the closest-match range", msg)
	}
	if !strings.Contains(msg, "2-4 line anchor") {
		t.Fatalf("err = %q, want small-anchor alternative", msg)
	}
	if strings.Contains(msg, "copy them exactly") {
		t.Fatalf("err = %q, memory-rebuild hint must not appear under large drift", msg)
	}
}

// In-place replacements (a retyped line between two intact lines) are
// substitutions, not drift: the error shows the differing lines without
// claiming a line-count difference, and four or fewer differing lines stay
// copyable from the display instead of forcing a fresh read.
func TestEditToolClosestMatchInPlaceReplacementIsNotDrift(t *testing.T) {
	dir := t.TempDir()
	file := "func a() {\n\tcallOne()\n}\n\nfunc b() {\n\tcallTwo()\n}\n"
	path := writeEditFixture(t, dir, "a.go", file)
	_, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "func a() {\n\tcallOneX()\n}\n\nfunc b() {\n\tcallTwo()\n}\n",
		"new_string": "func a() {\n\tcallOne()\n}\n\nfunc b() {\n\tcallTwo()\n}\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	msg := err.Error()
	if strings.Contains(msg, "line-count difference") {
		t.Fatalf("err = %q, an in-place replacement must not report a line-count difference", msg)
	}
	if !strings.Contains(msg, "your line 2") || !strings.Contains(msg, "callOneX()") {
		t.Fatalf("err = %q, want the differing line shown", msg)
	}
	if !strings.Contains(msg, "copy them exactly") {
		t.Fatalf("err = %q, a single retyped line stays copyable from the display", msg)
	}
	if strings.Contains(msg, "read the file with offset=") {
		t.Fatalf("err = %q, a single retyped line must not force a fresh read", msg)
	}
}

// Once more lines differ in place than the display can show, the copyable
// hint would send the model back to stale memory; the error must switch to
// read coordinates without inventing a line-count difference.
func TestEditToolClosestMatchManyInPlaceReplacementsDirectsFreshRead(t *testing.T) {
	dir := t.TempDir()
	var file strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&file, "value %d\n", i)
	}
	path := writeEditFixture(t, dir, "values.txt", file.String())
	var old strings.Builder
	for i := 1; i <= 10; i++ {
		if i == 2 || i == 4 || i == 6 || i == 8 {
			fmt.Fprintf(&old, "value %dx\n", i)
		} else {
			fmt.Fprintf(&old, "value %d\n", i)
		}
	}
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": old.String(), "new_string": "rewritten\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	msg := err.Error()
	if strings.Contains(msg, "line-count difference") {
		t.Fatalf("err = %q, in-place replacements must not report a line-count difference", msg)
	}
	if !strings.Contains(msg, "read the file with offset=1 limit=10") {
		t.Fatalf("err = %q, want read coordinates once the display cannot show every differing line", msg)
	}
	if strings.Contains(msg, "copy them exactly") {
		t.Fatalf("err = %q, the copyable hint must not appear once the display truncates", msg)
	}
}

// A character-level difference with no line drift keeps the original
// rebuild-from-displayed-lines hint; read coordinates are only for drift.
func TestEditToolClosestMatchSmallDiffKeepsCopyHint(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "demo.md", "alpha beta\ngamma delta\n")
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": "alpha betax\ngamma delta\n", "new_string": "alpha tau\ngamma delta\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "copy them exactly") {
		t.Fatalf("err = %q, want copy-them-exactly hint", msg)
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

// TestEditToolClosestMatchSkipsOversizedWindows guards the budget semantics: a
// single window whose work exceeds the per-window cap must be skipped, not
// abort the scan — a cheap near-match later in the file must still be found.
func TestEditToolClosestMatchSkipsOversizedWindows(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("x", 10_000) + "\nalpha beta\ngamma delta\n"
	path := writeEditFixture(t, dir, "demo.md", content)
	// The 2-line window covering the 10k-char line busts the per-window work
	// cap; the later 2-line window is cheap and one character away.
	oldText := "alpha betax\ngamma delta\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": "alpha beta\ngamma zeta\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	if msg := err.Error(); !strings.Contains(msg, "Closest match is at line 2") {
		t.Fatalf("err = %q, want closest match at line 2 after skipping the oversized window", msg)
	}
}

// TestEditToolClosestMatchLargeOldStringDegradesToGenericHint guards the
// uniform-file dead zone: when the old_string itself is large enough that every
// window (including the closest one) exceeds the per-window work cap, no
// closest-match suggestion is produced and the failure degrades to the generic
// re-read hint instead. This pins the current budget semantics so a future
// change in the budget formula cannot silently alter the user-visible error.

func TestEditToolClosestMatchLargeOldStringDegradesToGenericHint(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	// 80 lines of the same scale as old_string (600 runes each); line
	// 40 differs from old_string by two characters, so without the budget
	// cap it would be reported as the closest match (≈99% similar).
	for i := range 80 {
		if i == 40 {
			b.WriteString(strings.Repeat("a", 600))
		} else {
			b.WriteString(strings.Repeat("z", 600))
		}
		b.WriteString("\n")
	}
	path := writeEditFixture(t, dir, "demo.md", b.String())
	// 600 runes: every window costs 600×600≈360k rune-pairs, over the
	// 200k budget, so the scan skips them all and falls back to the generic
	// hint rather than reporting line 41 as the closest match.

	oldText := strings.Repeat("a", 598) + "bb" // 600 runes, 2 chars from line 40

	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": "ok",
	})
	if err == nil {
		t.Fatal("Execute error = nil, want generic re-read hint")
	}
	msg := err.Error()
	if !strings.Contains(msg, "The target text may be stale") {
		t.Fatalf("err = %q, want generic re-read hint in the budget dead zone", msg)
	}
	if strings.Contains(msg, "Closest match is at line") {
		t.Fatalf("err = %q, want no closest-match suggestion in the budget dead zone", msg)
	}
}

// TestEditToolTolerantMatchReportsLandingLine guards that a punctuation/
// whitespace-tolerant match reports where the replacement landed, so the
// model does not have to guess which candidate was rewritten.
func TestEditToolTolerantMatchReportsLandingLine(t *testing.T) {
	dir := t.TempDir()
	file := "line one： quoted here\nline two plain\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "line one: quoted here\n"
	newText := "line one@ quoted here\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
	}
	if !strings.Contains(out, " at line") {
		t.Fatalf("output = %q, want the landing line report", out)
	}
}

// TestEditToolTolerantMatchReportsLandingLinesMany guards the replace_all
// path: multiple tolerant hits list every landing line, so the model sees
// that each occurrence was rewritten, not just the first one.
func TestEditToolTolerantMatchReportsLandingLinesMany(t *testing.T) {
	dir := t.TempDir()
	file := "line one： quoted here\nline two： quoted there\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := ": quoted"
	newText := "@ quoted"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText, "replace_all": true,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want punctuation/whitespace-tolerant success", err)
	}
	if !strings.Contains(out, "punctuation/whitespace-tolerant") {
		t.Fatalf("output = %q, want punctuation/whitespace-tolerant marker", out)
	}
	if !strings.Contains(out, " at lines") {
		t.Fatalf("output = %q, want the multi-hit landing lines report", out)
	}
}

// TestEditToolClosestMatchBeyondOldLineCap guards that raising the file-line
// cap restores the closest-match hint for files longer than the old 2000-line cap.
func TestEditToolClosestMatchBeyondOldLineCap(t *testing.T) {
	dir := t.TempDir()
	file := strings.Repeat("x\n", 1050) + "alpha beta\n" + strings.Repeat("y\n", 1448)
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "alpha betax\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": "alpha tau\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want closest-match error")
	}
	if msg := err.Error(); !strings.Contains(msg, "Closest match is at line") {
		t.Fatalf("err = %q, want closest match despite the old 2000-line cap", msg)
	}
}

// TestEditToolClosestMatchStillRefusedPastCap guards the 10000-line guard: a
// file longer than the cap still falls back to the generic re-read hint.
func TestEditToolClosestMatchStillRefusedPastCap(t *testing.T) {
	dir := t.TempDir()
	file := strings.Repeat("x\n", 6000) + "alpha beta\n" + strings.Repeat("y\n", 5999)
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "alpha betax\n"
	_, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": "alpha tau\n",
	})
	if err == nil {
		t.Fatal("Execute err = nil, want generic error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "old_string not found in file, even after punctuation/whitespace tolerance") {

		t.Fatalf("err = %q, want generic old_string not found", msg)
	}
	if strings.Contains(msg, "Closest match") {

		t.Fatalf("err = %q, must not claim a closest match beyond the cap", msg)
	}
}

// TestEditToolAbsorbedInvisibleRunesHint guards that when the model's
// old_string carries invisible format runes (variation selectors), the
// success message explicitly says so, so the model learns to strip them
// before the next copy.
func TestEditToolAbsorbedInvisibleRunesHint(t *testing.T) {
	dir := t.TempDir()
	file := "ab,cd\n"
	path := writeEditFixture(t, dir, "demo.md", file)
	oldText := "ab\uFE0F,cd\n"
	newText := "ab;cd\n"
	out, err := runEdit(t, dir, map[string]any{
		"path": path, "old_string": oldText, "new_string": newText,
	})
	if err != nil {
		t.Fatalf("Execute err = %v, want tolerant success", err)
	}
	if !strings.Contains(out, "cleaned 1 invisible character") {
		t.Fatalf("output = %q, want the cleaned-invisible-character hint", out)
	}
}

// The closest-match failure hint must name the exact first differing rune:
// a visible-space difference ("var  x" vs "var x") is otherwise invisible in
// the two quoted lines, and the model cannot tell what to fix.
func TestEditToolFailureNamesFirstDifferingRune(t *testing.T) {
	dir := t.TempDir()
	file := "var x = 1\n"
	path := writeEditFixture(t, dir, "a.go", file)
	_, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "var  x = 1",
		"new_string": "var y = 1",
	})
	if err == nil {
		t.Fatal("Execute = nil, want closest-match failure")
	}
	for _, want := range []string{"first mismatch at rune 4", "your line has U+0020", "file has U+0078"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
}

// When the model's block carries an extra whole line (here a non-blank line
// the file lacks), the hint must say so via the line-count difference instead
// of only showing a position-shifted content mismatch, and a missing file
// line must read as "file line is empty", not a cryptic "<absent>".
func TestEditToolFailureReportsLineCountDifference(t *testing.T) {
	dir := t.TempDir()
	file := "line1\n\nline2\n"
	path := writeEditFixture(t, dir, "a.txt", file)
	_, err := runEdit(t, dir, map[string]any{
		"path":       path,
		"old_string": "line1\nline2\nline3\n",
		"new_string": "line1\nline2\n",
	})
	if err == nil {
		t.Fatal("Execute = nil, want closest-match failure")
	}
	for _, want := range []string{
		"first mismatch at rune 0: your line has U+006C, file has no characters (file line is empty)",
		"line-count difference: your old_string has 1 extra line(s), the file has 1 extra line(s)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "<absent>") {
		t.Fatalf("error = %q, must not contain the <absent> placeholder", err.Error())
	}
}
