package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSpeculativeWritePromoteKeepsFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "new.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	call := message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"speculative"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	payload, ok, drift := exec.Promote(call)
	if drift {
		t.Fatal("Promote reported drift")
	}
	if !ok || payload == nil || payload.Error != nil {
		t.Fatalf("Promote payload=%#v ok=%v", payload, ok)
	}
	a.commitPromotedToolSideEffects(call, payload)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "speculative" {
		t.Fatalf("file content = %q, want speculative", data)
	}
}

func TestSpeculativeWriteDiscardRemovesNewFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "new.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	call := message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"discarded"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	waitForFileContent(t, path, "discarded")
	info, ok := exec.DiscardCall(call.ID, "filtered")
	if !ok || !info.Started || !info.Completed {
		t.Fatalf("discard info=%#v ok=%v", info, ok)
	}
	waitForMissingFile(t, path)
}

func TestSpeculativeEditDiscardRestoresExistingFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "existing.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.EditTool{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	call := message.ToolCall{ID: "edit-1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"old_string":"before","new_string":"after"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	waitForFileContent(t, path, "after")
	if _, ok := exec.DiscardCall(call.ID, "filtered"); !ok {
		t.Fatal("DiscardCall returned false")
	}
	waitForFileContent(t, path, "before")
}

func TestSpeculativeDeleteDiscardRestoresDeletedFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "delete.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.DeleteTool{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	call := message.ToolCall{ID: "delete-1", Name: tools.NameDelete, Args: json.RawMessage(`{"paths":[` + mustJSONString(t, path) + `],"reason":"test rollback"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	waitForMissingFile(t, path)
	if _, ok := exec.DiscardCall(call.ID, "filtered"); !ok {
		t.Fatal("DiscardCall returned false")
	}
	waitForFileContent(t, path, "before")
}

func TestSpeculativeFileMutationConflictSkipsSecondEarlyExecution(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "conflict.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	first := message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"first"}`)}
	second := message.ToolCall{ID: "write-2", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"second"}`)}
	if !exec.Start(first) {
		t.Fatal("first Start returned false")
	}
	if exec.Start(second) {
		t.Fatal("second Start returned true, want conflict skip")
	}
	payload, ok, drift := exec.Promote(first)
	if drift || !ok || payload == nil {
		t.Fatalf("Promote payload=%#v ok=%v drift=%v", payload, ok, drift)
	}
	a.commitPromotedToolSideEffects(first, payload)
	waitForFileContent(t, path, "first")
}

func TestSpeculativeReadSkipsDuringUnpromotedFileMutation(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "barrier.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.ReadTool{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	write := message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"uncommitted"}`)}
	read := message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)}
	if !exec.Start(write) {
		t.Fatal("write Start returned false")
	}
	if exec.Start(read) {
		t.Fatal("read Start returned true while speculative mutation was unpromoted")
	}
	payload, ok, drift := exec.Promote(write)
	if drift || !ok || payload == nil {
		t.Fatalf("Promote payload=%#v ok=%v drift=%v", payload, ok, drift)
	}
	a.commitPromotedToolSideEffects(write, payload)
	if !exec.Start(read) {
		t.Fatal("read Start returned false after mutation promote")
	}
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}

func waitForFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(want) {
		t.Fatalf("file content = %q, want %q", data, want)
	}
}

func waitForMissingFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("path %s still exists or stat failed with non-not-exist error: %v", path, err)
	}
}
