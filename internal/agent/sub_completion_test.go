package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestCompletionDefersForQueuedInputAcrossDeliveryPaths(t *testing.T) {
	for _, degraded := range []bool{false, true} {
		for _, mixed := range []bool{false, true} {
			for _, joined := range []bool{false, true} {
				t.Run(fmt.Sprintf("degraded=%t/mixed=%t/joined=%t", degraded, mixed, joined), func(t *testing.T) {
					parent, sub := newMixedBatchTestSubAgent(t)
					sub.turn.Ctx = sub.parentCtx
					sub.turn.SubAgentCompletionRecoveryCount = 1
					providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
						Type:   config.ProviderTypeChatCompletions,
						Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
					}, []string{"key"})
					provider := &blockingStreamProvider{calls: []scriptedStreamCall{{resp: &message.Response{
						ToolCalls: convertCalls([]messageToolCall{mustJSONToolCall(t, "next-complete", tools.NameComplete, map[string]any{"summary": "Updated result"})}),
					}}}}
					sub.llmClient = llm.NewClient(providerCfg, provider, "model", 1024, "sys")
					if joined {
						parent.setTaskRecords(map[string]*DurableTaskRecord{
							"task-child": {TaskID: "task-child", OwnerTaskID: sub.taskID, JoinToOwner: true, State: string(SubAgentStateRunning)},
						})
					}
					sub.inputCh = make(chan pendingUserMessage, 1)
					for _, text := range []string{"Update the requirements", "Check the additional case"} {
						if !sub.enqueueUserMessage(pendingUserMessage{Content: text}) {
							t.Fatal("failed to enqueue input")
						}
					}
					args := map[string]any{"summary": "Original result"}
					if degraded {
						args["result"] = map[string]any{"value": 1}
					}
					calls := []messageToolCall{mustJSONToolCall(t, "complete-call", tools.NameComplete, args)}
					if mixed {
						calls = append(calls, mustJSONToolCall(t, "sibling-call", "Dummy", map[string]any{"value": "sample"}))
					}
					sub.handleLLMResponse(&llmResult{turnID: 1, resp: &message.Response{ToolCalls: convertCalls(calls)}})
					if mixed {
						if !sub.TryEnqueueContextAppend(message.Message{Content: "Additional context"}) {
							t.Fatal("failed to enqueue context")
						}
						sub.handleToolResult(&toolResult{CallID: "sibling-call", Name: "Dummy", ArgsJSON: `{"value":"sample"}`, Result: "ok", TurnID: 1})
					}
					select {
					case evt := <-parent.eventCh:
						t.Fatalf("completed or deferred to children before handling input: %#v", evt)
					default:
					}
					if sub.PendingCompleteIntent() != nil {
						t.Fatal("kept a completion invalidated by new input")
					}
					next := waitForSubAgentLLMResult(t, sub, time.Second)
					if next.err != nil {
						t.Fatal(next.err)
					}
					seen, _ := provider.snapshot()
					if len(seen) != 1 {
						t.Fatalf("continuation requests = %d, want one", len(seen))
					}
					messages := seen[0]
					resultIndex, firstInputIndex, secondInputIndex := -1, -1, -1
					for i, msg := range messages {
						if msg.Role == "tool" && msg.ToolCallID == "complete-call" && strings.Contains(msg.Content, "received new user input") {
							resultIndex = i
						}
						if msg.Role == "user" && msg.Content == "Update the requirements" {
							firstInputIndex = i
						}
						if msg.Role == "user" && msg.Content == "Check the additional case" {
							secondInputIndex = i
						}
					}
					if resultIndex < 0 || firstInputIndex <= resultIndex || secondInputIndex <= firstInputIndex {
						t.Fatalf("continuation lost tool pairing or FIFO input: %#v", messages)
					}
					if !joined {
						sub.handleLLMResponse(next)
						select {
						case evt := <-parent.eventCh:
							if evt.Type != EventAgentDone || evt.Payload.(*AgentResult).Summary != "Updated result" {
								t.Fatalf("completion = %#v, want updated result", evt)
							}
						case <-time.After(time.Second):
							t.Fatal("updated result did not complete")
						}
					}
				})
			}
		}
	}
}
