package agent

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
)

type scenarioTool struct {
	dummyTool
	calls atomic.Int64
}

func (s *scenarioTool) Execute(context.Context, json.RawMessage) (string, error) {
	s.calls.Add(1)
	return "sample result", nil
}

func scenarioAgent(t *testing.T, calls ...scriptedStreamCall) (*MainAgent, *blockingStreamProvider) {
	t.Helper()
	a := newReadyTestMainAgent(t)
	p := &blockingStreamProvider{calls: calls}
	cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, Models: map[string]config.ModelConfig{"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}}}}, []string{"test-key"})
	a.swapLLMClientWithRef(llm.NewClient(cfg, p, "test-model", 4096, "sys"), "test-model", 128000, "sample/test-model")
	return a, p
}

func assertScenarioPersistence(t *testing.T, a *MainAgent) {
	t.Helper()
	a.flushPersist()
	got, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
	if err != nil {
		t.Fatal(err)
	}
	want := a.GetMessages()
	if len(got) != len(want) {
		t.Fatalf("durable messages = %d, context = %d", len(got), len(want))
	}
	for idx := range want {
		if got[idx].Role != want[idx].Role || got[idx].Content != want[idx].Content || got[idx].ToolCallID != want[idx].ToolCallID || !slices.EqualFunc(got[idx].ToolCalls, want[idx].ToolCalls, func(a, b message.ToolCall) bool {
			return a.ID == b.ID && a.Name == b.Name && string(a.Args) == string(b.Args)
		}) {
			t.Fatalf("durable message %d differs", idx)
		}
	}
}

func TestScenarioToolChainAndPermissionBoundary(t *testing.T) {
	for _, action := range []permission.Action{permission.ActionAllow, permission.ActionAsk} {
		t.Run(string(action), func(t *testing.T) {
			a, p := scenarioAgent(t,
				scriptedStreamCall{resp: &message.Response{ToolCalls: []message.ToolCall{{ID: "call-1", Name: "sample_tool", Args: json.RawMessage(`{"value":"sample"}`)}}, StopReason: "tool_use"}},
				scriptedStreamCall{resp: &message.Response{Content: "complete", StopReason: "end_turn"}},
			)
			tool := new(scenarioTool)
			tool.name = "sample_tool"
			a.tools.Register(tool)
			a.ruleset = permission.Ruleset{{Permission: "sample_tool", Pattern: "*", Action: action}}
			entered, release := make(chan struct{}), make(chan struct{})
			a.confirmFn = func(ctx context.Context, _ string, _ string, _, _, _, _ []string) (ConfirmResponse, error) {
				close(entered)
				select {
				case <-release:
					return ConfirmResponse{Approved: true}, nil
				case <-ctx.Done():
					return ConfirmResponse{}, ctx.Err()
				}
			}
			startMainAgentLoopForTest(t, a)
			a.SendUserMessageWithReceipt("run sample", "input-1")
			if action == permission.ActionAsk {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("confirmation not reached")
				}
				msgs, _ := p.snapshot()
				if tool.calls.Load() != 0 || len(msgs) != 1 {
					t.Fatal("work crossed permission boundary")
				}
				close(release)
			}
			collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
				for _, ev := range events {
					if _, ok := ev.(GlobalIdleEvent); ok {
						return true
					}
				}
				return false
			})
			msgs, _ := p.snapshot()
			if len(msgs) != 2 || tool.calls.Load() != 1 {
				t.Fatalf("requests = %d, tool calls = %d", len(msgs), tool.calls.Load())
			}
			found := false
			for _, msg := range msgs[1] {
				if msg.ToolCallID == "call-1" && msg.Content == "sample result" {
					found = true
				}
			}
			if !found {
				t.Fatal("continuation missed tool result")
			}
			assertScenarioPersistence(t, a)
			journal, err := a.recoveryManager().LoadToolActivity()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := journal[recovery.ToolActivityKey{AgentID: identity.MainAgentID, CallID: "call-1"}]; !ok || len(journal) != 1 {
				t.Fatalf("started journal = %#v", journal)
			}
		})
	}
}

func TestScenarioCancellationAndResume(t *testing.T) {
	a, p := scenarioAgent(t,
		scriptedStreamCall{holdAfterStreams: true},
		scriptedStreamCall{resp: &message.Response{Content: "resumed", StopReason: "end_turn"}},
	)
	p.streamedCh, p.releaseCh = make(chan struct{}), make(chan struct{})
	startMainAgentLoopForTest(t, a)
	a.SendUserMessageWithReceipt("first", "input-1")
	select {
	case <-p.streamedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("provider gate not reached")
	}
	if !a.CancelCurrentTurn() {
		t.Fatal("active request not cancelled")
	}
	collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		for _, ev := range events {
			if _, ok := ev.(GlobalIdleEvent); ok {
				return true
			}
		}
		return false
	})
	a.SendUserMessageWithReceipt("second", "input-2")
	collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		for _, ev := range events {
			if _, ok := ev.(GlobalIdleEvent); ok {
				return true
			}
		}
		return false
	})
	msgs, _ := p.snapshot()
	if len(msgs) != 2 {
		t.Fatalf("requests = %d", len(msgs))
	}
	if last := a.GetMessages(); len(last) != 3 || last[2].Content != "resumed" {
		t.Fatalf("resume transcript = %#v", last)
	}
	assertScenarioPersistence(t, a)
}
