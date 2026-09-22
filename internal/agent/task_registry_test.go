package agent

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

// configureNoFileWriteWorkerRole registers a "worker" agent definition whose
// ruleset denies every file-modifying tool, so its tasks carry empty write
// scopes and never conflict with other tasks over file paths: the read-only
// property of a task is expressed through the role's tool surface.
func configureNoFileWriteWorkerRole(t *testing.T, a *MainAgent) {
	t.Helper()
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:       "worker",
			Mode:       "subagent",
			Permission: parsePermissionNode(t, "write: deny\nedit: deny\ndelete: deny\napply_patch: deny\n"),
		},
	}
}

// findDuplicateOrConflictingTask is a test-only locking wrapper; production
// callers hold subs.mu and use findDuplicateOrConflictingTaskLocked directly.
func (a *MainAgent) findDuplicateOrConflictingTask(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey string, semanticKeyExplicit bool, expectedWriteScope tools.WriteScope) (*DurableTaskRecord, taskDuplicateDisposition, bool) {
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	return a.findDuplicateOrConflictingTaskLocked(ownerAgentID, ownerTaskID, agentType, planTaskRef, semanticTaskKey, semanticKeyExplicit, expectedWriteScope)
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
			name: "unspecified write scope is exclusive",
			a:    tools.WriteScope{},
			b:    tools.WriteScope{Files: []string{"internal/foo/a.go"}},
			want: true,
		},
		{
			name: "two unspecified write scopes overlap",
			a:    tools.WriteScope{},
			b:    tools.WriteScope{},
			want: true,
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

// writeScopesOverlap is a pure path-overlap predicate over two declared
// boundaries; it has no role knowledge. Whether a task can write at all is a
// property of its role's ruleset, and the callers gate the predicate with that
// classification (see taskScopesConflict). These tests pin the role-aware gate.
func TestTaskScopesConflictGatesOnRoleFileWriteSurface(t *testing.T) {
	newRuntime := func(t *testing.T) *MainAgent {
		a := newTestMainAgent(t, t.TempDir())
		a.agentConfigs = map[string]*config.AgentConfig{
			"worker": {
				Name:       "worker",
				Mode:       "subagent",
				Permission: parsePermissionNode(t, "write: deny\nedit: deny\ndelete: deny\napply_patch: deny\n"),
			},
		}
		return a
	}
	for _, tc := range []struct {
		name   string
		aRole  string
		aScope tools.WriteScope
		bRole  string
		bScope tools.WriteScope
		want   bool
	}{
		{name: "no-write-role task never conflicts", aRole: "worker", bRole: "builder", bScope: tools.WriteScope{PathPrefix: []string{"internal"}}},
		{name: "no-write-role tasks do not conflict with each other", aRole: "worker", bRole: "worker"},
		{name: "write-capable empty scope is exclusive", aRole: "builder", bRole: "builder", aScope: tools.WriteScope{PathPrefix: []string{"internal"}}, want: true},
		{name: "declared write scopes overlap by path", aRole: "builder", aScope: tools.WriteScope{PathPrefix: []string{"internal/foo"}}, bRole: "builder", bScope: tools.WriteScope{Files: []string{"internal/foo/a.go"}}, want: true},
		{name: "disjoint write scopes do not conflict", aRole: "builder", aScope: tools.WriteScope{PathPrefix: []string{"internal/foo"}}, bRole: "builder", bScope: tools.WriteScope{Files: []string{"lib/a.go"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newRuntime(t)
			if got := a.taskScopesConflict(tc.aRole, tc.aScope, tc.bRole, tc.bScope, a.writeScopeBaseDir()); got != tc.want {
				t.Fatalf("taskScopesConflict(%q, %#v, %q, %#v) = %v, want %v", tc.aRole, tc.aScope, tc.bRole, tc.bScope, got, tc.want)
			}
		})
	}
}

func TestResolveSemanticTaskKeyDerivesFromDescription(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		description string
		want        string
	}{
		{
			name:        "explicit key wins",
			key:         "  restore-identity  ",
			description: "Restore the durable identity",
			want:        "restore-identity",
		},
		{
			name:        "derived from description",
			key:         "",
			description: "Fix the parser bug.",
			want:        "fix the parser bug",
		},
		{
			// Two wordings of the same deliverable must collide, otherwise the
			// description-derived probable-duplicate hint never fires for the
			// common case of a description and nothing else.
			name:        "punctuation and case normalized",
			key:         "",
			description: "FIX the Parser  bug!!",
			want:        "fix the parser bug",
		},
		{
			name:        "truncated to the opening clause",
			key:         "",
			description: "one two three four five six seven eight nine ten eleven twelve thirteen fourteen",
			want:        "one two three four five six seven eight nine ten eleven twelve",
		},
		{
			name:        "no usable words",
			key:         "",
			description: "   ...   ",
			want:        "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSemanticTaskKey(tc.key, tc.description); got != tc.want {
				t.Fatalf("resolveSemanticTaskKey(%q, %q) = %q, want %q", tc.key, tc.description, got, tc.want)
			}
		})
	}
}

