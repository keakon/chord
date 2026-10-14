package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestRehydratePersistenceFailureDoesNotRegisterRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	record := &DurableTaskRecord{
		TaskID:           "adhoc-rehydrate-persist",
		AgentDefName:     "worker",
		TaskDesc:         "resume",
		State:            string(SubAgentStateIdle),
		RuntimeParked:    true,
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		// The fixture carries a realistic write boundary, matching records a
		// delegated task would have been admitted under; this test only
		// exercises the persistence-failure path.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	// Block the registration file while keeping transcript and native journal
	// recovery readable, so admission reaches the intended persistence failure.
	if err := os.MkdirAll(durableTaskRegistryPath(a.sessionDir), 0o700); err != nil {
		t.Fatalf("block task registry: %v", err)
	}

	_, _, err := a.rehydrateTask(record)
	if err == nil || !strings.Contains(err.Error(), "persist rehydrated durable task registration") {
		t.Fatalf("rehydrateTask() error = %v, want persistence failure", err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0 after failed rehydrate", got)
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("semaphore use = %d, want 0 after failed rehydrate", got)
	}
}

func TestRehydrateTaskRejectsWhileSessionTransitionIsPaused(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	record := &DurableTaskRecord{
		TaskID:           "parked-task",
		AgentDefName:     "worker",
		TaskDesc:         "resume work",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		RuntimeParked:    true,
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	a.prepareSessionSwitch()

	sub, _, _, err := a.getOrRehydrateTask(cloneDurableTaskRecord(record))
	if err == nil || sub != nil {
		t.Fatalf("getOrRehydrateTask during transition = (sub=%v, err=%v), want rejection", sub != nil, err)
	}
	if got := len(a.subs.snapshotSubAgents()); got != 0 {
		t.Fatalf("live SubAgents = %d, want 0", got)
	}
}

func TestSendMessageToCompletedWorkerCreatesFollowupWithoutNewTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-3")
	sub.setState(SubAgentStateCompleted, "finished initial pass")
	// Keep the durable record in step with the runtime: a settled task that
	// still has a live worker attached is admitted for a follow-up attempt
	// from the record's terminal state, not from the stale running snapshot
	// the helper's initial sync left behind.
	a.syncTaskRecordFromSub(sub, "task completed")
	sub.setLastMailboxID("worker-1-9")

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-3", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if handle.AgentID != sub.instanceID {
		t.Fatalf("handle.AgentID = %q, want %q", handle.AgentID, sub.instanceID)
	}
	if handle.TaskID != sub.taskID {
		t.Fatalf("handle.TaskID = %q, want %q", handle.TaskID, sub.taskID)
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected completed worker follow-up to reacquire semaphore")
	}
}

func TestFocusedCompletedWorkerDirectInputResumesSameWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	sub.setState(SubAgentStateCompleted, "finished initial pass")
	sub.ownerAgentID = "main-owner"
	sub.ownerTaskID = "main-task"
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)

	a.SendUserMessage("follow up on edge cases")

	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("expected focused completed worker direct input to reacquire semaphore")
	}
	select {
	case msg := <-sub.inputCh:
		if got := pendingUserMessageText(msg); got != "[follow_up] follow up on edge cases" {
			t.Fatalf("queued message = %q, want %q", got, "[follow_up] follow up on edge cases")
		}
	default:
		t.Fatal("expected focused completed worker to receive direct follow-up input")
	}
}

func TestInvalidTaskIDReturnsErrorWithoutImplicitSpawn(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if _, err := a.NotifySubAgent(context.Background(), "missing-task", "hello", "reply"); err == nil {
		t.Fatal("expected NotifySubAgent to fail for missing task_id")
	}
	if _, err := a.CancelSubAgent(context.Background(), "missing-task", "stop"); err == nil {
		t.Fatal("expected CancelSubAgent to fail for missing task_id")
	}
}

func TestSessionSwitchInvalidatesOldTaskID(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	_ = newControllableTestSubAgent(t, a, "adhoc-4")
	if abandoned := a.abandonSubAgentsForSessionSwitch(); abandoned != 1 {
		t.Fatalf("abandonSubAgentsForSessionSwitch() = %d, want 1", abandoned)
	}
	if _, err := a.NotifySubAgent(context.Background(), "adhoc-4", "continue", "reply"); err == nil {
		t.Fatal("expected old task_id to be invalid after session switch abandonment")
	}
}

func TestCompletedWorkerParksAndPersistsTaskRecord(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-5")
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("expected completed worker runtime to be parked")
	}
	record := a.taskRecordByTaskID(sub.taskID)
	if record == nil {
		t.Fatal("expected durable task record for completed worker")
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("record.State = %q, want %q", record.State, SubAgentStateCompleted)
	}
	if !record.RuntimeParked {
		t.Fatal("record.RuntimeParked = false, want true")
	}
}

func TestCompletedChildReleasesSlotWhileParentWaitsForManualContinue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	parent := newControllableTestSubAgent(t, a, "adhoc-parent")
	parent.instanceID = "worker-parent"
	parent.setState(SubAgentStateWaitingDescendant, "waiting for child")
	parent.semHeld = false

	child := newControllableTestSubAgent(t, a, "adhoc-child")
	child.instanceID = "worker-child"
	child.ownerAgentID = parent.instanceID
	child.ownerTaskID = parent.taskID
	child.joinToOwner = true
	child.semHeld = true
	a.sem <- struct{}{}

	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[parent.instanceID] = parent
	a.subs.subAgents[child.instanceID] = child
	a.subs.mu.Unlock()

	a.handleAgentDone(Event{SourceID: child.instanceID, Payload: &AgentResult{Summary: "done"}})

	if child.semHeld {
		t.Fatal("completed child still holds a semaphore slot")
	}
	if parent.semHeld {
		t.Fatal("waiting parent acquired a slot before manual continue")
	}
	if got := len(a.sem); got != 0 {
		t.Fatalf("len(a.sem) = %d, want 0 after child completion", got)
	}
}

