package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

// newTaskRegistryTestAgent exercises coordination predicates without opening a
// durable session. Persistence ordering is covered by the full-runtime test below.
func newTaskRegistryTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	root := t.TempDir()
	return &MainAgent{subs: newSubAgentRegistry(), contentRoot: root, cachedWorkDir: root}
}

// findDuplicateTask is a test-only locking wrapper; production callers hold
// subs.mu and use findDuplicateTaskLocked directly.
func (a *MainAgent) findDuplicateTask(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string) (*DurableTaskRecord, taskDuplicateDisposition) {
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	return a.findDuplicateTaskLocked(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey)
}

// An identical explicit semantic_task_key is the only confirmed duplicate: the
// caller deliberately asserted the identity, so the runtime keeps rejecting the
// re-delegation with already_exists.
func TestFindDuplicateTaskExplicitSemanticKeyIsHardDuplicate(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:          "adhoc-1",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			SemanticTaskKey: "audit-report",
			State:           string(SubAgentStateRunning),
		},
	})

	existing, disposition := a.findDuplicateTask("owner", "parent", "worker", "", "audit-report")
	if existing == nil || existing.TaskID != "adhoc-1" {
		t.Fatalf("findDuplicateTask() = (%#v, %v), want the existing task", existing, disposition)
	}
	if disposition != taskDuplicateExplicitKey {
		t.Fatalf("disposition = %v, want taskDuplicateExplicitKey for an explicit key match", disposition)
	}
}

// A confirmed duplicate must never hide behind a merely probable match that
// also exists in the registry: an explicit semantic_task_key collision is the
// only hard rejection and must win regardless of which record the scan visits
// first (taskRecords is a map, so iteration order is arbitrary). The finder is
// therefore exercised repeatedly.
func TestFindDuplicateTaskExplicitKeyOutranksProbableMatch(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:       "adhoc-1",
			OwnerAgentID: "owner",
			OwnerTaskID:  "parent",
			AgentDefName: "worker",
			PlanTaskRef:  "shared-plan",
			State:        string(SubAgentStateRunning),
		},
		"adhoc-2": {
			TaskID:          "adhoc-2",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			SemanticTaskKey: "audit-report",
			State:           string(SubAgentStateRunning),
		},
	})

	// adhoc-1 shares only the plan reference (it declared no key of its own);
	// adhoc-2 is a confirmed duplicate of the new deliverable. Every call must
	// resolve to the explicit-key record, no matter the iteration order.
	for i := range 20 {
		existing, disposition := a.findDuplicateTask(
			"owner", "parent", "worker", "shared-plan", "audit-report",
		)
		if existing == nil || existing.TaskID != "adhoc-2" {
			t.Fatalf("iteration %d: findDuplicateTask() = (%#v, %v), want the explicit-key task adhoc-2", i, existing, disposition)
		}
		if disposition != taskDuplicateExplicitKey {
			t.Fatalf("iteration %d: disposition = %v, want taskDuplicateExplicitKey", i, disposition)
		}
	}
}

// Reusing a plan_task_ref without sharing an explicit semantic_task_key is
// heuristic (the same plan item can cover several distinct delegates), so it is
// only a probable duplicate. The earlier task may carry its own key as long as
// the new call does not assert a different one.
func TestFindDuplicateTaskPlanTaskRefMatchWithoutKeyIsProbable(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:          "adhoc-1",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			PlanTaskRef:     "plan-item-7",
			SemanticTaskKey: "first-deliverable",
			State:           string(SubAgentStateRunning),
		},
	})

	existing, disposition := a.findDuplicateTask("owner", "parent", "worker", "plan-item-7", "")
	if existing == nil || existing.TaskID != "adhoc-1" {
		t.Fatalf("findDuplicateTask() = (%#v, %v), want the earlier task", existing, disposition)
	}
	if disposition != taskDuplicateProbable {
		t.Fatalf("disposition = %v, want taskDuplicateProbable for a plan_task_ref-only match", disposition)
	}
}

