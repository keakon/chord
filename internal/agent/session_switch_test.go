package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/filelock"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareSessionSwitchTerminatesBackgroundObjects(t *testing.T) {
	projectRoot := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	tools.StopAllJobsForShutdown()
	resetJobRegistryForAgentTests(t)

	if _, err := tools.ExecuteJobForTest(tools.WithAgentID(context.Background(), a.instanceID), "sleep 5", "Main background service", nil); err != nil {
		t.Fatalf("start main background: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := &SubAgent{instanceID: "agent-1", parentCtx: ctx, cancel: cancel}
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()
	if _, err := tools.ExecuteJobForTest(tools.WithAgentID(context.Background(), sub.instanceID), "sleep 5", "Sub background object", new(5)); err != nil {
		t.Fatalf("start sub background: %v", err)
	}

	oldRecovery, turnCtx := a.prepareSessionSwitch()
	if oldRecovery == nil {
		t.Fatal("expected old recovery manager")
	}
	if turnCtx == nil {
		t.Fatal("expected non-nil turn context")
	}
	for _, state := range tools.SnapshotJobs() {
		if state.Status == "running" || state.Status == "stopping" {
			t.Fatalf("job %s status = %s after prepareSessionSwitch, want terminal", state.ID, state.Status)
		}
	}
}

func resetJobRegistryForAgentTests(t *testing.T) {
	t.Helper()
	restore := tools.ResetJobRegistryForTest()
	t.Cleanup(restore)
}

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

func TestResetSessionRuntimeStateClearsModelDrivenProposalAndNotice(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Session A armed a compact_context request and the worker settled it
	// with a not-applied notice that the next request would surface.
	a.armModelDrivenProposal("call-session-a", tools.CompactContextArgs{ActiveObjective: "compact session a"}, `{"active_objective":"compact session a"}`, "accepted by runtime validation")
	a.pendingModelDrivenNotice = "Context checkpoint not applied: projected savings too small. The session continues on the previous context."

	a.resetSessionRuntimeState()

	if !a.modelDrivenProposal.isEmpty() {
		t.Fatalf("model-driven proposal after session reset = %+v, want empty", a.modelDrivenProposal)
	}
	if a.pendingModelDrivenNotice != "" {
		t.Fatalf("pending model-driven notice after session reset = %q, want empty", a.pendingModelDrivenNotice)
	}
}

func TestResetSessionRuntimeStateKeepsServiceTier(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	client, _, _, _ := a.llmSnapshot()
	client.SetServiceTier(config.ServiceTierFast)

	a.resetSessionRuntimeState()

	if got := a.ServiceTier(); got != config.ServiceTierFast {
		t.Fatalf("service tier = %q, want fast after session runtime reset", got)
	}
}

func TestResetSessionRuntimeStateClearsCacheRoutingState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.noteCacheExpectation("provider/model", []message.Message{{Role: message.RoleUser, Content: "old session"}}, 0, a.computeToolDefinitionHash(), time.Now(), nil)
	for range 3 {
		a.cacheHitTracker.Observe("provider/model", 100, 90)
	}
	a.recordLLMModelRun("provider/model")

	a.resetSessionRuntimeState()

	if a.refCacheWarm("provider/model", time.Now()) {
		t.Fatal("new session inherited the previous session's warm cache signal")
	}
	if _, ok := a.cacheHitTracker.HitRate("provider/model"); ok {
		t.Fatal("new session inherited the previous session's cache hit observations")
	}
	if snapshot := a.llmModelContinuitySnapshot(); snapshot.PreviousModel != "" || snapshot.ProjectedModelRunLength != 1 {
		t.Fatalf("model continuity after session reset = %+v, want empty previous model and projected run length 1", snapshot)
	}
}

func TestResetPersistenceHealthForSessionTargetStartsFresh(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.persistenceHealth.markDegraded(errors.New("disk unavailable"))

	a.resetPersistenceHealthForSessionTarget()

	got := a.persistenceHealth.snapshot()
	if got.State != PersistenceHealthy || got.LastError != "" || !got.FailedAt.IsZero() || !got.RecoveredAt.IsZero() {
		t.Fatalf("persistence health after session target reset = %+v, want fresh healthy state", got)
	}
}

