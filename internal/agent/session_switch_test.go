package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareSessionSwitchTerminatesBackgroundObjects(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	tools.StopAllSpawnedForShutdown()
	resetSpawnRegistryForAgentTests(t)

	if _, err := tools.ExecuteSpawnForTest(tools.WithAgentID(context.Background(), a.instanceID), "service", "sleep 5", "Main background service", nil); err != nil {
		t.Fatalf("start main background: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := &SubAgent{instanceID: "agent-1", parentCtx: ctx, cancel: cancel}
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()
	if _, err := tools.ExecuteSpawnForTest(tools.WithAgentID(context.Background(), sub.instanceID), "job", "sleep 5", "Sub background object", intPtr(5)); err != nil {
		t.Fatalf("start sub background: %v", err)
	}

	oldRecovery, turnCtx := a.prepareSessionSwitch()
	if oldRecovery == nil {
		t.Fatal("expected old recovery manager")
	}
	if turnCtx == nil {
		t.Fatal("expected non-nil turn context")
	}
	if got := len(tools.SnapshotSpawnedProcesses()); got != 0 {
		t.Fatalf("len(SnapshotSpawnedProcesses()) after prepareSessionSwitch = %d, want 0", got)
	}
}

func resetSpawnRegistryForAgentTests(t *testing.T) {
	t.Helper()
	restore := tools.ResetSpawnRegistryForTest()
	t.Cleanup(restore)
}

func intPtr(v int) *int { return &v }

func TestResetSessionRuntimeStateClearsLoopControllerState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.EnableLoopMode("finish current task")
	a.pendingLoopContinuation = &LoopContinuationNote{
		Title:    "LOOP CONTINUE",
		Text:     "unfinished work",
		DedupKey: "loop",
	}

	a.resetSessionRuntimeState()

	if a.loopState.Enabled {
		t.Fatal("loop should be disabled after session runtime reset")
	}
	if a.loopState.State != LoopStateIdle {
		t.Fatalf("loopState.State = %q, want idle after session runtime reset", a.loopState.State)
	}
	if a.pendingLoopContinuation != nil {
		t.Fatalf("pendingLoopContinuation = %#v, want nil", a.pendingLoopContinuation)
	}
}

func TestSendUserMessageWithPartsRoutesImagesToFocusedSubAgent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newControllableTestSubAgent(t, a, "adhoc-img")
	a.SwitchFocus(sub.instanceID)

	parts := []message.ContentPart{
		{Type: "text", Text: "look at this"},
		{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}, FileName: "shot.png"},
	}
	a.SendUserMessageWithParts(parts)

	select {
	case got := <-sub.inputCh:
		if got.Content != "look at this" {
			t.Fatalf("queued content = %q, want %q", got.Content, "look at this")
		}
		if len(got.Parts) != 2 {
			t.Fatalf("queued parts len = %d, want 2", len(got.Parts))
		}
		if got.Parts[1].Type != "image" {
			t.Fatalf("queued image part type = %q, want image", got.Parts[1].Type)
		}
		if got.Parts[1].FileName != "shot.png" {
			t.Fatalf("queued image filename = %q, want shot.png", got.Parts[1].FileName)
		}
		parts[1].Data[0] = 9
		if got.Parts[1].Data[0] != 1 {
			t.Fatal("queued image bytes were aliased to caller slice")
		}
	default:
		t.Fatal("focused subagent did not receive multipart input")
	}
}

func TestFocusedCompletedSubAgentDirectInputContinuesTask(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newControllableTestSubAgent(t, a, "adhoc-completed")
	sub.setState(SubAgentStateCompleted, "done")
	a.SwitchFocus(sub.instanceID)

	a.SendUserMessage("should continue")

	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	select {
	case got := <-sub.inputCh:
		if text := pendingUserMessageText(got); text != "[follow_up] should continue" {
			t.Fatalf("queued message = %q, want %q", text, "[follow_up] should continue")
		}
	default:
		t.Fatal("expected completed focused subagent to receive follow-up input")
	}
}

