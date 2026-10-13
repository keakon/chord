package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestSubRestoredPauseInputsDeduplicateRepeatedInstall(t *testing.T) {
	sub := &SubAgent{}
	sub.RestorePauseInputs([]pendingUserMessage{{InputID: "pending-1", Content: "first instruction", Revision: 1, AcceptedOrder: 1}})
	sub.RestorePauseInputs([]pendingUserMessage{{InputID: "pending-1", Content: "updated instruction", Revision: 2, AcceptedOrder: 1}, {InputID: "pending-2", Content: "next instruction", Revision: 1, AcceptedOrder: 2}})
	sub.RestorePauseInputs([]pendingUserMessage{{InputID: "pending-1", Content: "first instruction", Revision: 1, AcceptedOrder: 1}})
	if len(sub.restoredPauseInputs) != 2 || sub.restoredPauseInputs[0].Content != "updated instruction" || sub.inputSequence != 2 {
		t.Fatalf("repeated installation changed pending identity: %#v", sub.restoredPauseInputs)
	}
}

func TestSubPauseLoopCapturesInputOverflowAndReservation(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.executionGate = &executiongate.Gate{}
	sub.inputCh = make(chan pendingUserMessage, 1)
	sub.setState(SubAgentStateRunning, "working")
	if !sub.InjectUserMessage("first") || !sub.InjectUserMessage("second") {
		t.Fatal("enqueue failed")
	}
	reservation := sub.reserveUserMessage(pendingUserMessage{Content: "third"})
	if reservation == nil {
		t.Fatal("reservation failed")
	}
	parent.executionGate.Freeze()
	sub.startRunLoop()
	if sub.InjectUserMessage("rejected") {
		t.Fatal("frozen session accepted input")
	}
	if !reservation.Commit() {
		t.Fatal("pre-freeze reservation was lost")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	capture, err := sub.CaptureSessionPause(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.PendingInputs) != 3 {
		t.Fatalf("capture inputs=%#v", capture.PendingInputs)
	}
	for i, want := range []string{"first", "second", "third"} {
		if capture.PendingInputs[i].Content != want || capture.PendingInputs[i].InputID == "" {
			t.Fatalf("input %d=%#v", i, capture.PendingInputs[i])
		}
	}
	capture.PendingInputs[0].Content = "mutated"
	again, err := sub.DrainSessionPause(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.PendingInputs[0].Content != "first" {
		t.Fatal("capture aliases owner state")
	}
	if sub.turn.ID != 1 || sub.State() != SubAgentStateRunning {
		t.Fatal("freeze created a new turn or terminal task")
	}
	if err := sub.StopAfterSessionPause(ctx); err != nil {
		t.Fatal(err)
	}
	if sub.parentCtx.Err() != nil {
		t.Fatal("pause stop cancelled execution context")
	}
}

func TestSubPauseKeepsTranscriptStagedInput(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.executionGate = &executiongate.Gate{}
	input := pendingUserMessage{InputID: "input-1", AcceptedOrder: 1, Content: "pending", Stage: recovery.PendingInputStageTranscriptStaged}
	before := sub.ctxMgr.MessageCount()
	sub.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: input.Content, InputID: input.InputID})
	sub.RestorePauseInputs([]pendingUserMessage{input})
	sub.appendPendingUserMessage(input)
	if got := sub.ctxMgr.MessageCount(); got != before+1 {
		t.Fatalf("staged input appended twice: count=%d", got)
	}
	parent.executionGate.Freeze()
	sub.startRunLoop()
	capture, err := sub.CaptureSessionPause(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.PendingInputs) != 1 || capture.PendingInputs[0].Stage != recovery.PendingInputStageTranscriptStaged {
		t.Fatalf("capture=%#v", capture)
	}
	if err := sub.StopAfterSessionPause(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type pauseProbeTool struct {
	started   chan context.Context
	release   chan struct{}
	returnErr error
}

func (*pauseProbeTool) Name() string        { return "PauseProbe" }
func (*pauseProbeTool) Description() string { return "execution admission probe" }
func (*pauseProbeTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (*pauseProbeTool) IsReadOnly() bool { return true }
func (p *pauseProbeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	p.started <- ctx
	select {
	case <-p.release:
		return "settled", p.returnErr
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestToolPauseKeepsStartedExecutionAlive(t *testing.T) {
	gate := &executiongate.Gate{}
	tool := &pauseProbeTool{started: make(chan context.Context, 1), release: make(chan struct{})}
	reg := tools.NewRegistry()
	reg.Register(tool)
	pipeline := toolExecutionPipeline{executionGate: gate, registry: reg}
	done := make(chan error, 1)
	go func() {
		_, err := pipeline.execute(t.Context(), message.ToolCall{ID: "call-1", Name: tool.Name(), Args: json.RawMessage(`{}`)}, false)
		done <- err
	}()
	var running context.Context
	select {
	case running = <-tool.started:
	case err := <-done:
		t.Fatalf("execute returned before start: %v", err)
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	gate.Freeze()
	if running.Err() != nil {
		t.Fatal("freeze cancelled started tool")
	}
	close(tool.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.execute(t.Context(), message.ToolCall{ID: "call-2", Name: tool.Name(), Args: json.RawMessage(`{}`)}, false); !executiongate.IsPaused(err) {
		t.Fatalf("new execution error=%v", err)
	}
}

func TestToolPauseAbortsPreparationBeforeStartedJournal(t *testing.T) {
	gate := &executiongate.Gate{}
	reg := tools.NewRegistry()
	reg.Register(dummyTool{name: "Dummy"})
	preparing := make(chan struct{})
	done := make(chan error, 1)
	journal := 0
	pipeline := toolExecutionPipeline{executionGate: gate, registry: reg, appendToolActivity: func(recovery.ToolActivityRecord) error { journal++; return nil }, fireHook: func(ctx context.Context, _ string, _ uint64, _ string, _ map[string]any) (*hook.Result, error) {
		close(preparing)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	go func() {
		_, err := pipeline.execute(t.Context(), message.ToolCall{ID: "call-1", Name: "Dummy", Args: json.RawMessage(`{}`)}, true)
		done <- err
	}()
	<-preparing
	gate.Freeze()
	if err := <-done; !executiongate.IsPaused(err) {
		t.Fatalf("preparation error=%v", err)
	}
	if journal != 0 {
		t.Fatal("unstarted execution wrote started journal")
	}
}

func TestStreamingPauseSettlesDeferredAndPreservesStarted(t *testing.T) {
	gate := &executiongate.Gate{}
	started := make(chan string, 4)
	release := make(chan struct{})
	executor := NewStreamingToolExecutor(1, t.Context(), nil, func(ctx context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		started <- call.ID
		select {
		case <-release:
			return ToolExecutionResult{Result: "settled", EffectiveArgsJSON: string(call.Args)}, nil
		case <-ctx.Done():
			return ToolExecutionResult{}, ctx.Err()
		}
	})
	executor.SetExecutionGate(gate)
	for _, id := range []string{"one", "two", "three", "four"} {
		if !executor.Start(message.ToolCall{ID: id, Name: "Dummy", Args: json.RawMessage(`{}`)}) {
			t.Fatal("start failed")
		}
	}
	for range 4 {
		<-started
	}
	deferred := message.ToolCall{ID: "five", Name: "Dummy", Args: json.RawMessage(`{}`)}
	if !executor.Start(deferred) {
		t.Fatal("deferred start failed")
	}
	gate.Freeze()
	payload, ok, _ := executor.Promote(deferred)
	if !ok || !executiongate.IsPaused(payload.Error) || payload.RecoveryState != message.ToolRecoveryStateNotStarted {
		t.Fatalf("deferred result=%#v", payload)
	}
	close(release)
	if err := executor.WaitForSessionPause(t.Context()); err != nil {
		t.Fatal(err)
	}
	payload, ok, _ = executor.Promote(message.ToolCall{ID: "one", Name: "Dummy", Args: json.RawMessage(`{}`)})
	if !ok || payload.Error != nil || payload.Result != "settled" {
		t.Fatalf("started result=%#v", payload)
	}
}

func TestToolPausePreservesStartedFailure(t *testing.T) {
	gate := &executiongate.Gate{}
	original := errors.New("execution failed")
	tool := &pauseProbeTool{started: make(chan context.Context, 1), release: make(chan struct{}), returnErr: original}
	reg := tools.NewRegistry()
	reg.Register(tool)
	pipeline := toolExecutionPipeline{executionGate: gate, registry: reg}
	done := make(chan error, 1)
	go func() {
		_, err := pipeline.execute(t.Context(), message.ToolCall{ID: "call-1", Name: tool.Name(), Args: json.RawMessage(`{}`)}, false)
		done <- err
	}()
	<-tool.started
	gate.Freeze()
	close(tool.release)
	if err := <-done; !errors.Is(err, original) || executiongate.IsPaused(err) {
		t.Fatalf("started failure reclassified as unstarted: %v", err)
	}
}

func TestSubPauseLoopPersistsResponseAndDefersComplete(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.executionGate = &executiongate.Gate{}
	sub.setState(SubAgentStateRunning, "working")
	sub.llmRequestInFlight.Store(true)
	sub.pauseRequestStarted.Store(true)
	parent.executionGate.Freeze()
	sub.startRunLoop()
	sub.llmCh <- &llmResult{turnID: sub.turn.ID, resp: &message.Response{Content: "response retained", StopReason: "tool_use", ToolCalls: []message.ToolCall{{ID: "complete-1", Name: tools.NameComplete, Args: json.RawMessage(`{"summary":"done"}`)}}}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	capture, err := sub.DrainSessionPause(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capture.LLMInFlight || capture.ToolInFlight != 0 {
		t.Fatalf("drain capture=%#v", capture)
	}
	msgs := sub.ctxMgr.Snapshot()
	sawResponse, sawUnstarted := false, false
	for _, msg := range msgs {
		if msg.Role == message.RoleAssistant && msg.Content == "response retained" {
			sawResponse = true
		}
		if msg.Role == message.RoleTool && msg.ToolCallID == "complete-1" && msg.ToolRecoveryState == message.ToolRecoveryStateNotStarted {
			sawUnstarted = true
		}
	}
	if !sawResponse || !sawUnstarted {
		t.Fatalf("response or deferred complete missing: %#v", msgs)
	}
	if sub.State() != SubAgentStateRunning {
		t.Fatalf("pause invented terminal task state: %v", sub.State())
	}
	if err := sub.StopAfterSessionPause(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSubRestoredInputsWaitForNewFollowupAndShareFirstRequest(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.executionGate = &executiongate.Gate{}
	provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{Content: "response", StopReason: "stop"}, holdAfterStreams: true}}, streamedCh: make(chan struct{}), releaseCh: make(chan struct{})}
	client := testLLMClientWithProvider(provider)
	client.SetExecutionGate(parent.executionGate, sub.taskID)
	sub.llmClient = client
	sub.turn = nil
	sub.RestorePauseInputs([]pendingUserMessage{{InputID: "restored-input", AcceptedOrder: 1, Content: "original pending input", Stage: recovery.PendingInputStageQueued}})
	sub.startRunLoop()
	if requests, _ := provider.snapshot(); len(requests) != 0 {
		t.Fatal("restored input started model execution")
	}
	if !sub.InjectUserMessage("new constraint") {
		t.Fatal("followup rejected")
	}
	select {
	case <-provider.streamedCh:
	case <-time.After(time.Second):
		t.Fatal("explicit followup did not start request")
	}
	requests, _ := provider.snapshot()
	if len(requests) != 1 {
		t.Fatalf("request count=%d", len(requests))
	}
	var texts []string
	for _, msg := range requests[0] {
		if msg.Role == message.RoleUser {
			texts = append(texts, msg.Content)
		}
	}
	if len(texts) < 2 || texts[len(texts)-2] != "original pending input" || texts[len(texts)-1] != "new constraint" {
		t.Fatalf("first request user order=%#v", texts)
	}
	parent.executionGate.Freeze()
	close(provider.releaseCh)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := sub.DrainSessionPause(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sub.StopAfterSessionPause(ctx); err != nil {
		t.Fatal(err)
	}
}
