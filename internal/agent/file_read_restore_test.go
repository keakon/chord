package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRestoreTrackedFileStateDurableReadAllowsEdit(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	writeTestFile(t, path, "before")

	a := newRestoreEditTestAgent(t, projectRoot)
	msgs := restoreReadMessages(t, "read-1", path, computeFileHash(path), nil)
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 1 || result.RestoredStale != 0 {
		t.Fatalf("restore result = %+v, want one durable usable restore", result)
	}

	mustExecuteEdit(t, a, path, "before", "after")
	if got := readTestFile(t, path); got != "after" {
		t.Fatalf("file content = %q, want after", got)
	}
}

func TestRestoreTrackedFileStateDurableHashMismatchRestoresStaleSentinel(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	historicalHash := sha256String("before")
	writeTestFile(t, path, "changed")

	a := newRestoreEditTestAgent(t, projectRoot)
	msgs := restoreReadMessages(t, "read-1", path, historicalHash, nil)
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredStale != 1 || result.RestoredUsable != 0 {
		t.Fatalf("restore result = %+v, want one stale durable restore", result)
	}

	mustExecuteEdit(t, a, path, "changed", "after")
}

func TestRestoreTrackedFileStateFailedReadDoesNotRestoreUsableRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	writeTestFile(t, path, "before")

	a := newRestoreEditTestAgent(t, projectRoot)
	msgs := []message.Message{
		restoreAssistantCall(t, "read-1", tools.NameRead, map[string]any{"path": path}, nil),
		{Role: "tool", ToolCallID: "read-1", ToolStatus: string(ToolResultStatusError), Content: "Error: failed"},
	}
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 0 {
		t.Fatalf("restore result = %+v, want no usable restore", result)
	}

	if a.fileTrack.HasSnapshot(path, a.instanceID) {
		t.Fatal("failed read should not restore a usable tracked snapshot")
	}
}

func TestRestoreTrackedFileStateEffectiveArgsWinOverOriginalArgs(t *testing.T) {
	projectRoot := t.TempDir()
	oldPath := filepath.Join(projectRoot, "old.txt")
	realPath := filepath.Join(projectRoot, "real.txt")
	writeTestFile(t, oldPath, "old")
	writeTestFile(t, realPath, "real")

	a := newRestoreEditTestAgent(t, projectRoot)
	oldArgs := mustJSONRaw(t, map[string]any{"path": oldPath})
	realArgs := mustJSONText(t, map[string]any{"path": realPath})
	msgs := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "read-1", Name: tools.NameRead, Args: oldArgs}}},
		{
			Role:       "tool",
			ToolCallID: "read-1",
			ToolStatus: string(ToolResultStatusSuccess),
			Content:    "1\treal",
			Audit: &message.ToolArgsAudit{
				OriginalArgsJSON:  string(oldArgs),
				EffectiveArgsJSON: realArgs,
				UserModified:      true,
			},
			FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: realPath, SHA256: computeFileHash(realPath), Exists: true}}},
		},
	}
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 1 {
		t.Fatalf("restore result = %+v, want real path restored", result)
	}

	if a.fileTrack.HasSnapshot(oldPath, a.instanceID) {
		t.Fatal("original args path should not be restored as a tracked snapshot")
	}
	mustExecuteEdit(t, a, realPath, "real", "updated")
}

func TestRestoreTrackedFileStateImportedProvenanceDoesNotRestoreUsableRead(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	writeTestFile(t, path, "before")

	a := newRestoreEditTestAgent(t, projectRoot)
	imported := &message.MessageProvenance{Source: "import:claude", Imported: true}
	msgs := restoreReadMessages(t, "read-1", path, computeFileHash(path), imported)
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 0 {
		t.Fatalf("restore result = %+v, want no imported restore", result)
	}

	if a.fileTrack.HasSnapshot(path, a.instanceID) {
		t.Fatal("imported provenance should not restore a usable tracked snapshot")
	}
}

