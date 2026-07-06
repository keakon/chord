package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

	raw, _ := json.Marshal(map[string]any{"pattern": "thinking", "paths": []string{dir}})
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
	raw, _ := json.Marshal(map[string]any{"pattern": "match", "paths": []string{dir}})
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

	raw, _ := json.Marshal(map[string]any{"pattern": "FAIL", "paths": []string{path}})
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

	raw, _ := json.Marshal(map[string]any{"pattern": "FAIL", "paths": []string{"/dev/stdin"}})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for blocked device path")
	}
	if !strings.Contains(err.Error(), "blocked device path") {
		t.Fatalf("error = %v, want blocked-device rejection", err)
	}
}

func TestGrepInvalidRegexFallsBackToLiteralSearch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "args.txt")
	if err := os.WriteFile(path, []byte("Args []byte\nArgs string\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "Args []byte", "paths": []string{dir}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "searched as literal text") || !strings.Contains(out, "Args []byte") {
		t.Fatalf("literal fallback output missing note or match:\n%s", out)
	}
}

func TestGrepPathsParameterDescribesMultiplePaths(t *testing.T) {
	params := GrepTool{}.Parameters()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has type %T, want map[string]any", params["properties"])
	}
	pathProp, ok := props["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths property has type %T, want map[string]any", props["paths"])
	}
	desc, ok := pathProp["description"].(string)
	if !ok {
		t.Fatalf("paths description has type %T, want string", pathProp["description"])
	}
	for _, want := range []string{"One or more files/directories to search", "Relative paths resolve from the session working directory", "Supports ~", "Defaults to the session working directory"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("paths description %q missing %q", desc, want)
		}
	}
}

func TestGrepPathErrorHintsForSpaceSeparatedExistingPaths(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 0, 2)
	for _, name := range []string{"cmd", "internal"} {
		path := filepath.Join(dir, name)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	searchPath := strings.Join(paths, " ")

	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{searchPath}})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected path error")
	}
	for _, want := range []string{"path not found: " + searchPath, "grep.paths accepts an array", "separate array item"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestGrepSupportsMultiplePaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"cmd", "internal"} {
		root := filepath.Join(dir, name)
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+".go"), []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{filepath.Join(dir, "cmd"), filepath.Join(dir, "internal")}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"cmd.go", "internal.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestGrepMixedPathNoMatchReportsPartialNotAllFailed(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.Mkdir(good, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file that exists but does not contain the pattern, so the
	// successful root yields zero matches.
	if err := os.WriteFile(filepath.Join(good, "a.go"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist")

	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{good, missing}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error for partial success: %v", err)
	}
	if strings.Contains(out, "all search paths failed") {
		t.Fatalf("successful-but-empty search plus one failed path should not be reported as all-failed:\n%s", out)
	}
	if !strings.Contains(out, "No matches found.") {
		t.Fatalf("output should report no matches:\n%s", out)
	}
	if !strings.Contains(out, "grep: skipped path:") {
		t.Fatalf("output should note the skipped failing path:\n%s", out)
	}
}

func TestGrepAllPathsFailReturnsAggregateError(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{
		filepath.Join(dir, "missing-a"),
		filepath.Join(dir, "missing-b"),
	}})
	_, err := GrepTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error when all search paths fail")
	}
	if !strings.Contains(err.Error(), "all search paths failed") {
		t.Fatalf("error = %v, want all-failed aggregate", err)
	}
}

func TestGrepIncludesPathGlobs(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"internal/a.go": "needle\n",
		"cmd/a.go":      "needle\n",
		"internal/a.md": "needle\n",
	}
	for rel, content := range paths {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{dir}, "includes": []string{"internal/**/*.go"}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "internal/a.go") || strings.Contains(out, "cmd/a.go") || strings.Contains(out, "internal/a.md") {
		t.Fatalf("path include filter mismatch:\n%s", out)
	}
}

func TestGrepSupportsExistingPathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	spaceDir := filepath.Join(dir, "dir with spaces")
	if err := os.Mkdir(spaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spaceDir, "notes.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{spaceDir}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "notes.txt") || !strings.Contains(out, "needle") {
		t.Fatalf("missing match for path with spaces; got:\n%s", out)
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
	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{dir}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "needle"); got <= 0 || got > maxGrepMatches {
		t.Fatalf("match count = %d, want within 1..%d", got, maxGrepMatches)
	}
	if !strings.Contains(out, "narrow paths/includes/pattern") {
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
	raw, _ := json.Marshal(map[string]any{"pattern": "needle", "paths": []string{path}})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.Count(out, "needle"); got <= 0 || got >= 10 {
		t.Fatalf("match count = %d, want byte-bounded subset of 10; output length=%d", got, len(out))
	}
	if !strings.Contains(out, "within 12 KiB") || !strings.Contains(out, "narrow paths/includes/pattern") {
		t.Fatalf("missing byte-bound refine hint in output:\n%s", out)
	}
}

func TestGrepFirstLongLineIsBoundedByBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "first-long.txt")
	line := "needle " + strings.Repeat("界", maxGrepOutputBytes)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, truncated, err := searchFile(path, func() string { return path }, regexp.MustCompile("needle"), maxGrepMatches, maxGrepOutputBytes)
	if err != nil {
		t.Fatalf("searchFile: %v", err)
	}
	if !truncated {
		t.Fatal("searchFile should report byte truncation for first long match")
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want one truncated match", len(matches))
	}
	if joinedLinesBytes(matches) > maxGrepOutputBytes {
		t.Fatalf("match bytes = %d, want <= %d", joinedLinesBytes(matches), maxGrepOutputBytes)
	}
	if !strings.Contains(matches[0], "needle") || !strings.HasSuffix(matches[0], "...") {
		t.Fatalf("truncated match should keep prefix and marker, got %q", matches[0])
	}
}

func TestSearchFileComputesDisplayPathLazily(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("alpha\nneedle one\nneedle two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls int
	matches, truncated, err := searchFile(path, func() string {
		calls++
		return "display/notes.txt"
	}, regexp.MustCompile("missing"), maxGrepMatches, maxGrepOutputBytes)
	if err != nil {
		t.Fatalf("searchFile missing: %v", err)
	}
	if truncated || len(matches) != 0 {
		t.Fatalf("missing search returned matches=%v truncated=%v", matches, truncated)
	}
	if calls != 0 {
		t.Fatalf("display path resolver calls for no-match search = %d, want 0", calls)
	}

	matches, truncated, err = searchFile(path, func() string {
		calls++
		return "display/notes.txt"
	}, regexp.MustCompile("needle"), maxGrepMatches, maxGrepOutputBytes)
	if err != nil {
		t.Fatalf("searchFile matching: %v", err)
	}
	if truncated || len(matches) != 2 {
		t.Fatalf("matching search returned matches=%v truncated=%v", matches, truncated)
	}
	if calls != 1 {
		t.Fatalf("display path resolver calls for matching search = %d, want 1", calls)
	}
	if !strings.HasPrefix(matches[0], "display/notes.txt:2:") || !strings.HasPrefix(matches[1], "display/notes.txt:3:") {
		t.Fatalf("matches used wrong display path: %#v", matches)
	}
}

func TestGlobInvalidPatternExplainsGlobSyntax(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"["}, "path": "."})
	_, err := GlobTool{}.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected invalid glob error")
	}
	for _, want := range []string{"glob error", "patterns use glob syntax like **/*.go, not regex syntax"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestGlobSupportsMultiplePatterns(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.md", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"*.go", "*.md"}, "path": dir})
	out, err := GlobTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.md") || strings.Contains(out, "c.txt") {
		t.Fatalf("multi-pattern output mismatch:\n%s", out)
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
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"*.txt"}, "path": dir})
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

