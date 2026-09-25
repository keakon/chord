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
			// The alias carries the contract only through the explicit
			// setting, which must turn on the same wire shape as the name.
			for _, model := range []string{"provider/deepseek-flash", "deployment-a"} {
				t.Run(wire+"/"+thinkingType+"/"+model, func(t *testing.T) {
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
					cfg := NewProviderConfig("sample", config.ProviderConfig{
						Type: wire, APIURL: server.URL,
						Models: map[string]config.ModelConfig{model: {
							Thinking: &config.ThinkingConfig{Type: thinkingType, Effort: "high"},
							Variants: map[string]config.ModelVariant{"max": {Thinking: &config.ThinkingConfig{Effort: "max"}}},
							// A generic replay window must not weaken this model's contract.
							Compat: &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{
								Contract:        config.ReasoningContractDeepSeek,
								ReasoningReplay: modelcompat.ReasoningReplayCurrentTurn,
							}},
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
}

func TestDeepSeekTargetSelection(t *testing.T) {
	cases := []struct {
		name     string
		wire     string
		model    string
		contract string
		want     bool
	}{
		{name: "chat model name", wire: config.ProviderTypeChatCompletions, model: "deepseek-v4.1-flash", want: true},
		{name: "messages provider-prefixed name", wire: config.ProviderTypeMessages, model: "vendor/deepseek-v4.1", want: true},
		{name: "alias with explicit contract", wire: config.ProviderTypeChatCompletions, model: "deployment-a", contract: config.ReasoningContractDeepSeek, want: true},
		{name: "alias without contract", wire: config.ProviderTypeChatCompletions, model: "deployment-a"},
		{name: "deepseek name with explicit none opts out", wire: config.ProviderTypeChatCompletions, model: "deepseek-v4.1-flash", contract: config.ReasoningContractNone},
		{name: "deepseek name with gemini-3 contract opts out", wire: config.ProviderTypeChatCompletions, model: "deepseek-v4.1-flash", contract: config.ReasoningContractGemini3},
		{name: "other family name without contract", wire: config.ProviderTypeChatCompletions, model: "glm-5.2"},
		{name: "embedded deepseek text without contract", wire: config.ProviderTypeChatCompletions, model: "my-deepseek-proxy"},
		{name: "responses wire stays out of the contract", wire: config.ProviderTypeResponses, model: "deepseek-v4.1-flash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := config.ModelConfig{}
			if tc.contract != "" {
				model.Compat = &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Contract: tc.contract}}
			}
			provider := NewProviderConfig("sample", config.ProviderConfig{
				Type:   tc.wire,
				Models: map[string]config.ModelConfig{tc.model: model},
			}, []string{"key"})
			if got := deepSeekTarget(provider, tc.model); got != tc.want {
				t.Fatalf("deepSeekTarget(%q, %q) = %v, want %v", tc.wire, tc.model, got, tc.want)
			}
		})
	}
}

func TestDeepSeekContractPreservesOnlySameTargetThinking(t *testing.T) {
	const model = "deployment-a"
	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{model: {
			Compat: &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{
				Contract: config.ReasoningContractDeepSeek,
			}},
		}},
	}, []string{"test-key"})
	for _, sameTarget := range []bool{true, false} {
		provenance := &message.MessageProvenance{ProviderID: "sample", ModelID: model, WireFamily: modelcompat.WireFamilyAnthropic}
		if !sameTarget {
			provenance.ModelID = "another-model"
		}
		history := []message.Message{{
			Role: message.RoleAssistant, Content: "Answer", Provenance: provenance,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "  reasoning\n", Signature: "opaque-signature"}},
		}}
		target := FallbackModel{ProviderConfig: provider, ModelID: model}
		out, _ := normalizeMessagesForPoolTargetWithOptions(history, target, tuningForPoolTarget(target), modelcompat.ReplayCompatNative)
		if len(out) != 1 || len(out[0].ThinkingBlocks) != 1 {
			t.Fatalf("sameTarget=%v: lost thinking: %+v", sameTarget, out)
		}
		block := out[0].ThinkingBlocks[0]
		if block.Thinking != "  reasoning\n" || (block.Signature != "") != sameTarget {
			t.Fatalf("sameTarget=%v: block=%+v", sameTarget, block)
		}
	}
}

func TestDeepSeekReplayRejectionDoesNotWeakenRequest(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Compat: &config.ProviderCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{
			Contract: config.ReasoningContractDeepSeek,
		}},
	}, []string{"key"})
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
	at, err := validateMessagesThinking(deepSeekRequestTuning(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "disabled", ThinkingEffort: "max"}}), true)
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
		at, err = validateMessagesThinking(deepSeekRequestTuning(RequestTuning{Anthropic: AnthropicTuning{ThinkingType: typ, ThinkingBudget: 4096, ThinkingEffort: "max"}}), true)
		if err != nil || at.ThinkingBudget != 0 || at.ThinkingEffort != "max" {
			t.Fatalf("DeepSeek tuning = %+v, %v", at, err)
		}
	}
}