func TestSwitchFocusUnknownAgentClearsStaleFocus(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-9")
	a.focusedAgent.Store(sub)

	a.SwitchFocus("missing-agent")

	if got := a.FocusedAgentID(); got != "" {
		t.Fatalf("FocusedAgentID() = %q, want empty after switching to unknown agent", got)
	}
}

func TestStaleFocusedAgentFallsBackToMainForUserInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-10")
	a.focusedAgent.Store(sub)
	a.mu.Lock()
	delete(a.subAgents, sub.instanceID)
	a.mu.Unlock()

	a.SendUserMessage("route to main")

	if got := a.PendingUserMessageCount(); got != 0 {
		t.Fatalf("PendingUserMessageCount() = %d, want 0 before loop handles event", got)
	}
	select {
	case msg := <-sub.inputCh:
		t.Fatalf("stale focused subagent unexpectedly received input %q", pendingUserMessageText(msg))
	default:
	}
}

func TestStaleFocusedAgentFallsBackToMainViews(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ctxMgr.Append(message.Message{Role: "user", Content: "main-msg"})
	sub := newControllableTestSubAgent(t, a, "adhoc-11")
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "sub-msg"})
	a.focusedAgent.Store(sub)
	a.mu.Lock()
	delete(a.subAgents, sub.instanceID)
	a.mu.Unlock()

	msgs := a.GetMessages()
	if len(msgs) != 1 || msgs[0].Content != "main-msg" {
		t.Fatalf("GetMessages() = %+v, want main context only", msgs)
	}
}

func TestHandleAgentDoneDoesNotAppendPseudoUserMessage(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := &SubAgent{
		instanceID: "agent-1",
		taskID:     "adhoc-1",
		parentCtx:  ctx,
		cancel:     cancel,
	}
	sub.setState(SubAgentStateRunning, "")
	sub.semHeld = true
	a.sem <- struct{}{}
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()

	a.newTurn()
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "finished"},
	})

	if got := len(a.GetMessages()); got != 0 {
		t.Fatalf("handleAgentDone polluted transcript, len(GetMessages()) = %d", got)
	}
	if got := len(a.GetSubAgents()); got != 0 {
		t.Fatalf("len(GetSubAgents()) = %d, want 0 after immediate close", got)
	}
	if sub.State() != SubAgentStateCompleted {
		t.Fatalf("sub.State() = %q, want %q", sub.State(), SubAgentStateCompleted)
	}
	if sub.semHeld {
		t.Fatal("completed subagent still holds semaphore slot")
	}
	record := a.taskRecordByTaskID(sub.taskID)
	if record == nil {
		t.Fatal("expected durable task record for completed subagent")
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("record.State = %q, want %q", record.State, SubAgentStateCompleted)
	}
}

func TestHandleNewSessionCommandReleasesOldStartupLock(t *testing.T) {
	projectRoot := t.TempDir()
	oldSessionDir := testProjectSessionDir(t, projectRoot, "locked-old")
	if err := os.MkdirAll(oldSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir old session: %v", err)
	}
	oldLock, err := recovery.AcquireSessionLock(oldSessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock(old): %v", err)
	}

	a := newTestMainAgentForRestore(t, projectRoot, oldSessionDir)
	a.SetSessionLock(oldLock)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	a.handleNewSessionCommand()
	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	if _, err := recovery.AcquireSessionLock(oldSessionDir); err != nil {
		t.Fatalf("old session lock should be released after /new, got %v", err)
	}
}

func TestSetSessionLockRefreshesSessionSummaryLockedFlag(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "lock-refresh")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if summary := a.GetSessionSummary(); summary == nil || summary.Locked {
		t.Fatalf("initial GetSessionSummary() = %+v, want locked=false", summary)
	}
	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock: %v", err)
	}
	defer func() { _ = lock.Release() }()
	a.SetSessionLock(lock)
	if summary := a.GetSessionSummary(); summary == nil || !summary.Locked {
		t.Fatalf("GetSessionSummary() after SetSessionLock = %+v, want locked=true", summary)
	}
}

