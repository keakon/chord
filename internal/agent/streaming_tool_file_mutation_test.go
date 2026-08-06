package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

func TestSpeculativeApplyPatchPromoteIncludesEveryFileDiff(t *testing.T) {
	projectRoot := t.TempDir()
	files := []string{"one.txt", "two.txt", "three.txt", "four.txt"}
	for _, name := range files {
		content := "before-" + strings.TrimSuffix(name, ".txt") + "\n"
		if err := os.WriteFile(filepath.Join(projectRoot, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	patch := "*** Begin Patch\n"
	for _, name := range files {
		stem := strings.TrimSuffix(name, ".txt")
		patch += "*** Update File: " + name + "\n@@\n-before-" + stem + "\n+after-" + stem + "\n"
	}
	patch += "*** End Patch"
	args, err := json.Marshal(tools.ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})
	call := message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: args}
	exec := NewStreamingToolExecutor(7, t.Context(), nil, a.executeToolCallSpeculative)
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	payload, ok, drift := exec.Promote(call)
	if drift || !ok || payload == nil || payload.Error != nil {
		t.Fatalf("Promote payload=%#v ok=%v drift=%v", payload, ok, drift)
	}
	a.commitPromotedToolSideEffects(call, payload)

	for _, name := range files {
		if !strings.Contains(payload.Diff, "--- "+name+"\n") || !strings.Contains(payload.Diff, "+++ "+name+"\n") {
			t.Fatalf("diff missing %s:\n%s", name, payload.Diff)
		}
	}
	if got := strings.Count(payload.Diff, "--- "); got != len(files) {
		t.Fatalf("diff contains %d file sections, want %d:\n%s", got, len(files), payload.Diff)
	}
	if payload.DiffAdded != len(files) || payload.DiffRemoved != len(files) {
		t.Fatalf("diff counts = +%d/-%d, want +%d/-%d", payload.DiffAdded, payload.DiffRemoved, len(files), len(files))
	}
}

func TestSpeculativeApplyPatchBacksUpEveryStaleFile(t *testing.T) {
	projectRoot := t.TempDir()
	files := []string{"one.txt", "two.txt", "three.txt"}
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

	for _, name := range files {
		path := filepath.Join(projectRoot, name)
		if err := os.WriteFile(path, []byte("tracked\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		readArgs := json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)
		if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-" + name, Name: tools.NameRead, Args: readArgs}); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Drift every tracked file so the patch runs against stale snapshots.
		if err := os.WriteFile(path, []byte("external\n"), 0o644); err != nil {
			t.Fatalf("external write %s: %v", name, err)
		}
	}

	patch := "*** Begin Patch\n"
	for _, name := range files {
		patch += "*** Update File: " + name + "\n@@\n-external\n+patched\n"
	}
	patch += "*** End Patch"
	args, err := json.Marshal(tools.ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}

	call := message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: args}
	exec := NewStreamingToolExecutor(7, t.Context(), nil, a.executeToolCallSpeculative)
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	payload, ok, drift := exec.Promote(call)
	if drift || !ok || payload == nil || payload.Error != nil {
		t.Fatalf("Promote payload=%#v ok=%v drift=%v", payload, ok, drift)
	}
	a.commitPromotedToolSideEffects(call, payload)

	if got := strings.Count(payload.Result, "Backup saved to:"); got != len(files) {
		t.Fatalf("result has %d backups, want %d:\n%s", got, len(files), payload.Result)
	}
	for _, name := range files {
		if !strings.Contains(payload.Result, strings.TrimSuffix(name, ".txt")) {
			t.Fatalf("result missing backup for %s:\n%s", name, payload.Result)
		}
	}
}

func TestSpeculativeApplyPatchMoveDiscardPreservesExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable mode bits")
	}
	projectRoot := t.TempDir()
	source := filepath.Join(projectRoot, "run.sh")
	target := filepath.Join(projectRoot, "moved.sh")
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})
	patch := "*** Begin Patch\n*** Update File: run.sh\n*** Move to: moved.sh\n*** End Patch"
	args, err := json.Marshal(tools.ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	call := message.ToolCall{ID: "move-1", Name: tools.NameApplyPatch, Args: args}
	exec := NewStreamingToolExecutor(7, t.Context(), nil, a.executeToolCallSpeculative)
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	waitForFileContent(t, target, "#!/bin/sh\n")
	waitForStreamingToolDone(t, exec, call.ID)
	if _, ok := exec.DiscardCall(call.ID, "filtered"); !ok {
		t.Fatal("DiscardCall returned false")
	}
	waitForFileContent(t, source, "#!/bin/sh\n")
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("restored source mode = %#o, want 0755", got)
	}
	waitForMissingFile(t, target)
}

