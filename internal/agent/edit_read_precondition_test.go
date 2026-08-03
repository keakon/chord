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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

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

func TestMainAgent_WriteExistingFileRequiresCurrentWholeFileRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{BaseDir: projectRoot})
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})

	partialArgs, _ := json.Marshal(map[string]any{"path": path, "offset": 1, "limit": 1})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-partial", Name: tools.NameRead, Args: partialArgs}); err != nil {
		t.Fatalf("partial read: %v", err)
	}
	writeArgs, _ := json.Marshal(map[string]any{"path": path, "content": "replacement\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-after-partial", Name: tools.NameWrite, Args: writeArgs}); err == nil || !strings.Contains(err.Error(), "read the complete file first") {
		t.Fatalf("write after partial read error = %v, want full-read requirement", err)
	}

	fullArgs, _ := json.Marshal(map[string]any{"path": path})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-full", Name: tools.NameRead, Args: fullArgs}); err != nil {
		t.Fatalf("full read: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-after-full", Name: tools.NameWrite, Args: writeArgs}); err != nil {
		t.Fatalf("write after full read: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "replacement\n" {
		t.Fatalf("file content = %q, %v; want replacement", got, err)
	}
}

func TestMainAgent_WriteAfterOwnWholeFileWriteNeedsNoReread(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})
	a.tools.Register(tools.DeleteTool{BaseDir: projectRoot})

	firstArgs, _ := json.Marshal(map[string]any{"path": path, "content": "v1\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: firstArgs}); err != nil {
		t.Fatalf("create write: %v", err)
	}
	secondArgs, _ := json.Marshal(map[string]any{"path": path, "content": "v2\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-2", Name: tools.NameWrite, Args: secondArgs}); err != nil {
		t.Fatalf("overwrite after own whole-file write should not require a re-read: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "v2\n" {
		t.Fatalf("file content = %q, %v; want v2", got, err)
	}
	deleteArgs, _ := json.Marshal(map[string]any{"paths": []string{path}, "reason": "cleanup after rewrite"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "delete-1", Name: tools.NameDelete, Args: deleteArgs}); err != nil {
		t.Fatalf("delete after own whole-file write should not require a re-read: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: %v", err)
	}
}

func TestMainAgent_WriteAfterExternallyChangedOwnWriteRequiresReread(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})

	firstArgs, _ := json.Marshal(map[string]any{"path": path, "content": "v1\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: firstArgs}); err != nil {
		t.Fatalf("create write: %v", err)
	}
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}
	secondArgs, _ := json.Marshal(map[string]any{"path": path, "content": "v2\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-2", Name: tools.NameWrite, Args: secondArgs}); err == nil || !strings.Contains(err.Error(), "changed after the last read") {
		t.Fatalf("write over externally changed content error = %v, want reread requirement", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "external\n" {
		t.Fatalf("file content = %q, %v; want external content preserved", got, err)
	}
}

func TestMainAgent_PartialFileMentionDoesNotAuthorizeWholeFileWrite(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type: message.ContentPartText,
		Text: `<file path="demo.txt" lines="1-1">` + "\none\n</file>",
	}}})
	args, _ := json.Marshal(map[string]any{"path": "demo.txt", "content": "replacement\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "read the complete file first") {
		t.Fatalf("write error = %v, want full-read requirement", err)
	}
}

func TestMainAgent_StaleFileMentionDoesNotAuthorizeCurrentWholeFileWrite(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("current unseen content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type: message.ContentPartText,
		Text: `<file path="demo.txt">` + "\nstale displayed content\n</file>",
	}}})
	args, _ := json.Marshal(map[string]any{"path": "demo.txt", "content": "replacement\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write", Name: tools.NameWrite, Args: args}); err == nil || !strings.Contains(err.Error(), "read the complete file first") {
		t.Fatalf("write error = %v, want full-read requirement", err)
	}
}

type readThenMutateTool struct {
	delegate    tools.ReadTool
	path        string
	replacement []byte
}

