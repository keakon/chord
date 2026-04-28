package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeGrepLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "hello world", "hello world"},
		{"keeps tab", "a\tb", "a\tb"},
		{"strips ESC/CSI", "before\x1b[31mred\x1b[0mafter", "before[31mred[0mafter"},
		{"strips bell/NUL/DEL", "a\x00b\x07c\x7fd", "abcd"},
		{"replaces invalid utf8", "ok\xffend", "ok\ufffdend"},
		{"keeps cjk", "过滤不需要转发的请求头", "过滤不需要转发的请求头"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeGrepLine(tt.in)
			if got != tt.want {
				t.Errorf("sanitizeGrepLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGrepSkipsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	// Write a binary file containing a NUL byte and some text matching the pattern.
	binPath := filepath.Join(dir, "sample.pyc")
	if err := os.WriteFile(binPath, []byte("\x00header\x1b[31mthinking\x00tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a regular text file with a matching line.
	txtPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txtPath, []byte("line1\nthinking here\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "thinking", "path": dir})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "sample.pyc") {
		t.Errorf("binary file should be skipped; got:\n%s", out)
	}
	if !strings.Contains(out, "notes.txt") {
		t.Errorf("text file should be matched; got:\n%s", out)
	}
	if strings.ContainsRune(out, 0x1b) || strings.ContainsRune(out, 0x00) {
		t.Errorf("output must not contain control bytes; got:\n%q", out)
	}
}

func TestGrepSanitizesEmbeddedControlBytes(t *testing.T) {
	dir := t.TempDir()
	// File has no NUL but a stray ESC sequence in a matched line. Should still
	// be searched (not detected as binary), and the ESC bytes must be stripped
	// from the output.
	path := filepath.Join(dir, "log.txt")
	content := "normal line\nmatch \x1b[31mred\x1b[0m end\nanother\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"pattern": "match", "path": dir})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("ESC byte must be stripped; got:\n%q", out)
	}
	if !strings.Contains(out, "red") {
		t.Errorf("non-control content must be preserved; got:\n%s", out)
	}
}