func TestSendMessageToCompletedTaskRehydratesParkedWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	sub := newControllableTestSubAgent(t, a, "adhoc-13")
	sub.agentDefName = "restorer"
	sub.taskDesc = "Investigate issue"
	sub.planTaskRef = "plan-item-13"
	sub.semanticTaskKey = "investigate-issue"
	sub.writeScope = tools.WriteScope{Files: []string{"internal/agent/main_subagent_control.go"}}
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Investigate issue"})
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	oldInstanceID := sub.instanceID
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-13", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if !handle.Rehydrated {
		t.Fatal("completed parked worker should rehydrate")
	}
	if handle.PreviousAgentID != oldInstanceID {
		t.Fatalf("handle.PreviousAgentID = %q, want %q", handle.PreviousAgentID, oldInstanceID)
	}
	if handle.AgentID == oldInstanceID {
		t.Fatalf("handle.AgentID = %q, want a new runtime instance", handle.AgentID)
	}
	restored := a.subAgentByTaskID("adhoc-13")
	if restored == nil {
		t.Fatal("expected rehydrated live worker")
	}
	if restored.instanceID != handle.AgentID {
		t.Fatalf("restored.instanceID = %q, want %q", restored.instanceID, handle.AgentID)
	}
	if restored.State() != SubAgentStateRunning {
		t.Fatalf("restored.State() = %q, want running", restored.State())
	}
	record := a.taskRecordByTaskID("adhoc-13")
	if record == nil {
		t.Fatal("expected durable task record")
	}
	if record.LatestInstanceID != handle.AgentID {
		t.Fatalf("record.LatestInstanceID = %q, want %q", record.LatestInstanceID, handle.AgentID)
	}
	if len(record.InstanceHistory) != 2 {
		t.Fatalf("len(record.InstanceHistory) = %d, want 2", len(record.InstanceHistory))
	}
	if record.PlanTaskRef != sub.planTaskRef || record.SemanticTaskKey != sub.semanticTaskKey {
		t.Fatalf("rehydrated task identity = (%q, %q), want (%q, %q)", record.PlanTaskRef, record.SemanticTaskKey, sub.planTaskRef, sub.semanticTaskKey)
	}
	if len(record.ExpectedWriteScope.Files) != 1 || record.ExpectedWriteScope.Files[0] != "internal/agent/main_subagent_control.go" {
		t.Fatalf("rehydrated write scope = %#v, want original file scope", record.ExpectedWriteScope)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !restored.hasActiveTurn() {
		time.Sleep(time.Millisecond)
	}
	if !restored.hasActiveTurn() {
		t.Fatal("rehydrated worker did not consume the delivered message and create a turn")
	}
}