func TestRestoreTrackedFileStateReadThenDeleteDoesNotAuthorizeRecreatedPath(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	historicalHash := sha256String("before")
	writeTestFile(t, path, "recreated")

	a := newRestoreEditTestAgent(t, projectRoot)
	msgs := append(restoreReadMessages(t, "read-1", path, historicalHash, nil),
		restoreAssistantCall(t, "delete-1", tools.NameDelete, map[string]any{"paths": []string{path}, "reason": "cleanup"}, nil),
		message.Message{
			Role:       "tool",
			ToolCallID: "delete-1",
			ToolStatus: string(ToolResultStatusSuccess),
			Content:    "Deleted (1):\n- " + path,
			FileState:  &message.ToolFileState{Deletes: []message.TrackedFileState{{Path: path, Exists: false}}},
		},
	)
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 0 || result.RestoredStale != 0 {
		t.Fatalf("restore result = %+v, want delete to remove candidate", result)
	}

	if a.fileTrack.HasSnapshot(path, a.instanceID) {
		t.Fatal("delete should remove restored tracked snapshot candidate")
	}
}

func TestRestoreTrackedFileStateReadThenEditUsesPostWriteHash(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	readHash := sha256String("before")
	postHash := sha256String("after")
	writeTestFile(t, path, "after")

	a := newRestoreEditTestAgent(t, projectRoot)
	msgs := append(restoreReadMessages(t, "read-1", path, readHash, nil),
		restoreAssistantCall(t, "patch-1", tools.NameEdit, map[string]any{"path": "demo.txt", "patch": "@@\n-before\n+after\n"}, nil),
		message.Message{
			Role:       "tool",
			ToolCallID: "patch-1",
			ToolStatus: string(ToolResultStatusSuccess),
			Content:    "Updated " + path,
			FileState:  &message.ToolFileState{Writes: []message.TrackedFileState{{Path: path, SHA256: postHash, Exists: true}}},
		},
	)
	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 1 || result.RestoredStale != 0 {
		t.Fatalf("restore result = %+v, want post-write hash usable", result)
	}

	mustExecuteEdit(t, a, path, "after", "final")
}

func TestActivateLoadedSessionRebuildsFileTrackerFromLoadedMessages(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "demo.txt")
	writeTestFile(t, path, "before")

	a := newRestoreEditTestAgent(t, projectRoot)
	otherPath := filepath.Join(projectRoot, "other.txt")
	writeTestFile(t, otherPath, "other")
	a.fileTrack.TrackSnapshot(otherPath, a.instanceID, computeFileHash(otherPath))

	loaded := &loadedSessionState{
		SessionPath: filepath.Join(projectRoot, ".chord", "sessions", "loaded"),
		Messages:    restoreReadMessages(t, "read-1", path, computeFileHash(path), nil),
	}
	if err := os.MkdirAll(loaded.SessionPath, 0o755); err != nil {
		t.Fatalf("mkdir loaded session: %v", err)
	}
	a.activateLoadedSession(loaded)

	mustExecuteEdit(t, a, path, "before", "after")
	if a.fileTrack.HasSnapshot(otherPath, a.instanceID) {
		t.Fatal("loaded session activation should reset old session tracked snapshot")
	}
}

func TestRestoreTrackedFileStateUsesPersistedHookOnlyEffectiveArgs(t *testing.T) {
	projectRoot := t.TempDir()
	oldPath := filepath.Join(projectRoot, "old.txt")
	realPath := filepath.Join(projectRoot, "real.txt")
	writeTestFile(t, oldPath, "old")
	writeTestFile(t, realPath, "real")

	a := newRestoreEditTestAgent(t, projectRoot)
	a.tools.Register(tools.ReadTool{})
	a.hookEngine = &modifyReadPathHookEngine{path: realPath}

	oldArgs := mustJSONRaw(t, map[string]any{"path": oldPath})
	realArgs := mustJSONText(t, map[string]any{"path": realPath})
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "read-hook-1",
			Name: tools.NameRead,
			Args: oldArgs,
		}},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()

	a.newTurn()
	a.turn.PendingToolCalls.Store(2)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "read-hook-1", Name: tools.NameRead, ArgsJSON: string(oldArgs)})

	execResult, err := a.executeToolCall(context.Background(), assistant.ToolCalls[0])
	if err != nil {
		t.Fatalf("Read with hook-modified args failed: %v", err)
	}
	a.handleToolResult(Event{TurnID: a.turn.ID, Payload: &ToolResultPayload{
		CallID:      "read-hook-1",
		Name:        tools.NameRead,
		ArgsJSON:    execResult.EffectiveArgsJSON,
		Audit:       execResult.Audit,
		Result:      execResult.Result,
		Duration:    0,
		FileState:   execResult.FileState,
		LSPReviews:  execResult.LSPReviews,
		FileCreated: false,
	}})
	a.flushPersist()

	msgs := a.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", len(msgs))
	}
	if msgs[1].Audit == nil {
		t.Fatal("expected persisted tool audit for hook-only args mutation")
	}
	if msgs[1].Audit.OriginalArgsJSON != string(oldArgs) || msgs[1].Audit.EffectiveArgsJSON != realArgs {
		t.Fatalf("persisted audit = %#v, want original=%s effective=%s", msgs[1].Audit, string(oldArgs), realArgs)
	}
	if !msgs[1].Audit.UserModified {
		t.Fatal("expected persisted audit to mark hook-only args mutation as user_modified")
	}

	restored, err := a.recovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("len(restored) = %d, want 2", len(restored))
	}
	if restored[1].Audit == nil || restored[1].Audit.EffectiveArgsJSON != realArgs {
		t.Fatalf("restored audit = %#v, want effective args %s", restored[1].Audit, realArgs)
	}

	b := newRestoreEditTestAgent(t, projectRoot)
	result := restoreTrackedFileStateFromMessages(b.fileTrack, b.instanceID, restored)
	if result.RestoredUsable != 1 {
		t.Fatalf("restore result = %+v, want one usable restore from persisted effective args", result)
	}
	if b.fileTrack.HasSnapshot(oldPath, b.instanceID) {
		t.Fatal("original hook args path should not be restored as a tracked snapshot")
	}
	mustExecuteEdit(t, b, realPath, "real", "updated")
}

