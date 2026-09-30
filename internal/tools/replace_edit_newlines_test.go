package tools

import (
	"maps"
	"os"
	"strings"
	"testing"
)

func TestEditAcceptsReadLineEndings(t *testing.T) {
	for _, newline := range []string{"\r\n", "\r", "\n"} {
		for _, batch := range []bool{false, true} {
			t.Run(fmtNewlineCase(newline, batch), func(t *testing.T) {
				dir := t.TempDir()
				path := writeEditFixture(t, dir, "sample.txt", strings.Join([]string{"first", "second", "untouched", ""}, newline))
				args := map[string]any{"path": path}
				change := map[string]any{"old_string": "first\nsecond\n", "new_string": "changed\nsecond\n"}
				if batch {
					args["edits"] = []map[string]any{change}
				} else {
					maps.Copy(args, change)
				}
				if _, err := runEdit(t, dir, args); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				want := strings.Join([]string{"changed", "second", "untouched", ""}, newline)
				if string(got) != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			})
		}
	}
}

func fmtNewlineCase(newline string, batch bool) string {
	name := "lf"
	if newline == "\r" {
		name = "cr"
	} else if newline == "\r\n" {
		name = "crlf"
	}
	if batch {
		name += "-batch"
	}
	return name
}

func TestBatchEditDoesNotNormalizeMixedFile(t *testing.T) {
	content := "first\r\nsecond\n"
	plan, err := planExactReplacements(content, []textReplacement{{OldString: "first\r\n", NewString: new("changed\r\n")}})
	if err != nil || plan.content != "changed\r\nsecond\n" {
		t.Fatalf("got %q, err %v", plan.content, err)
	}
}

func TestFileLineEnding(t *testing.T) {
	for content, want := range map[string]string{
		"":              "",
		"single line":   "",
		"a\nb\n":        "",
		"a\r\nb\r\n":    "\r\n",
		"a\r\nb":        "\r\n",
		"a\rb\r":        "\r",
		"a\r\nb\n":      "",
		"a\nb\r\n":      "",
		"a\r\nb\rc":     "",
		"a\rb\r\n":      "",
		"trailing cr\r": "\r",
		"\r\n\r\n":      "\r\n",
		"a\r\n\nb":      "",
	} {
		if got := fileLineEnding(content); got != want {
			t.Errorf("fileLineEnding(%q) = %q, want %q", content, got, want)
		}
	}
}

