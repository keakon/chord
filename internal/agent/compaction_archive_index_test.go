package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
)

// sampleArchiveMarkdown mimics what session.ExportToMarkdown writes: the
// session header, then `---`-separated message blocks that begin with a
// convformat label or a `# Tool result:` heading.
const sampleArchiveMarkdown = "Session Export\n\n" +
	"Date: 2026-09-02T00:00:00Z\n\n" +
	"---\n\n" +
	"User:\n\nRefactor the parser into two passes.\n\n" +
	"---\n\n" +
	"Assistant:\n\nLet me look at the current shape.\n\n" +
	"# Tool call: Read\n\n## Arguments\n\n```json\n{\"path\":\"internal/parser.go\"}\n```\n\n" +
	"---\n\n" +
	"# Tool result: Read\n\nline one of the result\nline two of the result\n\n" +
	"---\n\n" +
	"Assistant:\n\nThinking:\n\nthinking text\n\nApplied the refactor.\n\n" +
	"---\n\n" +
	convformat.LabelLocalShell + "\n\ncommand:\necho hi\n\noutput:\nhi\n\n" +
	"---\n\n" +
	"Assistant:\n\nFinished.\n\n" +
	"---\n\n" +
	"Session Statistics\n\nInput Tokens: 100\n"

func TestScanCompactionArchiveSegmentsClassifiesBlocks(t *testing.T) {
	segs := scanCompactionArchiveSegments(sampleArchiveMarkdown)
	want := []struct {
		kind string
		name string
	}{
		{kind: "User"},
		{kind: "Assistant"},
		{kind: "Tool result", name: "Read"},
		{kind: "Assistant"}, // thinking-only start, content follows the Thinking line
		{kind: "User (terminal)"},
		{kind: "Assistant"},
	}
	if len(segs) != len(want) {
		t.Fatalf("len(segs) = %d, want %d: %+v", len(segs), len(want), segs)
	}
	for i, w := range want {
		if segs[i].kind != w.kind || segs[i].name != w.name {
			t.Fatalf("segment %d = %+v, want kind %q name %q", i, segs[i], w.kind, w.name)
		}
		if segs[i].mdLine < 1 {
			t.Fatalf("segment %d has invalid mdLine %d", i, segs[i].mdLine)
		}
	}
	// The second assistant leads with a Thinking block; the snippet quotes its
	// first line (the label itself is skipped), which still identifies the
	// segment.
	if segs[3].snippet == "" || !strings.Contains(segs[3].snippet, "thinking text") {
		t.Fatalf("assistant snippet = %q, want the first line after the Thinking label", segs[3].snippet)
	}
	// Terminal blocks are user-side runs and quoted like user messages.
	if !strings.Contains(segs[4].snippet, "command:") {
		t.Fatalf("terminal snippet = %q, want the command line", segs[4].snippet)
	}
	// The Session Statistics trailer is not a message and must not be scanned.
	if segs[len(segs)-1].kind != "Assistant" {
		t.Fatalf("last segment = %+v, want the final Assistant", segs[len(segs)-1])
	}
}

func TestScanCompactionArchiveSegmentsIgnoresContentNoise(t *testing.T) {
	md := "Session Export\n\n---\n\n" +
		"User:\n\n" +
		"here is a body with a divider\n\n---\n\n" +
		"followed by text that is not a label\n\n" +
		"Assistant:\n\n" + // inside the body, not after a divider that starts a block
		"still the same user message\n\n" +
		"---\n\n" +
		"User:\n\nsecond message\n"
	segs := scanCompactionArchiveSegments(md)
	if len(segs) != 2 {
		t.Fatalf("len(segs) = %d, want 2 (content noise must not split messages): %+v", len(segs), segs)
	}
	if segs[0].kind != "User" || !strings.Contains(segs[0].snippet, "here is a body") {
		t.Fatalf("first segment = %+v, want the whole noisy user message", segs[0])
	}
	if segs[1].kind != "User" || segs[1].mdLine <= segs[0].mdLine {
		t.Fatalf("segments out of order: %+v", segs)
	}
}

func TestBuildCompactionArchiveIndexLineNumbersMatchDocument(t *testing.T) {
	final := buildCompactionArchiveIndexedMarkdown(sampleArchiveMarkdown)
	if !strings.HasPrefix(final, archiveIndexHeading+"\n") {
		t.Fatalf("archive must start with the index heading:\n%s", final)
	}
	lines := strings.Split(final, "\n")
	segs := scanCompactionArchiveSegments(sampleArchiveMarkdown)
	entryCount := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		entryCount++
		numStr, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
		if !ok {
			t.Fatalf("index entry without line number: %q", line)
		}
		n, err := strconv.Atoi(numStr)
		if err != nil {
			t.Fatalf("index entry line number %q: %v", numStr, err)
		}
		if n < 1 || n > len(lines) {
			t.Fatalf("index entry references line %d, document has %d lines", n, len(lines))
		}
		// The referenced line must be the start of a real message block.
		if kind, _, ok := archiveSegmentStartKind(strings.TrimSpace(lines[n-1])); !ok || kind == "" {
			t.Fatalf("index entry %q points at %q, want a message start", line, lines[n-1])
		}
	}
	if entryCount != len(segs) {
		t.Fatalf("index entries = %d, want %d segments", entryCount, len(segs))
	}
}

