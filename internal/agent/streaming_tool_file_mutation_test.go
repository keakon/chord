package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestSpeculativeWritePromoteKeepsFile(t *testing.T) {
	projectRoot := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	path := filepath.Join(projectRoot, "new.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})

	ctx := t.Context()
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

func TestSpeculativeWriteOnStaleFileWarnsAndBacksUp(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "stale.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.WriteTool{})

	readArgs := json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
		t.Fatalf("external WriteFile: %v", err)
	}

	ctx := t.Context()
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
	if !strings.Contains(payload.Result, "Warning: the file changed on disk") || !strings.Contains(payload.Result, "Backup saved to:") {
		t.Fatalf("result missing stale warning/backup: %q", payload.Result)
	}
	backupPath := strings.TrimSpace(payload.Result[strings.LastIndex(payload.Result, "Backup saved to:")+len("Backup saved to:"):])
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("ReadFile backup %q: %v", backupPath, err)
	}
	if string(backup) != "external" {
		t.Fatalf("backup content = %q, want external", backup)
	}
}

func TestSpeculativeWriteDiscardRemovesNewFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "new.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})

	ctx := t.Context()
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
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	// Baseline Read so Edit satisfies the read-before-patch precondition.
	readArgs := json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	ctx := t.Context()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	call := message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"` + filepath.Base(path) + `","patch":"@@\n-before\n+after\n"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	waitForFileContent(t, path, "after")
	if _, ok := exec.DiscardCall(call.ID, "filtered"); !ok {
		t.Fatal("DiscardCall returned false")
	}
	waitForFileContent(t, path, "before")
}

func TestSpeculativeEditDiscardWhileExecutingRestoresExistingFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "existing.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.EditTool{})

	// Baseline Read so Edit satisfies the read-before-patch precondition.
	readArgs := json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	ctx := t.Context()
	toolReturned := make(chan struct{})
	releaseExecutor := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseExecutor) }) }
	defer release()
	exec := NewStreamingToolExecutor(7, ctx, nil, func(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		result, err := a.executeToolCallSpeculative(ctx, tc)
		close(toolReturned)
		select {
		case <-releaseExecutor:
		case <-ctx.Done():
		}
		return result, err
	})
	call := message.ToolCall{ID: "patch-1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"` + filepath.Base(path) + `","patch":"@@\n-before\n+after\n"}`)}
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	select {
	case <-toolReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("speculative edit did not return")
	}
	waitForFileContent(t, path, "after")
	if info, ok := exec.DiscardCall(call.ID, "filtered"); !ok {
		t.Fatal("DiscardCall returned false")
	} else if info.Completed {
		t.Fatalf("discard happened after executor completion; test did not exercise executing discard path: %#v", info)
	}
	release()
	waitForFileContent(t, path, "before")
}

func TestSpeculativeWriteDiscardWhileExecutingRetainsConflictUntilRollback(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "new.txt")
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.ReadTool{})

	ctx := t.Context()
	toolReturned := make(chan struct{})
	releaseExecutor := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseExecutor) }) }
	defer release()
	exec := NewStreamingToolExecutor(7, ctx, nil, func(ctx context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		result, err := a.executeToolCallSpeculative(ctx, tc)
		if tc.ID == "write-1" {
			close(toolReturned)
			select {
			case <-releaseExecutor:
			case <-ctx.Done():
			}
		}
		return result, err
	})
	first := message.ToolCall{ID: "write-1", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"first"}`)}
	second := message.ToolCall{ID: "write-2", Name: tools.NameWrite, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `,"content":"second"}`)}
	read := message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)}
	if !exec.Start(first) {
		t.Fatal("first Start returned false")
	}
	select {
	case <-toolReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("first speculative write did not return")
	}
	waitForFileContent(t, path, "first")
	if info, ok := exec.DiscardCall(first.ID, "filtered"); !ok {
		t.Fatal("DiscardCall returned false")
	} else if !info.Started || info.Completed {
		t.Fatalf("discard info=%#v, want started and not completed", info)
	}
	if exec.Start(second) {
		t.Fatal("second same-path write started before discarded write rolled back")
	}
	if exec.Start(read) {
		t.Fatal("read started while discarded speculative mutation was still dirty")
	}
	finalizedSlot := make(chan func(), 1)
	go func() {
		finalizedSlot <- exec.AcquireExecutionSlot(ctx)
	}()
	select {
	case releaseSlot := <-finalizedSlot:
		if releaseSlot != nil {
			releaseSlot()
		}
		t.Fatal("finalized execution slot acquired before discarded write rolled back")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	waitForMissingFile(t, path)
	var releaseSlot func()
	select {
	case releaseSlot = <-finalizedSlot:
	case <-time.After(2 * time.Second):
		t.Fatal("finalized execution slot did not unblock after discarded write rollback")
	}
	if releaseSlot == nil {
		t.Fatal("AcquireExecutionSlot returned nil before context cancellation")
	}
	releaseSlot()
	deadline := time.Now().Add(2 * time.Second)
	for !exec.Start(second) {
		if time.Now().After(deadline) {
			t.Fatal("second same-path write did not start after discarded write rollback")
		}
		time.Sleep(10 * time.Millisecond)
	}
	payload, ok, drift := exec.Promote(second)
	if drift || !ok || payload == nil || payload.Error != nil {
		t.Fatalf("Promote payload=%#v ok=%v drift=%v", payload, ok, drift)
	}
	a.commitPromotedToolSideEffects(second, payload)
	waitForFileContent(t, path, "second")
}

func TestSpeculativeDeleteDiscardRestoresDeletedFile(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "delete.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.DeleteTool{})

	ctx := t.Context()
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

	ctx := t.Context()
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

	ctx := t.Context()
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
