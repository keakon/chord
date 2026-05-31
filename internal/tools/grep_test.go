package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestGrepRejectsNamedPipePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipe filesystem semantics differ on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "input.pipe")
	if err := makeNamedPipeForTest(path); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "FAIL", "path": path})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for named pipe path")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want regular-file rejection", err)
	}
}

func TestGrepRejectsBlockedDevicePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device path blacklist is unix-specific")
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "FAIL", "path": "/dev/stdin"})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for blocked device path")
	}
	if !strings.Contains(err.Error(), "blocked device path") {
		t.Fatalf("error = %v, want blocked-device rejection", err)
	}
}

func TestGrepInvalidRegexExplainsEscaping(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"pattern": "Args []byte", "path": "."})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	for _, want := range []string{"invalid regex pattern", `escape literal special characters such as [] as \[\]`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestGrepLargeResultIsBoundedWithRefineHint(t *testing.T) {
	dir := t.TempDir()
	for i := range maxGrepMatches + 5 {
		path := filepath.Join(dir, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "path": dir})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "needle"); got <= 0 || got > maxGrepMatches {
		t.Fatalf("match count = %d, want within 1..%d", got, maxGrepMatches)
	}
	if !strings.Contains(out, "narrow path/glob/pattern") {
		t.Fatalf("missing refine hint in output:\n%s", out)
	}
}

func TestGrepLongLinesAreBoundedByBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	line := "needle " + strings.Repeat("x", maxGrepOutputBytes/2)
	content := strings.Join([]string{line, line, line, line, line, line, line, line, line, line}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "path": path})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "needle"); got <= 0 || got >= 10 {
		t.Fatalf("match count = %d, want byte-bounded subset of 10; output length=%d", got, len(out))
	}
	if !strings.Contains(out, "within 12 KiB") || !strings.Contains(out, "narrow path/glob/pattern") {
		t.Fatalf("missing byte-bound refine hint in output:\n%s", out)
	}
}

func TestGlobInvalidPatternExplainsGlobSyntax(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"pattern": "[", "path": "."})
	_, err := GlobTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected invalid glob error")
	}
	for _, want := range []string{"glob error", "pattern uses glob syntax like **/*.go, not regex syntax"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestGlobLargeResultIsBoundedWithRefineHint(t *testing.T) {
	dir := t.TempDir()
	for i := range maxGlobResults + 5 {
		path := filepath.Join(dir, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"pattern": "*.txt", "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if got := len(lines) - 2; got != maxGlobResults {
		t.Fatalf("result line count = %d, want %d; output:\n%s", got, maxGlobResults, out)
	}
	if !strings.Contains(out, "refine pattern/path") {
		t.Fatalf("missing refine hint in output:\n%s", out)
	}
}

func TestGlobLongPathsAreBoundedByBytes(t *testing.T) {
	dir := t.TempDir()
	longName := strings.Repeat("nested", 35)
	for i := range 200 {
		path := filepath.Join(dir, longName+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"pattern": "*.txt", "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(out) > maxGlobOutputBytes+200 {
		t.Fatalf("output length = %d, want near byte budget %d", len(out), maxGlobOutputBytes)
	}
	if !strings.Contains(out, "within 16 KiB") || !strings.Contains(out, "refine pattern/path") {
		t.Fatalf("missing byte-bound refine hint in output:\n%s", out)
	}
}
