package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/mcp"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestRestoreSessionAtStartupClearsTodosRestoresUsageAndQueuesToast(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "123")

	persistRestorableSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.todoItems = []tools.TodoItem{{ID: "stale", Status: "pending", Content: "old todo"}}

	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	if got := len(a.GetMessages()); got != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", got)
	}
	if todos := a.GetTodos(); len(todos) != 0 {
		t.Fatalf("len(GetTodos()) = %d, want 0", len(todos))
	}
	if summary := a.GetSessionSummary(); summary == nil || summary.ID != "123" {
		t.Fatalf("GetSessionSummary() = %+v, want session id 123", summary)
	}

	stats := a.GetUsageStats()
	if stats.LLMCalls != 1 {
		t.Fatalf("LLMCalls = %d, want 1", stats.LLMCalls)
	}
	if stats.InputTokens != 11 || stats.OutputTokens != 7 {
		t.Fatalf("usage tokens = (%d, %d), want (11, 7)", stats.InputTokens, stats.OutputTokens)
	}
	if pending, sid := a.StartupResumeStatus(); !pending || sid != "123" {
		t.Fatalf("StartupResumeStatus() = (%t, %q), want (true, %q)", pending, sid, "123")
	}

	assertRestoreToast(t, a, "123")
}

func TestRestoreSessionAtStartupKeepsContextUsageZeroWithoutSnapshotOrMessageUsage(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "123")

	persistRestorableSessionWithoutContextUsage(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)

	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	current, limit := a.GetContextStats()
	if current != 0 {
		t.Fatalf("GetContextStats current = %d, want 0 without persisted usage", current)
	}
	if limit <= 0 {
		t.Fatalf("GetContextStats limit = %d, want configured limit", limit)
	}
}

