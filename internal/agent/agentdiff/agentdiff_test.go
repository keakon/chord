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
	gotPath, content, existed := CapturePreWriteState(toolCall(tools.NameEdit, editArgs(missing)), "")
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState missing = (%q, %q, %v)", gotPath, content, existed)
	}

	gotPath, content, existed = CapturePreWriteState(toolCall("Read", map[string]string{"path": path}), "")
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState read = (%q, %q, %v)", gotPath, content, existed)
	}

	gotPath, content, existed = CapturePreWriteState(message.ToolCall{Name: "write", Args: json.RawMessage(`{`)}, "")
	if gotPath != "" || content != "" || existed {
		t.Fatalf("CapturePreWriteState write = (%q, %q, %v)", gotPath, content, existed)
	}
}

func TestCapturePreWriteStateUsesBaseDirForReplaceEdit(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "file.txt"), []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, _ := os.Getwd()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	gotPath, content, existed := CapturePreWriteState(toolCall(tools.NameEdit, map[string]string{"path": "file.txt"}), dir)
	if gotPath != path || content != "before\n" || !existed {
		t.Fatalf("CapturePreWriteState edit = (%q, %q, %v), want project file", gotPath, content, existed)
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
	summary := GenerateToolDiff(call, preContent, tools.ExtractEditPathFromArgs(call.Args), true)
	if summary.Added != 1 || summary.Removed != 1 || !strings.Contains(summary.Text, "+AFTER") {
		t.Fatalf("unexpected Edit diff: %+v", summary)
	}

	if got := GenerateToolDiff(toolCall(tools.NameEdit, editArgs("missing.txt")), "before", "", true); got != (Summary{}) {
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
	summary := GenerateToolDiff(call, "before\nafter\n", path, true)
	if summary.Added != 1 || summary.Removed != 1 || !strings.Contains(summary.Text, "+AFTER") {
		t.Fatalf("unexpected Edit diff with captured path: %+v", summary)
	}
}

func TestGenerateToolDiffForWrite(t *testing.T) {
	dir := t.TempDir()
	oldWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// Test new file creation
	newPath := "newfile.txt"
	if err := os.WriteFile(newPath, []byte("line1\nline2\nline3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := toolCall(tools.NameWrite, map[string]string{"path": newPath})
	summary := GenerateToolDiff(call, "", newPath, false)
	if summary.Added != 3 || summary.Removed != 0 {
		t.Fatalf("new file diff = %+v, want Added=3, Removed=0", summary)
	}

	// Test file overwrite
	preContent := "old1\nold2\n"
	if err := os.WriteFile(newPath, []byte("new1\nnew2\nnew3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary = GenerateToolDiff(call, preContent, newPath, true)
	if summary.Added != 3 || summary.Removed != 2 {
		t.Fatalf("overwrite diff = %+v, want Added=3, Removed=2", summary)
	}

	// Test overwriting an empty existing file (regression test for empty file handling)
	emptyPath := "empty.txt"
	if err := os.WriteFile(emptyPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	// Now write content to it
	if err := os.WriteFile(emptyPath, []byte("new1\nnew2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyCall := toolCall(tools.NameWrite, map[string]string{"path": emptyPath})
	// File existed but was empty, so preContent="" but preExisted=true
	summary = GenerateToolDiff(emptyCall, "", emptyPath, true)
	// Should show as a diff (2 added, 0 removed), not as a new file
	if summary.Added != 2 || summary.Removed != 0 {
		t.Fatalf("empty file overwrite diff = %+v, want Added=2, Removed=0", summary)
	}
	// Should have diff text (not empty)
	if summary.Text == "" {
		t.Fatalf("empty file overwrite should have diff text, got empty")
	}
}

func TestGenerateToolDiffForWriteUsesCapturedPath(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "file.txt"), []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWD, _ := os.Getwd()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	call := toolCall(tools.NameWrite, map[string]string{"path": "file.txt"})
	summary := GenerateToolDiff(call, "old\n", path, true)
	if summary.Added != 1 || summary.Removed != 1 || !strings.Contains(summary.Text, "+new") {
		t.Fatalf("unexpected Write diff with captured path: %+v", summary)
	}
}