func TestSpeculativeApplyPatchMoveRejectsSymlinkWithoutMutation(t *testing.T) {
	projectRoot := t.TempDir()
	backing := filepath.Join(projectRoot, "backing.txt")
	source := filepath.Join(projectRoot, "link.txt")
	target := filepath.Join(projectRoot, "moved.txt")
	if err := os.WriteFile(backing, []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("backing.txt", source); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})
	patch := "*** Begin Patch\n*** Update File: link.txt\n*** Move to: moved.txt\n*** End Patch"
	args, err := json.Marshal(tools.ApplyPatchArgs{Patch: patch})
	if err != nil {
		t.Fatal(err)
	}
	call := message.ToolCall{ID: "move-1", Name: tools.NameApplyPatch, Args: args}
	exec := NewStreamingToolExecutor(7, t.Context(), nil, a.executeToolCallSpeculative)
	if !exec.Start(call) {
		t.Fatal("Start returned false")
	}
	waitForStreamingToolDone(t, exec, call.ID)
	payload, ok, drift := exec.Promote(call)
	if drift || !ok || payload == nil || payload.Error == nil {
		t.Fatalf("Promote payload=%#v ok=%v drift=%v, want symlink rejection", payload, ok, drift)
	}
	if !strings.Contains(payload.Error.Error(), "not a regular file") {
		t.Fatalf("error = %v, want regular-file rejection", payload.Error)
	}
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("restored source mode = %v, want symlink", info.Mode())
	}
	if got, err := os.Readlink(source); err != nil || got != "backing.txt" {
		t.Fatalf("restored symlink target = %q, %v", got, err)
	}
	waitForMissingFile(t, target)
}

func TestSpeculativeWriteOnStaleFileIsRejected(t *testing.T) {
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
	if !ok || payload == nil || payload.Error == nil {
		t.Fatalf("Promote payload=%#v ok=%v, want rejection", payload, ok)
	}
	if !strings.Contains(payload.Error.Error(), "complete file") {
		t.Fatalf("error missing full-read guidance: %v", payload.Error)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "external" {
		t.Fatalf("file content = %q, %v; want unchanged external", got, err)
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
	waitForStreamingToolDone(t, exec, call.ID)
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

	// Baseline Read so Edit satisfies the read-before-patch precondition.
	readArgs := json.RawMessage(`{"path":` + mustJSONString(t, path) + `}`)
	if _, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "read-1", Name: tools.NameRead, Args: readArgs}); err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	ctx := t.Context()
	exec := NewStreamingToolExecutor(7, ctx, nil, a.executeToolCallSpeculative)
	call := message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"path":"` + filepath.Base(path) + `","patch":"@@\n-before\n+after\n"}`)}
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
	a.tools.Register(tools.ApplyPatchTool{BaseDir: projectRoot})

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
	call := message.ToolCall{ID: "patch-1", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"path":"` + filepath.Base(path) + `","patch":"@@\n-before\n+after\n"}`)}
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
	a.fileTrack.TrackObservedSnapshot(path, a.instanceID, computeFileHash(path))

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

func waitForStreamingToolDone(t *testing.T, exec *StreamingToolExecutor, callID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		exec.mu.Lock()
		entry := exec.entries[callID]
		var done <-chan struct{}
		if entry != nil {
			done = entry.done
		}
		exec.mu.Unlock()
		if done == nil {
			t.Fatalf("streaming tool %s is not tracked", callID)
		}
		select {
		case <-done:
			return
		case <-deadline:
			t.Fatalf("streaming tool %s did not finish", callID)
		case <-tick.C:
		}
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