func TestRehydratePreservesConfiguredOrchestrationAndWorkDir(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig = config.DefaultConfig()
	a.globalConfig.Context.Compaction.Threshold = 0.65
	a.projectConfig = &config.Config{Orchestration: config.OrchestrationConfig{
		SubAgentQueueMessages: 7,
		SubAgentQueueBytes:    12345,
	}}
	wantWorkDir := filepath.Join(a.contentRoot, "workspace")
	setCachedWorkDirForTest(a, wantWorkDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   config.AgentModeSubAgent,
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	record := &DurableTaskRecord{
		TaskID:           "adhoc-rehydrate-config",
		AgentDefName:     "restorer",
		TaskDesc:         "resume with configured limits",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "restorer-old",
		InstanceHistory:  []string{"restorer-old"},
		RuntimeParked:    true,
		// The completed worker may only be revived under the write boundary
		// its admission recorded; this test pins the orchestration/limits it
		// resumes with, not the scope itself.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	sub, _, err := a.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if sub.queueMessageLimit != 7 || sub.queueByteLimit != 12345 {
		t.Fatalf("rehydrated orchestration = messages:%d bytes:%d", sub.queueMessageLimit, sub.queueByteLimit)
	}
	if got := sub.applyModelCompactionConfig(); got != 0.65 {
		t.Fatalf("rehydrated compaction threshold = %v, want shared global 0.65", got)
	}
	if sub.workDir != wantWorkDir {
		t.Fatalf("rehydrated workDir = %q, want %q", sub.workDir, wantWorkDir)
	}
}

// A task admitted under a role that registers no file-modifying tools carries
// an empty write scope ("nothing to declare"); revival must accept that record
// because the role's tool surface is the boundary, not a declared path list.
func TestRehydrateAcceptsEmptyScopeForNoFileWriteToolRole(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	setCachedWorkDirForTest(a, a.contentRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"reviewer": {
			Name:   "reviewer",
			Mode:   config.AgentModeSubAgent,
			Models: map[string][]string{"default": {"test/test-model"}},
			Permission: parsePermissionNode(t,
				"write: deny\nedit: deny\ndelete: deny\napply_patch: deny\nread: allow\nshell: deny\n"),
		},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.WriteTool{})
	a.tools.Register(tools.NewShellTool("bash"))
	record := &DurableTaskRecord{
		TaskID:             "adhoc-rehydrate-empty",
		AgentDefName:       "reviewer",
		TaskDesc:           "resume read-only survey",
		State:              string(SubAgentStateCompleted),
		ResumePolicy:       taskResumePolicyNotify,
		LatestInstanceID:   "reviewer-old",
		InstanceHistory:    []string{"reviewer-old"},
		RuntimeParked:      true,
		ExpectedWriteScope: tools.WriteScope{},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	sub, _, err := a.rehydrateTask(record)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if !sub.currentWriteScope().Empty() {
		t.Fatalf("rehydrated scope = %#v, want empty preserved", sub.currentWriteScope())
	}
	for _, name := range []string{tools.NameWrite, tools.NameEdit, tools.NameDelete, tools.NameApplyPatch} {
		if _, ok := sub.tools.Get(name); ok {
			t.Fatalf("file-modifying tool %s registered for a denied role after rehydrate", name)
		}
	}
	if _, ok := sub.tools.Get(tools.NameRead); !ok {
		t.Fatal("read tool lost for a role that allows it")
	}
}

func TestConcurrentTaskRehydratePublishesOneRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	var factoryCalls atomic.Int32
	factoryStarted := make(chan struct{})
	releaseFactory := make(chan struct{})
	a.SetLLMFactory(func(string, []string, string) *llm.Client {
		if factoryCalls.Add(1) == 1 {
			close(factoryStarted)
		}
		<-releaseFactory
		return newTestLLMClient()
	})
	record := &DurableTaskRecord{
		TaskID:           "adhoc-concurrent-rehydrate",
		AgentDefName:     "restorer",
		TaskDesc:         "resume once",
		State:            string(SubAgentStateCompleted),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "restorer-40",
		InstanceHistory:  []string{"restorer-40"},
		RuntimeParked:    true,
		// The concurrent-activation fixture carries the boundary a real
		// delegated task would have been admitted under.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})

	type result struct {
		sub        *SubAgent
		rehydrated bool
		err        error
	}
	results := make(chan result, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Go(func() {
			sub, _, rehydrated, err := a.getOrRehydrateTask(cloneDurableTaskRecord(record))
			results <- result{sub: sub, rehydrated: rehydrated, err: err}
		})
	}
	<-factoryStarted
	// Give the follower a bounded chance to (incorrectly) reach the LLM
	// factory while the leader is blocked: poll with a timeout instead of a
	// single fixed sleep so slow machines still observe the overlap. The
	// correct path pays the window in full, so keep it short.
	activationDeadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(activationDeadline) {
		if got := factoryCalls.Load(); got != 1 {
			t.Fatalf("LLM factory calls while activation is in flight = %d, want 1", got)
		}
		time.Sleep(time.Millisecond)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("LLM factory calls while activation is in flight = %d, want 1", got)
	}
	close(releaseFactory)
	callers.Wait()
	close(results)

	var first *SubAgent
	rehydratedCount := 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("getOrRehydrateTask: %v", got.err)
		}
		if first == nil {
			first = got.sub
		} else if got.sub != first {
			t.Fatalf("concurrent callers received different runtimes: %p and %p", first, got.sub)
		}
		if got.rehydrated {
			rehydratedCount++
		}
	}
	if rehydratedCount != 1 {
		t.Fatalf("rehydrated result count = %d, want one activation leader", rehydratedCount)
	}
	live := 0
	for _, sub := range a.subs.snapshotSubAgents() {
		if sub.taskID == record.TaskID {
			live++
		}
	}
	if live != 1 || len(a.sem) != 1 {
		t.Fatalf("live runtimes = %d, semaphore slots = %d; want 1 and 1", live, len(a.sem))
	}
}

func TestRehydrateDecidesAttemptFromCurrentRecordNotStaleSnapshot(t *testing.T) {
	a, record := newRevivalRaceTestMainAgent(t)
	// The sweep settles the attempt after the caller took its snapshot but
	// before rehydration begins: the current record is terminal even though
	// the snapshot still says waiting_main.
	if got := a.settleDetachedTerminalTask(record.TaskID, SubAgentStateCancelled, "expired waiting for main reply", "expired"); got != SubAgentStateCancelled {
		t.Fatalf("settleDetachedTerminalTask = %q, want cancelled", got)
	}

	sub, _, err := a.rehydrateTask(cloneDurableTaskRecord(record))
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if sub == nil {
		t.Fatal("expected revived runtime")
	}
	rec := a.taskRecordByTaskID(record.TaskID)
	if rec.Attempt != 4 {
		t.Fatalf("revived attempt = %d, want 4 (bumped past the settled attempt)", rec.Attempt)
	}
	if rec.LatestSettlement != nil || rec.SettlementDurable {
		t.Fatalf("revived record kept the settled attempt's settlement: %+v", rec.LatestSettlement)
	}
}