// Two different explicit keys are two different deliverables by the caller's
// own assertion, so even a shared plan_task_ref must not hint that the new
// delegation repeats the earlier task.
func TestFindDuplicateTaskDifferentExplicitKeysSuppressPlanTaskRefHint(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:          "adhoc-1",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			PlanTaskRef:     "plan-item-7",
			SemanticTaskKey: "first-deliverable",
			State:           string(SubAgentStateRunning),
		},
	})

	existing, disposition := a.findDuplicateTask("owner", "parent", "worker", "plan-item-7", "second-deliverable")
	if existing != nil || disposition != taskDuplicateNone {
		t.Fatalf("findDuplicateTask() = (%#v, %v), want no duplicate for distinct explicit keys", existing, disposition)
	}
}

// A pending admission is screened like a running record, but a caller may only
// wait for and reuse its result when the match is an identical explicit
// semantic_task_key. These two tests pin the split CreateSubAgent relies on:
// an explicit-key match on a pending admission stays hard (the caller may wait
// for the first task's handle), while a plan_task_ref-only match is probable
// (the caller must start its own task, never block on another admission's
// handle).
func TestFindPendingDuplicateClassifiesExplicitKeyAsHard(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.subs.mu.Lock()
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:          "adhoc-9",
		ownerAgentID:    "",
		ownerTaskID:     "",
		agentType:       "worker",
		semanticTaskKey: "audit-report",
	})
	existing, disposition, pending := a.findPendingDuplicateTaskLocked("", "", "worker", "", "audit-report")
	a.subs.mu.Unlock()
	if existing == nil || pending == nil || pending.taskID != "adhoc-9" {
		t.Fatalf("findPendingDuplicateTaskLocked() = (%#v, %v, %#v), want the pending admission", existing, disposition, pending)
	}
	if disposition != taskDuplicateExplicitKey {
		t.Fatalf("disposition = %v, want taskDuplicateExplicitKey", disposition)
	}
}

func TestFindPendingDuplicateClassifiesPlanTaskRefAsProbable(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.subs.mu.Lock()
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:       "adhoc-9",
		ownerAgentID: "",
		ownerTaskID:  "",
		agentType:    "worker",
		planTaskRef:  "plan-item-7",
	})
	existing, disposition, pending := a.findPendingDuplicateTaskLocked("", "", "worker", "plan-item-7", "")
	a.subs.mu.Unlock()
	if existing == nil || pending == nil || pending.taskID != "adhoc-9" {
		t.Fatalf("findPendingDuplicateTaskLocked() = (%#v, %v, %#v), want the pending admission", existing, disposition, pending)
	}
	if disposition != taskDuplicateProbable {
		t.Fatalf("disposition = %v, want taskDuplicateProbable", disposition)
	}
}

// A pending explicit-key admission outranks a pending probable match for the
// same reason the registry scan prefers confirmed duplicates: only the
// identical explicit semantic_task_key may be waited on, and a probable match
// must not hide it regardless of admission iteration order.
func TestFindPendingDuplicateExplicitKeyOutranksProbableMatch(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:       "adhoc-9",
		ownerAgentID: "owner",
		ownerTaskID:  "parent",
		agentType:    "worker",
		planTaskRef:  "shared-plan",
	})
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:          "adhoc-10",
		ownerAgentID:    "owner",
		ownerTaskID:     "parent",
		agentType:       "worker",
		semanticTaskKey: "audit-report",
	})

	for i := range 20 {
		existing, disposition, pending := a.findPendingDuplicateTaskLocked(
			"owner", "parent", "worker", "shared-plan", "audit-report",
		)
		if existing == nil || pending == nil || pending.taskID != "adhoc-10" {
			t.Fatalf("iteration %d: findPendingDuplicateTaskLocked() = (%#v, %v, %#v), want the explicit-key admission adhoc-10", i, existing, disposition, pending)
		}
		if disposition != taskDuplicateExplicitKey {
			t.Fatalf("iteration %d: disposition = %v, want taskDuplicateExplicitKey", i, disposition)
		}
	}
}

