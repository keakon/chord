package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// generateLines returns n lines joined by "\n", each with format "line-NNNN".
func generateLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "line-%04d", i)
	}
	return b.String()
}

// generatePaddedLines returns n lines joined by "\n", each padded to exactly
// lineLen bytes with format "line-NNNN:" followed by 'x' padding.
func generatePaddedLines(n, lineLen int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		prefix := fmt.Sprintf("line-%04d:", i)
		if pad := lineLen - len(prefix); pad > 0 {
			b.WriteString(prefix)
			b.WriteString(strings.Repeat("x", pad))
		} else {
			b.WriteString(prefix[:lineLen])
		}
	}
	return b.String()
}

func TestTruncateOutputReusesStableArtifactPathForSameKey(t *testing.T) {
	sessionDir := t.TempDir()
	input := generateLines(MaxOutputLines + 10)

	first := TruncateOutputWithOptions(input, sessionDir, TruncateOptions{ArtifactKey: "call-123"})
	if !first.Truncated {
		t.Fatal("expected truncation")
	}
	second := TruncateOutputWithOptions(input, sessionDir, TruncateOptions{ArtifactKey: "call-123"})
	if first.SavedPath == "" || second.SavedPath == "" {
		t.Fatalf("saved paths = %q / %q, want non-empty", first.SavedPath, second.SavedPath)
	}
	if first.SavedPath != second.SavedPath {
		t.Fatalf("saved path changed across replay: %q vs %q", first.SavedPath, second.SavedPath)
	}
	if !strings.Contains(first.Hint, first.SavedPath) {
		t.Fatalf("hint %q should reference saved path %q", first.Hint, first.SavedPath)
	}
}

