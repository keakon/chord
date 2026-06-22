package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

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
		"Successful output starts with one READ_RESULT metadata line",
		"`READ_RESULT lines=a-b total=N`",
		"`READ_RESULT lines=none total=N`",
		"A read that simply did not reach the end of the file is not truncation",
		"`truncated=budget requested_lines=a-d`",
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

func TestReadToolExecuteReportsEmptyContentForEmptyRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":1,"limit":10}`, path))
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

	// offset is valid but offset+limit runs past EOF: return through the last
	// line without error or truncation marker.
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":3,"limit":50}`, path))
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
	// expectation is wrong); offset == totalLines is covered elsewhere as valid.
	raw := json.RawMessage(fmt.Sprintf(`{"path":%q,"offset":10,"limit":5}`, path))
	_, err := (ReadTool{}).Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("ReadTool.Execute err = nil, want offset-exceeds-length error")
	}
	for _, want := range []string{
		"offset 10 exceeds file length (3 lines)",
		"suggested_offset=0 reads the last 3 lines with limit=5",
		"eof_offset=3 is valid but returns no lines",
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
	for i := 0; i < MaxOutputLines+3; i++ {
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
		fmt.Sprintf("offset 99999 exceeds file length (%d lines)", MaxOutputLines+3),
		"suggested_offset=3 reads the last 2000 lines with limit=2000",
		fmt.Sprintf("eof_offset=%d is valid but returns no lines", MaxOutputLines+3),
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
	got, err := (ReadTool{}).Execute(context.Background(), raw)
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
