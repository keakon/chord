package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestReadToolReportsHashFromReturnedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	content := []byte("one\r\ntwo\r\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	collector := &ReadObservationCollector{}
	ctx := WithReadObservationSink(context.Background(), collector)
	if _, err := (ReadTool{BaseDir: dir}).Execute(ctx, json.RawMessage(`{"path":"sample.txt"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	observation, ok := collector.Observation()
	if !ok {
		t.Fatal("missing read observation")
	}
	sum := sha256.Sum256(content)
	if observation.Path != path || observation.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("observation = %#v", observation)
	}
}

type recordingLSPStarter struct {
	ctx   context.Context
	path  string
	calls int
}

func (r *recordingLSPStarter) Start(ctx context.Context, path string) {
	r.ctx = ctx
	r.path = path
	r.calls++
}

func TestReadToolDescriptionExplainsRawOutputForEdits(t *testing.T) {
	desc := (ReadTool{}).Description()
	for _, want := range []string{
		"Read file contents by line for code inspection and edits",
		"optional offset/limit line paging",
		"offset is a 1-based line number (1 = the first line); omit it to start from the beginning",
		"Prefer grep or lsp to locate symbols before reading a small nearby block",
		"For a file you will edit or consult repeatedly, prefer reading it in full once",
		"Normal output starts with one READ_RESULT metadata line",
		"`READ_RESULT lines=a-b total=N`",
		"`READ_RESULT lines=none total=N`",
		"a read that simply did not reach the end of the file is not truncation",
		"`truncated=budget`: the tool itself dropped requested lines to fit the approximate 20k-token read budget",
		"`requested_lines=a-d`",
		"`truncated=stale`",
		"`truncated=superseded`",
		"use grep to locate patterns or a script/parser via shell for structured processing instead of character-range reads",
		"omits encoding for UTF-8 files",
		"everything after that first line is exact file text without line-number gutters or extra indentation",
		"copy only the text after READ_RESULT into edit hunks",
		"approximate 20k-token read budget",
		"if you need more surrounding context, read the intended nearby block before patching",
		"For edit, include a few unchanged source lines around the intended change",
		"read output normalizes line endings to LF",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
}

func TestReadToolParametersKeepLinePagingFocused(t *testing.T) {
	props := (ReadTool{}).Parameters()["properties"].(map[string]any)
	if _, ok := props["char_offset"]; ok {
		t.Fatalf("read parameters should not expose char_offset for normal code-reading workflow")
	}
	if _, ok := props["char_limit"]; ok {
		t.Fatalf("read parameters should not expose char_limit for normal code-reading workflow")
	}
	for _, name := range []string{"offset", "limit"} {
		desc := props[name].(map[string]any)["description"].(string)
		if strings.Contains(desc, "char_offset") || strings.Contains(desc, "char_limit") {
			t.Fatalf("%s description should not mention removed char paging: %q", name, desc)
		}
	}
	offsetDesc := props["offset"].(map[string]any)["description"].(string)
	if !strings.Contains(offsetDesc, "1-based") {
		t.Fatalf("offset description should teach 1-based line numbers, got %q", offsetDesc)
	}
	if _, ok := props["offset"].(map[string]any)["minimum"]; !ok {
		t.Fatal("offset should declare a minimum")
	}
}

func TestReadToolPathDescriptionWarnsAgainstGuessing(t *testing.T) {
	props := (ReadTool{}).Parameters()["properties"].(map[string]any)
	desc := props["path"].(map[string]any)["description"].(string)
	for _, want := range []string{"existing file", "Do not guess paths", "verify uncertain paths before reading"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("path description missing %q: %q", want, desc)
		}
	}
}

func readTestHeaderAndBody(t *testing.T, out string) (string, string) {
	t.Helper()
	header, body, ok := strings.Cut(out, "\n")
	if !ok {
		t.Fatalf("read output missing READ_RESULT header newline: %q", out)
	}
	if !strings.HasPrefix(header, "READ_RESULT ") {
		t.Fatalf("read output header = %q, want READ_RESULT", header)
	}
	return header, body
}

func TestSplitReadToolLinesNormalizesLineEndings(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "lf", content: "a\nb\n", want: []string{"a", "b"}},
		{name: "crlf", content: "a\r\nb\r\n", want: []string{"a", "b"}},
		{name: "bare cr", content: "a\rb\r", want: []string{"a", "b"}},
		{name: "mixed", content: "a\r\nb\rc\n", want: []string{"a", "b", "c"}},
		{name: "single blank line", content: "\r\n", want: []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitReadToolLines(tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitReadToolLines(%q) = %#v, want %#v", tc.content, got, tc.want)
			}
		})
	}
}

func TestReadToolExecuteOffsetIsOneBasedStartLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for _, tc := range []struct {
		name      string
		offset    any
		wantLines string
		wantBody  string
	}{
		{name: "omitted", offset: nil, wantLines: "lines=1-2", wantBody: "a\nb\n"},
		{name: "one", offset: 1, wantLines: "lines=1-2", wantBody: "a\nb\n"},
		// 0 was the 0-based offset for the first line; keep it accepted so a
		// model that reasons in 0-based offsets cannot silently off-by-one.
		{name: "zero treated as first line", offset: 0, wantLines: "lines=1-2", wantBody: "a\nb\n"},
		{name: "two", offset: 2, wantLines: "lines=2-3", wantBody: "b\nc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"path":%q,"limit":2}`, path)
			if tc.offset != nil {
				raw = fmt.Sprintf(`{"path":%q,"offset":%d,"limit":2}`, path, tc.offset)
			}
			got, err := (ReadTool{}).Execute(context.Background(), json.RawMessage(raw))
			if err != nil {
				t.Fatalf("ReadTool.Execute: %v", err)
			}
			header, body := readTestHeaderAndBody(t, got)
			if !strings.Contains(header, tc.wantLines) {
				t.Fatalf("ReadTool.Execute header = %q, want %q", header, tc.wantLines)
			}
			if body != tc.wantBody {
				t.Fatalf("ReadTool.Execute body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestReadToolExecuteReportsEmptyContentForEmptyRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// offset past the last line (2 on a 1-line file, since offsets are 1-based)
	// is the natural EOF paging position: valid but returns no lines.
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":2,"limit":10}`, path))
	got, err := (ReadTool{}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	header, body := readTestHeaderAndBody(t, got)
	if !strings.Contains(header, "lines=none") || !strings.Contains(header, "total=1") {
		t.Fatalf("ReadTool.Execute header = %q, want empty range metadata", header)
	}
	if strings.Contains(header, "truncated") {
		t.Fatalf("ReadTool.Execute header = %q, empty paged range must not be marked truncated", header)
	}
	if body != "" {
		t.Fatalf("ReadTool.Execute body = %q, want empty body", body)
	}
}

func TestReadToolExecuteClampsOffsetPlusLimitToEndOfFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// offset is valid (4 starts at line 4, 1-based) but offset+limit runs past
	// EOF: return through the last line without error or truncation marker.
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":4,"limit":50}`, path))
	got, err := (ReadTool{}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	header, body := readTestHeaderAndBody(t, got)
	if !strings.Contains(header, "lines=4-5") || !strings.Contains(header, "total=5") {
		t.Fatalf("ReadTool.Execute header = %q, want lines=4-5 total=5", header)
	}
	if strings.Contains(header, "truncated") {
		t.Fatalf("ReadTool.Execute header = %q, reaching EOF via limit is not truncation", header)
	}
	if body != "d\ne\n" {
		t.Fatalf("ReadTool.Execute body = %q, want d\\ne\\n", body)
	}
}

func TestReadToolExecuteErrorsWhenOffsetPastEndOfFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// offset strictly past the last line is an error (caller's file-size
	// expectation is wrong); the natural EOF paging position (offset ==
	// totalLines+1) is covered elsewhere as valid and returns no lines.
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":10,"limit":5}`, path))
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want offset-exceeds-length error")
	}
	for _, want := range []string{
		"offset 10 exceeds this file length (3 lines)",
		"suggested_offset=1 reads the last 3 lines with limit=5",
		"eof_offset=4 is valid but returns no lines",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ReadTool.Execute err = %v, want substring %q", err, want)
		}
	}
}

func TestReadToolExecuteOffsetPastEndSuggestionUsesDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	var content strings.Builder
	for i := range MaxOutputLines + 3 {
		fmt.Fprintf(&content, "line %d\n", i+1)
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":99999}`, path))
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want offset-exceeds-length error")
	}
	for _, want := range []string{
		fmt.Sprintf("offset 99999 exceeds this file length (%d lines)", MaxOutputLines+3),
		"suggested_offset=4 reads the last 2000 lines with limit=2000",
		fmt.Sprintf("eof_offset=%d is valid but returns no lines", MaxOutputLines+4),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ReadTool.Execute err = %v, want substring %q", err, want)
		}
	}
}

func TestReadToolExecuteNormalizesCRLFOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.csv")
	content := "col1,col2\r\n\"a\",\"b\"\r\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tool := ReadTool{}
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))
	got, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if got == "" {
		t.Fatal("ReadTool.Execute returned empty content")
	}
	header, body := readTestHeaderAndBody(t, got)
	if !strings.Contains(header, "lines=1-2") || !strings.Contains(header, "total=2") {
		t.Fatalf("ReadTool.Execute header = %q, want range metadata", header)
	}
	if strings.Contains(header, "encoding=") {
		t.Fatalf("ReadTool.Execute header = %q, UTF-8 reads must omit encoding", header)
	}
	if body != "col1,col2\n\"a\",\"b\"\n" {
		t.Fatalf("ReadTool.Execute body = %q, want normalized raw LF output", body)
	}
	if containsRawCarriageReturn(got) {
		t.Fatalf("ReadTool.Execute output should not contain raw carriage returns: %q", got)
	}
}

func TestReadToolWarmupUsesProvidedContextAndAbsolutePath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir temp dir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	}()

	if err := os.WriteFile("sample.txt", []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	starter := &recordingLSPStarter{}
	tool := ReadTool{LSP: starter}
	raw := json.RawMessage(`{"path":"sample.txt"}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	if got == "" {
		t.Fatal("ReadTool.Execute returned empty content")
	}
	if starter.calls != 1 {
		t.Fatalf("Start calls = %d, want 1", starter.calls)
	}
	if starter.ctx != ctx {
		t.Fatalf("Start context = %v, want ctx", starter.ctx)
	}
	wantPath, err := filepath.Abs("sample.txt")
	if err != nil {
		t.Fatalf("Abs sample.txt: %v", err)
	}
	if starter.path != wantPath {
		t.Fatalf("Start path = %q, want %q", starter.path, wantPath)
	}
}

func TestReadToolExecuteTruncatesOversizedFormattedOutputByTokenBudget(t *testing.T) {
	dir := t.TempDir()
	sessionDir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	var content strings.Builder
	for range 1200 {
		content.WriteString(strings.Repeat("abcdefghij", 8))
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(`{"path":` + "\"" + path + "\"" + `}`)
	got, err := (ReadTool{}).Execute(WithSessionDir(context.Background(), sessionDir), raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	header, body := readTestHeaderAndBody(t, got)
	if !strings.Contains(header, "READ_RESULT ") || !strings.Contains(header, "lines=1-") || !strings.Contains(header, "total=1200") || !strings.Contains(header, "truncated=budget") || !strings.Contains(header, "requested_lines=1-1200") {
		t.Fatalf("expected token-budget truncation metadata, got header %q", header)
	}
	if strings.Contains(body, strings.Repeat("abcdefghij", 8)+"\n"+"READ_RESULT") {
		t.Fatalf("expected inline read output to truncate before the end of file, got %q", got)
	}
	if !readOutputFitsBudget(got) {
		t.Fatalf("truncated read output should fit inline budget: bytes=%d tokens=%d", len(got), estimateReadOutputTokens(got))
	}
	artifactPath := filepath.Join(sessionDir, sessionToolOutputsDirName, "read-result.log")
	if strings.Contains(got, "Full output saved to ") {
		t.Fatalf("read output should direct callers to page the original file instead of an artifact, got %q", got)
	}
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("read should not create artifact %q, stat error = %v", artifactPath, err)
	}
}

func TestReadToolExecuteAllowsTargetedRangeWithinTokenBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	var content strings.Builder
	for range 1200 {
		content.WriteString(strings.Repeat("abcdefghij", 8))
		content.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":0,"limit":50}`, path))
	got, err := (ReadTool{}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	header, body := readTestHeaderAndBody(t, got)
	if !strings.Contains(header, "lines=1-50") || !strings.Contains(header, "total=1200") {
		t.Fatalf("expected ranged output metadata, got %q", header)
	}
	if strings.Contains(header, "truncated") {
		t.Fatalf("targeted range within budget must not be marked truncated, got %q", header)
	}
	if strings.Count(body, "\n") != 50 {
		t.Fatalf("expected 50 content lines, got body with %d newlines", strings.Count(body, "\n"))
	}
}

