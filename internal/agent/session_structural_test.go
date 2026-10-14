package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestSessionStructuralFreezesRuntimeMutations(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.sessionExecutionGate().Freeze()
	if _, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "worker", Description: "Sample task"}); !executiongate.IsPaused(err) {
		t.Fatalf("Create = %v", err)
	}
	if _, _, _, err := a.getOrRehydrateTask(&DurableTaskRecord{TaskID: "task-1"}); !executiongate.IsPaused(err) {
		t.Fatalf("rehydrate = %v", err)
	}
	if _, err := a.NotifySubAgent(context.Background(), "task-1", "Sample follow-up", ""); !executiongate.IsPaused(err) {
		t.Fatalf("follow-up = %v", err)
	}
	if err := a.SwitchRole("builder"); !executiongate.IsPaused(err) {
		t.Fatalf("role = %v", err)
	}
	if err := a.SwitchModel("sample/model-1"); !executiongate.IsPaused(err) {
		t.Fatalf("model = %v", err)
	}
	if err := a.setCurrentModelPool("default"); !executiongate.IsPaused(err) {
		t.Fatalf("pool = %v", err)
	}
	if ok, _ := a.scheduleCompactionPlan(false); ok {
		t.Fatal("compaction scheduled after freeze")
	}
}

func TestSessionStructuralExistingToolMayCompleteChildRegistration(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	gate := a.sessionExecutionGate()
	lease, err := gate.TryBegin(executiongate.Tool, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := executiongate.WithLease(context.Background(), lease)
	gate.Freeze()
	finish, err := a.beginSessionStructural(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if _, err := a.beginSessionStructural(ctx, false); !executiongate.IsPaused(err) {
		t.Fatalf("existing tool authorized unrelated structural work: %v", err)
	}
	lease.Finish()
	if _, err := a.beginSessionStructural(ctx, true); !executiongate.IsPaused(err) {
		t.Fatalf("finished tool authorized creation: %v", err)
	}
}

func TestSessionStructuralPreparationBlocksMutationsBeforeFreeze(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.pause.mu.Lock()
	a.pause.status.State = recovery.SessionPausePreparing
	a.pause.mu.Unlock()
	if a.sessionExecutionPaused() {
		t.Fatal("preparing unexpectedly froze transport")
	}
	if err := a.SwitchRole("builder"); !executiongate.IsPaused(err) {
		t.Fatalf("role allowed during prepare: %v", err)
	}
	if _, err := a.beginSessionStructural(context.Background(), false); !executiongate.IsPaused(err) {
		t.Fatalf("structural = %v", err)
	}
}

func TestMainPreparationFreezeInterruptsHookWithoutCancellingTurn(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	entered := make(chan struct{})
	a.SetBusyPreparationHook(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	turnCtx := t.Context()
	result := make(chan error, 1)
	go func() {
		_, err := a.callLLMForRequest(turnCtx, llmRequestInput{Messages: []message.Message{{Role: message.RoleUser, Content: "Sample request"}}}, 0)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not start")
	}
	a.sessionExecutionGate().Freeze()
	select {
	case err := <-result:
		if !executiongate.IsPaused(err) || errors.Is(err, context.Canceled) {
			t.Fatalf("prepare returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("freeze left preparation blocked")
	}
	if turnCtx.Err() != nil {
		t.Fatal("freeze canceled original turn")
	}
}

func TestAdmittedChildCreationFinishesDurableRegistrationAcrossFreeze(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	configureNestedDelegationTestRuntime(a, 3)
	factoryEntered := make(chan struct{})
	finishFactory := make(chan struct{})
	a.llmFactory = func(string, []string, string) *llm.Client {
		close(factoryEntered)
		<-finishFactory
		return newTestLLMClient()
	}
	result := make(chan error, 1)
	var handle tools.TaskHandle
	go func() {
		var err error
		handle, err = a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "worker", Description: "Sample task"})
		result <- err
	}()
	select {
	case <-factoryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("creation did not reach factory")
	}
	gate := a.sessionExecutionGate()
	gate.Freeze()
	if len(gate.Snapshot().Activities) == 0 {
		t.Fatal("creation disappeared from drain barrier")
	}
	close(finishFactory)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("creation did not finish")
	}
	if a.taskRecordByTaskID(handle.TaskID) == nil || a.subAgentByTaskID(handle.TaskID) == nil {
		t.Fatal("admitted task was rolled back after freeze")
	}
	sub := a.subAgentByTaskID(handle.TaskID)
	capture, err := sub.CaptureSessionPause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.PendingInputs) != 1 || capture.PendingInputs[0].Content != "Sample task" {
		t.Fatalf("initial task input lost: %+v", capture)
	}
}
