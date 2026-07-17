package agent

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

// findDuplicateOrConflictingTask is a test-only locking wrapper; production
// callers hold subs.mu and use findDuplicateOrConflictingTaskLocked directly.
func (a *MainAgent) findDuplicateOrConflictingTask(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string, expectedWriteScope tools.WriteScope) (*DurableTaskRecord, bool) {
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	return a.findDuplicateOrConflictingTaskLocked(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey, expectedWriteScope)
}

func TestWriteScopesOverlapMatchesExactAndNestedPathsOnly(t *testing.T) {
	tests := []struct {
		name string
		a    tools.WriteScope
		b    tools.WriteScope
		want bool
	}{
		{
			name: "same file overlaps",
			a:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			b:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			want: true,
		},
		{
			name: "file under path prefix overlaps",
			a:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			want: true,
		},
		{
			name: "nested path prefixes overlap",
			a:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo/bar"}},
			want: true,
		},
		{
			name: "prefix-like sibling names do not overlap",
			a:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{Files: []string{"internal/foobar/baz.go"}},
			want: false,
		},
		{
			name: "path prefixes require boundary",
			a:    tools.WriteScope{PathPrefix: []string{"pkg/mod"}},
			b:    tools.WriteScope{PathPrefix: []string{"pkg/module"}},
			want: false,
		},
		{
			name: "readonly never overlaps",
			a:    tools.WriteScope{ReadOnly: true, PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := writeScopesOverlap(tc.a, tc.b, "/repo")
			if got != tc.want {
				t.Fatalf("writeScopesOverlap(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestMergeDurableTaskRecordsPreservesCoordinationIdentity(t *testing.T) {
	base := map[string]*DurableTaskRecord{
		"adhoc-identity": {
			TaskID:             "adhoc-identity",
			PlanTaskRef:        "plan-item-8",
			SemanticTaskKey:    "restore-identity",
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
			State:              string(SubAgentStateCompleted),
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
	if len(got.ExpectedWriteScope.PathPrefix) != 1 || got.ExpectedWriteScope.PathPrefix[0] != "internal/agent" {
		t.Fatalf("merged write scope = %#v, want durable path prefix", got.ExpectedWriteScope)
	}
}

func TestFindDuplicateOrConflictingTaskAllowsExplicitOnlyTerminalRetry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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

			existing, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "plan-item", "semantic-key", tools.WriteScope{})
			if existing != nil || conflict {
				t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v), want retry allowed", existing, conflict)
			}
		})
	}
}

func TestFindDuplicateOrConflictingTaskKeepsNotifyRehydratableCompletedTask(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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

	existing, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "", "semantic-key", tools.WriteScope{})
	if existing == nil || existing.TaskID != "completed-task" || conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v), want completed duplicate", existing, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskReleasesCompletedWriteScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"completed-task": {
			TaskID:             "completed-task",
			OwnerAgentID:       "owner",
			OwnerTaskID:        "parent",
			AgentDefName:       "worker",
			SemanticTaskKey:    "completed-work",
			ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/shared.go"}},
			State:              string(SubAgentStateCompleted),
			ResumePolicy:       taskResumePolicyNotify,
		},
	})

	existing, conflict := a.findDuplicateOrConflictingTask(
		"owner",
		"parent",
		"worker",
		"",
		"new-work",
		tools.WriteScope{Files: []string{"internal/shared.go"}},
	)
	if existing != nil || conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v), want completed write scope released", existing, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskKeepsNonTerminalWriteScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"running-task": {
			TaskID:             "running-task",
			OwnerAgentID:       "other-owner",
			OwnerTaskID:        "other-parent",
			AgentDefName:       "worker",
			SemanticTaskKey:    "other-work",
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal"}},
			State:              string(SubAgentStateWaitingMain),
			ResumePolicy:       taskResumePolicyNotify,
		},
	})

	existing, conflict := a.findDuplicateOrConflictingTask(
		"owner",
		"parent",
		"worker",
		"",
		"new-work",
		tools.WriteScope{Files: []string{"internal/shared.go"}},
	)
	if existing == nil || existing.TaskID != "running-task" || !conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v), want live scope conflict", existing, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskCanonicalizesScopeAliases(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)
	a.projectRoot = root
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"running-task": {
			TaskID:             "running-task",
			State:              string(SubAgentStateRunning),
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal"}},
		},
	})

	existing, conflict := a.findDuplicateOrConflictingTask(
		"owner",
		"parent",
		"worker",
		"",
		"new-work",
		tools.WriteScope{Files: []string{filepath.Join(root, "internal", "shared.go")}},
	)
	if existing == nil || !conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v), want canonical scope conflict", existing, conflict)
	}
}

func TestPersistTaskRegistrySerializesSnapshotOrder(t *testing.T) {
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
	writers.Add(1)
	go func() {
		defer writers.Done()
		a.persistTaskRegistry()
	}()
	<-firstSnapshot
	a.subs.mu.Lock()
	a.subs.taskRecords["adhoc-persist-order"].State = string(SubAgentStateCompleted)
	a.subs.mu.Unlock()
	secondDone := make(chan struct{})
	writers.Add(1)
	go func() {
		defer writers.Done()
		a.persistTaskRegistry()
		close(secondDone)
	}()
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