func TestHandleForkSessionCommandReleasesOldLockAfterSwitch(t *testing.T) {
	projectRoot := t.TempDir()
	oldSessionDir := testProjectSessionDir(t, projectRoot, "fork-old")
	if err := os.MkdirAll(oldSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir old session: %v", err)
	}
	oldLock, err := recovery.AcquireSessionLock(oldSessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock(old): %v", err)
	}

	a := newTestMainAgentForRestore(t, projectRoot, oldSessionDir)
	a.SetSessionLock(oldLock)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: "user", Content: "first"},
		{Role: "user", Content: "fork me"},
	})

	a.handleForkSessionCommand(1)
	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	if _, err := recovery.AcquireSessionLock(oldSessionDir); err != nil {
		t.Fatalf("old session lock should be released after fork, got %v", err)
	}
}

func TestHandleForkSessionCommandKeepsCurrentSessionWhenCreateFails(t *testing.T) {
	projectRoot := t.TempDir()
	oldSessionDir := filepath.Join(projectRoot, "current-session")
	if err := os.MkdirAll(oldSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir old session: %v", err)
	}
	oldLock, err := recovery.AcquireSessionLock(oldSessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock(old): %v", err)
	}

	a := newTestMainAgentForRestore(t, projectRoot, oldSessionDir)
	a.SetSessionLock(oldLock)
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: "user", Content: "task list"},
		{Role: "assistant", Content: "mid"},
		{Role: "user", Content: "fork me"},
	})
	before := a.GetMessages()

	if err := os.MkdirAll(filepath.Join(projectRoot, ".chord"), 0o755); err != nil {
		t.Fatalf("mkdir .chord: %v", err)
	}
	sessionsDir := testProjectSessionsDir(t, projectRoot)
	if err := os.RemoveAll(sessionsDir); err != nil {
		t.Fatalf("remove sessions dir: %v", err)
	}
	if err := os.WriteFile(sessionsDir, []byte("not-a-directory"), 0o644); err != nil {
		t.Fatalf("write sessions file: %v", err)
	}

	a.handleForkSessionCommand(2)

	if a.sessionDir != oldSessionDir {
		t.Fatalf("sessionDir = %q, want unchanged %q", a.sessionDir, oldSessionDir)
	}
	after := a.GetMessages()
	if len(after) != len(before) {
		t.Fatalf("len(GetMessages()) after failed fork = %d, want %d", len(after), len(before))
	}
	if _, err := recovery.AcquireSessionLock(oldSessionDir); err == nil {
		t.Fatal("current session lock should still be held after failed fork")
	}
	evt := <-a.Events()
	if started, ok := evt.(SessionSwitchStartedEvent); !ok {
		t.Fatalf("first event = %T, want SessionSwitchStartedEvent", evt)
	} else if started.Kind != "fork" {
		t.Fatalf("SessionSwitchStartedEvent.Kind = %q, want fork", started.Kind)
	}
	evt = <-a.Events()
	if errEvt, ok := evt.(ErrorEvent); !ok {
		t.Fatalf("second event = %T, want ErrorEvent", evt)
	} else if errEvt.Err == nil {
		t.Fatal("expected non-nil fork error")
	}
}