func TestRestoreSessionAtStartupDefersRestoreReadyEventUntilRunStarts(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "123")
	persistRestorableSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	select {
	case evt := <-a.Events():
		if _, ok := evt.(ToastEvent); !ok {
			t.Fatalf("event before Run = %T, want ToastEvent", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for startup restore toast")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case evt := <-a.Events():
		started, ok := evt.(SessionSwitchStartedEvent)
		if !ok {
			t.Fatalf("first post-Run event = %T, want SessionSwitchStartedEvent", evt)
		}
		if started.Kind != "resume" || started.SessionID != "123" {
			t.Fatalf("SessionSwitchStartedEvent = %+v, want kind=resume session=123", started)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SessionSwitchStartedEvent after Run")
	}

	select {
	case evt := <-a.Events():
		if _, ok := evt.(SessionRestoredEvent); !ok {
			t.Fatalf("second post-Run event = %T, want SessionRestoredEvent", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SessionRestoredEvent after Run")
	}

	if pending, sid := a.StartupResumeStatus(); pending || sid != "123" {
		t.Fatalf("StartupResumeStatus() after Run = (%t, %q), want (false, %q)", pending, sid, "123")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run to exit")
	}
}

func TestHandleResumeCommandEmitsSessionSwitchStartedThenToast(t *testing.T) {
	projectRoot := t.TempDir()
	sourceSessionDir := testProjectSessionDir(t, projectRoot, "123")
	currentSessionDir := testProjectSessionDir(t, projectRoot, "999")

	persistRestorableSession(t, sourceSessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, currentSessionDir)
	a.handleResumeCommand("123")
	if summary := a.GetSessionSummary(); summary == nil || summary.ID != "123" {
		t.Fatalf("GetSessionSummary() = %+v, want session id 123", summary)
	}

	evt := <-a.Events()
	started, ok := evt.(SessionSwitchStartedEvent)
	if !ok {
		t.Fatalf("first event = %T, want SessionSwitchStartedEvent", evt)
	}
	if started.Kind != "resume" || started.SessionID != "123" {
		t.Fatalf("SessionSwitchStartedEvent = %+v, want kind=resume session=123", started)
	}
	evt = nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(SessionRestoredEvent); !ok {
		t.Fatalf("second event = %T, want SessionRestoredEvent", evt)
	}
	assertRestoreToast(t, a, "123")
}

func TestHandleResumeCommandKeepsCurrentSessionWhenLoadFails(t *testing.T) {
	projectRoot := t.TempDir()
	currentSessionDir := testProjectSessionDir(t, projectRoot, "999")
	targetSessionDir := testProjectSessionDir(t, projectRoot, "123")
	if err := os.MkdirAll(targetSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir target session: %v", err)
	}
	lock, err := recovery.AcquireSessionLock(targetSessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock(target): %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release(target): %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetSessionDir, "main.jsonl"), []byte("{broken-json\n"), 0o644); err != nil {
		t.Fatalf("write broken main.jsonl: %v", err)
	}

	persistRestorableSession(t, currentSessionDir)
	a := newTestMainAgentForRestore(t, projectRoot, currentSessionDir)
	currentLock, err := recovery.AcquireSessionLock(currentSessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock(current): %v", err)
	}
	a.SetSessionLock(currentLock)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup(current): %v", err)
	}
	// Drain the startup restore toast emitted by RestoreSessionAtStartup.
	<-a.Events()
	before := a.GetMessages()

	a.handleResumeCommand("123")

	evt := <-a.Events()
	if started, ok := evt.(SessionSwitchStartedEvent); !ok {
		t.Fatalf("first event = %T, want SessionSwitchStartedEvent", evt)
	} else if started.Kind != "resume" || started.SessionID != "123" {
		t.Fatalf("SessionSwitchStartedEvent = %+v, want kind=resume session=123", started)
	}

	if a.sessionDir != currentSessionDir {
		t.Fatalf("sessionDir = %q, want unchanged %q", a.sessionDir, currentSessionDir)
	}
	after := a.GetMessages()
	if len(after) != len(before) {
		t.Fatalf("len(GetMessages()) after failed resume = %d, want %d", len(after), len(before))
	}
	if _, err := recovery.AcquireSessionLock(currentSessionDir); err == nil {
		t.Fatal("current session lock should still be held after failed resume")
	}
	newLock, err := recovery.AcquireSessionLock(targetSessionDir)
	if err != nil {
		t.Fatalf("target session lock should have been released after failed resume: %v", err)
	}
	_ = newLock.Release()

	evt = <-a.Events()
	if errEvt, ok := evt.(ErrorEvent); !ok {
		t.Fatalf("event = %T, want ErrorEvent", evt)
	} else if errEvt.Err == nil {
		t.Fatal("expected non-nil resume error")
	}
}

func TestRestoreSessionAtStartupKeepsInterruptedMainToolResults(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "interrupted-main")

	persistInterruptedMainSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	msgs := a.GetMessages()
	if got := len(msgs); got != 3 {
		t.Fatalf("len(GetMessages()) = %d, want 3", got)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool-call message = %#v, want one tool call", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].Content != "Cancelled" {
		t.Fatalf("tool result message = %#v, want persisted cancelled result", msgs[2])
	}
}

func TestRestoreSessionAtStartupRepairsBlankReadToolResults(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "restored-read-blank")
	targetPath := filepath.Join(projectRoot, "empty-read-target.txt")
	if err := os.WriteFile(targetPath, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile(empty read target): %v", err)
	}

	persistRestorableBlankReadSession(t, sessionDir, targetPath)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	msgs := a.GetMessages()
	if got := len(msgs); got != 3 {
		t.Fatalf("len(GetMessages()) = %d, want 3", got)
	}
	if msgs[2].Role != "tool" {
		t.Fatalf("restored tool message role = %q, want tool", msgs[2].Role)
	}
	if msgs[2].Content != "(empty file)" {
		t.Fatalf("restored tool content = %q, want %q", msgs[2].Content, "(empty file)")
	}
}