// The DeepSeek name shortcut keeps the request shape without any selector: a
// deepseek-named Chat Completions model sends the thinking object on its own
// even when no thinking block is configured, while a model whose name does
// not identify the family sends no thinking field without an explicit
// native_thinking selector.
func TestDeepSeekNameShortcutWithoutSelector(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	history := []message.Message{{Role: message.RoleUser, Content: "Inspect the file."}}
	toolDefs := []message.ToolDefinition{{Name: "read", InputSchema: map[string]any{"type": "object"}}}
	send := func(t *testing.T, model, contract string) map[string]any {
		t.Helper()
		var modelCfg config.ModelConfig
		if contract != "" {
			modelCfg.Compat = &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Contract: contract}}
		}
		cfg := NewProviderConfig("sample", config.ProviderConfig{
			Type:   config.ProviderTypeChatCompletions,
			APIURL: server.URL,
			Models: map[string]config.ModelConfig{model: modelCfg},
		}, []string{"test-key"})
		impl := &OpenAIProvider{provider: cfg, client: server.Client()}
		client := NewClient(cfg, impl, model, 2048, "")
		target := FallbackModel{ProviderConfig: cfg, ModelID: model}
		tuning := tuningForPoolTarget(target)
		_, err := callCompleteStreamWithRetryForTest(client, context.Background(), cfg, impl, model, 2048, tuning, "", history, toolDefs, nil, false, nil, -2, &CallStatus{})
		if err != nil {
			t.Fatal(err)
		}
		return <-requests
	}

	t.Run("deepseek-named model sends the thinking object", func(t *testing.T) {
		body := send(t, "deepseek-v4.1-flash", "")
		if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "enabled"}) {
			t.Fatalf("thinking = %#v, want the name shortcut to turn thinking on without a selector", body["thinking"])
		}
		if effort, present := body["reasoning_effort"]; present {
			t.Fatalf("reasoning_effort = %v, want no effort without a thinking config", effort)
		}
	})

	t.Run("other model names send no thinking object", func(t *testing.T) {
		body := send(t, "glm-5.2", "")
		if shape, present := body["thinking"]; present {
			t.Fatalf("thinking = %v, want no thinking object without an explicit selector", shape)
		}
	})

	t.Run("explicit deepseek contract on an alias sends the thinking object", func(t *testing.T) {
		body := send(t, "deployment-a", config.ReasoningContractDeepSeek)
		if !reflect.DeepEqual(body["thinking"], map[string]any{"type": "enabled"}) {
			t.Fatalf("thinking = %#v, want the contract to select the DeepSeek shape like the name does", body["thinking"])
		}
	})

	t.Run("explicit none contract on a deepseek name sends no thinking object", func(t *testing.T) {
		body := send(t, "deepseek-v4.1-flash", config.ReasoningContractNone)
		if shape, present := body["thinking"]; present {
			t.Fatalf("thinking = %v, want an opted-out route to leave the DeepSeek shape out", shape)
		}
	})
}

// TestDeepSeekRequestTuningIsIdempotent pins the property that lets the retry
// layer own the DeepSeek request tuning: reapplying it to an already tuned
// request changes nothing.
func TestDeepSeekRequestTuningIsIdempotent(t *testing.T) {
	cases := []RequestTuning{
		{},
		{Anthropic: AnthropicTuning{ThinkingType: "disabled", ThinkingEffort: "max"}, OpenAI: OpenAITuning{ReasoningEffort: "high"}},
		{Anthropic: AnthropicTuning{ThinkingType: "adaptive", ThinkingBudget: 4096, ThinkingDisplay: "summarized"}},
		{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingEffort: "max"}},
		{OpenAI: OpenAITuning{ReasoningEffort: "high", ReasoningEffortMap: map[string]string{"high": "max"}}},
	}
	for _, tc := range cases {
		once := deepSeekRequestTuning(tc)
		if twice := deepSeekRequestTuning(once); !reflect.DeepEqual(twice, once) {
			t.Fatalf("deepSeekRequestTuning is not idempotent for %+v: once %+v, twice %+v", tc, once, twice)
		}
	}
}

func TestDeepSeekDisabledEffortOnWire(t *testing.T) {
	for _, wire := range []string{config.ProviderTypeChatCompletions, config.ProviderTypeMessages} {
		for _, source := range []string{"reasoning", "thinking", "disabled", "unset"} {
			t.Run(wire+"/"+source, func(t *testing.T) {
				requests := make(chan map[string]any, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
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
				modelCfg := config.ModelConfig{Compat: &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Contract: config.ReasoningContractDeepSeek}}}
				switch source {
				case "reasoning":
					modelCfg.Reasoning = &config.ReasoningConfig{Effort: "none"}
				case "thinking":
					modelCfg.Thinking = &config.ThinkingConfig{Type: "enabled", Effort: "none"}
				case "disabled":
					modelCfg.Thinking = &config.ThinkingConfig{Type: "disabled", Effort: "high"}
				}
				cfg := NewProviderConfig("sample", config.ProviderConfig{Type: wire, APIURL: server.URL, Models: map[string]config.ModelConfig{"model-1": modelCfg}}, []string{"test-key"})
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
				client := NewClient(cfg, impl, "model-1", 2048, "")
				tuning := tuningForPoolTarget(FallbackModel{ProviderConfig: cfg, ModelID: "model-1"})
				_, err := callCompleteStreamWithRetryForTest(client, context.Background(), cfg, impl, "model-1", 2048, tuning, "", []message.Message{{Role: message.RoleUser, Content: "Reply briefly."}}, nil, nil, false, nil, -2, &CallStatus{})
				if err != nil {
					t.Fatal(err)
				}
				body := <-requests
				want := "disabled"
				if source == "unset" {
					want = "enabled"
				}
				if !reflect.DeepEqual(body["thinking"], map[string]any{"type": want}) {
					t.Fatalf("thinking=%#v, want %s", body["thinking"], want)
				}
				if _, ok := body["reasoning_effort"]; ok {
					t.Fatalf("unexpected effort: %#v", body)
				}
				if _, ok := body["output_config"]; ok {
					t.Fatalf("unexpected effort: %#v", body)
				}
			})
		}
	}
}
