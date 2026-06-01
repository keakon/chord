package agentdiff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func toolCall(name string, args any) message.ToolCall {
	b, _ := json.Marshal(args)
	return message.ToolCall{Name: name, Args: b}
}

func editArgs(path string) map[string]string {
	return map[string]string{"path": filepath.ToSlash(path), "patch": "@@\n before\n-after\n+AFTER\n"}
}

func TestCapturePreWriteState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(dir, "missing.txt")
	gotPath, content, existed := CapturePreWriteState(toolCall(tools.NameEdit, editArgs(missing)))
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState missing = (%q, %q, %v)", gotPath, content, existed)
	}

	gotPath, content, existed = CapturePreWriteState(toolCall("Read", map[string]string{"path": path}))
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState read = (%q, %q, %v)", gotPath, content, existed)
	}

	gotPath, content, existed = CapturePreWriteState(message.ToolCall{Name: "write", Args: json.RawMessage(`{`)})
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState write = (%q, %q, %v)", gotPath, content, existed)
	}
}

func TestGenerateToolDiffForEdit(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv("CHORD_PROJECT_ROOT", dir)
	path := "file.txt"
	if err := os.WriteFile(path, []byte("before\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("before\nAFTER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preContent := "before\nafter\n"
	if decoded, err := tools.ReadDecodedTextFile(path); err != nil || decoded.Text != "before\nAFTER\n" {
		t.Fatalf("ReadDecodedTextFile = %q, %v", decoded.Text, err)
	}
	call := toolCall(tools.NameEdit, editArgs(path))
	summary := GenerateToolDiff(call, preContent, tools.ExtractEditPathFromArgs(call.Args))
	if summary.Added != 1 || summary.Removed != 1 || !strings.Contains(summary.Text, "+AFTER") {
		t.Fatalf("unexpected Edit diff: %+v", summary)
	}

	if got := GenerateToolDiff(toolCall(tools.NameEdit, editArgs("missing.txt")), "before", ""); got != (Summary{}) {
		t.Fatalf("missing captured path diff = %+v", got)
	}
}

func TestGenerateToolDiffUsesCapturedPath(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before\nAFTER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := toolCall(tools.NameEdit, editArgs("file.txt"))
	summary := GenerateToolDiff(call, "before\nafter\n", path)
	if summary.Added != 1 || summary.Removed != 1 || !strings.Contains(summary.Text, "+AFTER") {
		t.Fatalf("unexpected Edit diff with captured path: %+v", summary)
	}
}
