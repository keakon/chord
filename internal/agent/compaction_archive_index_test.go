package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/convformat"
	"github.com/keakon/chord/internal/message"
)

func TestExportCompactionHistoryWritesArchiveIndex(t *testing.T) {
	t.Parallel()
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
	absPath, _, _, err := a.exportCompactionHistory(msgs, msgs, 1, []string{"parser inspection"}, a.captureCompactionArchiveMeta())
	if err != nil {
		t.Fatalf("exportCompactionHistory: %v", err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "# Index\n") {
		t.Fatalf("archive missing index heading:\n%s", content)
	}
	// Every index entry points at a real message-block start in the file.
	entryCount := 0
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
		entryCount++
		fileLines := strings.Split(content, "\n")
		if n < 1 || n > len(fileLines) {
			t.Fatalf("entry %q references line %d beyond the %d-line archive", line, n, len(fileLines))
		}
		label := strings.TrimSpace(fileLines[n-1])
		if label != convformat.LabelUser && label != convformat.LabelAssistant && label != "# Tool result: Read" {
			t.Fatalf("entry %q points at non-message line %q", line, fileLines[n-1])
		}
		if strings.Contains(rest, "Tool result") && !strings.Contains(rest, "Read") {
			t.Fatalf("tool result entry missing tool name: %q", line)
		}
	}
	if entryCount != len(msgs) {
		t.Fatalf("index entries = %d, want %d", entryCount, len(msgs))
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