func TestToolMessageFileStateJSONOmitEmpty(t *testing.T) {
	plain, err := json.Marshal(message.Message{Role: "tool", ToolCallID: "x", Content: "ok"})
	if err != nil {
		t.Fatalf("marshal plain message: %v", err)
	}
	if strings.Contains(string(plain), "tool_status") || strings.Contains(string(plain), "file_state") {
		t.Fatalf("plain message JSON = %s, want status/file_state omitted", plain)
	}

	withState, err := json.Marshal(message.Message{
		Role:       "tool",
		ToolCallID: "x",
		Content:    "ok",
		ToolStatus: string(ToolResultStatusSuccess),
		FileState:  &message.ToolFileState{Reads: []message.TrackedFileState{{Path: "demo.txt", SHA256: "abc", Exists: true}}},
	})
	if err != nil {
		t.Fatalf("marshal message with file state: %v", err)
	}
	if !strings.Contains(string(withState), "tool_status") || !strings.Contains(string(withState), "file_state") {
		t.Fatalf("message JSON = %s, want status and file_state", withState)
	}
}

func TestRestoreTrackedFileStateSkipReasonCounters(t *testing.T) {
	projectRoot := t.TempDir()
	goodPath := filepath.Join(projectRoot, "good.txt")
	writeTestFile(t, goodPath, "good")
	goodHash := computeFileHash(goodPath)

	badMetaPath := filepath.Join(projectRoot, "bad-meta.txt")
	writeTestFile(t, badMetaPath, "bad")

	a := newRestoreEditTestAgent(t, projectRoot)
	msgs := []message.Message{
		restoreAssistantCall(t, "imported-read", tools.NameRead, map[string]any{"path": goodPath}, nil),
		{Role: "tool", ToolCallID: "imported-read", ToolStatus: string(ToolResultStatusSuccess), Content: "ok", Provenance: &message.MessageProvenance{Source: "import:claude", Imported: true}},

		restoreAssistantCall(t, "failed-read", tools.NameRead, map[string]any{"path": goodPath}, nil),
		{Role: "tool", ToolCallID: "failed-read", ToolStatus: string(ToolResultStatusError), Content: "Error: failed"},

		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "missing-args-read", Name: tools.NameRead}}},
		{Role: "tool", ToolCallID: "missing-args-read", ToolStatus: string(ToolResultStatusSuccess), Content: "ok"},

		restoreAssistantCall(t, "invalid-path-read", tools.NameRead, map[string]any{"path": "   "}, nil),
		{Role: "tool", ToolCallID: "invalid-path-read", ToolStatus: string(ToolResultStatusSuccess), Content: "ok"},

		restoreAssistantCall(t, "state-mismatch-read", tools.NameRead, map[string]any{"path": badMetaPath}, nil),
		{Role: "tool", ToolCallID: "state-mismatch-read", ToolStatus: string(ToolResultStatusSuccess), Content: "ok", FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: filepath.Join(projectRoot, "other.txt"), SHA256: "abc", Exists: true}}}},

		restoreAssistantCall(t, "delete-empty", tools.NameDelete, map[string]any{"paths": []string{}, "reason": "cleanup"}, nil),
		{Role: "tool", ToolCallID: "delete-empty", ToolStatus: string(ToolResultStatusSuccess), Content: "nothing happened"},

		restoreAssistantCall(t, "good-read", tools.NameRead, map[string]any{"path": goodPath}, nil),
		{Role: "tool", ToolCallID: "good-read", ToolStatus: string(ToolResultStatusSuccess), Content: "ok", FileState: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: goodPath, SHA256: goodHash, Exists: true}}}},
	}

	result := restoreTrackedFileStateFromMessages(a.fileTrack, a.instanceID, msgs)
	if result.RestoredUsable != 1 {
		t.Fatalf("restore result = %+v, want one usable restore", result)
	}
	if result.Skipped != 6 {
		t.Fatalf("restore result = %+v, want 6 skipped records", result)
	}
	if result.SkippedNonNativeProvenance != 1 ||
		result.SkippedNonSuccessResult != 1 ||
		result.SkippedMissingArgs != 1 ||
		result.SkippedInvalidPath != 1 ||
		result.SkippedStateMismatch != 1 ||
		result.SkippedDeleteState != 1 {
		t.Fatalf("restore result = %+v, want one skip in each classified bucket", result)
	}
}