// Two wordings of one deliverable collide on the description-derived fallback
// key, but so do genuinely different deliverables that merely share a long
// opening clause (the key is the first words of the description, lowercased).
// The old contract treated such a collision as proof the delegate already
// existed: every later task of a batch sharing a preamble was hard-rejected
// with already_exists, and a collision against a pending admission even made
// the caller block on the first task's handle. New contract: a fallback-key
// collision is only a probable duplicate (taskDuplicateProbable) — the runtime
// creates the new task and surfaces a duplicate_detected hint, leaving the
// "same deliverable?" decision to the model. Hard rejection is reserved for an
// identical explicit semantic_task_key, the identity a caller deliberately
// asserts. The trade-off is that identical deliverables that merely share an
// opening wording no longer get merged automatically; the hint is the merge
// signal instead.
func TestFindDuplicateOrConflictingTaskDerivedDescriptionCollisionIsProbableDuplicate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNoFileWriteWorkerRole(t, a)
	description := "Add regression coverage for the parser fallback path so the duplicate guard has a stable key"
	rewordedTail := "Add regression coverage for the parser fallback path so the duplicate guard is exercised end to end"
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:             "adhoc-1",
			OwnerAgentID:       "",
			OwnerTaskID:        "",
			AgentDefName:       "worker",
			SemanticTaskKey:    resolveSemanticTaskKey("", description),
			ExpectedWriteScope: tools.WriteScope{},
			State:              string(SubAgentStateRunning),
		},
	})

	// A re-delegation described with the same opening clause and no explicit
	// semantic key matches the earlier task as a probable duplicate only: the
	// finder still reports the record (the caller needs it for the hint), but
	// it is not a confirmed duplicate. A role that registers no file-modifying
	// tools never writes, so the live collision is not a scope conflict either.
	existing, disposition, conflict := a.findDuplicateOrConflictingTask(
		"",
		"",
		"worker",
		"",
		resolveSemanticTaskKey("", rewordedTail),
		false,
		tools.WriteScope{},
	)
	if existing == nil || existing.TaskID != "adhoc-1" {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want the earlier task", existing, disposition, conflict)
	}
	if disposition != taskDuplicateProbable {
		t.Fatalf("disposition = %v, want taskDuplicateProbable for a description-derived collision", disposition)
	}
	if conflict {
		t.Fatal("no-file-write-tool scopes reported a scope conflict on a description-derived collision")
	}
}

