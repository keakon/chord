package agent

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/tools"
)

// Nested delegation inherits the parent's write-scope boundary as a path
// containment rule: a write-capable child's scope must stay within the
// parent's declared paths. A child whose role registers no file-modifying
// tools cannot modify files at all, so its (typically empty) scope is always
// within the parent; whether a task may write files is decided by its role's
// ruleset, never propagated from the parent task.
func TestNestedCreateSubAgentScopeContainedInParent(t *testing.T) {
	newRuntime := func(t *testing.T, workerPermissionSrc string) *MainAgent {
		a := newTestMainAgent(t, t.TempDir())
		configureNestedDelegationTestRuntime(a, 2)
		if workerPermissionSrc != "" {
			a.agentConfigs["worker"].Permission = parsePermissionNode(t, workerPermissionSrc)
		}
		return a
	}
	writeRuntime := func(t *testing.T) *MainAgent { return newRuntime(t, "") }
	noWriteWorker := "write: deny\nedit: deny\ndelete: deny\napply_patch: deny\n"
	t.Run("writing child outside parent prefix", func(t *testing.T) {
		a := writeRuntime(t)
		parent := newControllableTestSubAgent(t, a, "task-parent")
		parent.depth = 1
		parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
		parent.writeScope = tools.WriteScope{PathPrefix: []string{"src"}}
		a.syncTaskRecordFromSub(parent, "")
		ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
		_, err := a.CreateSubAgent(ctx, "Fix lib", "worker", "", "", tools.WriteScope{Files: []string{"lib/sample.go"}})
		if err == nil || !strings.Contains(err.Error(), "must not be broader") {
			t.Fatalf("CreateSubAgent error = %v, want containment rejection", err)
		}
	})
	t.Run("writing child within parent prefix", func(t *testing.T) {
		a := writeRuntime(t)
		parent := newControllableTestSubAgent(t, a, "task-parent")
		parent.depth = 1
		parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
		parent.writeScope = tools.WriteScope{PathPrefix: []string{"internal"}}
		a.syncTaskRecordFromSub(parent, "")
		ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
		handle, err := a.CreateSubAgent(ctx, "Check sample package", "worker", "", "", tools.WriteScope{Files: []string{"internal/agent/main.go"}})
		if err != nil || handle.Status != "started" {
			t.Fatalf("child = %#v, %v", handle, err)
		}
	})
	t.Run("empty scope child of write-capable role under scoped parent", func(t *testing.T) {
		a := writeRuntime(t)
		parent := newControllableTestSubAgent(t, a, "task-parent")
		parent.depth = 1
		parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
		parent.writeScope = tools.WriteScope{PathPrefix: []string{"src"}}
		a.syncTaskRecordFromSub(parent, "")
		ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
		_, err := a.CreateSubAgent(ctx, "Unscoped work", "worker", "", "", tools.WriteScope{})
		if err == nil || !strings.Contains(err.Error(), "must not be broader") {
			t.Fatalf("CreateSubAgent error = %v, want empty write-capable scope rejected", err)
		}
	})
	t.Run("empty scope child of no-file-write-tool role under scoped parent", func(t *testing.T) {
		a := newRuntime(t, noWriteWorker)
		parent := newControllableTestSubAgent(t, a, "task-parent")
		parent.depth = 1
		parent.delegation = config.DelegationConfig{MaxChildren: 2, MaxDepth: 2}
		parent.writeScope = tools.WriteScope{PathPrefix: []string{"src"}}
		a.syncTaskRecordFromSub(parent, "")
		ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)
		handle, err := a.CreateSubAgent(ctx, "Survey parser", "worker", "", "", tools.WriteScope{})
		if err != nil || handle.Status != "started" {
			t.Fatalf("child = %#v, %v; want empty scope accepted for a role without file tools", handle, err)
		}
	})
}