type modifyReadPathHookEngine struct {
	path string
}

func (e *modifyReadPathHookEngine) Fire(_ context.Context, env hook.Envelope) (*hook.Result, error) {
	if env.Point != hook.OnToolCall {
		return &hook.Result{Action: hook.ActionContinue}, nil
	}
	data, ok := env.Data.(map[string]any)
	if !ok {
		return &hook.Result{Action: hook.ActionContinue}, nil
	}
	if toolName, _ := data["tool_name"].(string); toolName != tools.NameRead {
		return &hook.Result{Action: hook.ActionContinue}, nil
	}
	return &hook.Result{Action: hook.ActionModify, Data: map[string]any{"args": map[string]any{"path": e.path}}}, nil
}

func (e *modifyReadPathHookEngine) FireBackground(context.Context, hook.Envelope) {}

func (e *modifyReadPathHookEngine) RunAutomation(context.Context, hook.Envelope) ([]hook.AutomationJobResult, error) {
	return nil, nil
}

func newRestoreEditTestAgent(t *testing.T, projectRoot string) *MainAgent {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	a := newTestMainAgent(t, projectRoot)
	a.tools.Register(tools.EditTool{})
	a.fileTrack = filelock.NewFileTracker()
	return a
}

func restoreReadMessages(t *testing.T, callID, path, hash string, prov *message.MessageProvenance) []message.Message {
	t.Helper()
	return []message.Message{
		restoreAssistantCall(t, callID, tools.NameRead, map[string]any{"path": path}, prov),
		{
			Role:       "tool",
			ToolCallID: callID,
			ToolStatus: string(ToolResultStatusSuccess),
			Content:    "1\tcontent",
			Provenance: cloneProvenance(prov),
			FileState:  &message.ToolFileState{Reads: []message.TrackedFileState{{Path: path, SHA256: hash, Exists: true}}},
		},
	}
}

func restoreAssistantCall(t *testing.T, callID, name string, args map[string]any, prov *message.MessageProvenance) message.Message {
	t.Helper()
	return message.Message{
		Role:       "assistant",
		Provenance: cloneProvenance(prov),
		ToolCalls:  []message.ToolCall{{ID: callID, Name: name, Args: mustJSONRaw(t, args)}},
	}
}

func executeEdit(t *testing.T, a *MainAgent, path, oldString, newString string) error {
	t.Helper()
	args := mustJSONRaw(t, map[string]any{"path": filepath.Base(path), "patch": "@@\n-" + oldString + "\n+" + newString + "\n"})
	_, err := a.executeToolCall(context.Background(), message.ToolCall{ID: "patch-test", Name: tools.NameEdit, Args: args})
	return err
}

func mustExecuteEdit(t *testing.T, a *MainAgent, path, oldString, newString string) {
	t.Helper()
	if err := executeEdit(t, a, path, oldString, newString); err != nil {
		t.Fatalf("Edit(%s) failed: %v", path, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(data)
}

func mustJSONRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%#v): %v", v, err)
	}
	return json.RawMessage(data)
}

func mustJSONText(t *testing.T, v any) string {
	t.Helper()
	return string(mustJSONRaw(t, v))
}

func sha256String(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
