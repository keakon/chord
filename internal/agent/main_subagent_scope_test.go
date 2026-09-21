package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

// A child delegation's declared write scope never constrains creation: scope
// declarations are advisory, a worker may write any file its role's permission
// rules allow, and whether the child's declaration stays inside the parent's
// is information the child's own judgment (and the Delegate tool schema)
// governs, not a runtime boundary. A child whose role registers no
// file-modifying tools cannot modify files at all, and whether a task may
// write files is decided by its role's ruleset, never propagated from the
// parent task.
func TestNestedCreateSubAgentDeclaredScopeIsAdvisory(t *testing.T) {
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
		handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "Fix lib", AgentType: "worker", ExpectedWriteScope: tools.WriteScope{Files: []string{"lib/sample.go"}}})
		if err != nil || handle.Status != "started" {
			t.Fatalf("CreateSubAgent = (%#v, %v), want the broader child declared scope accepted", handle, err)
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
		handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "Check sample package", AgentType: "worker", ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/agent/main.go"}}})
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
		handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "Unscoped work", AgentType: "worker"})
		if err != nil || handle.Status != "started" {
			t.Fatalf("CreateSubAgent = (%#v, %v), want empty write-capable child accepted", handle, err)
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
		handle, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "Survey parser", AgentType: "worker"})
		if err != nil || handle.Status != "started" {
			t.Fatalf("child = %#v, %v; want empty scope accepted for a role without file tools", handle, err)
		}
	})
}

// Two sibling tasks from the same owner may declare overlapping write scopes:
// the overlap is advisory (declared scopes gate nothing), so the second
// delegation still starts and its handle carries the scope_conflict hint
// pointing at the first task so the caller can serialize the work, coordinate
// shared edits via notify, or move one task to its own worktree.
func TestCreateSubAgentOverlappingSiblingScopeStartsWithConflictHint(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 2)
	parent := newControllableTestSubAgent(t, a, "task-parent")
	parent.depth = 1
	parent.delegation = config.DelegationConfig{MaxChildren: 4, MaxDepth: 2}
	a.syncTaskRecordFromSub(parent, "")
	ctx := tools.WithTaskID(tools.WithAgentID(context.Background(), parent.instanceID), parent.taskID)

	first, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "Fix agent wiring", AgentType: "worker", ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}}})
	if err != nil || first.Status != "started" {
		t.Fatalf("first child = (%#v, %v), want started", first, err)
	}
	second, err := a.CreateSubAgent(ctx, tools.SubAgentRequest{Description: "Extract agent sub helpers", AgentType: "worker", ExpectedWriteScope: tools.WriteScope{Files: []string{"internal/agent/main.go"}}})
	if err != nil {
		t.Fatalf("overlapping second child rejected: %v", err)
	}
	if second.Status != "started" || !second.ScopeConflict {
		t.Fatalf("second child handle = %#v, want started with a scope_conflict hint", second)
	}
	if second.SuggestedTaskID != first.TaskID {
		t.Fatalf("suggested task = %q, want first child %q", second.SuggestedTaskID, first.TaskID)
	}
	if second.SuggestedAction != "serialize_or_worktree" {
		t.Fatalf("suggested action = %q, want serialize_or_worktree", second.SuggestedAction)
	}
	if got := a.orchestrationMetrics.scopeConflicts.Load(); got != 1 {
		t.Fatalf("scope conflict count = %d, want 1", got)
	}
}
