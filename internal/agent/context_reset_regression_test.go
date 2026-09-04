package agent

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestUpdateTodosPreservesModelDrivenApplyAnchor(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.lastModelDrivenApplyBatch = 7

	if err := a.UpdateTodos([]tools.TodoItem{{
		ID:      "task-1",
		Status:  "in_progress",
		Content: "keep working",
	}}); err != nil {
		t.Fatalf("update todos: %v", err)
	}
	snapshot, err := a.recoveryManager().Recover()
	if err != nil {
		t.Fatalf("recover snapshot: %v", err)
	}
	if snapshot.LastModelDrivenApplyBatch != 7 {
		t.Fatalf("last model-driven apply batch = %d, want 7", snapshot.LastModelDrivenApplyBatch)
	}
	if len(snapshot.Todos) != 1 || snapshot.Todos[0].ID != "task-1" {
		t.Fatalf("snapshot todos = %+v, want task-1", snapshot.Todos)
	}
}

func TestReminderClaimResetsWhenModelIdentityChangesWithSameBudget(t *testing.T) {
	a := &MainAgent{}
	a.SetProviderModelRef("sample/provider-model-1")

	a.overlayClaims.mu.Lock()
	a.overlayClaims.reminder = reminderOverlayClaim{
		windowEpoch: 1,
		windowIndex: 2,
		modelRef:    "sample/provider-model-1",
		budgetEpoch: 3,
		delivered:   true,
		ccCalled:    true,
	}
	a.overlayClaims.mu.Unlock()

	claim := a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(1, 2, 3))
	if !claim.delivered || !claim.ccCalled {
		t.Fatalf("same-model claim must be preserved, got %+v", claim)
	}

	a.SetProviderModelRef("sample/provider-model-2")
	claim = a.syncOverlayWindowClaim(&a.overlayClaims.reminder, a.overlayWindowKey(1, 2, 3))
	if claim.delivered || claim.ccCalled {
		t.Fatalf("same-budget model switch must reset claim, got %+v", claim)
	}
	if claim.modelRef != "sample/provider-model-2" {
		t.Fatalf("claim model ref = %q, want sample/provider-model-2", claim.modelRef)
	}
}

// TestConcurrentSnapshotWritersLeaveSnapshotEqualToLiveState pins the recovery
// snapshot write serialization. UpdateTodos (tool goroutine) and SubAgent
// persistence callbacks (SubAgent goroutines) both write snapshot.json, and
// SaveSnapshot replaces the whole file; without serialization a build that
// started before a newer state update could finish after that update's write
// and clobber it with stale contents. With build + write under one lock, once
// every writer has finished the final snapshot must equal the live todo list.
func TestConcurrentSnapshotWritersLeaveSnapshotEqualToLiveState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())

	const writers = 4
	const rounds = 20
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := a.UpdateTodos([]tools.TodoItem{{
					ID:      fmt.Sprintf("w%d-%d", w, i),
					Status:  "in_progress",
					Content: "keep working",
				}}); err != nil {
					t.Errorf("update todos: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	a.todoMu.RLock()
	want := snapshotTodos(a.todoItems)
	a.todoMu.RUnlock()

	snapshot, err := a.recoveryManager().Recover()
	if err != nil {
		t.Fatalf("recover snapshot: %v", err)
	}
	if !slices.Equal(snapshot.Todos, want) {
		t.Fatalf("snapshot todos = %+v, want live todos %+v", snapshot.Todos, want)
	}
	if len(snapshot.Todos) != 1 {
		t.Fatalf("snapshot todos length = %d, want 1", len(snapshot.Todos))
	}
}
