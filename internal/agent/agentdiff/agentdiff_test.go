package agentdiff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func toolCall(name string, args any) message.ToolCall {
	b, _ := json.Marshal(args)
	return message.ToolCall{Name: name, Args: b}
}

func TestCapturePreWriteState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	gotPath, content, existed := CapturePreWriteState(toolCall("Write", map[string]string{"path": path}))
	if gotPath != path || content != "before\n" || !existed {
		t.Fatalf("CapturePreWriteState existing = (%q, %q, %v)", gotPath, content, existed)
	}

	missing := filepath.Join(dir, "missing.txt")
	gotPath, content, existed = CapturePreWriteState(toolCall("Edit", map[string]string{"path": missing}))
	if gotPath != missing || content != "" || existed {
		t.Fatalf("CapturePreWriteState missing = (%q, %q, %v)", gotPath, content, existed)
	}

	gotPath, content, existed = CapturePreWriteState(toolCall("Read", map[string]string{"path": path}))
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState read = (%q, %q, %v)", gotPath, content, existed)
	}

	gotPath, content, existed = CapturePreWriteState(message.ToolCall{Name: "Write", Args: json.RawMessage(`{`)})
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState invalid json = (%q, %q, %v)", gotPath, content, existed)
	}
}

func TestGenerateToolDiffForWrite(t *testing.T) {
	summary := GenerateToolDiff(toolCall("Write", map[string]string{
		"path":    "file.txt",
		"content": "before\nafter\n",
	}), "before\n", "file.txt")
	if summary.Added != 1 || summary.Removed != 0 || !strings.Contains(summary.Text, "+after") {
		t.Fatalf("unexpected write diff: %+v", summary)
	}

	if got := GenerateToolDiff(message.ToolCall{Name: "Write", Args: json.RawMessage(`{`)}, "before", "file.txt"); got != (Summary{}) {
		t.Fatalf("invalid write diff = %+v", got)
	}
	if got := GenerateToolDiff(toolCall("Read", map[string]string{"path": "file.txt"}), "before", "file.txt"); got != (Summary{}) {
		t.Fatalf("read diff = %+v", got)
	}
}

func TestGenerateToolDiffForEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary := GenerateToolDiff(toolCall("Edit", map[string]string{"path": path}), "before\n", path)
	if summary.Added != 1 || summary.Removed != 0 || !strings.Contains(summary.Text, "+after") {
		t.Fatalf("unexpected edit diff: %+v", summary)
	}

	if got := GenerateToolDiff(toolCall("Edit", map[string]string{"path": filepath.Join(dir, "missing.txt")}), "before", path); got != (Summary{}) {
		t.Fatalf("missing edit diff = %+v", got)
	}
}