func TestListArtifactFilesReturnsSessionToolOutputs(t *testing.T) {
	sessionDir := t.TempDir()
	_ = TruncateOutputWithOptions(generateLines(MaxOutputLines+5), sessionDir, TruncateOptions{ArtifactKey: "call-a"})
	_ = TruncateOutputWithOptions(generateLines(MaxOutputLines+6), sessionDir, TruncateOptions{ArtifactKey: "call-b"})

	files, err := ListArtifactFiles(sessionDir)
	if err != nil {
		t.Fatalf("ListArtifactFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	for _, path := range files {
		if !strings.Contains(path, filepath.Join(sessionDir, sessionToolOutputsDirName)) {
			t.Fatalf("artifact path %q should be under %q", path, filepath.Join(sessionDir, sessionToolOutputsDirName))
		}
	}
}
func TestTruncateOutputWithOptions(t *testing.T) {
	tests := []struct {
		name  string
		input func() string
		opts  TruncateOptions
		check func(t *testing.T, input string, r TruncateResult)
	}{
		{
			name:  "no truncation needed",
			input: func() string { return generateLines(10) },
			opts:  TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if r.Truncated {
					t.Error("Truncated should be false")
				}
				if r.Content != input {
					t.Error("Content should be unchanged")
				}
				if r.SavedPath != "" {
					t.Errorf("SavedPath should be empty, got %q", r.SavedPath)
				}
				if r.Hint != "" {
					t.Errorf("Hint should be empty, got %q", r.Hint)
				}
			},
		},
		{
			name:  "line count exactly at threshold",
			input: func() string { return generateLines(MaxOutputLines) },
			opts:  TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if r.Truncated {
					t.Error("Truncated should be false when lines == MaxOutputLines")
				}
				if r.Content != input {
					t.Error("Content should be unchanged")
				}
				if r.SavedPath != "" {
					t.Error("SavedPath should be empty")
				}
			},
		},
		{
			name:  "lines exceed threshold head+tail default",
			input: func() string { return generateLines(3000) },
			opts:  TruncateOptions{}, // default direction = "head+tail"
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				// Default head+tail: headCount = MaxOutputLines*2/5 = 800
				//                     tailCount = MaxOutputLines - 800 = 1200
				// Kept: line-0000..line-0799  and  line-1800..line-2999
				mustContain := []string{"line-0000", "line-0799", "line-1800", "line-2999"}
				for _, s := range mustContain {
					if !strings.Contains(r.Content, s) {
						t.Errorf("Content should contain %q", s)
					}
				}
				mustNotContain := []string{"line-0800", "line-1799"}
				for _, s := range mustNotContain {
					if strings.Contains(r.Content, s) {
						t.Errorf("Content should NOT contain %q", s)
					}
				}
				if !strings.Contains(r.Content, "1000 lines truncated") {
					t.Error("Content should contain truncation marker mentioning 1000 lines")
				}
				// Verify saved file contains the original output.
				if r.SavedPath == "" {
					t.Fatal("SavedPath should be set")
				}
				data, err := os.ReadFile(r.SavedPath)
				if err != nil {
					t.Fatalf("failed to read saved file: %v", err)
				}
				if string(data) != input {
					t.Error("saved file should contain original full output")
				}
				if r.Hint == "" || !strings.Contains(r.Hint, "truncated") {
					t.Errorf("Hint should mention truncation, got %q", r.Hint)
				}
			},
		},
		{
			name:  "lines exceed threshold head direction",
			input: func() string { return generateLines(3000) },
			opts:  TruncateOptions{Direction: "head"},
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				// Head keeps first 2000 lines: line-0000 through line-1999
				if !strings.Contains(r.Content, "line-0000") {
					t.Error("should contain first line")
				}
				if !strings.Contains(r.Content, "line-1999") {
					t.Error("should contain line-1999")
				}
				if strings.Contains(r.Content, "line-2000") {
					t.Error("should NOT contain line-2000")
				}
				if !strings.Contains(r.Content, "1000 lines truncated") {
					t.Error("should contain truncation marker")
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
			},
		},
		{
			name:  "lines exceed threshold tail direction",
			input: func() string { return generateLines(3000) },
			opts:  TruncateOptions{Direction: "tail"},
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				// Tail keeps last 2000 lines: line-1000 through line-2999
				if !strings.Contains(r.Content, "line-1000") {
					t.Error("should contain line-1000")
				}
				if !strings.Contains(r.Content, "line-2999") {
					t.Error("should contain last line")
				}
				if strings.Contains(r.Content, "line-0999") {
					t.Error("should NOT contain line-0999")
				}
				if !strings.Contains(r.Content, "1000 lines truncated") {
					t.Error("should contain truncation marker")
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
			},
		},
		{
			name:  "bytes exceed threshold head+tail",
			input: func() string { return generatePaddedLines(100, 600) },
			opts:  TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				// 100 lines × 600 bytes + 99 newlines = 60099 > MaxOutputBytes (51200)
				// headBudget = 51200*2/5 = 20480 → fits 34 lines (34×600+33 = 20433)
				// tailBudget = 30720 → fits 51 lines from remainder (51×600+50 = 30650)
				// Kept: lines 0-33 (head) and lines 49-99 (tail)
				if !strings.Contains(r.Content, "line-0000:") {
					t.Error("should contain first line")
				}
				if !strings.Contains(r.Content, "line-0033:") {
					t.Error("should contain line-0033 (last head line)")
				}
				if strings.Contains(r.Content, "line-0034:") {
					t.Error("should NOT contain line-0034 (first dropped)")
				}
				if strings.Contains(r.Content, "line-0048:") {
					t.Error("should NOT contain line-0048 (last dropped)")
				}
				if !strings.Contains(r.Content, "line-0049:") {
					t.Error("should contain line-0049 (first tail line)")
				}
				if !strings.Contains(r.Content, "line-0099:") {
					t.Error("should contain last line")
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
				if !strings.Contains(r.Hint, "truncated") {
					t.Errorf("Hint should mention truncated, got %q", r.Hint)
				}
			},
		},
		{
			name: "single line exceeds MaxLineLength",
			input: func() string {
				return strings.Repeat("a", MaxLineLength+500)
			},
			opts: TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				// Total bytes = 2500 < MaxOutputBytes, lines = 1 ≤ MaxOutputLines
				// Only per-line truncation applies; overall Truncated stays false.
				if r.Truncated {
					t.Error("Truncated should be false (within overall limits)")
				}
				expected := strings.Repeat("a", MaxLineLength) + "..."
				if r.Content != expected {
					t.Errorf("Content length = %d, want %d", len(r.Content), len(expected))
				}
			},
		},
		{
			name: "first line exceeds MaxOutputBytes",
			input: func() string {
				return strings.Repeat("z", MaxOutputBytes+10000)
			},
			opts: TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				if len(r.Content) == 0 {
					t.Fatal("Content should not be empty")
				}
				// Byte trim: single line exceeds all budgets → fallback to
				// firstLine[:MaxOutputBytes] + "..."
				// Then per-line truncation: still > MaxLineLength → [:MaxLineLength] + "..."
				maxExpected := MaxLineLength + len("...")
				if len(r.Content) > maxExpected {
					t.Errorf("Content length %d should be ≤ %d", len(r.Content), maxExpected)
				}
				if !strings.HasSuffix(r.Content, "...") {
					t.Error("Content should end with ...")
				}
				if !strings.HasPrefix(r.Content, "zzz") {
					t.Error("Content should start with original characters")
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
				if r.Hint == "" {
					t.Error("Hint should be non-empty")
				}
			},
		},
		{
			name:  "empty string",
			input: func() string { return "" },
			opts:  TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if r.Truncated {
					t.Error("Truncated should be false")
				}
				if r.Content != "" {
					t.Errorf("Content should be empty, got %q", r.Content)
				}
				if r.SavedPath != "" {
					t.Errorf("SavedPath should be empty, got %q", r.SavedPath)
				}
				if r.Hint != "" {
					t.Errorf("Hint should be empty, got %q", r.Hint)
				}
			},
		},
		{
			name: "all lines extremely long exceeding byte limit",
			input: func() string {
				return generatePaddedLines(10, 60000)
			},
			opts: TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				if len(r.Content) == 0 {
					t.Fatal("Content should not be empty")
				}
				// Each 60000-byte line exceeds both head and tail byte budgets,
				// so trimLinesToByteLimitHeadTail returns nil.
				// Fallback: firstLine[:MaxOutputBytes]+"...", then per-line
				// truncation → [:MaxLineLength]+"..."
				if !strings.HasPrefix(r.Content, "line-0000:") {
					t.Error("Content should start with first line prefix")
				}
				maxExpected := MaxLineLength + len("...")
				if len(r.Content) > maxExpected {
					t.Errorf("Content length %d should be ≤ %d", len(r.Content), maxExpected)
				}
				if !strings.HasSuffix(r.Content, "...") {
					t.Error("Content should end with ...")
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
				if !strings.Contains(r.Hint, "truncated") {
					t.Errorf("Hint should mention truncated, got %q", r.Hint)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionDir := t.TempDir()
			input := tt.input()
			r := TruncateOutputWithOptions(input, sessionDir, tt.opts)
			tt.check(t, input, r)
		})
	}
}
