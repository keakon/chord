package llm

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestConvertMessagesToOpenAI_ReplaysReasoningContent(t *testing.T) {
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

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, msgs)
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
	if assistantWithToolCall.ReasoningContent != "I will call a tool now." {
		t.Fatalf("ReasoningContent = %q", assistantWithToolCall.ReasoningContent)
	}
	for _, m := range out {
		if m.Role == "assistant" && m.ReasoningContent != "" && len(m.ToolCalls) == 0 && m.Content == nil {
			t.Fatalf("unexpected standalone reasoning-only assistant message: %#v", m)
		}
	}
}

func TestConvertMessagesToOpenAI_ReplaysReasoningContentForOpenAIChat(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "deepseek thinking",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)}},
	}}

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, msgs)
	var replayed bool
	for _, m := range out {
		if m.Role == "assistant" && m.ReasoningContent == "deepseek thinking" && len(m.ToolCalls) > 0 {
			replayed = true
			break
		}
	}
	if !replayed {
		t.Fatal("expected reasoning_content replay on assistant tool_call message for openai-chat provenance")
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningForNonOpenAITarget(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: modelcompat.WireFamilyOpenAIChat},
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyAnthropic, msgs)
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
	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, msgs)
	for _, m := range out {
		if m.ReasoningContent != "" {
			t.Fatalf("unexpected reasoning replay: %#v", m)
		}
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

	out := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, msgs)
	if len(out) != 1 || out[0].Role != "tool" || out[0].ToolCallID != "c1" {
		t.Fatalf("tool message = %#v", out)
	}
	if out[0].Content != "Loaded image" {
		t.Fatalf("content = %#v, want text-only tool result", out[0].Content)
	}
}