func TestHandleForkSessionCommandSeedsPrefixAndRestoresDerivedState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	todoArgs, err := json.Marshal(map[string]any{
		"todos": []map[string]any{{
			"id":      "todo-1",
			"content": "follow up",
			"status":  "pending",
		}},
	})
	if err != nil {
		t.Fatalf("Marshal(todo args): %v", err)
	}
	msgs := []message.Message{
		{Role: "user", Content: "task list"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "todo-call-1", Name: "TodoWrite", Args: todoArgs}}},
		{Role: "user", Content: "edit me"},
	}
	a.ctxMgr.RestoreMessages(msgs)
	a.todoMu.Lock()
	a.todoItems = []tools.TodoItem{{ID: "stale", Content: "stale todo", Status: "completed"}}
	a.todoMu.Unlock()
	a.ctxMgr.SetLastInputTokens(999)
	a.ctxMgr.SetLastTotalContextTokens(999)
	a.usageTracker.RestoreStats(analytics.SessionStats{InputTokens: 42, LLMCalls: 1})
	oldSessionDir := a.sessionDir

	a.handleForkSessionCommand(2)

	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	gotMsgs := a.GetMessages()
	if len(gotMsgs) != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", len(gotMsgs))
	}
	if gotMsgs[0].Role != "user" || gotMsgs[0].Content != "task list" {
		t.Fatalf("first message = %+v, want original prefix user", gotMsgs[0])
	}
	if len(gotMsgs[1].ToolCalls) != 1 || gotMsgs[1].ToolCalls[0].Name != "TodoWrite" {
		t.Fatalf("second message = %+v, want TodoWrite assistant", gotMsgs[1])
	}
	if got := len(a.GetTodos()); got != 1 {
		t.Fatalf("len(GetTodos()) = %d, want 1", got)
	}
	if got := a.GetTodos()[0].ID; got != "todo-1" {
		t.Fatalf("todo id = %q, want todo-1", got)
	}
	if current, _ := a.GetContextStats(); current != 0 {
		t.Fatalf("GetContextStats current = %d, want 0 when fork prefix has no usage", current)
	}
	if stats := a.GetUsageStats(); stats.LLMCalls != 0 || stats.InputTokens != 0 || stats.OutputTokens != 0 {
		t.Fatalf("usage stats not reset: %+v", stats)
	}

	newRecovery := recovery.NewRecoveryManager(a.sessionDir)
	defer newRecovery.Close()
	persisted, err := newRecovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(new session): %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("len(persisted) = %d, want 2", len(persisted))
	}
	if info := recovery.SessionInfoForDir(a.sessionDir); info == nil || info.ForkedFrom != filepath.Base(oldSessionDir) {
		t.Fatalf("SessionInfoForDir(a.sessionDir) = %+v, want ForkedFrom %q", info, filepath.Base(oldSessionDir))
	}
	if summary := a.GetSessionSummary(); summary == nil || summary.ForkedFrom != filepath.Base(oldSessionDir) {
		t.Fatalf("GetSessionSummary() = %+v, want ForkedFrom %q", summary, filepath.Base(oldSessionDir))
	}

	evt := <-a.Events()
	started, ok := evt.(SessionSwitchStartedEvent)
	if !ok {
		t.Fatalf("first event = %T, want SessionSwitchStartedEvent", evt)
	}
	if started.Kind != "fork" {
		t.Fatalf("SessionSwitchStartedEvent.Kind = %q, want fork", started.Kind)
	}
	evt = nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(SessionRestoredEvent); !ok {
		t.Fatalf("second event = %T, want SessionRestoredEvent", evt)
	}
	evt = <-a.Events()
	forkEvt, ok := evt.(ForkSessionEvent)
	if !ok {
		t.Fatalf("third event = %T, want ForkSessionEvent", evt)
	}
	if len(forkEvt.Parts) != 1 || forkEvt.Parts[0].Text != "edit me" {
		t.Fatalf("fork parts = %+v, want single text part 'edit me'", forkEvt.Parts)
	}
	evt = <-a.Events()
	toast, ok := evt.(ToastEvent)
	if !ok {
		t.Fatalf("fourth event = %T, want ToastEvent", evt)
	}
	if toast.Level != "info" || !strings.Contains(toast.Message, filepath.Base(a.sessionDir)) {
		t.Fatalf("unexpected toast: %+v", toast)
	}
}

