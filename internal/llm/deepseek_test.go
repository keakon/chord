package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// Exercise the client, normalization and HTTP serializer together: the first
// tool response may have no reasoning, and a later user turn must not discard
// earlier reasoning or change the selected generation effort.
func TestDeepSeekWireContractAcrossTurns(t *testing.T) {
	for _, wire := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeMessages} {
		for _, thinkingType := range []string{"enabled", "adaptive"} {
			t.Run(wire+"/"+thinkingType, func(t *testing.T) {
				requests := make(chan map[string]any, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- body
					w.Header().Set("Content-Type", "text/event-stream")
					if wire == config.ProviderTypeMessages {
						io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":10}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					} else {
						io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				}))
				defer server.Close()
				const model = "provider/deepseek-flash"
				cfg := NewProviderConfig("sample", config.ProviderConfig{
					Type: wire, APIURL: server.URL,
					Models: map[string]config.ModelConfig{model: {
						Thinking: &config.ThinkingConfig{Type: thinkingType, Effort: "high"},
						Variants: map[string]config.ModelVariant{"max": {Thinking: &config.ThinkingConfig{Effort: "max"}}},
						// A generic replay window must not weaken this model's contract.
						Compat: &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{ReasoningReplay: modelcompat.ReasoningReplayCurrentTurn}},
					}},
				}, []string{"test-key"})
				var impl Provider
				if wire == config.ProviderTypeMessages {
					var err error
					impl, err = NewAnthropicProviderWithClient(cfg, server.Client(), "")
					if err != nil {
						t.Fatal(err)
					}
				} else {
					impl = &OpenAIProvider{provider: cfg, client: server.Client()}
				}
				client := NewClient(cfg, impl, model, 2048, "")
				target := FallbackModel{ProviderConfig: cfg, ModelID: model, Variant: "max"}
				tuning := tuningForPoolTarget(target)
				provenance := &message.MessageProvenance{ProviderID: "sample", ModelID: model, WireFamily: providerWireFamily(cfg)}
				history := []message.Message{{Role: message.RoleUser, Content: "Inspect the file."}}
				tools := []message.ToolDefinition{{Name: "read", InputSchema: map[string]any{"type": "object"}}}
				const reasoning = "  inspect the result\n"
				for round := range 3 {
					if round == 1 {
						history = append(history,
							message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}}, Provenance: provenance},
							message.Message{Role: message.RoleTool, ToolCallID: "call-1", Content: "file contents"})
					}
					if round == 2 {
						step := message.Message{Role: message.RoleAssistant, Content: "Inspected.", Provenance: provenance}
						if wire == config.ProviderTypeMessages {
							step.ThinkingBlocks = []message.ThinkingBlock{{Thinking: reasoning, Signature: "opaque-signature"}}
						} else {
							step.ReasoningContent = reasoning
						}
						history = append(history, step, message.Message{Role: message.RoleUser, Content: "Continue."})
					}
					_, err := callCompleteStreamWithRetryForTest(client, context.Background(), cfg, impl, model, 2048, tuning, "max", history, tools, nil, false, nil, -2, &CallStatus{})
					if err != nil {
						t.Fatal(err)
					}
					body := <-requests
					if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "enabled"}) {
						t.Fatalf("thinking = %#v", body["thinking"])
					}
					if wire == config.ProviderTypeMessages {
						if !reflect.DeepEqual(body["output_config"], map[string]any{"effort": "max"}) {
							t.Fatalf("output_config = %#v", body["output_config"])
						}
					} else if body["reasoning_effort"] != "max" {
						t.Fatalf("effort = %#v", body["reasoning_effort"])
					}
					if body["max_tokens"] != float64(2048) {
						t.Fatalf("max_tokens = %v", body["max_tokens"])
					}
					if round != 2 {
						continue
					}
					found := 0
					for _, raw := range body["messages"].([]any) {
						m := raw.(map[string]any)
						if m["role"] != "assistant" {
							continue
						}
						if wire == config.ProviderTypeChatCompletions {
							value, present := m["reasoning_content"]
							if !present {
								t.Fatal("historical assistant lost reasoning field")
							}
							if value == reasoning {
								found++
							}
						} else {
							for _, rawBlock := range m["content"].([]any) {
								block := rawBlock.(map[string]any)
								if block["type"] == "thinking" && block["thinking"] == reasoning {
									found++
									if block["signature"] != "opaque-signature" {
										t.Fatalf("signature = %v", block["signature"])
									}
								}
							}
						}
					}
					if found != 1 {
						t.Fatalf("verbatim historical reasoning count = %d", found)
					}
				}
			})
		}
	}
}

func TestDeepSeekReplayRejectionDoesNotWeakenRequest(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeMessages}, []string{"key"})
	impl := &replayRejectingProvider{rejectCount: 1, rejectionMessage: "messages.1.content[].thinking: signature field required"}
	client := NewClient(cfg, impl, "deepseek-flash", 2048, "")
	history := []message.Message{{Role: message.RoleUser, Content: "Continue."}, {
		Role: message.RoleAssistant, ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning"}},
		ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}},
	}, {Role: message.RoleTool, ToolCallID: "call-1", Content: "result"}}
	tuning := RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingEffort: "max"}}
	result, _, err := client.completeStreamTarget(context.Background(), streamRetryTarget{
		provider: cfg, impl: impl, modelID: "deepseek-flash", maxTokens: 2048, tuning: tuning,
	}, 0, history, nil, nil, nil, false, nil, roundCoolingWait{}, false, &CallStatus{}, "", 0, 0, func() error { return nil }, nil, "")
	if err == nil && result.lastErr == nil {
		t.Fatal("expected original replay rejection")
	}
	if len(impl.attempts) != 1 || impl.tunings[0].DisableReasoning || impl.tunings[0].Anthropic.ThinkingEffort != "max" {
		t.Fatalf("unexpected degraded attempts: %d, %v", len(impl.attempts), impl.tunings)
	}
}

func TestDeepSeekMessagesExplicitDisableAndClaudeValidation(t *testing.T) {
	at, err := validateMessagesThinking(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "disabled", ThinkingEffort: "max"}}, true)
	if err != nil || at.ThinkingType != "disabled" || at.ThinkingEffort != "" {
		t.Fatalf("disabled = %+v, %v", at, err)
	}
	_, err = validateMessagesThinking(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingEffort: "max"}}, false)
	if err == nil {
		t.Fatal("Claude must still require a manual thinking budget")
	}
	at, err = validateMessagesThinking(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 4096, ThinkingEffort: "max"}}, false)
	if err != nil || at.ThinkingBudget != 4096 || at.ThinkingEffort != "" {
		t.Fatalf("Claude enabled thinking must ignore the effort DeepSeek uses: %+v, %v", at, err)
	}
	for _, typ := range []string{"enabled", "adaptive"} {
		at, err = validateMessagesThinking(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: typ, ThinkingBudget: 4096, ThinkingEffort: "max"}}, true)
		if err != nil || at.ThinkingBudget != 0 || at.ThinkingEffort != "max" {
			t.Fatalf("DeepSeek tuning = %+v, %v", at, err)
		}
	}
}