func TestRehydrateBacksOffWhenTaskSettlesDuringCommit(t *testing.T) {
	a, record := newRevivalRaceTestMainAgent(t)
	// The sweep wins the race inside the rehydration window: after the attempt
	// decision was made from a live record, but before the runtime is published.
	a.rehydrateCommitHook = func() {
		a.rehydrateCommitHook = nil
		if got := a.settleDetachedTerminalTask(record.TaskID, SubAgentStateCancelled, "expired waiting for main reply", "expired"); got != SubAgentStateCancelled {
			t.Errorf("settleDetachedTerminalTask = %q, want cancelled", got)
		}
	}

	sub, _, err := a.rehydrateTask(cloneDurableTaskRecord(record))
	if err == nil || !strings.Contains(err.Error(), "settled") {
		t.Fatalf("rehydrateTask error = %v, want settled-while-reactivating conflict", err)
	}
	if sub != nil {
		t.Fatal("conflicting revival must not publish a runtime")
	}
	rec := a.taskRecordByTaskID(record.TaskID)
	if rec.State != string(SubAgentStateCancelled) || rec.Attempt != 3 {
		t.Fatalf("record after lost race = state:%s attempt:%d, want the settled cancelled attempt 3", rec.State, rec.Attempt)
	}
	if rec.LatestSettlement == nil || rec.LatestSettlement.Outcome != string(SubAgentStateCancelled) {
		t.Fatalf("settlement after lost race = %+v, want cancelled", rec.LatestSettlement)
	}
	if live := a.subAgentByTaskID(record.TaskID); live != nil {
		t.Fatal("no runtime should remain registered for the settled attempt")
	}

	// Retrying from the now-terminal record starts a clean next attempt.
	retried, _, err := a.rehydrateTask(a.taskRecordByTaskID(record.TaskID))
	if err != nil {
		t.Fatalf("retry rehydrateTask: %v", err)
	}
	if retried == nil {
		t.Fatal("expected revived runtime on retry")
	}
	if rec := a.taskRecordByTaskID(record.TaskID); rec.Attempt != 4 || rec.LatestSettlement != nil {
		t.Fatalf("retried record = attempt:%d settlement:%v, want fresh attempt 4", rec.Attempt, rec.LatestSettlement)
	}
}

func TestSubAgentWakeReevaluatesInputAfterRunningTransition(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-wake-transition")
	sub.setState(SubAgentStateWaitingMain, "waiting")
	sub.startRunLoop()
	t.Cleanup(sub.cancel)

	// Give runLoop time to enter select with its state-gated input channel
	// disabled, reproducing the rehydrate ordering that previously lost wakeups.
	time.Sleep(20 * time.Millisecond)
	sub.setState(SubAgentStateRunning, "resumed")
	if !sub.InjectUserMessage("resume this task") {
		t.Fatal("InjectUserMessage() rejected resumed input")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range sub.ctxMgr.Snapshot() {
			if msg.Role == message.RoleUser && strings.Contains(msg.Content, "resume this task") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("resumed input remained queued after wake")
}

func TestSubAgentStartupWatchdogRetriesWakeThenReportsError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-startup-watchdog")
	sub.startupTimeout = 10 * time.Millisecond
	if !sub.InjectUserMessage("queued without a running loop") {
		t.Fatal("InjectUserMessage() rejected queued input")
	}
	sub.armStartupWatchdog()

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventAgentError || evt.SourceID != sub.instanceID {
			t.Fatalf("watchdog event = %#v, want SubAgent agent_error", evt)
		}
		err, ok := evt.Payload.(error)
		if !ok || !strings.Contains(err.Error(), "automatic wake retry") {
			t.Fatalf("watchdog error = %#v, want automatic wake retry failure", evt.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("startup watchdog did not report stalled queued input")
	}
}

func TestOwnerMailboxQueuesDurablyWhenOwnerParksDuringDelivery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	owner := newControllableTestSubAgent(t, a, "adhoc-owner-park-mailbox")
	owner.setState(SubAgentStateWaitingDescendant, "waiting for child")
	a.releaseSubAgentSlot(owner)
	a.syncTaskRecordFromSub(owner, "")

	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	a.subAgentParkBarrierHook = func(sub *SubAgent) {
		if sub == owner {
			close(barrierReached)
			<-releaseBarrier
		}
	}
	parked := make(chan bool, 1)
	go func() { parked <- a.parkSubAgent(owner.instanceID) }()
	<-barrierReached

	msg := SubAgentMailboxMessage{
		MessageID:    "child-risk-1",
		AgentID:      "child-1",
		TaskID:       "adhoc-child-risk",
		OwnerAgentID: owner.instanceID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindRiskAlert,
		Priority:     SubAgentMailboxPriorityInterrupt,
		Summary:      "child failed",
		Payload:      "inspect child failure",
	}
	enqueued := make(chan struct{})
	go func() {
		a.enqueueSubAgentMailbox(msg)
		close(enqueued)
	}()
	close(releaseBarrier)
	if !<-parked {
		t.Fatal("parkSubAgent() = false")
	}
	<-enqueued

	queued := a.ownedSubAgentMailboxes[owner.instanceID]
	if len(queued) != 1 || queued[0].MessageID != msg.MessageID {
		t.Fatalf("durable owner mailbox queue = %#v, want retained risk alert", queued)
	}
}

func TestWaitingMainLifecycleExpiresAfterUserTurns(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// The turn budget only expires a wait once the minimum wall-clock wait has
	// also elapsed; this case exercises the turn clock, so the wall-clock guard
	// is disabled rather than slept through.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: config.DefaultWaitingMainExpiryTurns, minWait: 0, maxWait: time.Hour}
	sub := newControllableTestSubAgent(t, a, "adhoc-6")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)

	for range a.waitingMainExpiry.turns {
		a.explicitUserTurnCount.Add(1)
	}
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("expected expired waiting_main worker to be parked")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCancelled) || !rec.RuntimeParked {
		t.Fatalf("task record = %#v, want parked cancelled task", rec)
	}
	if !strings.Contains(rec.LastSummary, "user turns") && !strings.Contains(rec.ClosedReason, "user turns") {
		t.Fatalf("task record summary/reason = (%q, %q), want the turn clock named", rec.LastSummary, rec.ClosedReason)
	}
}

func TestWaitingMainLifecycleKeepsWorkerBeforeMinimumWait(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// A user holding several quick exchanges with the main agent must not
	// cancel a worker that escalated moments ago.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 2, minWait: time.Hour, maxWait: 24 * time.Hour}
	sub := newControllableTestSubAgent(t, a, "adhoc-min-wait")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)

	a.explicitUserTurnCount.Add(10)
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got == nil {
		t.Fatal("waiting_main worker was cancelled before the minimum wall-clock wait elapsed")
	}
}

