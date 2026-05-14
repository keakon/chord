package agent

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestCallLLMShowsKeySwitchToastOnFirstToolCallToken(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("primary-prov", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"primary-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"key-1", "key-2"})

	providerImpl := &blockingStreamProvider{
		streamedCh: make(chan struct{}),
		releaseCh:  make(chan struct{}),
		calls: []scriptedStreamCall{
			{err: io.ErrUnexpectedEOF},
			{
				streams: []message.StreamDelta{{
					Type: "tool_use_start",
					ToolCall: &message.ToolCallDelta{
						ID:    "call-1",
						Name:  "Read",
						Input: `{"path":"README.md"}`,
					},
				}},
				resp: &message.Response{
					ToolCalls: []message.ToolCall{{
						ID:   "call-1",
						Name: "Read",
						Args: []byte(`{"path":"README.md"}`),
					}},
					StopReason: "tool_use",
				},
				holdAfterStreams: true,
			},
		},
	}

	client := llm.NewClient(providerCfg, providerImpl, "primary-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "primary-model", 128000, "primary-prov/primary-model")

	done := make(chan error, 1)
	go func() {
		_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
		done <- err
	}()

	<-providerImpl.streamedCh
	toast := waitForToastEvent(t, a.Events(), "Switched key")
	if toast.Level != "info" {
		t.Fatalf("toast.Level = %q, want info", toast.Level)
	}

	close(providerImpl.releaseCh)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("callLLM: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for callLLM to finish")
	}
}
func TestCallLLMEmitsToolArgCompletionUpdateOnToolUseEnd(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read", Input: `{"path":"READ`}},
				{Type: "tool_use_delta", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read", Input: `ME.md"}`}},
				{Type: "tool_use_end", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read"}},
			},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "Read",
					Args: []byte(`{"path":"README.md"}`),
				}},
				StopReason: "tool_use",
			},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var sawDone bool
	for _, raw := range events {
		update, ok := raw.(ToolCallUpdateEvent)
		if !ok || update.ID != "call-1" {
			continue
		}
		if update.ArgsStreamingDone {
			sawDone = true
			if update.ArgsJSON != `{"path":"README.md"}` {
				t.Fatalf("done ArgsJSON = %q, want final accumulated args", update.ArgsJSON)
			}
		}
	}
	if !sawDone {
		t.Fatal("expected tool arg completion update on tool_use_end")
	}
}
func TestHandleLLMResponseDoesNotPromoteReadOnlyShellBehindAskGatedCommit(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.tools.Register(tools.NewShellTool("bash"))
	a.ruleset = permission.Ruleset{
		{Permission: tools.NameShell, Pattern: "git commit *", Action: permission.ActionAsk},
		{Permission: tools.NameShell, Pattern: "git status *", Action: permission.ActionAllow},
	}
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turn := a.turn
	turn.recordStreamingToolCall(PendingToolCall{CallID: "call-1", Name: tools.NameShell, ArgsJSON: `{"command":"git commit -m fix"}`})
	turn.recordStreamingToolCall(PendingToolCall{CallID: "call-2", Name: tools.NameShell, ArgsJSON: `{"command":"git status --short"}`})

	statusStarted := make(chan struct{}, 1)
	turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, turn.Ctx, nil, func(_ context.Context, tc message.ToolCall) (ToolExecutionResult, error) {
		if tc.ID == "call-2" {
			statusStarted <- struct{}{}
			return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "stale status"}, nil
		}
		return ToolExecutionResult{EffectiveArgsJSON: string(tc.Args), Result: "commit"}, nil
	})
	if !turn.streamingToolExec.Start(message.ToolCall{ID: "call-2", Name: tools.NameShell, Args: json.RawMessage(`{"command":"git status --short"}`)}) {
		t.Fatal("expected speculative status shell start")
	}
	select {
	case <-statusStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for speculative status shell start")
	}

	confirmEntered := make(chan struct{})
	releaseConfirm := make(chan struct{})
	a.confirmFn = func(ctx context.Context, toolName, argsJSON string, needsApproval []string, alreadyAllowed []string) (ConfirmResponse, error) {
		close(confirmEntered)
		<-releaseConfirm
		return ConfirmResponse{Approved: true}, nil
	}

	payload := &LLMResponsePayload{
		ToolCalls: []message.ToolCall{
			{ID: "call-1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"git commit -m fix"}`)},
			{ID: "call-2", Name: tools.NameShell, Args: json.RawMessage(`{"command":"git status --short"}`)},
		},
		StopReason: "tool_use",
	}
	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: turn.ID, Payload: payload})

	select {
	case <-confirmEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for commit confirmation path")
	}

	for _, raw := range drainAgentEvents(a.Events()) {
		if evt, ok := raw.(ToolResultEvent); ok && evt.Name == tools.NameShell && evt.CallID == "call-2" {
			t.Fatalf("unexpected status promotion while commit approval pending: %+v", evt)
		}
	}
	close(releaseConfirm)
}

func TestModelNameFromRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"meowoo/glm-5.1", "glm-5.1"},
		{"qt/gpt-5.5", "gpt-5.5"},
		{"a/b/c", "c"},          // nested: last segment wins
		{"glm-5.1", "glm-5.1"},  // bare name
		{"/glm-5.1", "glm-5.1"}, // leading slash
		{"", ""},                // empty
	}
	for _, tt := range tests {
		got := modelNameFromRef(tt.input)
		if got != tt.want {
			t.Errorf("modelNameFromRef(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