func TestRestoreSessionAtStartupClosesDanglingDeclaredToolCalls(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "dangling-main-tool")

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "inspect session"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-grep-1",
			Name: "Grep",
			Args: []byte(`{"pattern":"TODO","path":"internal","glob":"**/*.go"}`),
		}},
	}
	if err := rm.PersistMessage("main", assistant); err != nil {
		t.Fatalf("PersistMessage(assistant tool call): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	msgs := a.GetMessages()
	if got := len(msgs); got != 3 {
		t.Fatalf("len(GetMessages()) = %d, want 3", got)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "tool-grep-1" {
		t.Fatalf("restored synthetic tool message = %#v, want tool-grep-1", msgs[2])
	}
	if !strings.Contains(msgs[2].Content, "Model stopped before completing this tool call") {
		t.Fatalf("restored synthetic tool content = %q, want interruption message", msgs[2].Content)
	}
}

func TestRestoreSessionAtStartupRestoresSubAgentInterruptedToolResults(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "interrupted-sub")

	persistInterruptedSubAgentSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.agentConfigs = map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent"},
	}
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	a.mu.RLock()
	sub := a.subAgents["agent-1"]
	a.mu.RUnlock()
	if sub == nil {
		t.Fatal("expected restored sub-agent agent-1")
	}
	msgs := sub.GetMessages()
	if got := len(msgs); got != 2 {
		t.Fatalf("len(sub.GetMessages()) = %d, want 2", got)
	}
	if msgs[1].Role != "tool" || !strings.Contains(msgs[1].Content, "Model stopped before completing this tool call") {
		t.Fatalf("sub tool result message = %#v, want persisted failure result", msgs[1])
	}
}

func TestRestoreSessionAtStartupSynthesizesDanglingSubAgentToolResult(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "dangling-sub")

	persistDanglingSubAgentSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.agentConfigs = map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent"},
	}
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	a.mu.RLock()
	sub := a.subAgents["agent-1"]
	a.mu.RUnlock()
	if sub == nil {
		t.Fatal("expected restored sub-agent agent-1")
	}
	msgs := sub.GetMessages()
	if got := len(msgs); got != 2 {
		t.Fatalf("len(sub.GetMessages()) = %d, want 2", got)
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "tool-sub-1" {
		t.Fatalf("sub synthetic tool result = %#v, want tool-sub-1", msgs[1])
	}
	if !strings.Contains(msgs[1].Content, "session restored before tool result was persisted") {
		t.Fatalf("sub synthetic tool result content = %q, want restore-specific cause", msgs[1].Content)
	}
}

func TestRestoreSessionAtStartupDropsOrphanToolResults(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "orphan-tool-result")

	persistOrphanToolResultSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	msgs := a.GetMessages()
	if got := len(msgs); got != 1 {
		t.Fatalf("len(GetMessages()) = %d, want 1", got)
	}
	if msgs[0].Role != "user" {
		t.Fatalf("msgs[0] = %#v, want user only", msgs[0])
	}
}

func TestRestoreSessionAtStartupDropsDuplicateToolResults(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "duplicate-tool-result")

	persistDuplicateToolResultSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	msgs := a.GetMessages()
	if got := len(msgs); got != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", got)
	}
	if msgs[1].Role != "tool" || msgs[1].Content != "first" {
		t.Fatalf("msgs[1] = %#v, want first tool result only", msgs[1])
	}
}

func TestRestoreSessionAtStartupSynthesizesDanglingToolResultBeforeLaterUser(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "dangling-before-user")

	persistDanglingMainToolCallBeforeLaterUserSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	msgs := a.GetMessages()
	if got := len(msgs); got != 4 {
		t.Fatalf("len(GetMessages()) = %d, want 4", got)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "tool-main-1" {
		t.Fatalf("msgs[2] = %#v, want synthetic tool result before later user", msgs[2])
	}
	if msgs[3].Role != "user" || msgs[3].Content != "follow-up" {
		t.Fatalf("msgs[3] = %#v, want later user preserved", msgs[3])
	}
}

