package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// generateLines returns n lines joined by "\n", each with format "line-NNNN".
func generateLines(n int) string {
	var b strings.Builder
	for i := range n {
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
	for i := range n {
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
	input := generatePaddedLines(3000, 20)

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
	for _, want := range []string{"read with offset/limit for needed ranges", "script/parser for huge single-line structured output"} {
		if !strings.Contains(first.Hint, want) {
			t.Fatalf("hint %q should contain %q", first.Hint, want)
		}
	}
	if strings.Count(first.Content, "read with offset/limit for needed ranges") != 1 {
		t.Fatalf("truncated content should mention read guidance once, got %q", first.Content)
	}
	if !strings.Contains(first.Content, "Do not read the entire output by default") {
		t.Fatalf("truncated content should include conditional artifact guidance, got %q", first.Content)
	}
}

func TestExtractArtifactReferencesAcceptsGeneratedFormatsOnly(t *testing.T) {
	path := "/session/tool-outputs/call-123.log"
	guided := artifactReference(path)
	short := shortArtifactReference(path)
	marker := truncationMarker(12, 30, "1-18 and 21-30", path)

	got := ExtractArtifactReferences(strings.Join([]string{guided, marker, short, guided}, "\n"))
	want := []string{guided, short}
	if len(got) != len(want) {
		t.Fatalf("ExtractArtifactReferences() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ExtractArtifactReferences()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	ordinary := "tool echoed " + guided + " after user-controlled text"
	if refs := ExtractArtifactReferences(ordinary); len(refs) != 0 {
		t.Fatalf("ordinary line produced artifact references: %#v", refs)
	}
	oversized := ArtifactReferencePrefix + strings.Repeat("x", maxArtifactReferencePathBytes+1) + ". " + ArtifactReadGuidance
	if refs := ExtractArtifactReferences(oversized); len(refs) != 0 {
		t.Fatalf("oversized path produced artifact references: %#v", refs)
	}
}

func TestTruncateOutputCreatesPrivateArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	sessionDir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := TruncateOutputWithOptions(generatePaddedLines(3000, 20), sessionDir, TruncateOptions{ArtifactKey: "private"})
	if result.SavedPath == "" {
		t.Fatal("SavedPath is empty")
	}
	// The pre-existing session dir keeps its permissions; only the newly
	// created artifact directory and file get the private modes.
	for path, want := range map[string]os.FileMode{
		sessionDir:                     0o755,
		filepath.Dir(result.SavedPath): 0o700,
		result.SavedPath:               0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
		}
	}
}

func TestTruncateArtifactsLandUnderSessionToolOutputs(t *testing.T) {
	sessionDir := t.TempDir()
	first := TruncateOutputWithOptions(generatePaddedLines(3000, 20), sessionDir, TruncateOptions{ArtifactKey: "call-a"})
	second := TruncateOutputWithOptions(generatePaddedLines(3001, 20), sessionDir, TruncateOptions{ArtifactKey: "call-b"})

	for i, result := range []TruncateResult{first, second} {
		if result.SavedPath == "" {
			t.Fatalf("result %d SavedPath is empty", i)
		}
		if !strings.Contains(result.SavedPath, filepath.Join(sessionDir, sessionToolOutputsDirName)) {
			t.Fatalf("artifact path %q should be under %q", result.SavedPath, filepath.Join(sessionDir, sessionToolOutputsDirName))
		}
	}
	if first.SavedPath == second.SavedPath {
		t.Fatal("distinct artifact keys must produce distinct paths")
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
			name:  "line count within byte budget",
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
			name:  "line count above threshold within byte budget",
			input: func() string { return generateLines(MaxOutputLines + 1000) },
			opts:  TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				if r.Truncated {
					t.Fatal("line count alone should not trigger truncation")
				}
				if r.Content != input {
					t.Error("output should stay intact when it fits the byte budget")
				}
				if r.SavedPath != "" {
					t.Errorf("SavedPath should be empty, got %q", r.SavedPath)
				}
			},
		},
		{
			name:  "over-budget output still applies line preview limit",
			input: func() string { return generatePaddedLines(3000, 10) },
			opts:  TruncateOptions{MaxBytes: 25 * 1024},
			check: func(t *testing.T, input string, r TruncateResult) {
				if !r.Truncated {
					t.Fatal("Truncated should be true")
				}
				// Once the byte budget is exceeded, the preview is also capped at
				// MaxOutputLines and keeps both the beginning and end.
				mustContain := []string{"line-0000", "line-2999"}
				for _, s := range mustContain {
					if !strings.Contains(r.Content, s) {
						t.Errorf("Content should contain %q", s)
					}
				}
				if got := strings.Count(r.Content, "line-"); got > MaxOutputLines {
					t.Errorf("preview has too many data lines: %d", got)
				}
				if !strings.Contains(r.Content, "lines omitted") {
					t.Error("Content should contain a line truncation marker")
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
			name: "long line within byte budget remains intact",
			input: func() string {
				return strings.Repeat("a", MaxLineLength+500)
			},
			opts: TruncateOptions{},
			check: func(t *testing.T, input string, r TruncateResult) {
				// Total bytes stay below MaxOutputBytes, so a long line alone must
				// not force the model to read the saved artifact.
				if r.Truncated {
					t.Error("per-line length alone should not trigger truncation")
				}
				if r.Content != input {
					t.Errorf("Content length = %d, want %d", len(r.Content), len(input))
				}
				if r.SavedPath != "" {
					t.Fatalf("SavedPath should be empty, got %q", r.SavedPath)
				}
				if r.Hint != "" || r.ArtifactReference != "" {
					t.Errorf("truncation metadata should be empty, hint=%q ref=%q", r.Hint, r.ArtifactReference)
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
				// A single line exceeds every budget: it is cut once to
				// MaxLineLength and the marker reports the byte cut instead of
				// claiming the line is shown.
				parts := strings.SplitN(r.Content, "\n", 2)
				if len(parts) != 2 {
					t.Fatalf("fallback content should hold the first line plus a marker, got %q", r.Content)
				}
				if want := strings.Repeat("z", MaxLineLength) + "..."; parts[0] != want {
					t.Errorf("first line length %d, want %d bytes plus an ellipsis", len(parts[0]), MaxLineLength)
				}
				wantNotice := fmt.Sprintf("... [line 1 truncated to %d of %d bytes. ", MaxLineLength, len(input))
				if !strings.HasPrefix(parts[1], wantNotice) {
					t.Errorf("marker should report the byte cut %q, got %q", wantNotice, parts[1])
				}
				if strings.Contains(parts[1], "lines omitted") {
					t.Errorf("single-line marker must not claim omitted lines, got %q", parts[1])
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
				if !strings.Contains(parts[1], r.SavedPath) {
					t.Errorf("marker should reference the saved output %q, got %q", r.SavedPath, parts[1])
				}
				if len(ExtractArtifactReferences(r.Content)) == 0 {
					t.Error("fallback content should carry an artifact reference")
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
				// so previewWindow reports no fitting line. The first line is
				// cut to MaxLineLength and the marker reports that cut plus the
				// omitted lines and the reference.
				parts := strings.SplitN(r.Content, "\n", 2)
				if len(parts) != 2 {
					t.Fatalf("fallback content should hold the first line plus a marker, got %q", r.Content[:min(200, len(r.Content))])
				}
				if !strings.HasPrefix(r.Content, "line-0000:") {
					t.Error("Content should start with first line prefix")
				}
				maxExpected := MaxLineLength + len("...")
				if len(parts[0]) > maxExpected {
					t.Errorf("first line length %d should be ≤ %d", len(parts[0]), maxExpected)
				}
				if want := fmt.Sprintf("... [line 1 truncated to %d of 60000 bytes; 9 of 10 lines omitted. ", MaxLineLength); !strings.HasPrefix(parts[1], want) {
					t.Errorf("marker should count omitted lines, got %q", parts[1])
				}
				if r.SavedPath == "" {
					t.Error("SavedPath should be set")
				}
				if !strings.Contains(parts[1], r.SavedPath) {
					t.Errorf("marker should reference the saved output %q, got %q", r.SavedPath, parts[1])
				}
				if len(ExtractArtifactReferences(r.Content)) == 0 {
					t.Error("fallback content should carry an artifact reference")
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

// A byte-trimmed preview must report the lines it actually dropped: the count
// covers the byte budget as well as the line cap, and the marker names the
// sections that survived.
func TestTruncateMarkerCountsEveryDroppedLine(t *testing.T) {
	const total = 588
	input := generatePaddedLines(total, 40)

	r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 16 * 1024, ArtifactKey: "diff"})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}

	var kept []int
	for i := range total {
		if strings.Contains(r.Content, fmt.Sprintf("line-%04d:", i)) {
			kept = append(kept, i)
		}
	}
	head, tail := 0, 0
	for head < len(kept) && kept[head] == head {
		head++
	}
	for tail < len(kept)-head && kept[len(kept)-1-tail] == total-1-tail {
		tail++
	}
	if head+tail != len(kept) {
		t.Fatalf("preview kept lines %v, want a head and tail section", kept)
	}
	if head == 0 || tail == 0 || head+tail >= total {
		t.Fatalf("preview kept head=%d tail=%d of %d lines, want both sections", head, tail, total)
	}

	want := fmt.Sprintf("%d of %d lines omitted; showing lines 1-%d and %d-%d", total-head-tail, total, head, total-tail+1, total)
	if !strings.Contains(r.Content, want) {
		t.Fatalf("Content should contain %q, got:\n%s", want, r.Content)
	}
	if !strings.Contains(r.Content, r.SavedPath) {
		t.Fatalf("marker should reference the saved output %q", r.SavedPath)
	}
}

// A trailing newline terminates the output's last line; it must not be counted
// as one more (empty) line in the omission marker or the shown ranges.
func TestTruncateMarkerCountsTrailingNewlineAsTerminator(t *testing.T) {
	const total = 10
	var b strings.Builder
	for i := range total {
		fmt.Fprintf(&b, "line-%04d: %s\n", i, strings.Repeat("x", 44)) // 50 bytes per line
	}
	input := b.String()

	t.Run("head and tail", func(t *testing.T) {
		r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 300, ArtifactKey: "diff"})
		if !r.Truncated {
			t.Fatal("expected truncation")
		}
		var kept []int
		for i := range total {
			if strings.Contains(r.Content, fmt.Sprintf("line-%04d:", i)) {
				kept = append(kept, i)
			}
		}
		head := 0
		for head < len(kept) && kept[head] == head {
			head++
		}
		tail := 0
		for tail < len(kept)-head && kept[len(kept)-1-tail] == total-1-tail {
			tail++
		}
		if head == 0 || tail == 0 || head+tail != len(kept) {
			t.Fatalf("preview kept lines %v, want a head and tail section", kept)
		}
		want := fmt.Sprintf("%d of %d lines omitted; showing lines 1-%d and %d-%d", total-head-tail, total, head, total-tail+1, total)
		if !strings.Contains(r.Content, want) {
			t.Fatalf("Content should contain %q, got:\n%s", want, r.Content)
		}
	})

	t.Run("tail keeps the final newline", func(t *testing.T) {
		r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 100, ArtifactKey: "diff"})
		if !r.Truncated {
			t.Fatal("expected truncation")
		}
		want := fmt.Sprintf("%d of %d lines omitted; showing lines %d", total-1, total, total)
		if !strings.Contains(r.Content, want) {
			t.Fatalf("Content should contain %q, got:\n%s", want, r.Content)
		}
		if !strings.HasSuffix(r.Content, "\n") {
			t.Fatalf("preview should keep the input's trailing newline, got:\n%q", r.Content)
		}
	})
}

// An output that fits the byte budget once its trailing newline is set aside
// has nothing a preview could omit, so it stays verbatim instead of saving an
// artifact under a marker that reports zero omitted lines.
func TestTruncateIgnoresTrailingNewlineForBudget(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("x", 100) + "\n",
		strings.Repeat("x", 49) + "\n" + strings.Repeat("y", 50) + "\n",
	} {
		r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 100})
		if r.Truncated || r.Content != input || r.SavedPath != "" {
			t.Fatalf("result = %+v, want %q kept verbatim", r, input)
		}
	}
	r := TruncateOutputWithOptions(strings.Repeat("x", 101)+"\n", t.TempDir(), TruncateOptions{MaxBytes: 100})
	if !r.Truncated {
		t.Fatal("an output over budget without its newline must still truncate")
	}
}