// The same derived-key collision against a live copy whose writable scope
// overlaps is still a scope conflict: even when the fallback heuristic is wrong
// about the deliverable, it must never justify two concurrent writers over one
// scope.
func TestFindDuplicateOrConflictingTaskDerivedCollisionKeepsLiveScopeConflict(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	description := "Add regression coverage for the parser fallback path so the duplicate guard has a stable key"
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:             "adhoc-1",
			OwnerAgentID:       "",
			OwnerTaskID:        "",
			AgentDefName:       "worker",
			SemanticTaskKey:    resolveSemanticTaskKey("", description),
			ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
			State:              string(SubAgentStateRunning),
		},
	})

	existing, disposition, conflict := a.findDuplicateOrConflictingTask(
		"",
		"",
		"worker",
		"",
		resolveSemanticTaskKey("", description),
		false,
		tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
	)
	if existing == nil || existing.TaskID != "adhoc-1" {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want the earlier task", existing, disposition, conflict)
	}
	if !conflict {
		t.Fatal("derived collision on a live overlapping writable scope must stay a scope conflict")
	}
}

// An identical explicit semantic_task_key is the only confirmed duplicate: the
// caller deliberately asserted the identity, so the runtime keeps rejecting the
// re-delegation with already_exists.
func TestFindDuplicateOrConflictingTaskExplicitSemanticKeyIsHardDuplicate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
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

	existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "", "audit-report", true, tools.WriteScope{})
	if existing == nil || existing.TaskID != "adhoc-1" {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want the existing task", existing, disposition, conflict)
	}
	if disposition != taskDuplicateExplicitKey {
		t.Fatalf("disposition = %v, want taskDuplicateExplicitKey for an explicit key match", disposition)
	}
	if conflict {
		t.Fatal("an identical explicit key must never be reported as a scope conflict")
	}
}

// Scope conflicts only annotate started handles, so a conflicting record must
// never hide a confirmed duplicate that also exists in the registry: an
// explicit semantic_task_key collision is the only hard rejection and must win
// regardless of which record the scan visits first (taskRecords is a map, so
// iteration order is arbitrary). The finder is therefore exercised repeatedly.
func TestFindDuplicateOrConflictingTaskExplicitKeyOutranksScopeConflict(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"adhoc-1": {
			TaskID:             "adhoc-1",
			OwnerAgentID:       "owner",
			OwnerTaskID:        "parent",
			AgentDefName:       "worker",
			SemanticTaskKey:    "other-deliverable",
			ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
			State:              string(SubAgentStateRunning),
		},
		"adhoc-2": {
			TaskID:             "adhoc-2",
			OwnerAgentID:       "owner",
			OwnerTaskID:        "parent",
			AgentDefName:       "worker",
			SemanticTaskKey:    "audit-report",
			ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
			State:              string(SubAgentStateRunning),
		},
	})

	// adhoc-1 overlaps the new scope without matching its deliverable;
	// adhoc-2 is a confirmed duplicate of it. Every call must resolve to the
	// explicit-key rejection, no matter the iteration order.
	for i := range 20 {
		existing, disposition, conflict := a.findDuplicateOrConflictingTask(
			"owner", "parent", "worker", "", "audit-report", true,
			tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
		)
		if existing == nil || existing.TaskID != "adhoc-2" {
			t.Fatalf("iteration %d: findDuplicateOrConflictingTask() = (%#v, %v, %v), want the explicit-key task adhoc-2", i, existing, disposition, conflict)
		}
		if disposition != taskDuplicateExplicitKey || conflict {
			t.Fatalf("iteration %d: disposition/conflict = (%v, %v), want taskDuplicateExplicitKey without a scope conflict", i, disposition, conflict)
		}
	}
}

// Reusing a plan_task_ref without sharing an explicit semantic_task_key is as
// heuristic as a derived-key collision (the same plan item can cover several
// distinct delegates), so it too is only a probable duplicate. The live copy
// runs under a role that registers no file-modifying tools, so the collision is
// not a scope conflict either.
func TestFindDuplicateOrConflictingTaskPlanTaskRefCollisionIsProbableDuplicate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNoFileWriteWorkerRole(t, a)
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

	// Distinct explicit key but the same plan_task_ref: only the plan reference
	// collides, so the match is probable, not a confirmed duplicate.
	existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "plan-item-7", "second-deliverable", true, tools.WriteScope{})
	if existing == nil || existing.TaskID != "adhoc-1" {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want the earlier task", existing, disposition, conflict)
	}
	if disposition != taskDuplicateProbable {
		t.Fatalf("disposition = %v, want taskDuplicateProbable for a plan_task_ref-only match", disposition)
	}
	if conflict {
		t.Fatal("plan_task_ref-only match on no-file-write-tool scopes must not be a scope conflict")
	}
}