// duplicateHintedTaskHandle must keep the started status and its own task ids
// while attaching the duplicate_detected hint that points the model at the
// probable-duplicate task.
func TestDuplicateHintedTaskHandleAnnotatesStartedHandle(t *testing.T) {
	started := tools.TaskHandle{Status: "started", TaskID: "adhoc-2", AgentID: "worker-2", Message: "running in background"}
	existing := &DurableTaskRecord{TaskID: "adhoc-1", LatestInstanceID: "worker-1"}
	got := duplicateHintedTaskHandle(started, existing, nil)

	if got.Status != "started" || got.TaskID != "adhoc-2" || got.AgentID != "worker-2" {
		t.Fatalf("hinted handle = %#v, want the started task identity preserved", got)
	}
	if !got.DuplicateDetected {
		t.Fatal("hinted handle must report duplicate_detected")
	}
	if got.SuggestedTaskID != "adhoc-1" || got.SuggestedAgentID != "worker-1" {
		t.Fatalf("hinted handle suggestions = (%q, %q), want (adhoc-1, worker-1)", got.SuggestedTaskID, got.SuggestedAgentID)
	}
	if got.SuggestedAction != "notify_existing_if_same_deliverable" {
		t.Fatalf("hinted handle suggested_action = %q, want notify_existing_if_same_deliverable", got.SuggestedAction)
	}
	if got.Message == "" || got.Message == started.Message {
		t.Fatalf("hinted handle message = %q, want the duplicate explanation replacing %q", got.Message, started.Message)
	}
}

// When the probable duplicate is still a pending admission (no published task
// yet), the hint references the pending task id and leaves the agent id empty.
func TestDuplicateHintedTaskHandleReferencesPendingAdmission(t *testing.T) {
	started := tools.TaskHandle{Status: "started", TaskID: "adhoc-2", AgentID: "worker-2", Message: "running in background"}
	got := duplicateHintedTaskHandle(started, nil, &subAgentAdmission{taskID: "adhoc-1"})

	if !got.DuplicateDetected {
		t.Fatal("hinted handle must report duplicate_detected")
	}
	if got.SuggestedTaskID != "adhoc-1" || got.SuggestedAgentID != "" {
		t.Fatalf("hinted handle suggestions = (%q, %q), want (adhoc-1, )", got.SuggestedTaskID, got.SuggestedAgentID)
	}
	if got.TaskID != "adhoc-2" {
		t.Fatalf("hinted handle task_id = %q, want the caller's own new task adhoc-2", got.TaskID)
	}
}

// duplicateTaskHandle builds the hard already_exists handle for a confirmed
// duplicate (identical explicit semantic_task_key): it must reference the
// existing task and suggest continuing it with notify.
func TestDuplicateTaskHandleBuildsExplicitKeyRejection(t *testing.T) {
	existing := &DurableTaskRecord{
		TaskID:           "adhoc-1",
		LatestInstanceID: "worker-1",
		SemanticTaskKey:  "audit-report",
	}
	got := duplicateTaskHandle(existing)

	if got.Status != "already_exists" {
		t.Fatalf("duplicate handle = %#v, want already_exists", got)
	}
	if got.TaskID != "adhoc-1" || !got.DuplicateDetected || got.SuggestedAction != "notify_existing" {
		t.Fatalf("duplicate handle = %#v, want existing task identity and notify_existing suggestion", got)
	}
	if got.Message == "" {
		t.Fatal("duplicate handle must carry a model-facing message")
	}
}

func TestMergeDurableTaskRecordsPreservesCoordinationIdentity(t *testing.T) {
	base := map[string]*DurableTaskRecord{
		"adhoc-identity": {
			TaskID:          "adhoc-identity",
			PlanTaskRef:     "plan-item-8",
			SemanticTaskKey: "restore-identity",
			State:           string(SubAgentStateCompleted),
		},
	}
	extra := map[string]*DurableTaskRecord{
		"adhoc-identity": {
			TaskID: "adhoc-identity",
			State:  string(SubAgentStateIdle),
		},
	}

	got := mergeDurableTaskRecords(base, extra)["adhoc-identity"]
	if got.PlanTaskRef != "plan-item-8" || got.SemanticTaskKey != "restore-identity" {
		t.Fatalf("merged task identity = (%q, %q), want durable values", got.PlanTaskRef, got.SemanticTaskKey)
	}
}

