package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestCallLLMPromotesStreamingActivityOnToolUseStartWithoutStatusDelta(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
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
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	_, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var sawStreaming bool
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if !ok {
			continue
		}
		if act.Type == ActivityStreaming {
			sawStreaming = true
		}
	}
	if !sawStreaming {
		t.Fatal("expected streaming activity promoted from tool_use_start")
	}
}
func TestCallLLMClosesThinkingBeforeFirstText(t *testing.T) {
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
				{Type: "thinking", Text: "analyzing"},
				{Type: "text", Text: "answer"},
				{Type: "thinking", Text: "late reasoning"},
			},
			resp: &message.Response{Content: "answer", StopReason: "stop"},
		}},
	}

	client := llm.NewClient(providerCfg, providerImpl, "test-model", 4096, "sys")
	a.swapLLMClientWithRef(client, "test-model", 128000, "sample/test-model")

	if _, err := a.callLLM(context.Background(), []message.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("callLLM: %v", err)
	}

	events := drainAgentEvents(a.Events())
	var streamEvents []AgentEvent
	for _, evt := range events {
		switch evt.(type) {
		case ThinkingStartedEvent, StreamThinkingDeltaEvent, StreamThinkingEvent, StreamTextEvent:
			streamEvents = append(streamEvents, evt)
		}
	}
	if len(streamEvents) != 4 {
		t.Fatalf("stream event count = %d, want 4; events=%#v", len(streamEvents), streamEvents)
	}
	if _, ok := streamEvents[0].(ThinkingStartedEvent); !ok {
		t.Fatalf("streamEvents[0] = %T, want ThinkingStartedEvent", streamEvents[0])
	}
	thinkDelta, ok := streamEvents[1].(StreamThinkingDeltaEvent)
	if !ok {
		t.Fatalf("streamEvents[1] = %T, want StreamThinkingDeltaEvent", streamEvents[1])
	}
	if thinkDelta.Text != "analyzing" {
		t.Fatalf("thinking delta text = %q, want analyzing", thinkDelta.Text)
	}
	if _, ok := streamEvents[2].(StreamThinkingEvent); !ok {
		t.Fatalf("streamEvents[2] = %T, want StreamThinkingEvent", streamEvents[2])
	}
	text, ok := streamEvents[3].(StreamTextEvent)
	if !ok {
		t.Fatalf("streamEvents[3] = %T, want StreamTextEvent", streamEvents[3])
	}
	if text.Text != "answer" {
		t.Fatalf("text = %q, want answer", text.Text)
	}
}
func TestHandleLLMResponsePersistsOpenAIChatReasoningWithChatCompletionsProvenance(t *testing.T) {
	a := newReadyTestMainAgent(t)
	providerCfg := llm.NewProviderConfig("deepseek", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"deepseek-v4-pro": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	client := llm.NewClient(providerCfg, stubProvider{}, "deepseek-v4-pro", 4096, "sys")
	a.swapLLMClientWithRef(client, "deepseek-v4-pro", 128000, "deepseek/deepseek-v4-pro")
	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	payload := &LLMResponsePayload{
		ReasoningContent: "I need to inspect the repo before reading files.",
		ToolCalls: []message.ToolCall{{
			ID:   "call-1",
			Name: "Read",
			Args: []byte(`{"path":"README.md"}`),
		}},
		StopReason: "tool_calls",
	}

	a.handleLLMResponse(Event{Type: EventLLMResponse, TurnID: a.turn.ID, Payload: payload})

	msgs := a.GetMessages()
	if len(msgs) == 0 {
		t.Fatal("expected assistant message appended to context")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		t.Fatalf("last role = %q, want assistant", last.Role)
	}
	if last.ReasoningContent != "I need to inspect the repo before reading files." {
		t.Fatalf("ReasoningContent = %q", last.ReasoningContent)
	}
	if last.Provenance == nil {
		t.Fatal("expected assistant provenance")
	}
	if last.Provenance.WireFamily != modelcompat.WireFamilyOpenAIChat {
		t.Fatalf("WireFamily = %q, want %q", last.Provenance.WireFamily, modelcompat.WireFamilyOpenAIChat)
	}
}

func TestCallLLMOnlyEmitsOneStreamingActivityWhenStatusAlsoArrives(t *testing.T) {
	a := newReadyTestMainAgent(t)

	providerCfg := llm.NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})

	providerImpl := &blockingStreamProvider{
		calls: []scriptedStreamCall{{
			streams: []message.StreamDelta{
				{Type: "status", Status: &message.StatusDelta{Type: "waiting_token"}},
				{Type: "tool_use_start", ToolCall: &message.ToolCallDelta{ID: "call-1", Name: "Read", Input: `{"path":"README.md"}`}},
				{Type: "status", Status: &message.StatusDelta{Type: "streaming"}},
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
	streamingCount := 0
	for _, evt := range events {
		act, ok := evt.(AgentActivityEvent)
		if ok && act.Type == ActivityStreaming {
			streamingCount++
		}
	}
	if streamingCount != 1 {
		t.Fatalf("streaming activity count = %d, want 1", streamingCount)
	}
}