func TestTruncateReservesHeadTailSeparator(t *testing.T) {
	for _, trailing := range []string{"", "\n"} {
		input := "aaaa\nbbbbbb" + trailing
		r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 10})
		if !r.Truncated || r.Content == input {
			t.Fatalf("over-budget output must lose content: %+v", r)
		}
		if !strings.Contains(r.Content, "1 of 2 lines omitted") || r.SavedPath == "" {
			t.Fatalf("missing omission notice or saved output: %+v", r)
		}
		data, err := os.ReadFile(r.SavedPath)
		if err != nil || string(data) != input {
			t.Fatalf("saved output = %q, error = %v", data, err)
		}
	}
}

// A fallback preview that shows the first line whole must not decorate it with
// a truncation ellipsis: the marker belongs to the cut, not to the fallback.
func TestTruncateFallbackOmitsEllipsisWhenFirstLineFits(t *testing.T) {
	first := strings.Repeat("x", 50) // fits MaxBytes, exceeds the head budget (40)
	last := strings.Repeat("y", 70)  // exceeds the tail budget (60)
	input := first + "\n" + last
	r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 100, MaxLines: 2000})
	if !r.Truncated {
		t.Fatal("Truncated should be true")
	}
	parts := strings.SplitN(r.Content, "\n", 2)
	if len(parts) != 2 || parts[0] != first {
		t.Fatalf("first line = %q, want the whole first line without an ellipsis", r.Content)
	}
	if strings.HasSuffix(parts[0], "...") {
		t.Fatalf("fully shown first line wears a truncation marker: %q", parts[0])
	}
	if !strings.Contains(parts[1], "1 of 2 lines omitted") {
		t.Fatalf("marker should count omitted lines, got %q", parts[1])
	}
	if r.SavedPath == "" || !strings.Contains(parts[1], r.SavedPath) {
		t.Fatalf("marker should reference the saved output %q, got %q", r.SavedPath, parts[1])
	}
	if len(ExtractArtifactReferences(r.Content)) == 0 {
		t.Error("fallback content should carry an artifact reference")
	}
}