// A pending admission is screened like a running record, but a caller may only
// wait for and reuse its result when the match is an identical explicit
// semantic_task_key. These two tests pin the split CreateSubAgent relies on:
// an explicit-key match on a pending admission stays hard (the caller may wait
// for the first task's handle), while a derived-key collision on a pending
// admission is only probable (the caller must start its own task, never block
// on another admission's handle).
func TestFindPendingDuplicateClassifiesExplicitKeyAsHard(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subs.mu.Lock()
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:             "adhoc-9",
		ownerAgentID:       "",
		ownerTaskID:        "",
		agentType:          "worker",
		semanticTaskKey:    "audit-report",
		expectedWriteScope: tools.WriteScope{},
	})
	existing, disposition, conflict, pending := a.findPendingDuplicateOrConflictingTaskLocked("", "", "worker", "", "audit-report", true, tools.WriteScope{})
	a.subs.mu.Unlock()
	if existing == nil || pending == nil || pending.taskID != "adhoc-9" {
		t.Fatalf("findPendingDuplicateOrConflictingTaskLocked() = (%#v, %v, %v, %#v), want the pending admission", existing, disposition, conflict, pending)
	}
	if disposition != taskDuplicateExplicitKey || conflict {
		t.Fatalf("disposition/conflict = (%v, %v), want taskDuplicateExplicitKey and no scope conflict", disposition, conflict)
	}
}

func TestFindPendingDuplicateClassifiesDerivedKeyAsProbable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNoFileWriteWorkerRole(t, a)
	description := "Audit the report generator output for broken markdown links"
	a.subs.mu.Lock()
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:             "adhoc-9",
		ownerAgentID:       "",
		ownerTaskID:        "",
		agentType:          "worker",
		semanticTaskKey:    resolveSemanticTaskKey("", description),
		expectedWriteScope: tools.WriteScope{},
	})
	existing, disposition, conflict, pending := a.findPendingDuplicateOrConflictingTaskLocked("", "", "worker", "", resolveSemanticTaskKey("", description), false, tools.WriteScope{})
	a.subs.mu.Unlock()
	if existing == nil || pending == nil || pending.taskID != "adhoc-9" {
		t.Fatalf("findPendingDuplicateOrConflictingTaskLocked() = (%#v, %v, %v, %#v), want the pending admission", existing, disposition, conflict, pending)
	}
	if disposition != taskDuplicateProbable || conflict {
		t.Fatalf("disposition/conflict = (%v, %v), want taskDuplicateProbable and no scope conflict", disposition, conflict)
	}
}

