package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
						Name: "read",
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

func TestHandleLLMResponseNormalizesInvisibleAssistantContent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	a.handleLLMResponse(Event{
		Type:   EventLLMResponse,
		TurnID: a.turn.ID,
		Payload: &LLMResponsePayload{
			Content:    "\u200b\u200b",
			ToolCalls:  []message.ToolCall{{ID: "call-1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)}},
			StopReason: "tool_calls",
		},
	})

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		t.Fatal("expected assistant message in context")
	}
	last := msgs[len(msgs)-1]
	if last.Content != "" {
		t.Fatalf("assistant content = %q, want canonical empty string", last.Content)
	}
	if len(last.ToolCalls) != 1 || last.ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool calls = %+v, want call-1 preserved", last.ToolCalls)
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
				{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "read", Input: `{"path":"READ`}},
				{Type: "tool_use_delta", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "read", Input: `ME.md"}`}},
				{Type: "tool_use_end", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "read"}},
			},
			resp: &message.Response{
				ToolCalls: []message.ToolCall{{
					ID:   "call-1",
					Name: "read",
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
	a.confirmFn = func(ctx context.Context, toolName, argsJSON string, needsApproval []string, alreadyAllowed []string, needsApprovalRules []string, alreadyAllowedRules []string) (ConfirmResponse, error) {
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
	// The approved commit is executed by the real shell tool, which creates a
	// job log under the session directory. Wait for that new job to reach a
	// terminal state before returning so t.TempDir cleanup cannot race the
	// still-open log write and fail with "directory not empty". Jobs left over
	// from earlier -count iterations share the global registry, so only wait on
	// ids that appear after this point.
	preExisting := map[string]struct{}{}
	for _, j := range tools.SnapshotJobs() {
		preExisting[j.ID] = struct{}{}
	}
	close(releaseConfirm)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range tools.SnapshotJobs() {
			if _, seen := preExisting[j.ID]; seen {
				continue
			}
			if j.Command == "git commit -m fix" && !j.FinishedAt.IsZero() {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the approved commit job to finish")
}

func TestMainLLMResponseRejectsExcessiveToolCalls(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	calls := make([]message.ToolCall, maxToolCallsPerResponse+1)
	for i := range calls {
		calls[i] = message.ToolCall{ID: fmt.Sprintf("call-%d", i), Name: tools.NameRead, Args: json.RawMessage(`{"path":"README.md"}`)}
	}
	a.handleLLMResponse(Event{
		Type:   EventLLMResponse,
		TurnID: a.turn.ID,
		Payload: &LLMResponsePayload{
			ToolCalls:  calls,
			StopReason: "tool_use",
		},
	})
	if got := len(a.ctxMgr.Snapshot()); got != 0 {
		t.Fatalf("persisted messages = %d, want 0 for rejected response", got)
	}
	if a.turn != nil {
		t.Fatal("turn remained active after oversized response")
	}
}

func TestModelNameFromRef(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sample/glm-5.1", "glm-5.1"},
		{"provider/gpt-5.5", "gpt-5.5"},
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