func TestBuildCompactionArchiveIndexFlagsLargeToolResults(t *testing.T) {
	var big strings.Builder
	for range archiveIndexLargeLines + 5 {
		big.WriteString("payload line\n")
	}
	md := "Session Export\n\n---\n\n" +
		"User:\n\nrun the sweep\n\n" +
		"---\n\n" +
		"# Tool result: Grep\n\n" + big.String()
	final := buildCompactionArchiveIndexedMarkdown(md)
	if !strings.Contains(final, "LARGE") {
		t.Fatalf("index must flag oversized tool output:\n%s", final)
	}
	if !strings.Contains(final, "Tool result: Grep") {
		t.Fatalf("index missing the tool result entry:\n%s", final)
	}
	// Small tool results stay unflagged and quote their line count (label +
	// body lines in this handcrafted sample).
	mdSmall := "Session Export\n\n---\n\n# Tool result: Read\n\na\nb\n"
	small := buildCompactionArchiveIndexedMarkdown(mdSmall)
	if strings.Contains(small, "LARGE") || !strings.Contains(small, "(4 lines)") {
		t.Fatalf("small tool result index entry wrong:\n%s", small)
	}
}

func TestBuildCompactionArchiveIndexTruncatesSnippetsRuneSafe(t *testing.T) {
	longUser := strings.Repeat("字", 120) // 120 CJK runes, 360 bytes
	md := "Session Export\n\n---\n\nUser:\n\n" + longUser + "\n\n" +
		"---\n\n" +
		"Assistant:\n\nok\n"
	final := buildCompactionArchiveIndexedMarkdown(md)
	for line := range strings.SplitSeq(final, "\n") {
		if !utf8.ValidString(line) {
			t.Fatalf("index produced invalid UTF-8: %q", line)
		}
	}
	if !strings.Contains(final, "…") {
		t.Fatalf("long snippet must be ellipsized:\n%s", final)
	}
	// The snippet is capped, but the message content itself stays intact.
	if !strings.Contains(final, longUser) {
		t.Fatalf("archive body must keep the full user message:\n%s", final)
	}
}

func TestBuildCompactionArchiveIndexLeavesUnindexableMarkdownAlone(t *testing.T) {
	md := "Session Export\n\nDate: today\n"
	if got := buildCompactionArchiveIndexedMarkdown(md); got != md {
		t.Fatalf("header-only archive must stay unchanged, got:\n%s", got)
	}
	if got := buildCompactionArchiveIndexedMarkdown(""); got != "" {
		t.Fatalf("empty archive must stay unchanged, got %q", got)
	}
}

func TestExportCompactionHistoryWritesArchiveIndex(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "Inspect the tool results."},
		{
			Role:      message.RoleAssistant,
			Content:   "Reading the parser.",
			ToolCalls: []message.ToolCall{{ID: "c1", Name: "Read", Args: []byte(`{"path":"internal/parser.go"}`)}},
		},
		{Role: message.RoleTool, ToolCallID: "c1", Content: "package parser\n\nfunc Parse(...) {}"},
		{Role: message.RoleAssistant, Content: "Done inspecting."},
	}
	absPath, _, _, err := a.exportCompactionHistory(msgs, 1, []string{"parser inspection"}, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("exportCompactionHistory: %v", err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, archiveIndexHeading) {
		t.Fatalf("archive missing index heading:\n%s", content)
	}
	// Every index entry points at a real message-block start in the file.
	for line := range strings.SplitSeq(content, "\n") {
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		numStr, rest, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		fileLines := strings.Split(content, "\n")
		if n < 1 || n > len(fileLines) {
			t.Fatalf("entry %q references line %d beyond the %d-line archive", line, n, len(fileLines))
		}
		if _, _, ok := archiveSegmentStartKind(strings.TrimSpace(fileLines[n-1])); !ok {
			t.Fatalf("entry %q points at non-message line %q", line, fileLines[n-1])
		}
		if strings.Contains(rest, "Tool result") && !strings.Contains(rest, "Read") {
			t.Fatalf("tool result entry missing tool name: %q", line)
		}
	}
	// The full original content must still be present below the index.
	for _, want := range []string{"Inspect the tool results.", "package parser", "Done inspecting."} {
		if !strings.Contains(content, want) {
			t.Fatalf("archive lost message content %q:\n%s", want, content)
		}
	}
	// Sanity: the archive file the meta file references is the same one.
	if filepath.Base(absPath) != "history-1.md" {
		t.Fatalf("unexpected archive path %q", absPath)
	}
}