// A fallback cut must land on a UTF-8 boundary and report the bytes it kept.
func TestTruncateFallbackCutsFirstLineOnUTF8Boundary(t *testing.T) {
	// "é" is two bytes, so a 101-byte limit splits the 51st rune.
	input := strings.Repeat("é", 200)
	r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 101, ArtifactKey: "utf8"})
	if !r.Truncated {
		t.Fatal("Truncated should be true")
	}
	parts := strings.SplitN(r.Content, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("fallback content should hold the first line plus a marker, got %q", r.Content)
	}
	if want := strings.Repeat("é", 50) + "..."; parts[0] != want {
		t.Fatalf("first line = %q, want %q", parts[0], want)
	}
	if want := fmt.Sprintf("... [line 1 truncated to 100 of %d bytes. ", len(input)); !strings.HasPrefix(parts[1], want) {
		t.Fatalf("marker = %q, want prefix %q", parts[1], want)
	}
	refs := ExtractArtifactReferences(r.Content)
	if len(refs) != 1 || refs[0] != artifactReference(r.SavedPath) {
		t.Fatalf("ExtractArtifactReferences() = %#v, want the saved output reference", refs)
	}
}

func TestIsTruncationMarkerReferencePrefix(t *testing.T) {
	for prefix, want := range map[string]bool{
		"... [12 of 30 lines omitted; showing lines 1-18 and 21-30.":           true,
		"... [3 of 4 lines omitted.":                                           true,
		"... [line 1 truncated to 2000 of 102400 bytes.":                       true,
		"... [line 1 truncated to 2000 of 60000 bytes; 9 of 10 lines omitted.": true,
		"... [line 1 truncated to many of 60000 bytes.":                        false,
		"... [line 1 truncated to 2000 of 60000 bytes; see below.":             false,
		"... [line 1 truncated to 2000 of 60000 bytes and more.":               false,
		"... [some lines omitted.":                                             false,
		"tool echoed":                                                          false,
	} {
		if got := isTruncationMarkerReferencePrefix(prefix); got != want {
			t.Errorf("isTruncationMarkerReferencePrefix(%q) = %v, want %v", prefix, got, want)
		}
	}
}

// The last line's byte range keeps the output's trailing newline; a line of
// exactly MaxLineLength bytes must neither gain an ellipsis nor lose it.
func TestTruncateKeepsFullLengthLastLineWithNewline(t *testing.T) {
	last := strings.Repeat("z", MaxLineLength)
	input := strings.Repeat("line\n", 3000) + last + "\n"
	r := TruncateOutputWithOptions(input, t.TempDir(), TruncateOptions{MaxBytes: 10000})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if !strings.HasSuffix(r.Content, "\n"+last+"\n") {
		t.Fatalf("preview tail = %q, want the full last line and its newline", r.Content[max(0, len(r.Content)-40):])
	}
	longer := strings.Repeat("z", MaxLineLength+5)
	r = TruncateOutputWithOptions(strings.Repeat("line\n", 3000)+longer+"\n", t.TempDir(), TruncateOptions{MaxBytes: 10000})
	if !strings.HasSuffix(r.Content, strings.Repeat("z", MaxLineLength)+"...\n") {
		t.Fatalf("preview tail = %q, want the cut line to keep its newline", r.Content[max(0, len(r.Content)-40):])
	}
}
