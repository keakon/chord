package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningContentByDefault(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "do something"},
		{
			Role:             "assistant",
			ReasoningContent: "I will call a tool now.",
			Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)},
			},
			StopReason: "tool_calls",
		},
		{Role: "tool", ToolCallID: "c1", Content: "hi\n"},
	}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	if len(out) < 2 {
		t.Fatalf("got %d messages, want >= 2", len(out))
	}
	var assistantWithToolCall *openAIMessage
	for i := range out {
		m := &out[i]
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			assistantWithToolCall = m
			break
		}
	}
	if assistantWithToolCall == nil {
		t.Fatal("expected an assistant tool_call message")
	}
	if assistantWithToolCall.ReasoningContent != "" {
		t.Fatalf("ReasoningContent = %q, want empty", assistantWithToolCall.ReasoningContent)
	}
	for _, m := range out {
		if m.Role == "assistant" && m.ReasoningContent != "" && len(m.ToolCalls) == 0 && m.Content == nil {
			t.Fatalf("unexpected standalone reasoning-only assistant message: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningContentForOpenAIChatByDefault(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "deepseek thinking",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && m.ReasoningContent == "deepseek thinking" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if replayed {
		t.Fatal("unexpected reasoning_content replay on assistant tool_call message for openai-chat provenance")
	}
}

func TestConvertMessagesToOpenAI_ReplaysReasoningContentWhenOpenAIVisibleContinuityEnabled(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "glm preserved reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && m.ReasoningContent == "glm preserved reasoning" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Fatal("expected reasoning_content replay when openai_visible continuity is enabled")
	}
}

func TestConvertMessagesToOpenAI_SkipsReasoningOnlyAssistant(t *testing.T) {
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, []message.Message{
		{Role: "user", Content: "before"},
		{Role: "assistant", ReasoningContent: "hidden", Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat}},
		{Role: "user", Content: "after"},
	})
	if len(out) != 2 {
		t.Fatalf("convertMessagesToOpenAI() len = %d, want 2: %#v", len(out), out)
	}
	for _, msg := range out {
		if msg.Role == "assistant" {
			t.Fatalf("reasoning-only assistant was not skipped: %#v", out)
		}
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningForNonOpenAITarget(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyAnthropic, modelcompat.ReasoningContinuityNone, msgs)
	for _, m := range out {
		if m.ReasoningContent != "" {
			t.Fatalf("unexpected reasoning replay for non-openai target: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningWithoutProvenance(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	for _, m := range out {
		if m.ReasoningContent != "" {
			t.Fatalf("unexpected reasoning replay: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningWithNonOpenAIChatProvenance(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "foreign reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyGemini},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityOpenAIVisible, msgs)
	for _, m := range out {
		if m.ReasoningContent != "" {
			t.Fatalf("unexpected reasoning replay: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAIMarksInterruptedAssistant(t *testing.T) {
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, []message.Message{{Role: "assistant", Content: "partial", StopReason: "interrupted"}})
	if len(out) != 1 || out[0].Role != "assistant" {
		t.Fatalf("convertMessagesToOpenAI() = %#v", out)
	}
	text, ok := out[0].Content.(string)
	if !ok {
		t.Fatalf("assistant content type = %T", out[0].Content)
	}
	if !strings.Contains(text, "partial") || !strings.Contains(text, "interrupted before completion") {
		t.Fatalf("interrupted assistant content = %q", text)
	}
}

func TestConvertMessagesToOpenAI_ToolOutputWithImageParts(t *testing.T) {
	msgs := []message.Message{{
		Role:       "tool",
		ToolCallID: "c1",
		Content:    "Loaded image",
		Parts: []message.ContentPart{
			{Type: "text", Text: "Loaded image"},
			{Type: "image", MimeType: "image/png", Data: []byte("png")},
		},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, msgs)
	if len(out) != 1 || out[0].Role != "tool" || out[0].ToolCallID != "c1" {
		t.Fatalf("tool message = %#v", out)
	}
	if out[0].Content != "Loaded image" {
		t.Fatalf("content = %#v, want text-only tool result", out[0].Content)
	}
}

func TestOpenAICompleteStream_OpenAIVisibleContinuityAddsPreservedThinkingRequest(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewProviderConfig("glm-main", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL,
		Models: map[string]config.ModelConfig{
			"glm-5.2": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				},
			},
		},
	}, []string{"k"})
	r, err := NewOpenAIProviderWithClient(provider, server.Client(), "")
	if err != nil {
		t.Fatalf("NewOpenAIProviderWithClient: %v", err)
	}

	_, err = r.CompleteStream(
		context.Background(),
		"k",
		"glm-5.2",
		"",
		[]message.Message{{
			Role:             message.RoleAssistant,
			Content:          "Calling tool.",
			ReasoningContent: "preserved reasoning",
			Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
			ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
		}},
		nil,
		128,
		RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream returned error: %v", err)
	}

	thinking, ok := gotBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking object, got %#v", gotBody["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %#v, want enabled", thinking["type"])
	}
	if thinking["clear_thinking"] != false {
		t.Fatalf("thinking.clear_thinking = %#v, want false", thinking["clear_thinking"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	first, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatalf("first message = %#v", msgs[0])
	}
	if first["reasoning_content"] != "preserved reasoning" {
		t.Fatalf("reasoning_content = %#v, want preserved reasoning", first["reasoning_content"])
	}
	if gotBody["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v, want true", gotBody["parallel_tool_calls"])
	}
}