func TestScopeGrantChecksActiveAndPendingWriters(t *testing.T) {
	noWriteWorkerSrc := "write: deny\nedit: deny\ndelete: deny\napply_patch: deny\n"
	for _, tc := range []struct {
		name        string
		state       SubAgentState
		pending     bool
		siblingRole string
		owner       string
		wantReject  bool
	}{
		{name: "live sibling", state: SubAgentStateRunning, wantReject: true},
		{name: "parked sibling", state: SubAgentStateWaitingMain, wantReject: true},
		{name: "pending sibling", pending: true, wantReject: true},
		{name: "completed sibling", state: SubAgentStateCompleted},
		// A sibling whose role registers no file-modifying tools never writes,
		// so a widened scope cannot conflict with it.
		{name: "sibling role without file tools", state: SubAgentStateRunning, siblingRole: "worker"},
		{name: "descendant", state: SubAgentStateRunning, owner: "task-target"},
		{name: "pending descendant", pending: true, owner: "task-target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			if tc.siblingRole != "" {
				a.agentConfigs = map[string]*config.AgentConfig{
					"worker": {
						Name:       "worker",
						Mode:       "subagent",
						Permission: parsePermissionNode(t, noWriteWorkerSrc),
					},
				}
			}
			target := &DurableTaskRecord{
				TaskID: "task-target", State: string(SubAgentStateIdle),
				ExpectedWriteScope: tools.WriteScope{Files: []string{"src/sample.go"}},
			}
			scope := tools.WriteScope{Files: []string{"lib/sample.go"}}
			if tc.owner != "" {
				scope.Files = []string{"src/sample.go"}
			}
			a.setTaskRecords(map[string]*DurableTaskRecord{target.TaskID: target})
			a.subs.mu.Lock()
			if tc.pending {
				a.subs.admissions = map[string]*subAgentAdmission{
					"task-other": {taskID: "task-other", ownerTaskID: tc.owner, agentType: "worker", expectedWriteScope: scope},
				}
			} else {
				a.subs.taskRecords["task-other"] = &DurableTaskRecord{
					TaskID: "task-other", State: string(tc.state), OwnerTaskID: tc.owner,
					AgentDefName:       "worker",
					ExpectedWriteScope: scope, RuntimeParked: tc.state == SubAgentStateWaitingMain,
				}
			}
			a.subs.mu.Unlock()

			err := a.grantSubAgentWriteScope("", "", target.TaskID, tools.WriteScope{Files: []string{"lib/sample.go"}})
			if tc.wantReject {
				if err == nil || !strings.Contains(err.Error(), "overlaps with active task task-other") {
					t.Fatalf("grant error = %v, want competing writer", err)
				}
				if got := a.taskRecordByTaskID(target.TaskID).ExpectedWriteScope.Files; len(got) != 1 {
					t.Fatalf("rejected grant changed scope: %v", got)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestScopeGrantRejectsNoFileWriteToolTarget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:       "worker",
			Mode:       "subagent",
			Permission: parsePermissionNode(t, "write: deny\nedit: deny\ndelete: deny\napply_patch: deny\n"),
		},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-reader": {
			TaskID: "task-reader", AgentDefName: "worker", State: string(SubAgentStateIdle),
			ExpectedWriteScope: tools.WriteScope{},
		},
	})
	err := a.grantSubAgentWriteScope("", "", "task-reader", tools.WriteScope{Files: []string{"src/sample.go"}})
	if err == nil || !strings.Contains(err.Error(), "registers no file-modifying tools") {
		t.Fatalf("grant error = %v, want it refused because the role cannot write files", err)
	}
	if got := a.taskRecordByTaskID("task-reader").ExpectedWriteScope; !got.Empty() {
		t.Fatalf("rejected grant changed scope: %#v", got)
	}
}

func TestScopeGrantRespectsParentAndAncestorScopes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-parent": {
			TaskID: "task-parent", State: string(SubAgentStateRunning),
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"src"}},
		},
		"task-child": {
			TaskID: "task-child", OwnerTaskID: "task-parent", State: string(SubAgentStateIdle),
			ExpectedWriteScope: tools.WriteScope{Files: []string{"src/first.go"}},
		},
	})
	if err := a.grantSubAgentWriteScope("", "task-parent", "task-child", tools.WriteScope{Files: []string{"lib/sample.go"}}); err == nil || !strings.Contains(err.Error(), "broader than its parent") {
		t.Fatalf("out-of-parent grant error = %v", err)
	}
	if err := a.grantSubAgentWriteScope("", "task-parent", "task-child", tools.WriteScope{Files: []string{"src/second.go"}}); err != nil {
		t.Fatalf("ancestor's own scope must not conflict with child: %v", err)
	}
}

