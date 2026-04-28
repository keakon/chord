package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestReadToolDescriptionExplainsDisplayedGutterForLspPositions(t *testing.T) {
	desc := (ReadTool{}).Description()
	for _, want := range []string{
		"formatted with line numbers (cat -n format)",
		"approximate 20k-token read budget",
		"truncated to fit",
		"The displayed line-number gutter and separator tab are not part of the file content",
		"copy exact text from the raw source portion only",
		"When using Lsp line/character positions, count from the raw source line only",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("Description() missing %q: %q", want, desc)
		}
	}
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
	if got != "     1\tcol1,col2\n     2\t\"a\",\"b\"\n" {
		t.Fatalf("ReadTool.Execute output = %q, want normalized LF cat -n output", got)
	}
	if containsRawCarriageReturn(got) {
		t.Fatalf("ReadTool.Execute output should not contain raw carriage returns: %q", got)
	}
}

func TestReadToolWarmupUsesBackgroundContextAndAbsolutePath(t *testing.T) {
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
	if starter.ctx != context.Background() {
		t.Fatalf("Start context = %v, want context.Background()", starter.ctx)
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
	for i := 0; i < 1200; i++ {
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
	if !strings.Contains(got, "content truncated to fit the approximate 20000-token read budget") {
		t.Fatalf("expected token-budget truncation note, got %q", got)
	}
	if !strings.Contains(got, "(showing lines 1-") {
		t.Fatalf("expected truncated range footer, got %q", got)
	}
	if strings.Contains(got, "  1200\t") {
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
	for i := 0; i < 1200; i++ {
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
	if !strings.Contains(got, "(showing lines 1-50 of 1200 total)") {
		t.Fatalf("expected ranged output marker, got %q", got)
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