func TestWaitingMainLifecycleExpiresOnMaxWaitWithoutUserTurns(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Headless runs and an absent user produce no explicit turns at all, so the
	// unconditional clock is the only thing that can collect the wait.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	sub := newControllableTestSubAgent(t, a, "adhoc-max-wait")
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)

	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(sub.instanceID); got != nil {
		t.Fatal("waiting_main worker outlived the unconditional maximum wait")
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("task record = %#v, want cancelled task", rec)
	}
}

func TestTargetedNotifyRejectsParkedCancelledTask(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-cancelled-notify")
	sub.setState(SubAgentStateCancelled, "stopped by user")
	a.syncTaskRecordFromSub(sub, "stopped by user")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want parked cancelled worker")
	}

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "resume", "follow_up"); err == nil {
		t.Fatal("NotifySubAgent succeeded for cancelled parked task")
	}
}

func TestParkedCancelledTaskAllowsExplicitUserResume(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	sub := newControllableTestSubAgent(t, a, "adhoc-cancelled-user")
	sub.agentDefName = "restorer"
	// The explicit user resume revives the parked record through rehydration;
	// carry the boundary a real task would have had.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.setState(SubAgentStateCancelled, "stopped by user")
	a.syncTaskRecordFromSub(sub, "stopped by user")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want parked cancelled worker")
	}
	a.SwitchFocus(sub.instanceID)
	a.SendUserMessage("resume explicitly")

	restored := a.subAgentByTaskID(sub.taskID)
	if restored == nil || restored.State() != SubAgentStateRunning {
		t.Fatalf("restored worker = %#v, want running", restored)
	}
}

