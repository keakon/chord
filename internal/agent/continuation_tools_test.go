package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestContinuationToolsRespectMainAndSubPersistenceBarriers(t *testing.T) {
	t.Parallel()
	for _, subagent := range []bool{false, true} {
		for _, abandon := range []bool{false, true} {
			t.Run(fmt.Sprintf("sub=%v/abandon=%v", subagent, abandon), func(t *testing.T) {
				var a *MainAgent
				var sub *SubAgent
				var turn *Turn
				var registry *tools.Registry
				if subagent {
					a, sub = newMixedBatchTestSubAgent(t)
					a.installRecoveryManager(newBrokenPathRecoveryManager(t))
					turn, registry = sub.turn, sub.tools
				} else {
					a = newReadyTestMainAgent(t)
					a.installRecoveryManager(newBrokenPathRecoveryManager(t))
					a.newTurn("")
					turn, registry = a.turn, a.tools
				}
				tool := &barrierRecordingTool{name: tools.NameWrite}
				registry.Register(tool)
				turn.streamingToolExec = NewStreamingToolExecutor(turn.ID, context.Background(), nil, func(context.Context, message.ToolCall) (ToolExecutionResult, error) {
					t.Error("continuation tool executed speculatively")
					return ToolExecutionResult{}, nil
				})
				reducer := streamToolDeltaReducer{turn: turn, registry: registry}
				toolEvents := 0
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if requests == 1 {
						fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"write","args":{"path":"sample.txt","content":"sample"}}}]},"continuationToken":"T1","finishReason":"CONTINUATION"}]}`+"\n\n")
						return
					}
					if abandon {
						fmt.Fprint(w, "data: {invalid}\n\n")
						return
					}
					fmt.Fprint(w, `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1}}`+"\n\n")
				}))
				defer server.Close()
				cfg := llm.NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: server.URL + "/v1beta", Models: map[string]config.ModelConfig{"model-1": {Compat: &config.ModelCompatConfig{GenerateContent: &config.GenerateContentCompatConfig{AutomaticContinuation: new(true)}}}}}, []string{"test-key"})
				provider, err := llm.NewGeminiProviderWithClient(cfg, server.Client(), "")
				if err != nil {
					t.Fatal(err)
				}
				resp, err := provider.CompleteStream(t.Context(), "test-key", "model-1", "", nil, nil, 100, llm.RequestTuning{}, func(d message.StreamDelta) {
					if d.ToolCall != nil {
						toolEvents++
						if !d.ToolCall.DeferExecution {
							t.Error("missing deferral")
						}
					}
					reducer.Handle(d)
				})
				if abandon {
					if err == nil || resp != nil || toolEvents != 0 {
						t.Fatalf("resp=%+v err=%v tools=%d", resp, err, toolEvents)
					}
				} else {
					if err != nil || len(resp.ToolCalls) != 1 || toolEvents != 3 {
						t.Fatalf("resp=%+v err=%v tools=%d", resp, err, toolEvents)
					}
					if subagent {
						sub.handleLLMResponse(&llmResult{turnID: turn.ID, resp: resp})
					} else {
						a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: turn.ID, Payload: &LLMResponsePayload{ToolCalls: resp.ToolCalls, GeminiParts: resp.GeminiParts, StopReason: resp.StopReason, Usage: resp.Usage}})
						awaitSyntheticToolResult(t, a)
					}
					if turn.BarrierFailureRounds != 1 {
						t.Fatalf("barrier failures=%d", turn.BarrierFailureRounds)
					}
				}
				if tool.ran.Load() {
					t.Fatal("tool body ran without durable intent")
				}
				turn.streamingToolExec.mu.Lock()
				entries := len(turn.streamingToolExec.entries)
				turn.streamingToolExec.mu.Unlock()
				if entries != 0 {
					t.Fatal("speculative tool scheduled")
				}
			})
		}
	}
}