func TestRestoreSessionAtStartupRestoresTodoOrderFromSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "todo-order")

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume todos"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "assistant", Content: "ok"}); err != nil {
		t.Fatalf("PersistMessage(assistant): %v", err)
	}
	want := []recovery.TodoState{
		{ID: "2", Status: "in_progress", Content: "Phase 2"},
		{ID: "3", Status: "pending", Content: "Phase 3"},
		{ID: "1", Status: "completed", Content: "Phase 1"},
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: want}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	got := a.GetTodos()
	if len(got) != len(want) {
		t.Fatalf("len(GetTodos()) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Status != want[i].Status || got[i].Content != want[i].Content {
			t.Fatalf("todo[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRestoreSessionAtStartupRestoresActiveRoleFromSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "role-restore")

	persistRestorableSession(t, sessionDir)

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		Todos:      []recovery.TodoState{},
		ActiveRole: "planner",
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": config.DefaultBuilderAgent(),
		"planner": config.DefaultPlannerAgent(),
	})
	if got := a.CurrentRole(); got != "builder" {
		t.Fatalf("CurrentRole() before restore = %q, want builder", got)
	}

	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	if got := a.CurrentRole(); got != "planner" {
		t.Fatalf("CurrentRole() after restore = %q, want planner", got)
	}
}

func TestRestoreSessionAtStartupRestoresActiveRoleModelFromSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "role-model-restore")

	persistRestorableSession(t, sessionDir)

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		Todos:      []recovery.TodoState{},
		ActiveRole: "executor",
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {
			Name:   "builder",
			Mode:   "primary",
			Models: []string{"build/one"},
		},
		"executor": {
			Name:   "executor",
			Mode:   "primary",
			Models: []string{"exec/one"},
		},
	})
	a.SetProviderModelRef("build/one")
	a.SetModelSwitchFactory(func(providerModel string) (*llm.Client, string, int, error) {
		if providerModel != "exec/one" {
			t.Fatalf("providerModel = %q, want exec/one", providerModel)
		}
		return newRoleSwitchClient(t, "exec", "one", 16384, "exec-key"), "one", 16384, nil
	})

	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	if got := a.CurrentRole(); got != "executor" {
		t.Fatalf("CurrentRole() after restore = %q, want executor", got)
	}
	if got := a.ProviderModelRef(); got != "exec/one" {
		t.Fatalf("ProviderModelRef() after restore = %q, want exec/one", got)
	}
	if got := a.RunningModelRef(); got != "exec/one" {
		t.Fatalf("RunningModelRef() after restore = %q, want exec/one", got)
	}
}

func TestRestoreSessionAtStartupSkipsUnknownSnapshotRole(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "unknown-role")

	persistRestorableSession(t, sessionDir)

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		Todos:      []recovery.TodoState{},
		ActiveRole: "reviewer",
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": config.DefaultBuilderAgent(),
	})

	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	if got := a.CurrentRole(); got != "builder" {
		t.Fatalf("CurrentRole() after restore = %q, want builder", got)
	}
}

func TestRestoreSessionAtStartupRestoresSubAgentMailboxState(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-state")

	persistMailboxRestoreSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.agentConfigs = map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Description: "restore helper", Mode: "subagent"},
	}
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		client := llm.NewClient(llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {
					Limit: config.ModelLimit{Context: 8192, Output: 1024},
				},
			},
		}, nil), nil, "test-model", 1024, "")
		client.SetSystemPrompt(systemPrompt)
		return client
	})
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	subagents := a.GetSubAgents()
	if len(subagents) != 1 {
		t.Fatalf("len(GetSubAgents()) = %d, want 1", len(subagents))
	}
	if subagents[0].State != string(SubAgentStateWaitingPrimary) {
		t.Fatalf("subagents[0].State = %q, want %q", subagents[0].State, SubAgentStateWaitingPrimary)
	}
	if subagents[0].LastSummary != "need approval to continue" {
		t.Fatalf("subagents[0].LastSummary = %q, want %q", subagents[0].LastSummary, "need approval to continue")
	}
}