func TestScopeGrantPersistenceFailureDoesNotPublishAuthority(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-target")
	sub.writeScope = tools.WriteScope{Files: []string{"src/first.go"}}
	a.syncTaskRecordFromSub(sub, "")
	path := durableTaskRegistryPath(a.sessionDir)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.grantSubAgentWriteScope("", "", sub.taskID, tools.WriteScope{Files: []string{"src/second.go"}}); err == nil || !strings.Contains(err.Error(), "persist widened write scope") {
		t.Fatalf("grant error = %v, want persistence failure", err)
	}
	for _, got := range []tools.WriteScope{sub.currentWriteScope(), a.taskRecordByTaskID(sub.taskID).ExpectedWriteScope} {
		if !slices.Equal(got.Files, []string{"src/first.go"}) {
			t.Fatalf("failed grant published authority: %#v", got)
		}
	}
}

func TestScopeGrantHoldsAdmissionAndPreservesConcurrentState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-target")
	sub.writeScope = tools.WriteScope{Files: []string{"src/first.go"}}
	a.syncTaskRecordFromSub(sub, "")
	a.taskRegistryPersistHook = func() {
		if a.admissionMu.TryLock() {
			a.admissionMu.Unlock()
			t.Error("grant did not hold the admission boundary")
		}
		if sub.lifecycleMu.TryLock() {
			sub.lifecycleMu.Unlock()
			t.Error("parking can snapshot the old scope during the grant")
		}
		if len(sub.currentWriteScope().Files) != 1 {
			t.Error("scope became live before persistence")
		}
		sub.setState(SubAgentStateWaitingMain, "Waiting for a decision")
		a.updateTaskRecordFromSub(sub, "")
	}
	err := a.grantSubAgentWriteScope("", "", sub.taskID, tools.WriteScope{Files: []string{"src/second.go"}})
	a.taskRegistryPersistHook = nil
	if err != nil {
		t.Fatal(err)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec.State != string(SubAgentStateWaitingMain) || rec.LastSummary != "Waiting for a decision" {
		t.Fatalf("grant overwrote a concurrent state update: %#v", rec)
	}
	if len(rec.ExpectedWriteScope.Files) != 2 || len(sub.currentWriteScope().Files) != 2 {
		t.Fatal("grant was not published after persistence")
	}
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("failed to park the granted worker")
	}
	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records[sub.taskID].ExpectedWriteScope.Files) != 2 {
		t.Fatal("parking lost the committed scope")
	}
}

func TestScopeGrantDuringActivationUsesDurableScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	sub := newControllableTestSubAgent(t, a, "task-target")
	sub.writeScope = tools.WriteScope{Files: []string{"src/first.go"}}
	sub.setState(SubAgentStateIdle, "Waiting for input")
	a.syncTaskRecordFromSub(sub, "")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("failed to park worker")
	}
	preparing, release := make(chan struct{}), make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		close(preparing)
		<-release
		return newTestLLMClient()
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, err := a.getOrRehydrateTask(a.taskRecordByTaskID(sub.taskID))
		done <- err
	}()
	select {
	case <-preparing:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("activation did not reach runtime preparation")
	}
	err := a.grantSubAgentWriteScope("", "", sub.taskID, tools.WriteScope{Files: []string{"src/second.go"}})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("activation did not finish")
	}
	live := a.subAgentByTaskID(sub.taskID)
	if live == nil || len(live.currentWriteScope().Files) != 2 {
		t.Fatalf("activated worker lost the concurrent grant: %#v", live)
	}
	records, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records[sub.taskID].ExpectedWriteScope.Files) != 2 {
		t.Fatal("activation overwrote the committed scope")
	}
}