func TestHandleForkSessionCommandRePersistsImageAssetsIntoNewSession(t *testing.T) {
	projectRoot := t.TempDir()
	oldSessionDir := testProjectSessionDir(t, projectRoot, "old")
	if err := os.MkdirAll(oldSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir old session: %v", err)
	}
	oldRecovery := recovery.NewRecoveryManager(oldSessionDir)
	oldImagePath := filepath.Join(oldSessionDir, "images", "old.png")
	if err := os.MkdirAll(filepath.Dir(oldImagePath), 0o755); err != nil {
		t.Fatalf("mkdir old images: %v", err)
	}
	imgData := []byte{0x89, 'P', 'N', 'G', '\n'}
	if err := os.WriteFile(oldImagePath, imgData, 0o600); err != nil {
		t.Fatalf("write old image: %v", err)
	}
	if err := oldRecovery.PersistMessage("main", message.Message{
		Role: "user",
		Parts: []message.ContentPart{{
			Type:      "image",
			MimeType:  "image/png",
			Data:      append([]byte(nil), imgData...),
			ImagePath: oldImagePath,
			FileName:  "old.png",
		}},
	}); err != nil {
		t.Fatalf("PersistMessage(old image msg): %v", err)
	}
	oldRecovery.Close()

	a := newTestMainAgentForRestore(t, projectRoot, oldSessionDir)
	a.SetSessionLock(mustAcquireSessionLock(t, oldSessionDir))
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	<-a.Events() // startup restore toast

	a.ctxMgr.Append(message.Message{Role: "user", Content: "edit me"})
	a.handleForkSessionCommand(1)

	newRecovery := recovery.NewRecoveryManager(a.sessionDir)
	defer newRecovery.Close()
	persisted, err := newRecovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(new session): %v", err)
	}
	if len(persisted) != 1 || len(persisted[0].Parts) != 1 {
		t.Fatalf("persisted = %+v, want one image-only prefix message", persisted)
	}
	part := persisted[0].Parts[0]
	if part.ImagePath == "" {
		t.Fatal("forked image part missing ImagePath")
	}
	if !strings.HasPrefix(part.ImagePath, filepath.Join(a.sessionDir, "images")+string(os.PathSeparator)) {
		t.Fatalf("forked image path = %q, want under new session images dir", part.ImagePath)
	}
	if part.ImagePath == oldImagePath {
		t.Fatalf("forked image path = %q, should not reuse old session asset", part.ImagePath)
	}
	if _, err := os.Stat(part.ImagePath); err != nil {
		t.Fatalf("forked image asset stat: %v", err)
	}
}

func mustAcquireSessionLock(t *testing.T, sessionDir string) *recovery.SessionLock {
	t.Helper()
	lock, err := recovery.AcquireSessionLock(sessionDir)
	if err != nil {
		t.Fatalf("AcquireSessionLock(%q): %v", sessionDir, err)
	}
	return lock
}

