package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestMainAgent_ReplaceEditCanModifyFileWithoutPriorSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{BaseDir: projectRoot})

	editArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "old_string": "before", "new_string": "after"})
	if err != nil {
		t.Fatalf("Marshal edit args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: editArgs})
	if err != nil {
		t.Fatalf("ReplaceEdit without Read failed: %v", err)
	}
	if result.PreFilePath == "" || result.PreContent != "before" || !result.PreExisted {
		t.Fatalf("unread ReplaceEdit pre-write state = path %q existed %v content %q, want captured before content", result.PreFilePath, result.PreExisted, result.PreContent)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "after" {
		t.Fatalf("file content = %q, want after", got)
	}
}

func TestMainAgent_EditCanPatchFileWithoutPriorSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("Edit without Read failed: %v", err)
	}
	if result.PreFilePath == "" || result.PreContent != "before" || !result.PreExisted {
		t.Fatalf("unread Edit pre-write state = path %q existed %v content %q, want captured before content", result.PreFilePath, result.PreExisted, result.PreContent)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "after" {
		t.Fatalf("file content = %q, want after", got)
	}
}

func TestMainAgent_FailedEditWithoutReadDoesNotModifyFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})

	err := executeEdit(t, a, path, "missing", "after")
	if err == nil || !strings.Contains(err.Error(), "hunk not found") {
		t.Fatalf("error = %v, want hunk-not-found error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "before" {
		t.Fatalf("file content = %q, want unchanged before", got)
	}
}

func TestMainAgent_EditAfterWriteTracksSnapshotForStaleBackup(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})

	writeArgs, err := json.Marshal(map[string]any{"path": path, "content": "before\n"})
	if err != nil {
		t.Fatalf("Marshal write args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: writeArgs}); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-external\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("Edit after Write failed: %v", err)
	}
	if !strings.Contains(result.Result, "Warning: the file changed on disk") || !strings.Contains(result.Result, "Backup saved to:") {
		t.Fatalf("result missing stale warning/backup: %q", result.Result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "after\n" {
		t.Fatalf("file content = %q, want after", got)
	}
}

func TestMainAgent_FileMentionTracksSnapshotForStaleBackup(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})
	a.recordCommittedUserMessage(message.Message{Role: "user", Parts: []message.ContentPart{{Type: "text", Text: `<file path="` + path + `">` + "\nbefore\n</file>"}}})
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-external\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("Edit after @file mention failed: %v", err)
	}
	if !strings.Contains(result.Result, "Warning: the file changed on disk") || !strings.Contains(result.Result, "Backup saved to:") {
		t.Fatalf("result missing stale warning/backup: %q", result.Result)
	}
}

func TestFileRefPathFromContentUnescapesQuotedPath(t *testing.T) {
	got, ok := message.FirstFileRefPath(`<file path="dir/has\"quote&amp;space.txt">` + "\nbody\n</file>")
	if !ok {
		t.Fatal("expected file ref")
	}
	want := `dir/has"quote&space.txt`
	if got != want {
		t.Fatalf("FirstFileRefPath() = %q, want %q", got, want)
	}
}

func TestMainAgent_EditStaleCreatesBackup(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})

	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-external\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("Edit after stale file failed: %v", err)
	}
	if !strings.Contains(result.Result, "Warning: the file changed on disk") || !strings.Contains(result.Result, "Backup saved to:") {
		t.Fatalf("result missing stale warning/backup: %q", result.Result)
	}
	backupPath := strings.TrimSpace(result.Result[strings.LastIndex(result.Result, "Backup saved to:")+len("Backup saved to:"):])
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile backup %q: %v", backupPath, err)
	}
	if string(backup) != "external\n" {
		t.Fatalf("backup content = %q, want external", backup)
	}
}

func TestMainAgent_EditBackupFailureDoesNotBlockEdit(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "large.txt")
	largeTail := strings.Repeat("x", maxSingleToolBackupBytes+1)
	if err := os.WriteFile(path, []byte("before\n"+largeTail), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.PatchTool{BaseDir: projectRoot})

	readArgs, err := json.Marshal(map[string]any{"path": path, "limit": 1})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	updated := "external\n" + largeTail
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"path": "large.txt", "patch": "@@\n-external\n+patched\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NamePatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("Edit with backup failure should continue: %v", err)
	}
	if !strings.Contains(result.Result, "No backup was created: the file exceeds the backup size limit") {
		t.Fatalf("result missing no-backup warning: %q", result.Result)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(got), "patched\n") {
		t.Fatalf("file content = %q, want patched", got)
	}
}
