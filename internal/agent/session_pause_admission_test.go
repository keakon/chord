package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestSubNativeFreezePreservesCompletedResponse(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	gate := parent.sessionExecutionGate()
	provider := &hostedScriptProvider{respond: func(_ int, _ context.Context) (*message.Response, error) {
		return &message.Response{Content: "Searching.", StopReason: "pause_turn", Usage: &message.TokenUsage{InputTokens: 123, OutputTokens: 7}, Hosted: &message.HostedObservation{Items: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Searching."}`)}}}, nil
	}}
	model := config.ModelConfig{NativeWebSearch: &config.NativeWebSearchConfig{Contract: config.NativeWebSearchMessages, APIURL: "https://example.invalid/messages", Preauthorized: true}}
	pc := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: model.NativeWebSearch.APIURL, Models: map[string]config.ModelConfig{"test-model": model}}, []string{"key"})
	client := llm.NewClient(pc, provider, "test-model", 1024, "")
	defer client.Close()
	client.SetExecutionGate(gate, sub.taskID)
	policy := nativePolicy(sub.nativeJournal(), func(string) bool { return true }, nil)
	finish := policy.Finish
	policy.Finish = func(id string, outcome message.NativeRequestOutcome, resp *message.Response, err error) error {
		if err := finish(id, outcome, resp, err); err != nil {
			return err
		}
		gate.Freeze()
		return nil
	}
	resp, err := client.CompleteStreamWithOptions(t.Context(), nil, nil, nil, llm.CompleteStreamOptions{NativeTools: policy})
	if !executiongate.IsPaused(err) || resp == nil || resp.NativeTools == nil || provider.callCount() != 1 {
		t.Fatalf("unexpected native outcome: resp=%+v err=%v", resp, err)
	}
	before := sub.ctxMgr.MessageCount()
	sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, resp: resp, err: err})
	if sub.ctxMgr.MessageCount() != before+1 {
		t.Fatal("completed native response was not recorded once")
	}
	msg := sub.ctxMgr.Snapshot()[before]
	if msg.Content != "Searching." || msg.Usage == nil || msg.Usage.InputTokens != 123 || sub.ctxMgr.LastInputTokens() != 123 {
		t.Fatalf("completed native response lost text or usage: %+v", msg)
	}
	persisted, loadErr := sub.recoveryManager().LoadMessages(sub.instanceID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(persisted) != before+1 || persisted[before].Content != msg.Content || persisted[before].Usage == nil || persisted[before].Usage.InputTokens != 123 {
		t.Fatalf("completed native response not durable: %+v", persisted)
	}
	recovered, restoreErr := sub.nativeJournal().Restore(persisted, func(message.Message) error { return nil })
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if len(recovered) != before+1 || recovered[len(recovered)-1].NativeTools == nil || recovered[len(recovered)-1].NativeTools.OutcomeUnknown {
		t.Fatalf("completed native receipt was lost or classified unknown: %+v", recovered)
	}
	if provider.callCount() != 1 {
		t.Fatal("paused continuation reached the provider")
	}
}

func TestSubStartedHostedToolPreservesUnknownOutcome(t *testing.T) {
	for _, speculative := range []bool{false, true} {
		name := "finalized"
		if speculative {
			name = "speculative"
		}
		t.Run(name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			gate := parent.sessionExecutionGate()
			tool := &pauseProbeTool{started: make(chan context.Context, 1), release: make(chan struct{}), returnErr: &llm.HostedOutcomeUnknownError{Cause: &executiongate.ErrSessionPaused{}}}
			sub.tools.Register(tool)
			sub.setRuleset(permission.Ruleset{})
			prepareSubRequestDirectoryFixture(t, sub)
			runtime := sub.directoryInstructions.Load()
			runtime.attempt.Store(&directoryInstructionAttempt{request: directoryInstructionRequest{Binding: runtime.binding, RequestSeq: 1, AttemptSeq: 1}, gate: &directoryInstructionGate{}})
			call := message.ToolCall{ID: "started-call", Name: tool.Name(), Args: json.RawMessage(`{}`)}
			sub.ctxMgr.Append(message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{call}})
			sub.turn.toolExecutionBatches = []toolExecutionBatch{{Calls: []message.ToolCall{call}}}
			if speculative {
				sub.turn.streamingToolExec = NewStreamingToolExecutor(sub.turn.ID, sub.turn.Ctx, nil, sub.executeToolCallSpeculative)
				sub.turn.streamingToolExec.SetExecutionGate(gate)
				if !sub.turn.streamingToolExec.Start(call) {
					t.Fatal("speculative tool was not scheduled")
				}
			} else {
				sub.startNextToolBatch(sub.turn)
			}
			select {
			case <-tool.started:
			case <-time.After(3 * time.Second):
				t.Fatal("tool did not enter Execute")
			}
			gate.Freeze()
			close(tool.release)
			if speculative {
				sub.startNextToolBatch(sub.turn)
			}
			var result *toolResult
			select {
			case result = <-sub.toolCh:
			case <-time.After(3 * time.Second):
				t.Fatal("started tool did not settle")
			}
			if result.RecoveryState != message.ToolRecoveryStateOutcomeUnknown {
				t.Fatalf("started execution recovery state = %q", result.RecoveryState)
			}
			sub.handleToolResult(result)
			for _, msg := range sub.ctxMgr.Snapshot() {
				if msg.Role == message.RoleTool {
					if msg.ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
						t.Fatalf("canonical recovery state = %q", msg.ToolRecoveryState)
					}
					return
				}
			}
			t.Fatal("missing tool result")
		})
	}
}