func TestFindDuplicateTaskAllowsExplicitOnlyTerminalRetry(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	for _, state := range []SubAgentState{SubAgentStateFailed, SubAgentStateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			a.setTaskRecords(map[string]*DurableTaskRecord{
				"old-task": {
					TaskID:          "old-task",
					OwnerAgentID:    "owner",
					OwnerTaskID:     "parent",
					AgentDefName:    "worker",
					PlanTaskRef:     "plan-item",
					SemanticTaskKey: "semantic-key",
					State:           string(state),
					ResumePolicy:    taskResumePolicyExplicitOnly,
				},
			})

			existing, disposition := a.findDuplicateTask("owner", "parent", "worker", "plan-item", "semantic-key")
			if existing != nil || disposition != taskDuplicateNone {
				t.Fatalf("findDuplicateTask() = (%#v, %v), want retry allowed", existing, disposition)
			}
		})
	}
}

// A completed task under the notify resume policy is rehydratable, so it keeps
// participating in duplicate detection: an identical explicit semantic_task_key
// stays a confirmed duplicate and the runtime rejects the call, suggesting the
// completed task be continued with Notify.
func TestFindDuplicateTaskKeepsNotifyRehydratableCompletedTaskAsExplicitDuplicate(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"completed-task": {
			TaskID:          "completed-task",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			SemanticTaskKey: "semantic-key",
			State:           string(SubAgentStateCompleted),
			ResumePolicy:    taskResumePolicyNotify,
		},
	})

	existing, disposition := a.findDuplicateTask("owner", "parent", "worker", "", "semantic-key")
	if existing == nil || existing.TaskID != "completed-task" {
		t.Fatalf("findDuplicateTask() = (%#v, %v), want completed duplicate", existing, disposition)
	}
	if disposition != taskDuplicateExplicitKey {
		t.Fatalf("disposition = %v, want taskDuplicateExplicitKey for an explicit key match against a rehydratable completed task", disposition)
	}
}

// A plan_task_ref match without a shared explicit key against a rehydratable
// completed task is only a probable duplicate: the runtime must not redirect
// the new delegate to the completed record, it creates a fresh task with a
// duplicate hint instead.
func TestFindDuplicateTaskPlanTaskRefMatchOnCompletedIsProbable(t *testing.T) {
	a := newTaskRegistryTestAgent(t)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"completed-task": {
			TaskID:          "completed-task",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			PlanTaskRef:     "plan-item-8",
			SemanticTaskKey: "semantic-key",
			State:           string(SubAgentStateCompleted),
			ResumePolicy:    taskResumePolicyNotify,
		},
	})

	existing, disposition := a.findDuplicateTask("owner", "parent", "worker", "plan-item-8", "")
	if existing == nil || existing.TaskID != "completed-task" {
		t.Fatalf("findDuplicateTask() = (%#v, %v), want the completed task", existing, disposition)
	}
	if disposition != taskDuplicateProbable {
		t.Fatalf("disposition = %v, want taskDuplicateProbable against a completed task", disposition)
	}
}

func TestPersistTaskRegistrySerializesSnapshotOrder(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-persist-order": {
			TaskID: "adhoc-persist-order",
			State:  string(SubAgentStateIdle),
		},
	})
	firstSnapshot := make(chan struct{})
	releaseFirstWrite := make(chan struct{})
	var hookCalls atomic.Int32
	a.taskRegistryPersistHook = func() {
		if hookCalls.Add(1) == 1 {
			close(firstSnapshot)
			<-releaseFirstWrite
		}
	}

	var writers sync.WaitGroup
	writers.Go(func() {
		a.persistTaskRegistry()
	})
	<-firstSnapshot
	a.subs.mu.Lock()
	a.subs.taskRecords["adhoc-persist-order"].State = string(SubAgentStateCompleted)
	a.subs.mu.Unlock()
	secondDone := make(chan struct{})
	writers.Go(func() {
		a.persistTaskRegistry()
		close(secondDone)
	})
	select {
	case <-secondDone:
		t.Fatal("newer registry write bypassed the in-flight older snapshot")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirstWrite)
	writers.Wait()

	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := records["adhoc-persist-order"]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("persisted record = %#v, want newest completed state", got)
	}
}
