package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestSessionPauseMainAndSubPendingInputsRestoreWithoutExecution(t *testing.T) {
	a, mainProvider := scenarioAgent(t, scriptedStreamCall{resp: &message.Response{ToolCalls: []message.ToolCall{{ID: "main-call", Name: "sample_tool", Args: json.RawMessage(`{}`)}}, StopReason: "tool_use"}})
	tool := &pauseBarrierTool{name: "sample_tool", entered: make(chan struct{}), release: make(chan struct{})}
	a.tools.Register(tool)
	a.ruleset = permission.Ruleset{{Permission: "sample_tool", Pattern: "*", Action: permission.ActionAllow}}
	lock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetSessionLock(lock)
	subProvider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "sub response retained", StopReason: "stop"}, holdAfterStreams: true}}, streamedCh: make(chan struct{}), releaseCh: make(chan struct{})}
	subCtx, subCancel := context.WithCancel(a.parentCtx)
	client := testLLMClientWithProvider(subProvider)
	sub := NewSubAgent(SubAgentConfig{InstanceID: "worker-pause", TaskID: "task-pause", AgentDefName: "worker", OwnerAgentID: identity.MainAgentID, OwnerTaskID: identity.MainAgentID, Depth: 1, TaskDesc: "sample delegated work", LLMClient: client, Recovery: a.recoveryManager(), SessionEpoch: a.recoverySessionEpoch(), Parent: a, ParentCtx: subCtx, Cancel: subCancel, BaseTools: tools.NewRegistry(), WorkDir: a.WorkDir(), SessionDir: a.SessionDir(), ModelName: "test-model"})
	sub.setState(SubAgentStateRunning, "working")
	record := buildTaskRecordFromSub(sub, nil, "", 0, time.Now())
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.taskRecords[sub.taskID] = record
	a.subs.mu.Unlock()
	if err := a.persistSubAgentRegistration(a.SessionDir(), sub, record); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		subCancel()
		if sub.started.Load() {
			<-sub.done
		}
		client.Close()
	})
	if !sub.InjectUserMessage("start delegated request") {
		t.Fatal("sub initial input rejected")
	}
	sub.startRunLoop()
	select {
	case <-subProvider.streamedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("sub model request did not start")
	}
	startMainAgentLoopForTest(t, a)
	a.SendUserMessageWithReceipt("start main tool", "main-request")
	select {
	case <-tool.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("main tool did not start")
	}
	if !submitMainDraftForTest(a, "main-draft", []message.ContentPart{{Type: message.ContentPartText, Text: "main queued input"}}) {
		t.Fatal("main queue rejected")
	}
	if !sub.InjectUserMessage("sub queued input") {
		t.Fatal("sub queue rejected")
	}
	reservation := sub.reserveUserMessage(pendingUserMessage{Content: "sub reserved input", Parts: []message.ContentPart{{Type: message.ContentPartText, Text: "sub reserved input"}, {Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("sample attachment")}}})
	if reservation == nil {
		t.Fatal("sub reservation rejected")
	}
	pauseDone := make(chan error, 1)
	go func() {
		_, err := a.BeginSessionPause(context.Background(), SessionPauseRequest{Identity: testPauseIdentity(a)})
		pauseDone <- err
	}()
	select {
	case <-a.sessionExecutionGate().Frozen():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not freeze")
	}
	if !reservation.Commit() {
		t.Fatal("accepted reservation did not finish enqueue")
	}
	select {
	case err := <-pauseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial checkpoint waited for active execution")
	}
	initial := a.PendingSessionInputs()
	if len(initial) != 3 {
		t.Fatalf("initial pending input count=%d: %#v", len(initial), initial)
	}
	for _, input := range initial {
		if input.Content == "sub reserved input" {
			if len(input.Parts) != 2 || input.Parts[1].ImagePath == "" {
				t.Fatalf("pending attachment not persisted: %#v", input)
			}
		}
	}
	if subCtx.Err() != nil || tool.cancelled.Load() {
		t.Fatal("pause cancelled an existing execution")
	}
	close(tool.release)
	close(subProvider.releaseCh)
	waitPauseState(t, a, recovery.SessionPauseSaved)
	if err := a.ReleasePausedSession("transfer-1"); err != nil {
		t.Fatal(err)
	}
	if requests, _ := mainProvider.snapshot(); len(requests) != 1 {
		t.Fatalf("main model requests=%d", len(requests))
	}
	if requests, _ := subProvider.snapshot(); len(requests) != 1 {
		t.Fatalf("sub model requests=%d", len(requests))
	}
	manager := recovery.NewRecoveryManager(a.SessionDir())
	snapshot, err := manager.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Pause == nil || !snapshot.Pause.CheckpointComplete || len(snapshot.Pause.PendingInputs) != 2 || snapshot.Pause.InputDeliveryRevision == 0 {
		t.Fatalf("final checkpoint=%#v", snapshot.Pause)
	}
	subMessages, err := manager.LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, msg := range subMessages {
		if msg.Content == "sub response retained" {
			found = true
		}
	}
	if !found {
		t.Fatal("completed sub response was not saved")
	}
	for _, input := range snapshot.Pause.PendingInputs {
		if input.Content == "sub reserved input" {
			if _, err := os.Stat(input.Parts[1].ImagePath); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = manager.Close()
	b := newTestMainAgent(t, a.ContentRoot())
	b.markAgentsMDReady()
	b.MarkSkillsReady()
	b.markMCPReady()
	nextProvider := &blockingStreamProvider{}
	b.swapLLMClientWithRef(testLLMClientWithProvider(nextProvider), "test-model", 8192, "sample/test-model")
	configureNestedDelegationTestRuntime(b, 1)
	b.agentConfigs[b.activeConfig.Name] = b.activeConfig
	loaded, err := b.loadSessionState(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	nextLock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	b.SetSessionLock(nextLock)
	b.activateLoadedSession(loaded)
	if !b.sessionExecutionPaused() {
		t.Fatal("ordinary resume did not install paused admission")
	}
	restoredRecord := b.taskRecordByTaskID(sub.taskID)
	if restoredRecord == nil || !restoredRecord.RuntimeParked || restoredRecord.State != string(SubAgentStateIdle) {
		t.Fatalf("durable sub restored with wrong state: %#v", restoredRecord)
	}
	if len(b.pause.parkedInputs[sub.taskID]) != 2 {
		t.Fatalf("parked task pending inputs=%#v", b.pause.parkedInputs)
	}
	startMainAgentLoopForTest(t, b)
	if requests, _ := nextProvider.snapshot(); len(requests) != 0 {
		t.Fatal("ordinary resume started model work")
	}
	b.subs.mu.RLock()
	live := b.subs.subAgents[sub.instanceID]
	b.subs.mu.RUnlock()
	if live != nil {
		t.Fatal("ordinary resume activated parked sub runtime")
	}
}