func TestFrozenPipelineReportsUnstartedExecution(t *testing.T) {
	for _, speculative := range []bool{false, true} {
		name := "finalized"
		if speculative {
			name = "speculative"
		}
		t.Run(name, func(t *testing.T) {
			gate := &executiongate.Gate{}
			gate.Freeze()
			registry := tools.NewRegistry()
			registry.Register(dummyTool{name: "sample_tool"})
			pipeline := toolExecutionPipeline{executionGate: gate, registry: registry}
			execute := pipeline.executeSpeculative
			if !speculative {
				execute = func(ctx context.Context, call message.ToolCall) (ToolExecutionResult, error) {
					return pipeline.execute(ctx, call, false)
				}
			}
			result, err := execute(t.Context(), message.ToolCall{ID: "call-1", Name: "sample_tool", Args: json.RawMessage(`{}`)})
			if result.ExecutionStarted || result.recoveryState(err) != message.ToolRecoveryStateNotStarted {
				t.Fatalf("unstarted execution lost: %+v error=%v", result, err)
			}
		})
	}
}

func TestMainFrozenToolBatchPersistsNotStarted(t *testing.T) {
	a := newGateTestMainAgent(t)
	a.tools.Register(dummyTool{name: "sample_tool"})
	a.ruleset = permission.Ruleset{{Permission: "sample_tool", Pattern: "*", Action: permission.ActionAllow}}
	a.newTurn()
	engine := gateTestHookEngineOf(a)
	engine.hasHooks = true
	engine.modify = func(env hook.Envelope) *hook.Result {
		if env.Point == hook.OnToolCall {
			a.sessionExecutionGate().Freeze()
		}
		return nil
	}
	call := message.ToolCall{ID: "paused-call", Name: "sample_tool", Args: json.RawMessage(`{}`)}
	if !a.promoteStreamingToolBatch(a.turn, toolExecutionBatch{Calls: []message.ToolCall{call}}) {
		t.Fatal("batch was not scheduled")
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-a.eventCh:
			if event.Type != EventToolResult {
				continue
			}
			result := event.Payload.(*ToolResultPayload)
			if !executiongate.IsPaused(result.Error) {
				t.Fatalf("unexpected error: %v", result.Error)
			}
			msg := a.buildToolResultMessage(result, result.composedTexts.Context, nil, true, nil)
			if msg.ToolRecoveryState != message.ToolRecoveryStateNotStarted {
				t.Fatal("main batch rejected before Execute is not persisted as not_started")
			}
			return
		case <-deadline:
			t.Fatal("no tool result")
		}
	}
}

func TestSubPausedNilResponsePreservesInterruptedText(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.sessionExecutionGate().Freeze()
	sub.turn.appendPartialText("Partial response.")
	sub.handleLLMResponse(&llmResult{turnID: sub.turn.ID, err: &executiongate.ErrSessionPaused{}})
	history := sub.ctxMgr.Snapshot()
	if len(history) != 1 || history[0].Content != "Partial response." || history[0].StopReason != "interrupted" {
		t.Fatalf("interrupted response lost: %+v", history)
	}
}