func (t readThenMutateTool) Name() string               { return tools.NameRead }
func (t readThenMutateTool) Description() string        { return t.delegate.Description() }
func (t readThenMutateTool) Parameters() map[string]any { return t.delegate.Parameters() }
func (t readThenMutateTool) IsReadOnly() bool           { return true }
func (t readThenMutateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	out, err := t.delegate.Execute(ctx, args)
	if err != nil {
		return out, err
	}
	if err := os.WriteFile(t.path, t.replacement, 0o644); err != nil {
		return out, err
	}
	return out, nil
}

func TestMainAgent_ReadObservationUsesSameBytesAsResult(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("old-one\nold-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(readThenMutateTool{
		delegate:    tools.ReadTool{BaseDir: projectRoot},
		path:        path,
		replacement: []byte("external-one\nexternal-two\n"),
	})
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})
	readArgs, _ := json.Marshal(map[string]any{"path": "demo.txt"})
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read", Name: tools.NameRead, Args: readArgs})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(result.Result, "old-one") || strings.Contains(result.Result, "external-one") {
		t.Fatalf("unexpected read result: %q", result.Result)
	}
	writeArgs, _ := json.Marshal(map[string]any{"path": "demo.txt", "content": "replacement\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write", Name: tools.NameWrite, Args: writeArgs}); err == nil || !strings.Contains(err.Error(), "changed after the last read") {
		t.Fatalf("write error = %v, want changed-after-read rejection", err)
	}
}

func TestSubAgentRelativeWriteUsesWorkDirForObservation(t *testing.T) {
	parentRoot := t.TempDir()
	workDir := t.TempDir()
	parent := newTestMainAgent(t, parentRoot)
	parentPath := filepath.Join(parentRoot, "same.txt")
	childPath := filepath.Join(workDir, "same.txt")
	if err := os.WriteFile(parentPath, []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte("child-unread\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(tools.ReadTool{BaseDir: parentRoot})
	registry.Register(tools.WriteTool{BaseDir: parentRoot})
	sub := NewSubAgent(SubAgentConfig{
		InstanceID: "worker-1", AgentDefName: "worker", LLMClient: newTestLLMClient(),
		Parent: parent, ParentCtx: parent.parentCtx, Cancel: func() {}, BaseTools: registry,
		WorkDir: workDir, SessionDir: parent.sessionDir, ModelName: "test-model",
	})
	readArgs, _ := json.Marshal(map[string]any{"path": parentPath})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "read-parent", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	writeArgs, _ := json.Marshal(map[string]any{"path": "same.txt", "content": "replacement\n"})
	if _, err := sub.executeToolCall(context.Background(), message.ToolCall{ID: "write-child", Name: tools.NameWrite, Args: writeArgs}); err == nil || !strings.Contains(err.Error(), "read the complete file first") {
		t.Fatalf("relative child write error = %v, want full-read requirement", err)
	}
	if got, err := os.ReadFile(childPath); err != nil || string(got) != "child-unread\n" {
		t.Fatalf("child file = %q, %v; want unchanged", got, err)
	}
}

func TestMainAgent_WriteAfterEditRequiresReread(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{BaseDir: projectRoot})
	a.tools.Register(tools.EditTool{BaseDir: projectRoot})
	a.tools.Register(tools.WriteTool{BaseDir: projectRoot})
	readArgs, _ := json.Marshal(map[string]any{"path": path})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("read: %v", err)
	}
	editArgs, _ := json.Marshal(map[string]any{"path": path, "old_string": "before", "new_string": "after"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: editArgs}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	writeArgs, _ := json.Marshal(map[string]any{"path": path, "content": "stale whole file\n"})
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: writeArgs}); err == nil || !strings.Contains(err.Error(), "changed after the last read") {
		t.Fatalf("write after edit error = %v, want reread requirement", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "after\n" {
		t.Fatalf("file content = %q, %v; want edit preserved", got, err)
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

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
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})
	a.recordCommittedUserMessage(message.Message{Role: "user", Parts: []message.ContentPart{{Type: "text", Text: `<file path="` + path + `">` + "\nbefore\n\n</file>"}}})
	if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"path": "demo.txt", "patch": "@@\n-external\n+after\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

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
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

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
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
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