// read shows a mixed file's line endings all as LF, so a multi-line
// old_string must match across them; each replacement keeps the line ending
// of the block it replaced.
func TestEditMatchesLineBreaksInMixedFile(t *testing.T) {
	const original = "keep\r\nalpha\r\nbeta\r\nmiddle\ngamma\ndelta\n"
	const want = "keep\r\nALPHA\r\nBETA\r\nmiddle\nGAMMA\nDELTA\n"
	for _, batch := range []bool{false, true} {
		t.Run(fmtNewlineCase("\n", batch), func(t *testing.T) {
			dir := t.TempDir()
			path := writeEditFixture(t, dir, "mixed.txt", original)
			first := map[string]any{"old_string": "alpha\nbeta\n", "new_string": "ALPHA\nBETA\n"}
			second := map[string]any{"old_string": "gamma\ndelta", "new_string": "GAMMA\nDELTA"}
			if batch {
				if _, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{first, second}}); err != nil {
					t.Fatal(err)
				}
			} else {
				// Both blocks use the same logical matching, preserving
				// the CRLF and LF endings of their respective locations.
				for _, change := range []map[string]any{first, second} {
					args := map[string]any{"path": path}
					maps.Copy(args, change)
					result, err := runEdit(t, dir, args)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(result, lineBreakTolerantNote) {
						t.Fatalf("result = %q, want line-ending-tolerant matching", result)
					}
				}
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestEditMixedFileLineBreakMatchIsAmbiguityChecked(t *testing.T) {
	dir := t.TempDir()
	const original = "a\r\nb\nx\na\rb\n"
	path := writeEditFixture(t, dir, "mixed.txt", original)
	_, err := runEdit(t, dir, map[string]any{"path": path, "old_string": "a\nb", "new_string": "c\nd"})
	if err == nil || !strings.Contains(err.Error(), "found 2 times under "+lineBreakTolerantNote+" matching at lines 1, 4") {
		t.Fatalf("err = %v, want a line-ending-tolerant ambiguity at lines 1, 4", err)
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Fatalf("ambiguous edit changed the file: %q", got)
	}
}

// A CRLF counts as one line break: two LFs in old_string must not match it as
// a CR followed by an LF.
func TestLineBreakTolerantSpansKeepsCRLFWhole(t *testing.T) {
	if spans := lineBreakTolerantSpans("a\r\nb\nc\n", "", "a\n\nb", "x", 0); spans != nil {
		t.Fatalf("spans = %+v, want no match", spans)
	}
	spans := lineBreakTolerantSpans("x\ra\r\n\nb\n", "", "a\n\nb", "y\nz", 0)
	if len(spans) != 1 || spans[0].start != 2 || spans[0].end != 7 || spans[0].text != "y\r\nz" {
		t.Fatalf("spans = %+v, want one span over a\\r\\n\\nb taking CRLF", spans)
	}
}

// The closest-match diagnostic compares lines without their line endings: a
// CRLF old_string must not report a phantom CR difference on every line, and a
// CR-only file must still split into lines.
func TestEditClosestMatchIgnoresLineEndings(t *testing.T) {
	for _, newline := range []string{"\r\n", "\r"} {
		t.Run(fmtNewlineCase(newline, false), func(t *testing.T) {
			dir := t.TempDir()
			content := strings.Join([]string{"func alpha() {", "\treturn computeValue(1, 2)", "}", ""}, newline)
			path := writeEditFixture(t, dir, "sample.go", content)
			_, err := runEdit(t, dir, map[string]any{
				"path":       path,
				"old_string": "func alpha() {\n\treturn computeValue(1, 3)\n}\n",
				"new_string": "func alpha() {\n\treturn computeValue(1, 4)\n}\n",
			})
			if err == nil {
				t.Fatal("want a closest-match error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "Closest match is at line 1") || !strings.Contains(msg, "1 character difference") {
				t.Fatalf("err = %q, want a one-character closest match at line 1", msg)
			}
			if strings.Contains(msg, "U+000D") {
				t.Fatalf("err = %q, reports a carriage return the model never sent", msg)
			}
		})
	}
}

func TestEditAmbiguityLinesCountCROnlyLineBreaks(t *testing.T) {
	dir := t.TempDir()
	path := writeEditFixture(t, dir, "sample.txt", "target\rother\rtarget\r")
	_, err := runEdit(t, dir, map[string]any{"path": path, "old_string": "target", "new_string": "changed"})
	if err == nil || !strings.Contains(err.Error(), "found 2 times at lines 1, 3") {
		t.Fatalf("err = %v, want lines 1, 3", err)
	}
}

func TestCountLineBreaksSplitsAcrossCRLF(t *testing.T) {
	const content = "a\r\nb\rc\nd"
	if got := countLineBreaks(content, 0, len(content)); got != 3 {
		t.Fatalf("countLineBreaks(all) = %d, want 3", got)
	}
	// Splitting between the CR and LF of a CRLF must not count it twice.
	if got := countLineBreaks(content, 0, 2) + countLineBreaks(content, 2, len(content)); got != 3 {
		t.Fatalf("split counts sum to %d, want 3", got)
	}
}

func TestEditMixedExactAndEquivalentMatches(t *testing.T) {
	for _, batch := range []bool{false, true} {
		for _, all := range []bool{false, true} {
			t.Run(fmtNewlineCase("\n", batch)+map[bool]string{false: "-unique", true: "-all"}[all], func(t *testing.T) {
				dir := t.TempDir()
				const original = "a\nb\nx\na\r\nb\r\n"
				path := writeEditFixture(t, dir, "mixed.txt", original)
				change := map[string]any{"old_string": "a\nb", "new_string": "c\nd", "replace_all": all}
				args := map[string]any{"path": path}
				if batch {
					args["edits"] = []map[string]any{change}
				} else {
					maps.Copy(args, change)
				}
				_, err := runEdit(t, dir, args)
				want := original
				if all {
					if err != nil {
						t.Fatal(err)
					}
					want = "c\nd\nx\nc\r\nd\r\n"
				} else if err == nil || !strings.Contains(err.Error(), "2 times") {
					t.Fatalf("error = %v, want ambiguous match", err)
				}
				got, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			})
		}
	}
}