func TestDescendantMailboxRoutesThroughRehydratedOwnerAlias(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	owner := newControllableTestSubAgent(t, a, "adhoc-owner-alias")
	// The owner parks and is rehydrated from its durable record later in this
	// test; give the record the write boundary a rehydrated runtime requires.
	owner.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	owner.setState(SubAgentStateWaitingDescendant, "waiting")
	a.syncTaskRecordFromSub(owner, "")
	oldOwnerID := owner.instanceID
	if !a.parkSubAgent(oldOwnerID) {
		t.Fatal("parkSubAgent() = false, want parked owner")
	}
	rec := a.taskRecordByTaskID(owner.taskID)
	rehydrated, _, err := a.rehydrateTask(rec)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	rehydrated.setState(SubAgentStateRunning, "resumed")

	msg := SubAgentMailboxMessage{
		MessageID:    "child-alias-1",
		AgentID:      "child-1",
		TaskID:       "adhoc-child-alias",
		OwnerAgentID: oldOwnerID,
		OwnerTaskID:  owner.taskID,
		Kind:         SubAgentMailboxKindProgress,
		Summary:      "still working",
	}
	if !a.routeOwnedSubAgentMailbox(msg) {
		t.Fatal("routeOwnedSubAgentMailbox() = false for historical owner runtime ID")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, persisted := range rehydrated.ctxMgr.Snapshot() {
			if strings.Contains(persisted.Content, "still working") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("rehydrated owner did not append mailbox context")
}

func TestParkSubAgentWaitsForPendingTranscriptPersistence(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-persist")
	sub.setState(SubAgentStateCompleted, "done")
	sub.ctxMgr.Append(message.Message{Role: "assistant", Content: "final persisted reply"})
	sub.persistMessageAsync(message.Message{Role: "assistant", Content: "final persisted reply"}, "test reply", nil)
	a.syncTaskRecordFromSub(sub, "")

	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	msgs, err := loadTaskHistoryMessages(a.recoveryManager(), rec, nil)
	if err != nil {
		t.Fatalf("loadTaskHistoryMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "final persisted reply" {
		t.Fatalf("persisted messages = %#v, want final reply", msgs)
	}
}

func TestParkSubAgentPersistenceFailureKeepsRuntimeLive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-failure")
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")

	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(blockedRoot): %v", err)
	}
	a.sessionDir = blockedRoot
	if parked := a.parkSubAgent(sub.instanceID); parked {
		t.Fatal("parkSubAgent() = true, want false after persistence failure")
	}
	if got := a.subAgentByID(sub.instanceID); got != sub {
		t.Fatalf("live SubAgent = %#v, want original runtime", got)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.RuntimeParked {
		t.Fatalf("task record = %#v, want live non-parked record", rec)
	}
}

func TestParkSubAgentRecoversDegradedTranscriptWithCheckpoint(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-recover")
	sub.setState(SubAgentStateCompleted, "done")
	sub.ctxMgr.Append(message.Message{Role: "assistant", Content: "checkpointed reply"})
	sub.notePersistenceFailure(fmt.Errorf("temporary disk error"))

	if got := sub.PersistenceHealth().State; got != PersistenceDegraded {
		t.Fatalf("persistence state = %q, want degraded", got)
	}
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false after checkpoint recovery")
	}
	client, _ := sub.llmSnapshot()
	if client == nil {
		t.Fatal("parked SubAgent lost model metadata client")
	}
	if _, err := client.CompleteStream(context.Background(), nil, nil, nil); err == nil || err.Error() != "llm client is closed" {
		t.Fatalf("parked SubAgent client error = %v, want closed", err)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.Persistence.State != PersistenceHealthy {
		t.Fatalf("task persistence = %#v, want healthy", rec)
	}
	msgs, err := loadTaskHistoryMessages(a.recoveryManager(), rec, nil)
	if err != nil {
		t.Fatalf("loadTaskHistoryMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "checkpointed reply" {
		t.Fatalf("checkpointed messages = %#v", msgs)
	}
}

func TestParkBarrierConcurrentFocusedInputPreventsParkingAndPreservesMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	sub := newControllableTestSubAgent(t, a, "adhoc-park-input-race")
	// The message racing the park barrier is delivered through a rehydrated
	// runtime, which requires the record to carry a write boundary.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.setState(SubAgentStateIdle, "idle")
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)

	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	a.subAgentParkBarrierHook = func(*SubAgent) {
		close(barrierReached)
		<-releaseBarrier
	}
	parked := make(chan bool, 1)
	go func() { parked <- a.parkSubAgent(sub.instanceID) }()
	<-barrierReached

	inputDone := make(chan struct{})
	go func() {
		a.SendUserMessage("preserve during park")
		close(inputDone)
	}()
	close(releaseBarrier)
	wasParked := <-parked
	<-inputDone

	rec := a.taskRecordByTaskID(sub.taskID)
	if wasParked && (rec == nil || rec.LatestInstanceID == sub.instanceID) {
		t.Fatalf("task record = %#v, want input delivered through a rehydrated runtime", rec)
	}
	live := a.subAgentByTaskID(sub.taskID)
	if live == nil {
		t.Fatal("focused input was lost without a live runtime")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, msg := range live.ctxMgr.Snapshot() {
			if msg.Role == message.RoleUser && strings.Contains(msg.Content, "preserve during park") {
				return
			}
		}
		if len(live.inputCh)+len(live.inputOverflow) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("concurrent focused input was neither queued nor appended")
}

func TestParkReactivatesWaitingSubAgentWhenInputArrivesBeforeFinalCheck(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-park-waiting-input")
	sub.setState(SubAgentStateWaitingMain, "waiting")
	a.releaseSubAgentSlot(sub)
	a.syncTaskRecordFromSub(sub, "")

	barrierReached := make(chan struct{})
	releaseBarrier := make(chan struct{})
	a.subAgentParkBarrierHook = func(*SubAgent) {
		close(barrierReached)
		<-releaseBarrier
	}
	parked := make(chan bool, 1)
	go func() { parked <- a.parkSubAgent(sub.instanceID) }()
	<-barrierReached
	if !sub.InjectUserMessage("arrived before park") {
		t.Fatal("InjectUserMessage() = false")
	}
	close(releaseBarrier)

	if <-parked {
		t.Fatal("parkSubAgent() = true with queued input")
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running", sub.State())
	}
	if !sub.semHeld {
		t.Fatal("reactivated SubAgent did not reacquire a slot")
	}
}

func TestTerminalSubAgentsRemainAvailableAfterLifecycleSweep(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	failed := newControllableTestSubAgent(t, a, "adhoc-failed")
	failed.instanceID = "worker-failed"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[failed.instanceID] = failed
	a.subs.mu.Unlock()
	failed.setState(SubAgentStateFailed, "request failed")
	a.noteSubAgentStateTransition(failed, SubAgentStateFailed)
	cancelled := newControllableTestSubAgent(t, a, "adhoc-cancelled")
	cancelled.instanceID = "worker-cancelled"
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[cancelled.instanceID] = cancelled
	a.subs.mu.Unlock()
	cancelled.setState(SubAgentStateCancelled, "cancelled by user")
	a.noteSubAgentStateTransition(cancelled, SubAgentStateCancelled)

	a.explicitUserTurnCount.Add(config.DefaultWaitingMainExpiryTurns + 1)
	a.sweepSubAgentLifecycle()

	if got := a.subAgentByID(failed.instanceID); got != failed {
		t.Fatal("failed SubAgent was removed by lifecycle sweep")
	}
	if got := a.subAgentByID(cancelled.instanceID); got != cancelled {
		t.Fatal("cancelled SubAgent was removed by lifecycle sweep")
	}
}

func TestRehydrateSnapshotPersistFailureDoesNotLeakSlot(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	installSnapshotBlockingRecoveryManager(t, a)
	record := &DurableTaskRecord{
		TaskID:           "adhoc-rehydrate-snap",
		AgentDefName:     "worker",
		TaskDesc:         "resume after snapshot failure",
		State:            string(SubAgentStateIdle),
		RuntimeParked:    true,
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "worker-old",
		// This test pins slot governance around a refused snapshot persist,
		// so the rehydratable record carries the boundary real tasks ship with.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: cloneDurableTaskRecord(record)})

	sub, _, err := a.rehydrateTask(cloneDurableTaskRecord(record))
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if sub == nil {
		t.Fatal("rehydrateTask returned no SubAgent")
	}
	snap := a.runtimeGovernorSnapshot()
	if snap.RuntimeInUse != 1 || snap.RuntimeHolders != 1 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after rehydrate = in_use:%d holders:%d drift:%d, want 1/1/0", snap.RuntimeInUse, snap.RuntimeHolders, snap.RuntimeSlotDrift)
	}
	if rec := a.taskRecordByTaskID(record.TaskID); rec == nil || rec.LatestInstanceID != sub.instanceID {
		t.Fatalf("task record after rehydrate = %#v, want the new live instance", rec)
	}

	a.closeSubAgent(sub.instanceID)
	if snap := a.runtimeGovernorSnapshot(); snap.RuntimeInUse != 0 || snap.RuntimeHolders != 0 || snap.RuntimeSlotDrift != 0 {
		t.Fatalf("governor after close = %+v, want runtime 0/0/0", snap)
	}
}

// TestWaitingMainLifecycleSweepReclaimsParkedWorkerNotifiesOwnerAndExpiresRequest
// is the end-to-end regression for the "expiry depends on user speech" defect:
// a delegated worker parks waiting for the main agent, wall clock advances past
// maxWait with no user message, and the independent lifecycle-sweep event must
// reclaim the task, deliver a risk_alert mailbox to the owner, and move the
// durable escalation request to expired.
func TestWaitingMainLifecycleSweepReclaimsParkedWorkerNotifiesOwnerAndExpiresRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-expiry-e2e")
	sub.agentDefName = "worker"

	// A delegated worker asks its owner (the main agent) for a decision through
	// the production escalate path, which parks the worker in waiting_main and
	// writes a pending durable agent request.
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: config.DefaultWaitingMainExpiryTurns, minWait: 5 * time.Minute, maxWait: time.Hour}
	a.handleEscalate(Event{
		Type:     EventEscalate,
		SourceID: sub.instanceID,
		Payload:  tools.AgentRequestPayload{Reason: "which API should I use"},
	})

	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateWaitingMain || !rec.RuntimeParked {
		t.Fatalf("after escalate task record = %#v, want parked waiting_main worker", rec)
	}
	if live := a.subAgentByTaskID(sub.taskID); live != nil {
		t.Fatal("worker must be parked (no live runtime) while waiting for main")
	}
	request := pendingAgentRequestForSource(t, a, sub.taskID)
	if request == nil || request.State != "pending" {
		t.Fatalf("escalation request = %#v, want pending", request)
	}

	// Wall clock advances past maxWait while the user stays silent, then the
	// periodic trigger fires its sweep event. No EventUserMessage is involved.
	a.mailboxDeliveryPaused.Store(true)
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})

	rec = a.taskRecordByTaskID(sub.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateCancelled || !rec.RuntimeParked {
		t.Fatalf("after expiry task record = %#v, want parked cancelled worker", rec)
	}
	request = pendingAgentRequestForSource(t, a, sub.taskID)
	if request == nil || request.State != "expired" {
		t.Fatalf("escalation request after expiry = %#v, want expired", request)
	}
	loaded, err := loadAgentRequests(a.sessionDir)
	if err != nil {
		t.Fatalf("loadAgentRequests: %v", err)
	}
	if got := loaded[request.CorrelationID]; got == nil || got.State != "expired" {
		t.Fatalf("persisted request = %#v, want expired on disk too", got)
	}
	if !sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("owner did not receive the expiry AgentNotifyEvent")
	}

	// Drain the queued mailbox events (the escalate decision request and the
	// new expiry risk alert) so the owner-facing mailbox lands in the inbox.
	dispatchQueuedEvents(t, a)
	risk := findRiskAlertForTask(a, sub.taskID)
	if risk == nil {
		t.Fatalf("expected a risk_alert mailbox for task %s in the owner inbox, urgent=%v normal=%v spool=%v/%v",
			sub.taskID, a.subAgentInbox.urgent, a.subAgentInbox.normal, a.subAgentInbox.spoolUrgent, a.subAgentInbox.spoolNormal)
	}
	if risk.OwnerAgentID != "" || risk.OwnerTaskID != "" {
		t.Fatalf("expiry mailbox owner = (%q,%q), want main-owned empty owner", risk.OwnerAgentID, risk.OwnerTaskID)
	}
	if !strings.Contains(risk.Summary, "expired") {
		t.Fatalf("expiry mailbox summary = %q, want the expiry reason", risk.Summary)
	}
}

