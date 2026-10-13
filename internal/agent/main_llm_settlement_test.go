package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPausedMainResponsePreservesCompletedOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload LLMResponsePayload
	}{
		{name: "truncated_tool", payload: LLMResponsePayload{Content: "Partial response", StopReason: "length", ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`)}}}},
		{name: "malformed_tool", payload: LLMResponsePayload{Content: "Response text", StopReason: "tool_calls", ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`)}}}},
		{name: "truncated_thinking", payload: LLMResponsePayload{ReasoningContent: "Reasoning text", StopReason: "max_tokens", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "Reasoning text"}}}},
		{name: "native_malformed_tool", payload: LLMResponsePayload{Content: "Native response", StopReason: "tool_calls", NativeTools: &message.NativeToolHistory{}, ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.newTurn()
			turn := a.turn
			a.sessionExecutionGate().Freeze()
			tc.payload.Usage = &message.TokenUsage{InputTokens: 123, OutputTokens: 45}
			a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: turn.ID, Payload: &tc.payload})
			if a.turn != turn || turn.InLengthRecovery || a.pendingRecoveryPrompt != "" {
				t.Fatal("paused response entered recovery or discarded its turn")
			}
			msgs := a.ctxMgr.Snapshot()
			if len(msgs) != 1 {
				t.Fatalf("messages=%#v", msgs)
			}
			m := msgs[0]
			if m.Role != message.RoleAssistant || m.Content != tc.payload.Content || m.ReasoningContent != tc.payload.ReasoningContent || m.Usage == nil || m.Usage.OutputTokens != 45 || len(m.ToolCalls) != len(tc.payload.ToolCalls) || len(m.ThinkingBlocks) != len(tc.payload.ThinkingBlocks) {
				t.Fatalf("completed response changed: %#v", m)
			}
			if tc.payload.NativeTools != nil && (m.NativeTools == nil || m.NativeTools.OutcomeUnknown) {
				t.Fatal("completed native receipt was lost or marked unknown")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			for turn.PendingToolCalls.Load() > 0 {
				evt, err := a.nextEvent(ctx)
				if err != nil {
					t.Fatal(err)
				}
				a.dispatch(evt)
			}
			msgs = a.ctxMgr.Snapshot()
			if len(msgs) != 1+len(tc.payload.ToolCalls) {
				t.Fatalf("unsettled tools: %#v", msgs)
			}
			for _, m := range msgs[1:] {
				if m.Role != message.RoleTool || m.ToolRecoveryState != message.ToolRecoveryStateNotStarted {
					t.Fatalf("tool was not settled as not_started: %#v", m)
				}
			}
		})
	}
}

func TestPausedMainResponseSettlesStartedSpeculativeTool(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	turn := a.turn
	started, release := make(chan struct{}), make(chan struct{})
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, turn.Ctx, nil, func(ctx context.Context, call message.ToolCall) (ToolExecutionResult, error) {
		close(started)
		select {
		case <-release:
			return ToolExecutionResult{Result: "Completed tool result", EffectiveArgsJSON: string(call.Args), ExecutionStarted: true}, nil
		case <-ctx.Done():
			return ToolExecutionResult{}, ctx.Err()
		}
	})
	turn.streamingToolExec.SetExecutionGate(a.sessionExecutionGate())
	call := message.ToolCall{ID: "call-1", Name: "sample_tool", Args: json.RawMessage(`{}`)}
	if !turn.streamingToolExec.Start(call) {
		t.Fatal("speculative tool rejected")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("speculative tool did not start")
	}
	a.sessionExecutionGate().Freeze()
	payload := &LLMResponsePayload{Content: "Partial response", StopReason: "length", ToolCalls: []message.ToolCall{call, {ID: "call-2", Name: tools.NameShell, Args: json.RawMessage(`{"error":"malformed tool call arguments from model"}`)}}}
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: turn.ID, Payload: payload})
	close(release)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for turn.PendingToolCalls.Load() > 0 {
		evt, err := a.nextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a.dispatch(evt)
	}
	found := false
	for _, m := range a.ctxMgr.Snapshot() {
		if m.ToolCallID == call.ID {
			found = true
			if m.Content != "Completed tool result" || m.ToolRecoveryState != "" || m.ToolStatus != string(ToolResultStatusSuccess) {
				t.Fatalf("started result lost: %#v", m)
			}
		}
	}
	if !found {
		t.Fatal("started speculative result was not stored")
	}
}