// A pending explicit-key admission outranks a pending scope conflict for the
// same reason the registry scan prefers confirmed duplicates: only the
// identical explicit semantic_task_key may be waited on, and a conflict must
// not hide it regardless of admission iteration order.
func TestFindPendingDuplicateExplicitKeyOutranksPendingScopeConflict(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subs.mu.Lock()
	defer a.subs.mu.Unlock()
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:             "adhoc-9",
		ownerAgentID:       "owner",
		ownerTaskID:        "parent",
		agentType:          "worker",
		semanticTaskKey:    "other-deliverable",
		expectedWriteScope: tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
	})
	a.subs.addAdmissionLocked(&subAgentAdmission{
		taskID:             "adhoc-10",
		ownerAgentID:       "owner",
		ownerTaskID:        "parent",
		agentType:          "worker",
		semanticTaskKey:    "audit-report",
		expectedWriteScope: tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
	})

	for i := range 20 {
		existing, disposition, conflict, pending := a.findPendingDuplicateOrConflictingTaskLocked(
			"owner", "parent", "worker", "", "audit-report", true,
			tools.WriteScope{Files: []string{"internal/parser/parser_test.go"}},
		)
		if existing == nil || pending == nil || pending.taskID != "adhoc-10" {
			t.Fatalf("iteration %d: findPendingDuplicateOrConflictingTaskLocked() = (%#v, %v, %v, %#v), want the explicit-key admission adhoc-10", i, existing, disposition, conflict, pending)
		}
		if disposition != taskDuplicateExplicitKey || conflict {
			t.Fatalf("iteration %d: disposition/conflict = (%v, %v), want taskDuplicateExplicitKey without a scope conflict", i, disposition, conflict)
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
// existing task and suggest continuing it with notify, never report a scope
// conflict (overlap is advisory now and only annotates started handles).
func TestDuplicateTaskHandleBuildsExplicitKeyRejection(t *testing.T) {
	existing := &DurableTaskRecord{
		TaskID:           "adhoc-1",
		LatestInstanceID: "worker-1",
		SemanticTaskKey:  "audit-report",
	}
	got := duplicateTaskHandle(existing)

	if got.Status != "already_exists" || got.ScopeConflict {
		t.Fatalf("duplicate handle = %#v, want already_exists without a scope conflict", got)
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

			existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "plan-item", "semantic-key", true, tools.WriteScope{})
			if existing != nil || conflict || disposition != taskDuplicateNone {
				t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want retry allowed", existing, disposition, conflict)
			}
		})
	}
}

// A completed task under the notify resume policy is rehydratable, so it keeps
// participating in duplicate detection. Whether it still blocks a re-delegation
// now depends on how the new call identified the deliverable: an identical
// explicit semantic_task_key stays a confirmed duplicate (the runtime rejects
// the call and suggests continuing the completed task with Notify), while a
// description-derived collision against the same completed task would be only a
// probable duplicate — a hint on a newly started task, never a redirect to the
// completed record.
func TestFindDuplicateOrConflictingTaskKeepsNotifyRehydratableCompletedTaskAsExplicitDuplicate(t *testing.T) {
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

	existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "", "semantic-key", true, tools.WriteScope{})
	if existing == nil || existing.TaskID != "completed-task" {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want completed duplicate", existing, disposition, conflict)
	}
	if disposition != taskDuplicateExplicitKey {
		t.Fatalf("disposition = %v, want taskDuplicateExplicitKey for an explicit key match against a rehydratable completed task", disposition)
	}
	if conflict {
		t.Fatal("a completed task must not be reported as a scope conflict")
	}
}