func TestActivateLoadedSessionKeepsServiceTierForMainAndRestoredSubAgents(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 1)
	client, _, _, _ := a.llmSnapshot()
	client.SetServiceTier(config.ServiceTierFast)
	loaded := &loadedSessionState{
		SessionPath: a.sessionDir,
		SubAgentStates: []loadedSubAgentState{{
			InstanceID:   "worker-restored",
			TaskID:       "task-restored",
			AgentDefName: "worker",
			TaskDesc:     "restored work",
			State:        SubAgentStateIdle,
			// The record is rehydrated by the assertion below; rehydration
			// refuses scope-less records.
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
		}},
	}

	a.activateLoadedSession(loaded)

	if got := a.ServiceTier(); got != config.ServiceTierFast {
		t.Fatalf("service tier = %q, want fast after loaded session activation", got)
	}
	if sub := a.subAgentByTaskID("task-restored"); sub != nil {
		t.Fatal("restored SubAgent should remain parked before first use")
	}
	rec := a.taskRecordByTaskID("task-restored")
	if rec == nil || !rec.RuntimeParked {
		t.Fatalf("task record = %#v, want parked restored task", rec)
	}
	sub, _, err := a.rehydrateTask(rec)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	subClient, _ := sub.llmSnapshot()
	if subClient == nil {
		t.Fatal("expected restored SubAgent client")
	}
	if got := subClient.ServiceTier(); got != config.ServiceTierFast {
		t.Fatalf("restored SubAgent service tier = %q, want fast", got)
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

func TestSendUserMessageWithPartsLocalOnlyModelsWhileFocusedSubAgent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	agents := map[string]*config.AgentConfig{
		"builder": {
			Name:       "builder",
			Mode:       config.AgentModeMain,
			ModelPools: []string{"base", "fast"},
		},
		"reviewer": {
			Name:       "reviewer",
			Mode:       "subagent",
			ModelPools: []string{"base", "fast"},
		},
	}
	globalPools := map[string][]string{
		"base": {"provider/model-a"},
		"fast": {"provider/model-b"},
	}
	if err := config.ResolveAgentModelPools(agents, globalPools); err != nil {
		t.Fatalf("ResolveAgentModelPools: %v", err)
	}
	a.SetAgentConfigs(agents)
	a.SetModelPoolPolicy(NewRuntimeModelPoolPolicy(), "")
	a.SetProviderModelRef("provider/model-a")
	a.SetModelSwitchFactory(func(providerModel string, _ []string, _ string) (*llm.Client, string, int, error) {
		providerCfg := llm.NewProviderConfig("provider", config.ProviderConfig{
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
				"model-b": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		modelID := strings.TrimPrefix(providerModel, "provider/")
		return llm.NewClient(providerCfg, stubProvider{}, modelID, 1024, ""), modelID, 8192, nil
	})
	sub := newControllableTestSubAgent(t, a, "adhoc-models")
	sub.agentDefName = "reviewer"
	a.SwitchFocus(sub.instanceID)

	parts := []message.ContentPart{
		{Type: "text", Text: "/models fast"},
		{Type: "image", MimeType: "image/png", Data: []byte{1, 2, 3}, FileName: "shot.png"},
	}
	a.SendUserMessageWithParts(parts)
	// Local-only commands now dispatch via the event loop (fix:
	// cheerful-swinging-seal). Pull the queued event and run dispatch
	// synchronously to mimic what Run() would do.
	dispatchPendingEvents(t, a)

	select {
	case got := <-sub.inputCh:
		t.Fatalf("focused subagent unexpectedly received local-only /models payload: %+v", got)
	default:
	}
	if got := a.ModelPoolPolicy().CurrentModelPool(); got != "" {
		t.Fatalf("CurrentModelPool() = %q, want empty when focused subagent pool changes", got)
	}
	if got, ok := a.ModelPoolPolicy().AgentOverride(sub.agentDefName); !ok || got != "fast" {
		t.Fatalf("AgentOverride(%s) = (%q, %v), want (\"fast\", true)", sub.agentDefName, got, ok)
	}
	if got := a.ProviderModelRef(); got != "provider/model-a" {
		t.Fatalf("ProviderModelRef() = %q, want provider/model-a (main agent unchanged)", got)
	}
	foundRunning := false
	for {
		select {
		case evt := <-a.Events():
			switch e := evt.(type) {
			case RunningModelChangedEvent:
				if e.AgentID == sub.instanceID && e.ProviderModelRef == "provider/model-b" {
					foundRunning = true
				}
			}
		default:
			if !foundRunning {
				t.Fatal("missing RunningModelChangedEvent after focused subagent local-only /models switch")
			}
			return
		}
	}
}

func TestModelsAgentCommandSetsNamedAgentPool(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	agents := map[string]*config.AgentConfig{
		"builder":  {Name: "builder", Mode: config.AgentModeMain, ModelPools: []string{"base"}},
		"reviewer": {Name: "reviewer", Mode: "subagent", ModelPools: []string{"base", "fast"}},
	}
	globalPools := map[string][]string{"base": {"provider/model-a"}, "fast": {"provider/model-b"}}
	if err := config.ResolveAgentModelPools(agents, globalPools); err != nil {
		t.Fatalf("ResolveAgentModelPools: %v", err)
	}
	a.SetAgentConfigs(agents)
	a.SetModelPoolPolicy(NewRuntimeModelPoolPolicy(), "")
	a.SendUserMessage("/models --agent reviewer fast")
	// Local-only commands now dispatch via the event loop (fix:
	// cheerful-swinging-seal).
	dispatchPendingEvents(t, a)

	if got, ok := a.ModelPoolPolicy().AgentOverride("reviewer"); !ok || got != "fast" {
		t.Fatalf("AgentOverride(reviewer) = (%q, %v), want (\"fast\", true)", got, ok)
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

func TestSettledTerminalTaskFocusIsReadOnly(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	const taskID = "settled-task"
	const instanceID = "settled-worker-1"
	a.setTaskRecords(map[string]*DurableTaskRecord{
		taskID: {
			TaskID:            taskID,
			AgentDefName:      "worker",
			LatestInstanceID:  instanceID,
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      taskResumePolicyNotify,
			SettlementDurable: true,
			UpdatedAt:         time.Now(),
		},
	})
	manager := a.recoveryManager()
	if err := manager.PersistMessage(instanceID, message.Message{Role: "user", Content: "original ask"}); err != nil {
		t.Fatalf("persist original ask: %v", err)
	}
	if err := manager.PersistMessage(instanceID, message.Message{
		Role:    "user",
		Content: "[follow_up] continue with option B",
		Kind:    message.KindSubAgentMailbox,
		Mailbox: &message.MailboxMetadata{AgentID: "main", Kind: "follow_up"},
	}); err != nil {
		t.Fatalf("persist notify row: %v", err)
	}

	a.SwitchFocus(instanceID)
	if focused := a.focusedDurableTask(); focused == nil || focused.TaskID != taskID {
		t.Fatalf("focused durable task = %#v, want %s", focused, taskID)
	}
	if got := a.FocusedAgentID(); got != instanceID {
		t.Fatalf("FocusedAgentID() = %q, want %q (the focused conversation identifies the settled instance)", got, instanceID)
	}

	msgs := a.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("GetMessages() = %d rows, want the 2 persisted transcript rows", len(msgs))
	}
	notify := msgs[len(msgs)-1]
	if notify.Kind != message.KindSubAgentMailbox || notify.Mailbox == nil || notify.Mailbox.AgentID != "main" || notify.Mailbox.Kind != "follow_up" || notify.Mailbox.MessageID != "" {
		t.Fatalf("restored notify row = %#v, want the durable main follow-up row", notify)
	}

	a.SendUserMessage("please continue")
	waitForToastEvent(t, a.Events(), "Task settled-task has finished; delegate it again to send it a follow-up")
	if rows := a.ctxMgr.Snapshot(); len(rows) != 0 {
		t.Fatalf("main context = %d rows after SendUserMessage, want 0", len(rows))
	}
	if msgs := a.GetMessages(); len(msgs) != 2 {
		t.Fatalf("settled transcript = %d rows after SendUserMessage, want 2", len(msgs))
	}

	a.ContinueFromContext()
	waitForToastEvent(t, a.Events(), "Task settled-task has finished; delegate it again to continue")
	if msgs := a.GetMessages(); len(msgs) != 2 {
		t.Fatalf("settled transcript = %d rows after ContinueFromContext, want 2", len(msgs))
	}

	a.RemoveLastMessage()
	if msgs := a.GetMessages(); len(msgs) != 2 {
		t.Fatalf("settled transcript = %d rows after RemoveLastMessage, want 2", len(msgs))
	}
	if rows := a.ctxMgr.Snapshot(); len(rows) != 0 {
		t.Fatalf("main context = %d rows after RemoveLastMessage, want 0", len(rows))
	}
}

func TestStaleFocusedAgentFallsBackToMainForUserInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-10")
	a.focusedAgent.Store(sub)
	a.subs.mu.Lock()
	delete(a.subs.subAgents, sub.instanceID)
	a.subs.mu.Unlock()

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
	a.subs.mu.Lock()
	delete(a.subs.subAgents, sub.instanceID)
	a.subs.mu.Unlock()

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
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()

	a.newTurn()
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "finished"},
	})

	if got := len(a.GetMessages()); got != 0 {
		t.Fatalf("handleAgentDone polluted transcript, len(GetMessages()) = %d", got)
	}
	if got := len(a.GetSubAgents()); got != 1 {
		t.Fatalf("len(GetSubAgents()) = %d, want completed agent retained for manual continue", got)
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
		{Role: "assistant", Content: "tail reply"},
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
		{Role: "assistant", Content: "tail reply"},
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
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "todo-call-1", Name: "todo_write", Args: todoArgs}}},
		{Role: "user", Content: "edit me"},
		{Role: "assistant", Content: "tail reply"},
	}
	a.ctxMgr.RestoreMessages(msgs)
	a.todoMu.Lock()
	a.todoItems = []tools.TodoItem{{ID: "stale", Content: "stale todo", Status: "completed"}}
	a.todoMu.Unlock()
	a.ctxMgr.SetLastInputTokens(999)
	a.ctxMgr.SetLastTotalContextTokens(999)
	a.usageTracker.RestoreStats(analytics.SessionStats{InputTokens: 42, LLMCalls: 1})
	oldSessionDir := a.sessionDir
	if err := recovery.SaveSessionMeta(oldSessionDir, recovery.SessionMeta{Title: "Old custom title", MCPEnabledServers: []string{" manual-search ", "manual-files", "manual-search"}}); err != nil {
		t.Fatalf("SaveSessionMeta(old): %v", err)
	}
	a.refreshSessionSummary()

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
	if len(gotMsgs[1].ToolCalls) != 1 || gotMsgs[1].ToolCalls[0].Name != "todo_write" {
		t.Fatalf("second message = %+v, want todo_write assistant", gotMsgs[1])
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
	if forkMeta, err := recovery.LoadSessionMeta(a.sessionDir); err != nil || forkMeta == nil ||
		!slices.Equal(forkMeta.MCPEnabledServers, []string{"manual-files", "manual-search"}) {
		t.Fatalf("fork MCP intent = %#v, %v; want [manual-files manual-search]", forkMeta, err)
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

func TestHandleForkSessionCommandRestoresTrackedReadsFromPrefixOnly(t *testing.T) {
	projectRoot := t.TempDir()
	alphaPath := filepath.Join(projectRoot, "alpha.txt")
	betaPath := filepath.Join(projectRoot, "beta.txt")
	writeTestFile(t, alphaPath, "alpha")
	writeTestFile(t, betaPath, "beta")

	a := newRestoreEditTestAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.fileTrack.TrackSnapshot(betaPath, a.instanceID, computeFileHash(betaPath))

	msgs := []message.Message{{Role: "user", Content: "read alpha"}}
	msgs = append(msgs, restoreReadMessages(t, "read-alpha", alphaPath, computeFileHash(alphaPath), nil)...)
	msgs = append(msgs,
		message.Message{Role: "user", Content: "fork here"},
		restoreAssistantCall(t, "read-beta", tools.NameRead, map[string]any{"path": betaPath}, nil),
		message.Message{
			Role:       "tool",
			ToolCallID: "read-beta",
			ToolStatus: string(ToolResultStatusSuccess),
			Content:    "1\tbeta",
			FileState:  &message.ToolFileState{Reads: []message.TrackedFileState{{Path: betaPath, SHA256: computeFileHash(betaPath), Exists: true}}},
		},
	)
	a.ctxMgr.RestoreMessages(msgs)

	a.handleForkSessionCommand(3)

	mustExecuteEdit(t, a, alphaPath, "alpha", "alpha-updated")
	if a.fileTrack.HasSnapshot(betaPath, a.instanceID) {
		t.Fatal("fork should not preserve tracked snapshots that occur after the fork prefix")
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
	a.ctxMgr.Append(message.Message{Role: "assistant", Content: "tail reply"})
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
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()
	a.focusedAgent.Store(sub)
	a.sem <- struct{}{}

	a.persistAsync("main", message.Message{Role: "assistant", Content: "persisted tail"})

	a.handleNewSessionCommand()

	if a.sessionDir == oldSessionDir {
		t.Fatal("sessionDir was not switched")
	}
	if summary := a.GetSessionSummary(); summary == nil || summary.ID != filepath.Base(a.sessionDir) {
		t.Fatalf("GetSessionSummary() = %+v, want current session id %q", summary, filepath.Base(a.sessionDir))
	} else if summary.Title != "" {
		t.Fatalf("new session inherited custom title %q", summary.Title)
	}
	meta, err := recovery.LoadSessionMeta(a.sessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta(new): %v", err)
	}
	if meta != nil && meta.Title != "" {
		t.Fatalf("new session metadata inherited custom title: %+v", meta)
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

func TestHandleNewSessionCommandClearsPendingLSPDiagnosticOverlay(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.pendingLSPDiagnosticOverlay = pendingLSPDiagnosticOverlayText

	a.handleNewSessionCommand()

	if a.pendingLSPDiagnosticOverlay != "" {
		t.Fatalf("pendingLSPDiagnosticOverlay = %q, want empty after /new", a.pendingLSPDiagnosticOverlay)
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

func TestHandleForkSessionCommandTailUserEditsInPlaceWithoutFork(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	alphaPath := filepath.Join(projectRoot, "alpha.txt")
	writeTestFile(t, alphaPath, "alpha")

	msgs := []message.Message{{Role: "user", Content: "read alpha"}}
	msgs = append(msgs, restoreReadMessages(t, "read-alpha", alphaPath, computeFileHash(alphaPath), nil)...)
	msgs = append(msgs, message.Message{Role: "user", Content: "edit me"})
	a.ctxMgr.RestoreMessages(msgs)
	a.fileTrack = filelock.NewFileTracker()
	a.restoreMainTrackedFileState(msgs)
	if err := a.recoveryManager().RewriteLog("main", msgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}
	if err := a.usageLedger.SetFirstUserMessage("read alpha"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	a.refreshSessionSummary()
	oldSessionDir := a.sessionDir

	a.handleForkSessionCommand(3)

	if a.sessionDir != oldSessionDir {
		t.Fatalf("sessionDir = %q, want unchanged %q", a.sessionDir, oldSessionDir)
	}
	gotMsgs := a.GetMessages()
	if len(gotMsgs) != 3 {
		t.Fatalf("len(GetMessages()) = %d, want 3 after removing tail user message", len(gotMsgs))
	}
	if gotMsgs[2].Role != "tool" {
		t.Fatalf("last remaining message = %+v, want tool result before removed tail user message", gotMsgs[2])
	}
	if !a.fileTrack.HasSnapshot(alphaPath, a.instanceID) {
		t.Fatal("tracked snapshot for prefix message should be preserved after in-place tail edit")
	}

	persisted, err := a.recoveryManager().LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(persisted) != 3 {
		t.Fatalf("len(persisted) = %d, want 3 after dropping tail user message", len(persisted))
	}
	if persisted[2].Role != "tool" {
		t.Fatalf("persisted tail = %+v, want tool result after dropping tail user message", persisted[2])
	}

	if summary := a.GetSessionSummary(); summary == nil {
		t.Fatal("GetSessionSummary() = nil")
	} else {
		if summary.ForkedFrom != "" {
			t.Fatalf("ForkedFrom = %q, want empty for in-place tail edit", summary.ForkedFrom)
		}
		if summary.FirstUserMessage != "read alpha" {
			t.Fatalf("FirstUserMessage = %q, want read alpha", summary.FirstUserMessage)
		}
	}

	evt := nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(SessionRestoredEvent); !ok {
		t.Fatalf("first event = %T, want SessionRestoredEvent", evt)
	}
	evt = <-a.Events()
	forkEvt, ok := evt.(ForkSessionEvent)
	if !ok {
		t.Fatalf("second event = %T, want ForkSessionEvent", evt)
	}
	if len(forkEvt.Parts) != 1 || forkEvt.Parts[0].Text != "edit me" {
		t.Fatalf("fork parts = %+v, want single text part 'edit me'", forkEvt.Parts)
	}
	evt = <-a.Events()
	toast, ok := evt.(ToastEvent)
	if !ok {
		t.Fatalf("third event = %T, want ToastEvent", evt)
	}
	if toast.Level != "info" || !strings.Contains(toast.Message, "current session") {
		t.Fatalf("unexpected toast: %+v", toast)
	}
	select {
	case evt := <-a.Events():
		if _, ok := evt.(SessionSwitchStartedEvent); ok {
			t.Fatalf("unexpected SessionSwitchStartedEvent after in-place tail edit: %+v", evt)
		}
	default:
	}
}

// A compacted session's prefix begins with the checkpoint summary. Editing the
// tail user message in place must keep that summary flagged as synthetic (and
// keep the preserved original), so the session list never presents the
// checkpoint as the user's prompt.
func TestHandleForkSessionCommandTailEditKeepsCompactionFirstUserFlag(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	msgs := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- ship it", IsCompactionSummary: true},
		{Role: "assistant", Content: "carrying on"},
		{Role: "user", Content: "edit me"},
	}
	a.ctxMgr.RestoreMessages(msgs)
	a.fileTrack = filelock.NewFileTracker()
	a.restoreMainTrackedFileState(msgs)
	if err := a.recoveryManager().RewriteLog("main", msgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}
	a.refreshSessionSummary()
	a.updateSessionSummary(func(summary *SessionSummary) {
		if summary != nil {
			summary.OriginalFirstUserMessage = "original request"
		}
	})
	oldSessionDir := a.sessionDir

	a.handleForkSessionCommand(2)

	if a.sessionDir != oldSessionDir {
		t.Fatalf("sessionDir = %q, want unchanged %q", a.sessionDir, oldSessionDir)
	}
	summary := a.GetSessionSummary()
	if summary == nil {
		t.Fatal("GetSessionSummary() = nil")
	}
	if !summary.FirstUserMessageIsCompactionSummary {
		t.Fatal("FirstUserMessageIsCompactionSummary = false, want true for a compacted prefix")
	}
	if !strings.Contains(summary.FirstUserMessage, "Context Summary") {
		t.Fatalf("FirstUserMessage = %q, want the checkpoint preview", summary.FirstUserMessage)
	}
	if summary.OriginalFirstUserMessage != "original request" {
		t.Fatalf("OriginalFirstUserMessage = %q, want the preserved original", summary.OriginalFirstUserMessage)
	}
	for {
		select {
		case evt := <-a.Events():
			if _, ok := evt.(SessionSwitchStartedEvent); ok {
				t.Fatalf("unexpected SessionSwitchStartedEvent after in-place tail edit: %+v", evt)
			}
		default:
			return
		}
	}
}

func TestHandleForkSessionCommandTailEditDoesNotDrainSubAgentInbox(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.RestoreMessages([]message.Message{{Role: "user", Content: "edit me"}})
	a.subAgentInbox.normal = append(a.subAgentInbox.normal, SubAgentMailboxMessage{
		AgentID:   "reviewer-1",
		MessageID: "mail-1",
		Kind:      SubAgentMailboxKindCompleted,
	})

	a.handleForkSessionCommand(0)

	if a.currentTurn() != nil {
		t.Fatal("tail edit started a new main turn from queued SubAgent mailbox")
	}
	if got := len(a.subAgentInbox.normal); got != 1 {
		t.Fatalf("len(subAgentInbox.normal) = %d, want queued mailbox preserved", got)
	}
}

// Editing the tail user message in place can drop the session's only user
// prompt (the ee chord right after the first request). The cached usage summary
// must not keep that removed prompt as the preserved original: session lists
// prefer the original over the current preview, so the resume picker would keep
// showing the pre-edit question after the corrected one is submitted.
func TestHandleForkSessionCommandTailEditDropsRemovedFirstUserPreview(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	msgs := []message.Message{{Role: "user", Content: "question one"}}
	a.ctxMgr.RestoreMessages(msgs)
	if err := a.recoveryManager().RewriteLog("main", msgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}
	if err := a.usageLedger.SetFirstUserMessage("question one"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	a.refreshSessionSummary()

	a.handleForkSessionCommand(0)

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage != "" || summary.OriginalFirstUserMessage != "" {
		t.Fatalf("usage summary kept the removed prompt: first=%q original=%q", summary.FirstUserMessage, summary.OriginalFirstUserMessage)
	}

	a.recordCommittedUserMessage(message.Message{Role: "user", Content: "question two"})
	a.flushPersist()

	// Read the session back through the same path the resume picker uses.
	list, err := recovery.ListSessions(filepath.Dir(a.SessionDir()), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(ListSessions) = %d, want 1", len(list))
	}
	if list[0].FirstUserMessage != "question two" {
		t.Fatalf("FirstUserMessage = %q, want question two", list[0].FirstUserMessage)
	}
	if list[0].OriginalFirstUserMessage != "question two" {
		t.Fatalf("OriginalFirstUserMessage = %q, want question two", list[0].OriginalFirstUserMessage)
	}
}

// A prefix that starts with a compaction checkpoint has no user-authored head
// left: the first prompt in it is a mid-session one. Editing the tail must not
// promote that prompt to the session's original request — session lists prefer
// the original over the current preview, and every later checkpoint copies it
// forward as its "Original request:" anchor. When the cached original is missing
// (a summary written before OriginalFirstUserMessage existed, or one that lost
// it), the checkpoint's own anchors block is the only remaining source.
func TestHandleForkSessionCommandTailEditKeepsCheckpointAnchorAsOriginal(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	const (
		original   = "REAL original request"
		midSession = "mid-session prompt"
		tail       = "tail prompt"
	)
	msgs := []message.Message{
		checkpointWithAnchor(original),
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: midSession},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: tail},
	}
	a.ctxMgr.RestoreMessages(msgs)
	if err := a.recoveryManager().RewriteLog("main", msgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}
	// Mirror what the compaction path records for a summary that has no cached
	// original: the preview is the checkpoint and the original is left empty.
	if err := a.usageLedger.RewriteFirstUserMessageWithOriginalForCompaction(
		message.UserPromptPlainText(msgs[0]), ""); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginalForCompaction: %v", err)
	}
	a.refreshSessionSummary()

	a.handleForkSessionCommand(len(msgs) - 1)

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != original {
		t.Fatalf("usage summary original = %q, want %q", summary.OriginalFirstUserMessage, original)
	}

	// Read the session back through the same path the resume picker uses.
	list, err := recovery.ListSessions(filepath.Dir(a.SessionDir()), "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(ListSessions) = %d, want 1", len(list))
	}
	if list[0].OriginalFirstUserMessage != original {
		t.Fatalf("picker original = %q, want %q", list[0].OriginalFirstUserMessage, original)
	}
}

// The same edit on a checkpoint that carries no anchors block has nothing left
// that could name the original request. Leaving it empty is the only honest
// outcome: recording the post-checkpoint prompt there would be permanent, since
// session lists prefer the original and every later checkpoint copies it forward
// as its "Original request:" anchor.
func TestHandleForkSessionCommandTailEditLeavesUnknownOriginalEmpty(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	const (
		midSession = "mid-session prompt"
		tail       = "tail prompt"
	)
	msgs := []message.Message{
		{Role: "user", Content: "[Context Summary]\n## Goal\n- carry on", IsCompactionSummary: true},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: midSession},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: tail},
	}
	a.ctxMgr.RestoreMessages(msgs)
	if err := a.recoveryManager().RewriteLog("main", msgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}
	if err := a.usageLedger.RewriteFirstUserMessageWithOriginalForCompaction(
		message.UserPromptPlainText(msgs[0]), ""); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginalForCompaction: %v", err)
	}
	a.refreshSessionSummary()

	a.handleForkSessionCommand(len(msgs) - 1)

	summary, err := a.usageLedger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != "" {
		t.Fatalf("usage summary original = %q, want empty", summary.OriginalFirstUserMessage)
	}
	if summary.FirstUserMessage != midSession {
		t.Fatalf("usage summary preview = %q, want %q", summary.FirstUserMessage, midSession)
	}
}

// The tail edit removes messages, so everything derived from them has to be
// rebuilt: a deleted later user message must leave no trace in the inputs the
// compaction summary is built from (transcript, evidence pack, anchors), while
// the surviving constraint stays.
func TestHandleForkSessionCommandTailEditKeepsDeletedMessageOutOfSummaryInputs(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()

	const (
		deletedTail = "DELETED tail prompt"
		constraint  = "DO NOT TOUCH the public API"
	)
	msgs := []message.Message{
		{Role: "user", Content: "FIRST prompt"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: constraint},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: deletedTail},
	}
	a.ctxMgr.RestoreMessages(msgs)
	for _, msg := range msgs {
		a.recordEvidenceFromMessage(msg)
	}
	if err := a.recoveryManager().RewriteLog("main", msgs); err != nil {
		t.Fatalf("RewriteLog(main): %v", err)
	}
	if err := a.usageLedger.SetFirstUserMessage("FIRST prompt"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	a.refreshSessionSummary()

	a.handleForkSessionCommand(4)
	a.recordCommittedUserMessage(message.Message{Role: "user", Content: "CORRECTED tail prompt"})

	snapshot := a.ctxMgr.Snapshot()
	var transcript []string
	for _, msg := range snapshot {
		transcript = append(transcript, msg.Content)
	}
	if strings.Contains(strings.Join(transcript, "\n"), deletedTail) {
		t.Fatalf("transcript still carries the deleted message: %+v", transcript)
	}

	evidenceText := make([]string, 0, len(a.evidence.snapshot()))
	for _, item := range a.evidence.snapshot() {
		evidenceText = append(evidenceText, item.Excerpt, item.WhyNeeded)
	}
	joinedEvidence := strings.Join(evidenceText, "\n")
	if strings.Contains(joinedEvidence, deletedTail) {
		t.Fatalf("evidence pack still carries the deleted message: %s", joinedEvidence)
	}
	if !strings.Contains(joinedEvidence, constraint) {
		t.Fatalf("evidence pack dropped the surviving constraint: %s", joinedEvidence)
	}

	anchors := buildCompactionAnchors(
		latestCompactionAnchors(snapshot),
		a.captureOriginalFirstUserHint(),
		a.evidenceItemsForCompaction(a.ctxMgr.GetMaxTokens()),
	)
	if anchors.OriginalRequest != "FIRST prompt" {
		t.Fatalf("anchors.OriginalRequest = %q, want the session's first prompt", anchors.OriginalRequest)
	}
	if strings.Contains(strings.Join(anchors.Constraints, "\n"), deletedTail) {
		t.Fatalf("anchors carry the deleted message: %+v", anchors.Constraints)
	}
	if !slices.Contains(anchors.Constraints, constraint) {
		t.Fatalf("anchors dropped the surviving constraint: %+v", anchors.Constraints)
	}
}

func TestHandleRenameCommandPreservesMetadataAndUpdatesSessionSummary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	originalID := filepath.Base(a.SessionDir())
	if err := recovery.SaveSessionMeta(a.SessionDir(), recovery.SessionMeta{ForkedFrom: "parent-session"}); err != nil {
		t.Fatalf("SaveSessionMeta(): %v", err)
	}

	a.handleRenameCommand("Release review")

	meta, err := recovery.LoadSessionMeta(a.SessionDir())
	if err != nil {
		t.Fatalf("LoadSessionMeta(): %v", err)
	}
	if meta == nil || meta.Title != "Release review" || meta.ForkedFrom != "parent-session" {
		t.Fatalf("metadata after rename = %+v", meta)
	}
	if got := filepath.Base(a.SessionDir()); got != originalID {
		t.Fatalf("session ID after rename = %q, want %q", got, originalID)
	}
	if summary := a.GetSessionSummary(); summary == nil || summary.Title != "Release review" || summary.ID != originalID {
		t.Fatalf("session summary after rename = %+v", summary)
	}

	event := <-a.Events()
	changed, ok := event.(SessionTitleChangedEvent)
	if !ok || changed.Title != "Release review" {
		t.Fatalf("first event = %#v, want SessionTitleChangedEvent", event)
	}
	toast := waitForToastEvent(t, a.Events(), "Session title set to: Release review")
	if toast.Level != "info" {
		t.Fatalf("toast level = %q, want info", toast.Level)
	}

	a.handleRenameCommand("")
	meta, err = recovery.LoadSessionMeta(a.SessionDir())
	if err != nil {
		t.Fatalf("LoadSessionMeta() after clear: %v", err)
	}
	if meta == nil || meta.Title != "" || meta.ForkedFrom != "parent-session" {
		t.Fatalf("metadata after clear = %+v", meta)
	}
	event = <-a.Events()
	changed, ok = event.(SessionTitleChangedEvent)
	if !ok || changed.Title != "" {
		t.Fatalf("clear event = %#v, want empty SessionTitleChangedEvent", event)
	}
}
