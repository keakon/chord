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
	got, _, _, err := planExactReplacements(content, []textReplacement{{OldString: "first\r\n", NewString: new("changed\r\n")}})
	if err != nil || got != "changed\r\nsecond\n" {
		t.Fatalf("got %q, err %v", got, err)
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