// TestWaitingMainLifecycleSweepNotifiesAndExpiresForLiveWaitingMainWorker covers
// the same notification contract for the live-worker branch of the sweep: a
// worker still registered in waiting_main (park refused or never attempted)
// must alert its owner and expire its pending request when collected.
func TestWaitingMainLifecycleSweepNotifiesAndExpiresForLiveWaitingMainWorker(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-live-expiry")
	sub.agentDefName = "worker"
	if _, err := a.createAgentRequest(sub, tools.AgentRequestPayload{Reason: "need a decision"}); err != nil {
		t.Fatalf("createAgentRequest: %v", err)
	}
	a.handleSubAgentStateChangedEvent(Event{
		Type:     EventSubAgentStateChanged,
		SourceID: sub.instanceID,
		Payload:  &SubAgentStateChangedPayload{State: SubAgentStateWaitingMain, Summary: "need a decision"},
	})
	if live := a.subAgentByTaskID(sub.taskID); live == nil {
		t.Fatal("worker must stay live for the live-waiting_main sweep branch")
	}

	a.mailboxDeliveryPaused.Store(true)
	a.waitingMainExpiry = waitingMainExpiryPolicy{turns: 1 << 30, minWait: time.Hour, maxWait: time.Nanosecond}
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})

	if live := a.subAgentByTaskID(sub.taskID); live != nil {
		t.Fatal("live waiting_main worker survived the lifecycle sweep")
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || SubAgentState(rec.State) != SubAgentStateCancelled || !rec.RuntimeParked {
		t.Fatalf("task record after live expiry = %#v, want parked cancelled worker", rec)
	}
	request := pendingAgentRequestForSource(t, a, sub.taskID)
	if request == nil || request.State != "expired" {
		t.Fatalf("escalation request after live expiry = %#v, want expired", request)
	}
	if !sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("owner did not receive the expiry AgentNotifyEvent")
	}
	dispatchQueuedEvents(t, a)
	risk := findRiskAlertForTask(a, sub.taskID)
	if risk == nil {
		t.Fatalf("expected a risk_alert mailbox for task %s in the owner inbox", sub.taskID)
	}
	if risk.TaskID != request.SourceTaskID {
		t.Fatalf("expiry mailbox task = %q, want %q", risk.TaskID, request.SourceTaskID)
	}
}