func TestGlobTruncatedResultSavesFullFilteredResults(t *testing.T) {
	dir := t.TempDir()
	sessionDir := t.TempDir()
	for i := range maxGlobResults + 5 {
		path := filepath.Join(dir, "f"+strconv.Itoa(i)+".txt")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"*.txt"}, "path": dir})
	out, err := GlobTool{}.Execute(WithSessionDir(context.Background(), sessionDir), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	artifactPath := filepath.Join(sessionDir, sessionToolOutputsDirName, "glob-results.log")
	if !strings.Contains(out, artifactPath) {
		t.Fatalf("output should mention full results artifact %q:\n%s", artifactPath, out)
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != maxGlobResults+5 {
		t.Fatalf("artifact line count = %d, want %d", len(lines), maxGlobResults+5)
	}
	if !strings.Contains(string(data), "f"+strconv.Itoa(maxGlobResults+4)+".txt") {
		t.Fatalf("artifact missing final match: %q", string(data))
	}
}

func TestGlobAccumulatorOnlyKeepsFullOutputWhenRequested(t *testing.T) {
	withoutFullOutput := newGlobMatchAccumulator(t.TempDir(), 0, false)
	withFullOutput := newGlobMatchAccumulator(t.TempDir(), 0, true)
	for i := range maxGlobResults + 2 {
		match := "f" + strconv.Itoa(i) + ".txt"
		withoutFullOutput.addCandidate(match)
		withFullOutput.addCandidate(match)
	}

	withoutResult := withoutFullOutput.result()
	if !withoutResult.truncated {
		t.Fatal("accumulator should report truncation after inline result limit")
	}
	if withoutResult.fullFiltered != "" {
		t.Fatalf("full output should not be retained when artifact capture is disabled, got %q", withoutResult.fullFiltered)
	}

	withResult := withFullOutput.result()
	if got := strings.Count(withResult.fullFiltered, "\n") + 1; got != maxGlobResults+2 {
		t.Fatalf("full output line count = %d, want %d", got, maxGlobResults+2)
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
	raw, _ := json.Marshal(map[string]any{"patterns": []string{"*.txt"}, "path": dir})
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

func TestGrepAcceptsScalarPathsAndIncludesWithCoerceNote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"pattern":"hello","paths":"` + dir + `","includes":"**/*.go"}`)
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "paths was a single string") || !strings.Contains(out, "includes was a single string") {
		t.Fatalf("scalar coerce notes missing:\n%s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("expected match line, got:\n%s", out)
	}
}

func TestGrepConcurrencyPolicyCoercesScalarPaths(t *testing.T) {
	scalar := GrepTool{}.ConcurrencyPolicy(json.RawMessage(`{"pattern":"x","paths":"."}`))
	array := GrepTool{}.ConcurrencyPolicy(json.RawMessage(`{"pattern":"x","paths":["."]}`))
	if scalar != array {
		t.Fatalf("scalar policy = %+v, want array-form policy %+v", scalar, array)
	}
	if scalar.Resource == "workspace" {
		t.Fatalf("single scalar path should keep a per-path policy, got %+v", scalar)
	}
}

func TestGrepArrayPathsAndIncludesDoNotEmitCoerceNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"pattern":  "hello",
		"paths":    []string{dir},
		"includes": []string{"**/*.go"},
	})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "single string") {
		t.Fatalf("array form must not emit coerce note:\n%s", out)
	}
}

func TestValidateGrepArgsAcceptsLegacyPathAndGlobFields(t *testing.T) {
	if err := ValidateToolArgs(GrepTool{}, json.RawMessage(`{"pattern":"x","path":"internal","glob":"*.go"}`)); err != nil {
		t.Fatalf("legacy singular path/glob fields should validate via alias, got %v", err)
	}
}

func TestGrepExecutesWithLegacyPathAndGlobFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"pattern":"needle","path":"` + dir + `","glob":"**/*.go"}`)
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute with legacy path/glob fields: %v", err)
	}
	if !strings.Contains(out, "a.go") || strings.Contains(out, "a.md") {
		t.Fatalf("legacy path/glob filter mismatch:\n%s", out)
	}
}

func TestGrepIncludesBraceAlternationViaDoublestar(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.ts"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "b.tsx"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "c.go"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"pattern":  "hit",
		"paths":    []string{dir},
		"includes": []string{"**/*.{ts,tsx}"},
	})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "a.ts") || !strings.Contains(out, "b.tsx") {
		t.Fatalf("expected brace alternation to match .ts and .tsx, got:\n%s", out)
	}
	if strings.Contains(out, "c.go") {
		t.Fatalf("brace include filter should exclude .go, got:\n%s", out)
	}
}

// TestGrepExactIncludeFastPathHitsNestedFile verifies that when includes is a
// plain relative file path, grep reads the file directly under the search root
// instead of relying on the recursive walker. The sibling must not be scanned.
func TestGrepExactIncludeFastPathHitsNestedFile(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "src", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(nested, "target.go")
	if err := os.WriteFile(target, []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A decoy with the same basename but different parent must NOT be reached
	// when the include names the relative path src/deep/target.go.
	decoyDir := filepath.Join(dir, "other")
	if err := os.MkdirAll(decoyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoyDir, "target.go"), []byte("needle decoy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{
		"pattern":  "needle",
		"paths":    []string{dir},
		"includes": []string{"src/deep/target.go"},
	})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "src/deep/target.go") {
		t.Fatalf("expected fast-path match on nested target, got:\n%s", out)
	}
	if strings.Contains(out, "decoy") {
		t.Fatalf("fast path must only read the named relative file, got:\n%s", out)
	}
}

// TestGrepExactIncludeFastPathSkipsMissingFile keeps the fast path when one of
// several exact includes is absent, without erroring the whole call.
func TestGrepExactIncludeFastPathSkipsMissingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "there.txt"), []byte("hit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"pattern":  "hit",
		"paths":    []string{dir},
		"includes": []string{"there.txt", "missing.txt"},
	})
	out, err := GrepTool{}.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "there.txt") {
		t.Fatalf("expected match on present file, got:\n%s", out)
	}
}