func TestReadToolExecuteReportsEncodingOnlyForNonUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf16.txt")
	// UTF-16LE BOM (0xFF 0xFE) followed by "hi" so detection picks a non-UTF-8
	// encoding and the header must surface it.
	data := []byte{0xFF, 0xFE, 'h', 0x00, 'i', 0x00}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))
	got, err := (ReadTool{}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("ReadTool.Execute: %v", err)
	}
	header, _ := readTestHeaderAndBody(t, got)
	if !strings.Contains(header, `encoding="utf-16le"`) {
		t.Fatalf("non-UTF-8 read header = %q, want encoding reported", header)
	}
}

func TestReadToolRejectsNamedPipePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipe filesystem semantics differ on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "input.pipe")
	if err := makeNamedPipeForTest(path); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(`{"path":%q}`, path))
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for named pipe path")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want regular-file rejection", err)
	}
}

func TestReadToolRejectsBlockedDevicePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device path blacklist is unix-specific")
	}

	raw := json.RawMessage(`{"path":"/dev/stdin"}`)
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for blocked device path")
	}
	if !strings.Contains(err.Error(), "blocked device path") {
		t.Fatalf("error = %v, want blocked-device rejection", err)
	}
}

func containsRawCarriageReturn(s string) bool {
	for _, r := range s {
		if r == '\r' {
			return true
		}
	}
	return false
}