// TestWaitingMainLifecycleSweepCandidatesPinsLiveAndParkedWaits pins the gate
// the periodic trigger relies on: only live or parked waiting_main state counts
// as a candidate, so the trigger stays silent for sessions that never delegate.
func TestWaitingMainLifecycleSweepCandidatesPinsLiveAndParkedWaits(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	if a.waitingMainExpiryCandidatesAmong(a.subs.snapshotSubAgents()) {
		t.Fatal("empty session must not produce expiry candidates")
	}
	live := newControllableTestSubAgent(t, a, "adhoc-candidate-live")
	live.setState(SubAgentStateWaitingMain, "waiting")
	if !a.waitingMainExpiryCandidatesAmong(a.subs.snapshotSubAgents()) {
		t.Fatal("live waiting_main worker not detected as candidate")
	}
	live.setState(SubAgentStateRunning, "")
	rec := &DurableTaskRecord{
		TaskID:        "adhoc-candidate-parked",
		AgentDefName:  "worker",
		State:         string(SubAgentStateWaitingMain),
		RuntimeParked: true,
		Attempt:       1,
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[rec.TaskID] = rec
	a.subs.mu.Unlock()
	if !a.waitingMainExpiryCandidatesAmong(a.subs.snapshotSubAgents()) {
		t.Fatal("parked waiting_main record not detected as candidate")
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[rec.TaskID].State = string(SubAgentStateCancelled)
	a.subs.mu.Unlock()
	if a.waitingMainExpiryCandidatesAmong(a.subs.snapshotSubAgents()) {
		t.Fatal("terminal parked record must not remain an expiry candidate")
	}
}

// A worker that failed still holds the transcript that produced the failure, so
// telling it what went wrong resumes that run instead of making a fresh worker
// rediscover the whole context. This is the path a wrap-up rejection needs: the
// deliverable is already in that worker's history.
func TestSendMessageToFailedTaskRehydratesWorkerForAnotherAttempt(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"fixer": {
			Name:   "fixer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	sub := newControllableTestSubAgent(t, a, "adhoc-failed-resume")
	sub.agentDefName = "fixer"
	sub.taskDesc = "Fix the parser"
	sub.writeScope = tools.WriteScope{Files: []string{"internal/parser/parse.go"}}
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Fix the parser"})
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Fix the parser"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	oldInstanceID := sub.instanceID
	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: context.DeadlineExceeded})

	record := a.taskRecordByTaskID("adhoc-failed-resume")
	if record == nil || SubAgentState(record.State) != SubAgentStateFailed {
		t.Fatalf("record state = %#v, want a failed task", record)
	}
	priorAttempt := record.Attempt

	handle, err := a.NotifySubAgent(context.Background(), "adhoc-failed-resume", "the wrap-up call was malformed; re-send it", "correction")
	if err != nil {
		t.Fatalf("NotifySubAgent on a failed task: %v", err)
	}
	if !handle.Rehydrated {
		t.Fatal("failed worker should rehydrate for another attempt")
	}
	if handle.PreviousAgentID != oldInstanceID {
		t.Fatalf("handle.PreviousAgentID = %q, want %q", handle.PreviousAgentID, oldInstanceID)
	}

	restored := a.subAgentByTaskID("adhoc-failed-resume")
	if restored == nil {
		t.Fatal("expected a live worker after rehydrating the failed task")
	}
	if restored.State() == SubAgentStateFailed {
		t.Fatal("restored.State() = failed; a resumed worker must not stay terminal")
	}
	// The transcript that produced the failure is what makes resuming cheaper
	// than re-delegating, so it has to come back with the worker.
	restoredMsgs := restored.ctxMgr.Snapshot()
	if len(restoredMsgs) == 0 || restoredMsgs[0].Content != "Fix the parser" {
		t.Fatalf("restored transcript = %#v, want the original task history", restoredMsgs)
	}
	record = a.taskRecordByTaskID("adhoc-failed-resume")
	if record.Attempt != priorAttempt+1 {
		t.Fatalf("record.Attempt = %d, want %d (a resumed terminal task starts a new attempt)", record.Attempt, priorAttempt+1)
	}
	if record.LatestSettlement != nil {
		t.Fatalf("record.LatestSettlement = %#v, want the previous attempt's settlement cleared", record.LatestSettlement)
	}
	if record.ExpectedWriteScope.Files[0] != "internal/parser/parse.go" {
		t.Fatalf("rehydrated write scope = %#v, want the original file scope", record.ExpectedWriteScope)
	}
}

// Cancellation is a decision someone made. A follow-up message must not quietly
// reverse it, and the rejection has to point at the way forward.
func TestSendMessageToCancelledTaskIsRejectedWithDelegateGuidance(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-cancelled": {
			TaskID:            "adhoc-cancelled",
			AgentDefName:      "fixer",
			State:             string(SubAgentStateCancelled),
			ResumePolicy:      taskResumePolicyExplicitOnly,
			RuntimeParked:     true,
			SettlementDurable: true,
		},
	})

	_, err := a.NotifySubAgent(context.Background(), "adhoc-cancelled", "please continue", "follow_up")
	if err == nil {
		t.Fatal("NotifySubAgent on a cancelled task = nil error, want a rejection")
	}
	if !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), "delegate the work again") {
		t.Fatalf("error = %q, want it to name the cancellation and point at re-delegation", err.Error())
	}
}
