package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func installGroupTask(a *MainAgent, taskID string, attempt uint64, state SubAgentState) {
	installCollectTask(a, &DurableTaskRecord{TaskID: taskID, Attempt: attempt, State: string(state)}, nil, false)
}

func TestCreateTaskGroupPinsSortedAttemptsAndPersists(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installGroupTask(a, "task-b", 2, SubAgentStateRunning)
	installGroupTask(a, "task-a", 1, SubAgentStateCompleted)
	result, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{
		TaskIDs: []string{"task-b", "task-a", "task-a"}, Label: "phase", SemanticKey: "phase-key",
	})
	if err != nil {
		t.Fatalf("CreateTaskGroup: %v", err)
	}
	if result.GroupID != "group-1" || result.JoinPolicy != "all_settled" || len(result.Members) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Members[0] != (tools.TaskGroupMember{TaskID: "task-a", Attempt: 1}) || result.Members[1] != (tools.TaskGroupMember{TaskID: "task-b", Attempt: 2}) {
		t.Fatalf("members = %#v", result.Members)
	}
	loaded, err := loadTaskGroups(a.sessionDir)
	if err != nil || loaded["group-1"] == nil {
		t.Fatalf("loaded groups = %#v error=%v", loaded, err)
	}
}

func TestCreateTaskGroupSemanticKeyIsIdempotentAndImmutable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installGroupTask(a, "task-a", 1, SubAgentStateRunning)
	request := tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}, Label: "phase", SemanticKey: "phase-key"}
	first, err := a.CreateTaskGroup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.CreateTaskGroup(context.Background(), request)
	if err != nil || second.GroupID != first.GroupID {
		t.Fatalf("idempotent result = %#v error=%v", second, err)
	}
	request.Label = "renamed"
	if _, err := a.CreateTaskGroup(context.Background(), request); err == nil || !strings.Contains(err.Error(), "different immutable content") {
		t.Fatalf("label conflict error = %v", err)
	}
	installGroupTask(a, "task-a", 2, SubAgentStateRunning)
	request.Label = "phase"
	if _, err := a.CreateTaskGroup(context.Background(), request); err == nil || !strings.Contains(err.Error(), "different immutable content") {
		t.Fatalf("member conflict error = %v", err)
	}
}

func TestCreateTaskGroupConcurrentSemanticRetryReturnsOneGroup(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installGroupTask(a, "task-a", 1, SubAgentStateRunning)
	request := tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}, SemanticKey: "phase-key"}
	results := make(chan tools.TaskGroupCreateResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			result, err := a.CreateTaskGroup(context.Background(), request)
			results <- result
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CreateTaskGroup: %v", err)
		}
	}
	var groupID string
	for result := range results {
		if groupID == "" {
			groupID = result.GroupID
		} else if result.GroupID != groupID {
			t.Fatalf("group IDs differ: %q and %q", groupID, result.GroupID)
		}
	}
}

func TestCreateTaskGroupPersistenceFailureDoesNotPublish(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installGroupTask(a, "task-a", 1, SubAgentStateRunning)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.sessionDir = blocked
	if _, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}}); err == nil {
		t.Fatal("expected persistence error")
	}
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	if len(a.subs.taskGroups) != 0 {
		t.Fatalf("published groups = %#v", a.subs.taskGroups)
	}
}

func TestCollectTaskGroupKeepsOldAttemptAcrossRehydrate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	settlement := testSettlement("task-a", 1, 2)
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateCompleted)}, settlement, true)
	group, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}, SemanticKey: "phase"})
	if err != nil {
		t.Fatal(err)
	}
	installGroupTask(a, "task-a", 2, SubAgentStateRunning)
	result, err := a.CollectTasks(context.Background(), tools.TaskCollectRequest{GroupID: group.GroupID})
	if err != nil {
		t.Fatalf("CollectTasks: %v", err)
	}
	if !result.AllSettled || !result.AllDurable || result.GroupID != group.GroupID || result.Tasks[0].MemberAttempt != 1 || result.Tasks[0].CurrentAttempt != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestTaskGroupRoundTripRestoresSequenceAndCollect(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	settlement := testSettlement("task-a", 1, 2)
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, State: string(SubAgentStateCompleted)}, settlement, true)
	first, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadTaskGroups(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	a.resetTaskGroups(loaded)
	result, err := a.CollectTasks(context.Background(), tools.TaskCollectRequest{GroupID: first.GroupID, Wait: true, Timeout: time.Second})
	if err != nil || !result.AllSettled {
		t.Fatalf("collect result = %#v error=%v", result, err)
	}
	second, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}})
	if err != nil || second.GroupID != "group-2" {
		t.Fatalf("second group = %#v error=%v", second, err)
	}
}

func TestCreateTaskGroupRejectsNonDirectOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installCollectTask(a, &DurableTaskRecord{TaskID: "task-a", Attempt: 1, OwnerTaskID: "parent", State: string(SubAgentStateRunning)}, nil, false)
	if _, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}}); err == nil || !strings.Contains(err.Error(), "not directly owned") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateTaskGroupRejectsWhileSessionTransitionIsPaused(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installGroupTask(a, "task-a", 1, SubAgentStateRunning)
	a.admissionPaused.Store(true)

	if _, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}}); err == nil || !strings.Contains(err.Error(), "session transition") {
		t.Fatalf("CreateTaskGroup error = %v, want transition rejection", err)
	}
	a.admissionPaused.Store(false)
	if _, err := a.CreateTaskGroup(context.Background(), tools.TaskGroupCreateRequest{TaskIDs: []string{"task-a"}}); err != nil {
		t.Fatalf("CreateTaskGroup after transition: %v", err)
	}
}

func TestLoadTaskGroupsRejectsInvalidMembers(t *testing.T) {
	dir := t.TempDir()
	path := taskGroupsPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `[{"version":1,"group_id":"group-1","owner_principal":"session-root","members":[{"task_id":"task-a","attempt":0}],"created_at":"2026-01-01T00:00:00Z"}]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTaskGroups(dir); err == nil {
		t.Fatal("expected invalid member error")
	}
}