func TestHandleNewSessionCommandStartsFreshSessionAndIgnoresLateSubAgent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	oldSessionDir := a.sessionDir
	a.ctxMgr.Append(message.Message{Role: "user", Content: "stale message"})
	a.todoMu.Lock()
	a.todoItems = []tools.TodoItem{{ID: "todo-1", Status: "pending", Content: "stale todo"}}
	a.todoMu.Unlock()
	a.usageTracker.RestoreStats(analytics.SessionStats{
		InputTokens:  11,
		OutputTokens: 7,
		LLMCalls:     1,
	})

	cancelled := false
	sub := &SubAgent{
		instanceID: "agent-1",
		cancel: func() {
			cancelled = true
		},
	}
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.nudgeCounts[sub.instanceID] = 1
	a.mu.Unlock()
	a.focusedAgent.Store(sub)
	a.sem <- struct{}{}

	a.persistAsync("main", message.Message{Role: "assistant", Content: "persisted tail"})

	a.handleNewSessionCommand()

	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	if summary := a.GetSessionSummary(); summary == nil || summary.ID != filepath.Base(a.sessionDir) {
		t.Fatalf("GetSessionSummary() = %+v, want current session id %q", summary, filepath.Base(a.sessionDir))
	}
	if got := len(a.GetMessages()); got != 0 {
		t.Fatalf("len(GetMessages()) = %d, want 0", got)
	}
	if got := len(a.GetTodos()); got != 0 {
		t.Fatalf("len(GetTodos()) = %d, want 0", got)
	}
	if stats := a.GetUsageStats(); stats.InputTokens != 0 || stats.OutputTokens != 0 || stats.LLMCalls != 0 {
		t.Fatalf("usage stats not reset: %+v", stats)
	}
	if !cancelled {
		t.Fatal("expected running SubAgent to be cancelled")
	}
	if got := a.FocusedAgentID(); got != "" {
		t.Fatalf("FocusedAgentID() = %q, want empty", got)
	}
	if got := len(a.GetSubAgents()); got != 0 {
		t.Fatalf("len(GetSubAgents()) = %d, want 0", got)
	}

	oldRecovery := recovery.NewRecoveryManager(oldSessionDir)
	defer oldRecovery.Close()
	oldMsgs, err := oldRecovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(old session): %v", err)
	}
	if len(oldMsgs) != 1 || oldMsgs[0].Content != "persisted tail" {
		t.Fatalf("old session messages = %+v, want persisted tail only", oldMsgs)
	}

	newRecovery := recovery.NewRecoveryManager(a.sessionDir)
	defer newRecovery.Close()
	newMsgs, err := newRecovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(new session): %v", err)
	}
	if len(newMsgs) != 0 {
		t.Fatalf("new session should start empty, got %+v", newMsgs)
	}

	evt := <-a.Events()
	started, ok := evt.(SessionSwitchStartedEvent)
	if !ok {
		t.Fatalf("first event = %T, want SessionSwitchStartedEvent", evt)
	}
	if started.Kind != "new" {
		t.Fatalf("SessionSwitchStartedEvent.Kind = %q, want new", started.Kind)
	}
	evt = nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(SessionRestoredEvent); !ok {
		t.Fatalf("second event = %T, want SessionRestoredEvent", evt)
	}
	evt = <-a.Events()
	toast, ok := evt.(ToastEvent)
	if !ok {
		t.Fatalf("third event = %T, want ToastEvent", evt)
	}
	if toast.Level != "info" || !strings.Contains(toast.Message, filepath.Base(a.sessionDir)) {
		t.Fatalf("unexpected toast: %+v", toast)
	}

	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "late result"},
	})
	if got := len(a.GetMessages()); got != 0 {
		t.Fatalf("late subagent event polluted new session, len(GetMessages()) = %d", got)
	}
}

// TestHandleForkSessionCommandFirstUserEmptyPrefix verifies that forking
// at msgIndex=0 (the first user message) produces a new session with an
// empty prefix and the forked message loaded as the composer draft.
func TestHandleForkSessionCommandFirstUserEmptyPrefix(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: "user", Content: "first message"},
		{Role: "assistant", Content: "first reply"},
	})
	oldSessionDir := a.sessionDir

	a.handleForkSessionCommand(0)

	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	gotMsgs := a.GetMessages()
	if len(gotMsgs) != 0 {
		t.Fatalf("len(GetMessages()) = %d, want 0 (empty prefix for fork at first message)", len(gotMsgs))
	}

	// Verify the forked message was emitted as a ForkSessionEvent with the
	// first user message's content.
	evt := <-a.Events()
	if _, ok := evt.(SessionSwitchStartedEvent); !ok {
		t.Fatalf("first event = %T, want SessionSwitchStartedEvent", evt)
	}
	evt = nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(SessionRestoredEvent); !ok {
		t.Fatalf("second event = %T, want SessionRestoredEvent", evt)
	}
	evt = <-a.Events()
	forkEvt, ok := evt.(ForkSessionEvent)
	if !ok {
		t.Fatalf("third event = %T, want ForkSessionEvent", evt)
	}
	if len(forkEvt.Parts) != 1 || forkEvt.Parts[0].Text != "first message" {
		t.Fatalf("fork parts = %+v, want single text part 'first message'", forkEvt.Parts)
	}

	// Verify new session has no persisted messages (empty prefix).
	newRecovery := recovery.NewRecoveryManager(a.sessionDir)
	defer newRecovery.Close()
	persisted, err := newRecovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(new session): %v", err)
	}
	if len(persisted) != 0 {
		t.Fatalf("persisted messages = %d, want 0 for empty prefix fork", len(persisted))
	}
}
