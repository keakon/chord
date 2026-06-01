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

func TestMainAgent_ApplyPatchRequiresObservationFirst(t *testing.T) {
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
	a.tools.Register(tools.ApplyPatchTool{})

	patchArgs, err := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Update File: demo.txt\n@@\n-before\n+after\n*** End Patch\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "has not been observed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PreFilePath != "" || result.PreContent != "" || result.PreExisted {
		t.Fatalf("unread ApplyPatch captured pre-write state before read precondition: path=%q existed=%v content=%q", result.PreFilePath, result.PreExisted, result.PreContent)
	}

	readArgs, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Marshal read args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-2", Name: tools.NameApplyPatch, Args: patchArgs}); err != nil {
		t.Fatalf("ApplyPatch after Read failed: %v", err)
	}
}

func TestMainAgent_FailedUnreadApplyPatchDoesNotGrantObservation(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ApplyPatchTool{})

	for i := 0; i < 2; i++ {
		err := executeApplyPatch(t, a, path, "before", "after")
		if err == nil || !strings.Contains(err.Error(), "has not been observed") {
			t.Fatalf("attempt %d error = %v, want unread-file error", i+1, err)
		}
	}
}

func TestMainAgent_ApplyPatchAfterWriteUsesWriteAsObservation(t *testing.T) {
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
	a.tools.Register(tools.ApplyPatchTool{})

	writeArgs, err := json.Marshal(map[string]any{"path": path, "content": "before\n"})
	if err != nil {
		t.Fatalf("Marshal write args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: writeArgs}); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	patchArgs, err := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Update File: demo.txt\n@@\n-before\n+after\n*** End Patch\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs}); err != nil {
		t.Fatalf("ApplyPatch after Write failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "after\n" {
		t.Fatalf("file content = %q, want after", got)
	}
}

func TestMainAgent_ApplyPatchAfterFileMentionObservation(t *testing.T) {
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
	a.tools.Register(tools.ApplyPatchTool{})
	a.recordCommittedUserMessage(message.Message{Role: "user", Parts: []message.ContentPart{{Type: "text", Text: `<file path="` + path + `">` + "\nbefore\n</file>"}}})

	patchArgs, err := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Update File: demo.txt\n@@\n-before\n+after\n*** End Patch\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs}); err != nil {
		t.Fatalf("ApplyPatch after @file mention failed: %v", err)
	}
}

func TestFileRefPathFromContentUnescapesQuotedPath(t *testing.T) {
	got := fileRefPathFromContent(`<file path="dir/has\"quote&amp;space.txt">` + "\nbody\n</file>")
	want := `dir/has"quote&space.txt`
	if got != want {
		t.Fatalf("fileRefPathFromContent() = %q, want %q", got, want)
	}
}

func TestMainAgent_ApplyPatchStaleCreatesBackup(t *testing.T) {
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
	a.tools.Register(tools.ApplyPatchTool{})

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

	patchArgs, err := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Update File: demo.txt\n@@\n-external\n+after\n*** End Patch\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("ApplyPatch after stale file failed: %v", err)
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

func TestMainAgent_ApplyPatchBackupFailureDoesNotBlockEdit(t *testing.T) {
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
	a.tools.Register(tools.ApplyPatchTool{})

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

	patchArgs, err := json.Marshal(map[string]any{"patch": "*** Begin Patch\n*** Update File: large.txt\n@@\n-external\n+patched\n*** End Patch\n"})
	if err != nil {
		t.Fatalf("Marshal patch args: %v", err)
	}
	result, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: patchArgs})
	if err != nil {
		t.Fatalf("ApplyPatch with backup failure should continue: %v", err)
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
