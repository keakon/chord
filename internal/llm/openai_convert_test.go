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

	out := convertMessagesToOpenAI("", msgs)
	if len(out) < 3 {
		t.Fatalf("got %d messages, want >= 3", len(out))
	}
	var foundReasoning bool
	for _, m := range out {
		if m.Role == "assistant" && m.ReasoningContent != "" {
			foundReasoning = true
			if m.ReasoningContent != "I will call a tool now." {
				t.Fatalf("ReasoningContent = %q", m.ReasoningContent)
			}
			break
		}
	}
	if !foundReasoning {
		t.Fatal("expected an assistant message with reasoning_content")
	}
}

func TestConvertMessagesToOpenAI_DoesNotReplayReasoningWithoutProvenance(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		ToolCalls:        []message.ToolCall{{ID: "c1", Name: "Shell", Args: json.RawMessage(`{"command":"echo hi"}`)}},
	}}
	out := convertMessagesToOpenAI("", msgs)
	for _, m := range out {
		if m.ReasoningContent != "" {
			t.Fatalf("unexpected reasoning replay: %#v", m)
		}
	}
}