// The flip side of the previous test: a description-derived collision against
// the same completed+notify task is only a probable duplicate. The runtime must
// not redirect the new delegate to the completed record; the fallback key is a
// heuristic, so the caller creates a fresh task and lets the model decide
// whether the deliverable is really the same as the finished one.
func TestFindDuplicateOrConflictingTaskDerivedCollisionOnCompletedIsProbableDuplicate(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	description := "Investigate the stale-cache failure and its reproduction steps"
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"completed-task": {
			TaskID:          "completed-task",
			OwnerAgentID:    "owner",
			OwnerTaskID:     "parent",
			AgentDefName:    "worker",
			SemanticTaskKey: resolveSemanticTaskKey("", description),
			State:           string(SubAgentStateCompleted),
			ResumePolicy:    taskResumePolicyNotify,
		},
	})

	existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "", resolveSemanticTaskKey("", description), false, tools.WriteScope{})
	if existing == nil || existing.TaskID != "completed-task" {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want the completed task", existing, disposition, conflict)
	}
	if disposition != taskDuplicateProbable {
		t.Fatalf("disposition = %v, want taskDuplicateProbable for a description-derived collision against a completed task", disposition)
	}
	if conflict {
		t.Fatal("a completed task must not be reported as a scope conflict")
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

	existing, disposition, conflict := a.findDuplicateOrConflictingTask(
		"owner",
		"parent",
		"worker",
		"",
		"new-work",
		true,
		tools.WriteScope{Files: []string{"internal/shared.go"}},
	)
	if existing != nil || conflict || disposition != taskDuplicateNone {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want completed write scope released", existing, disposition, conflict)
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

	existing, disposition, conflict := a.findDuplicateOrConflictingTask(
		"owner",
		"parent",
		"worker",
		"",
		"new-work",
		true,
		tools.WriteScope{Files: []string{"internal/shared.go"}},
	)
	if existing == nil || existing.TaskID != "running-task" || !conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want live scope conflict", existing, disposition, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskTreatsUnspecifiedScopeAsExclusive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"running-task": {
			TaskID:             "running-task",
			AgentDefName:       "worker",
			SemanticTaskKey:    "other-work",
			ExpectedWriteScope: tools.WriteScope{},
			State:              string(SubAgentStateRunning),
		},
	})

	existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "worker", "", "new-work", true, tools.WriteScope{PathPrefix: []string{"internal/agent"}})
	if existing == nil || existing.TaskID != "running-task" || !conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want unscoped exclusive conflict", existing, disposition, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskExcludesFullOwnerLineage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"root-task": {
			TaskID:             "root-task",
			AgentDefName:       "worker",
			ExpectedWriteScope: tools.WriteScope{},
			State:              string(SubAgentStateRunning),
		},
		"parent-task": {
			TaskID:             "parent-task",
			AgentDefName:       "worker",
			OwnerTaskID:        "root-task",
			ExpectedWriteScope: tools.WriteScope{},
			State:              string(SubAgentStateRunning),
		},
	})

	existing, disposition, conflict := a.findDuplicateOrConflictingTask("parent-agent", "parent-task", "worker", "", "grandchild-work", true, tools.WriteScope{})
	if existing != nil || conflict || disposition != taskDuplicateNone {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want no conflict with owner lineage", existing, disposition, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskAllowsNoFileWriteToolRoleAlongsideUnspecifiedScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: "subagent"},
		"auditor": {
			Name:       "auditor",
			Mode:       "subagent",
			Permission: parsePermissionNode(t, "write: deny\nedit: deny\ndelete: deny\napply_patch: deny\n"),
		},
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"running-task": {
			TaskID:             "running-task",
			AgentDefName:       "worker",
			SemanticTaskKey:    "other-work",
			ExpectedWriteScope: tools.WriteScope{},
			State:              string(SubAgentStateRunning),
		},
	})

	// The incoming delegation runs under a role without file-modifying tools,
	// so even though the running writer holds an empty (exclusive) scope, the
	// two never conflict: the auditor cannot write files.
	existing, disposition, conflict := a.findDuplicateOrConflictingTask("owner", "parent", "auditor", "", "read-only-work", true, tools.WriteScope{})
	if existing != nil || conflict || disposition != taskDuplicateNone {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want no conflict with a running writer", existing, disposition, conflict)
	}
}

func TestFindDuplicateOrConflictingTaskCanonicalizesScopeAliases(t *testing.T) {
	root := t.TempDir()
	a := newTestMainAgent(t, root)
	a.contentRoot = root
	// Scope comparisons resolve relative declarations against the agent's
	// working directory (writeScopeBaseDir), so the fixture must make the
	// cached workdir the same root the absolute path below names.
	setCachedWorkDirForTest(a, root)
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"running-task": {
			TaskID:             "running-task",
			State:              string(SubAgentStateRunning),
			ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal"}},
		},
	})

	existing, disposition, conflict := a.findDuplicateOrConflictingTask(
		"owner",
		"parent",
		"worker",
		"",
		"new-work",
		true,
		tools.WriteScope{Files: []string{filepath.Join(root, "internal", "shared.go")}},
	)
	if existing == nil || !conflict {
		t.Fatalf("findDuplicateOrConflictingTask() = (%#v, %v, %v), want canonical scope conflict", existing, disposition, conflict)
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