func TestRestoreSessionAtStartupSkipsConsumedMailboxMessages(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-consumed")

	persistMailboxRestoreSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	if len(a.subAgentInbox.urgent) != 1 {
		t.Fatalf("len(urgent) = %d, want 1", len(a.subAgentInbox.urgent))
	}
	a.drainSubAgentInbox()
	if a.activeSubAgentMailbox == nil {
		t.Fatal("expected activeSubAgentMailbox after drain")
	}
	a.setIdleAndDrainPending()

	a2 := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a2.agentConfigs = map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Description: "restore helper", Mode: "subagent"},
	}
	a2.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		client := llm.NewClient(llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {
					Limit: config.ModelLimit{Context: 8192, Output: 1024},
				},
			},
		}, nil), nil, "test-model", 1024, "")
		client.SetSystemPrompt(systemPrompt)
		return client
	})
	if err := a2.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup(second): %v", err)
	}
	if len(a2.subAgentInbox.urgent) != 0 {
		t.Fatalf("len(urgent) after consumed restore = %d, want 0", len(a2.subAgentInbox.urgent))
	}
}

func TestRestoreSessionAtStartupAdvancesSubAgentMailboxSeq(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-seq")

	persistMailboxRestoreSessionWithID(t, sessionDir, "agent-1-7")

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}

	next := a.nextSubAgentMailboxMessageID("agent-1")
	if next != "agent-1-8" {
		t.Fatalf("nextSubAgentMailboxMessageID() = %q, want %q", next, "agent-1-8")
	}
}

func TestSetIdleAndDrainPendingDoesNotConsumeMailboxWhenAckFalse(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-ack-false")
	persistMailboxRestoreSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	msgs, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	a.activeSubAgentMailbox = &msgs[0]
	a.activeSubAgentMailboxAck = false
	a.setIdleAndDrainPending()

	msgs, err = loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages(after): %v", err)
	}
	if msgs[0].Consumed {
		t.Fatal("mailbox was consumed even though ack=false")
	}
	if len(a.subAgentInbox.urgent) != 1 {
		t.Fatalf("len(urgent) after requeue = %d, want 1", len(a.subAgentInbox.urgent))
	}
	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks(after): %v", err)
	}
	ack, ok := acks[msgs[0].MessageID]
	if !ok {
		t.Fatalf("retryable ack for %q not found", msgs[0].MessageID)
	}
	if ack.Outcome != "retryable" {
		t.Fatalf("ack.Outcome = %q, want retryable", ack.Outcome)
	}
}

func TestSetIdleAndDrainPendingConsumesMailboxWhenAckTrue(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-ack-true")
	persistMailboxRestoreSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	msgs, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
	a.activeSubAgentMailbox = &msgs[0]
	a.activeSubAgentMailboxAck = true
	a.setIdleAndDrainPending()

	msgs, err = loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages(after): %v", err)
	}
	if !msgs[0].Consumed {
		t.Fatal("mailbox was not consumed even though ack=true")
	}
	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	ack, ok := acks[msgs[0].MessageID]
	if !ok {
		t.Fatalf("ack for %q not found", msgs[0].MessageID)
	}
	if ack.TurnID != 0 {
		t.Fatalf("ack.TurnID = %d, want 0 when no active turn", ack.TurnID)
	}
}

func TestSetIdleAndDrainPendingRecordsReplySummaryInAck(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-reply-summary")
	persistMailboxRestoreSession(t, sessionDir)

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	msgs, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "Handled mailbox and replied to user."})
	a.activeSubAgentMailbox = &msgs[0]
	a.activeSubAgentMailboxAck = true
	a.setIdleAndDrainPending()

	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	ack, ok := acks[msgs[0].MessageID]
	if !ok {
		t.Fatalf("ack for %q not found", msgs[0].MessageID)
	}
	if ack.ReplySummary != "Handled mailbox and replied to user." {
		t.Fatalf("ack.ReplySummary = %q, want %q", ack.ReplySummary, "Handled mailbox and replied to user.")
	}
}

func persistRestorableSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{
		Role:    "assistant",
		Content: "world",
		Usage: &message.TokenUsage{
			InputTokens:  11,
			OutputTokens: 7,
		},
	}); err != nil {
		t.Fatalf("PersistMessage(assistant): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		Todos:                  []recovery.TodoState{},
		LastTotalContextTokens: 0,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistRestorableSessionWithoutContextUsage(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "assistant", Content: "world"}); err != nil {
		t.Fatalf("PersistMessage(assistant): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistInterruptedMainSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "check docs"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-main-1",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://slow.example","timeout":40}`),
		}},
	}
	if err := rm.PersistMessage("main", assistant); err != nil {
		t.Fatalf("PersistMessage(assistant tool call): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "tool", ToolCallID: "tool-main-1", Content: "Cancelled"}); err != nil {
		t.Fatalf("PersistMessage(cancelled tool result): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistRestorableBlankReadSession(t *testing.T, sessionDir, targetPath string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "check placeholder implementation"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-read-blank",
			Name: "Read",
			Args: []byte(fmt.Sprintf(`{"path":%q,"limit":240,"offset":358}`, targetPath)),
		}},
	}
	if err := rm.PersistMessage("main", assistant); err != nil {
		t.Fatalf("PersistMessage(assistant tool call): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "tool", ToolCallID: "tool-read-blank", Content: "   359\t\n"}); err != nil {
		t.Fatalf("PersistMessage(blank read tool result): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistDanglingMainToolCallBeforeLaterUserSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "inspect session"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-main-1",
			Name: "Read",
			Args: []byte(`{"path":"go.mod"}`),
		}},
	}
	if err := rm.PersistMessage("main", assistant); err != nil {
		t.Fatalf("PersistMessage(assistant tool call): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "follow-up"}); err != nil {
		t.Fatalf("PersistMessage(later user): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistOrphanToolResultSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "inspect session"}); err != nil {
		t.Fatalf("PersistMessage(user): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "tool", ToolCallID: "missing", Content: "unexpected"}); err != nil {
		t.Fatalf("PersistMessage(orphan tool result): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistDuplicateToolResultSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-main-1",
			Name: "Read",
			Args: []byte(`{"path":"go.mod"}`),
		}},
	}
	if err := rm.PersistMessage("main", assistant); err != nil {
		t.Fatalf("PersistMessage(assistant tool call): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "tool", ToolCallID: "tool-main-1", Content: "first"}); err != nil {
		t.Fatalf("PersistMessage(first tool result): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "tool", ToolCallID: "tool-main-1", Content: "second"}); err != nil {
		t.Fatalf("PersistMessage(second tool result): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{Todos: []recovery.TodoState{}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistInterruptedSubAgentSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "delegate task"}); err != nil {
		t.Fatalf("PersistMessage(main user): %v", err)
	}
	if err := rm.PersistMessage("main", message.Message{Role: "assistant", Content: "ok"}); err != nil {
		t.Fatalf("PersistMessage(main assistant): %v", err)
	}
	subAssistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-sub-1",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://missing.example"}`),
		}},
	}
	if err := rm.PersistMessage("agent-1", subAssistant); err != nil {
		t.Fatalf("PersistMessage(sub assistant tool call): %v", err)
	}
	if err := rm.PersistMessage("agent-1", message.Message{Role: "tool", ToolCallID: "tool-sub-1", Content: "Model stopped before completing this tool call: context deadline exceeded"}); err != nil {
		t.Fatalf("PersistMessage(sub failed tool result): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		Todos: []recovery.TodoState{},
		ActiveAgents: []recovery.AgentSnapshot{{
			InstanceID:   "agent-1",
			TaskID:       "restored",
			AgentDefName: "restorer",
			TaskDesc:     "Fetch docs",
		}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func persistMailboxRestoreSession(t *testing.T, sessionDir string) {
	persistMailboxRestoreSessionWithID(t, sessionDir, "agent-1-1")
}

func persistMailboxRestoreSessionWithID(t *testing.T, sessionDir, mailboxID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	msgs := []message.Message{
		{Role: "user", Content: "Investigate issue"},
		{Role: "assistant", Content: "Working on it"},
	}
	for _, msg := range msgs {
		if err := rm.PersistMessage("agent-1", msg); err != nil {
			t.Fatalf("PersistMessage(agent-1): %v", err)
		}
	}
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		LastInputTokens:        1,
		LastTotalContextTokens: 2,
		ActiveAgents: []recovery.AgentSnapshot{{
			InstanceID:   "agent-1",
			AgentDefName: "restorer",
			TaskID:       "restored",
			TaskDesc:     "Investigate issue",
		}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	mailbox := []SubAgentMailboxMessage{{
		MessageID:   mailboxID,
		AgentID:     "agent-1",
		TaskID:      "restored",
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need approval to continue",
		Payload:     "need approval to continue",
		RequiresAck: true,
		CreatedAt:   time.Now(),
	}}
	f, err := os.Create(filepath.Join(sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("Create(mailbox.jsonl): %v", err)
	}
	enc := json.NewEncoder(f)
	for _, msg := range mailbox {
		if err := enc.Encode(msg); err != nil {
			_ = f.Close()
			t.Fatalf("Encode(mailbox): %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(mailbox): %v", err)
	}
	rm.Close()
}

func persistDanglingSubAgentSession(t *testing.T, sessionDir string) {
	t.Helper()

	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "delegate task"}); err != nil {
		t.Fatalf("PersistMessage(main user): %v", err)
	}
	subAssistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-sub-1",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://missing.example"}`),
		}},
	}
	if err := rm.PersistMessage("agent-1", subAssistant); err != nil {
		t.Fatalf("PersistMessage(sub assistant tool call): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		Todos: []recovery.TodoState{},
		ActiveAgents: []recovery.AgentSnapshot{{
			InstanceID:   "agent-1",
			TaskID:       "restored",
			AgentDefName: "restorer",
			TaskDesc:     "Fetch docs",
		}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
}

func assertRestoreToast(t *testing.T, a *MainAgent, sessionID string) {
	t.Helper()

	evt := <-a.Events()
	toast, ok := evt.(ToastEvent)
	if !ok {
		t.Fatalf("event = %T, want ToastEvent", evt)
	}
	if toast.Level != "info" {
		t.Fatalf("toast.Level = %q, want info", toast.Level)
	}
	if !strings.Contains(toast.Message, sessionID) || !strings.Contains(toast.Message, "2 messages restored") {
		t.Fatalf("unexpected toast message: %q", toast.Message)
	}
}

func testProjectSessionsDir(t *testing.T, projectRoot string) string {
	t.Helper()
	stateDir := filepath.Join(projectRoot, ".test-state")
	t.Setenv("CHORD_STATE_DIR", stateDir)
	locator, err := config.DefaultPathLocator()
	if err != nil {
		t.Fatalf("DefaultPathLocator: %v", err)
	}
	pl, err := locator.EnsureProject(projectRoot)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	return pl.ProjectSessionsDir
}

func testProjectSessionDir(t *testing.T, projectRoot, sessionID string) string {
	t.Helper()
	return filepath.Join(testProjectSessionsDir(t, projectRoot), sessionID)
}

func newTestMainAgentForRestore(t *testing.T, projectRoot, sessionDir string) *MainAgent {
	t.Helper()
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {
				Limit: config.ModelLimit{
					Context: 8192,
					Output:  1024,
				},
			},
		},
	}, nil)
	a := NewMainAgent(
		context.Background(),
		llm.NewClient(providerCfg, nil, "test-model", 1024, ""),
		ctxmgr.NewManager(8192, false, 0),
		tools.NewRegistry(),
		&hook.NoopEngine{},
		sessionDir,
		"test-model",
		projectRoot,
		&config.Config{},
		nil,
		mcp.ClientInfo{Name: "chord-test", Version: "test"},
	)
	a.startPersistLoop()
	t.Cleanup(func() {
		a.closePersistLoop()
		<-a.persistDone
		a.cancel()
		if a.recovery != nil {
			a.recovery.Close()
		}
	})
	return a
}
