package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestNormalizeMessagesForPoolTarget_PreservesAnthropicThinkingForAnthropicTarget(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     &message.MessageProvenance{Source: "import:claude", WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "claude-sonnet"}, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled"}})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 1 {
		t.Fatalf("thinking unexpectedly removed: %+v", out)
	}
	if rep.DroppedThinkingBlocks != 0 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 0", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeMessagesForPoolTarget_DropsAnthropicThinkingWithoutConfiguredThinking(t *testing.T) {
	provider := NewProviderConfig("deepseek", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	msgs := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
			ToolCalls:      []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
			Provenance:     &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", WireFamily: modelcompat.WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "toolu_1", Content: "/tmp\n"},
	}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-pro"}, RequestTuning{})
	if len(out) != 2 || len(out[0].ThinkingBlocks) != 0 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("thinking should be removed without configured thinking: %+v", out)
	}
	if rep.DroppedThinkingBlocks != 1 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 1", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeMessagesForPoolTarget_PreservesAnthropicThinkingWhenConfigured(t *testing.T) {
	provider := NewProviderConfig("deepseek", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		ToolCalls:      []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
		Provenance:     &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-pro"}, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive"}})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 1 {
		t.Fatalf("thinking unexpectedly removed with configured thinking: %+v", out)
	}
	if rep.DroppedThinkingBlocks != 0 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 0", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeMessagesForPoolTarget_PreservesOpenAIReasoningForResponsesTarget(t *testing.T) {
	provider := NewProviderConfig("openai-main", config.ProviderConfig{Type: config.ProviderTypeResponses}, nil)
	msgs := []message.Message{
		{
			Role:             message.RoleAssistant,
			ReasoningContent: "hidden reasoning",
			ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
			Provenance:       &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "/tmp/project\n"},
	}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "gpt-5"}, RequestTuning{})
	if len(out) != 2 || out[0].ReasoningContent != "hidden reasoning" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("reasoning and tool call should survive for responses target: %+v", out)
	}
	if rep.DowngradedToolCalls != 0 {
		t.Fatalf("DowngradedToolCalls=%d, want 0", rep.DowngradedToolCalls)
	}
	if rep.DowngradedReasoning != 0 {
		t.Fatalf("DowngradedReasoning=%d, want 0", rep.DowngradedReasoning)
	}
}

func TestNormalizeMessagesForPoolTarget_DropsOpenAIReasoningWhenSwitchingToAnthropic(t *testing.T) {
	provider := NewProviderConfig("deepseek", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	msgs := []message.Message{
		{
			Role:             message.RoleAssistant,
			ReasoningContent: "hidden reasoning",
			ToolCalls:        []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
			Provenance:       &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "toolu_1", Content: "/tmp\n"},
	}
	out, _ := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-pro"}, RequestTuning{})
	if len(out) != 2 {
		t.Fatalf("len(out)=%d, want 2", len(out))
	}
	if out[0].ReasoningContent != "" {
		t.Fatalf("reasoning should be dropped for anthropic target: %+v", out[0])
	}
}

func TestNormalizeMessagesForPoolTarget_ReplaysOpenAIToolCallsForAnthropicTarget(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "check status"},
		{
			Role:       message.RoleAssistant,
			Content:    "I will inspect the repository.",
			ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "shell", Args: json.RawMessage(`{"command":"git status --short"}`)}},
			Provenance: &message.MessageProvenance{Source: "chord", ProviderID: "openai-main", WireFamily: modelcompat.WireFamilyOpenAIResponses},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: " M internal/modelcompat/normalize.go\n"},
	}

	normalized, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "claude-sonnet"}, RequestTuning{})
	if rep.DowngradedToolCalls != 0 {
		t.Fatalf("DowngradedToolCalls=%d, want 0", rep.DowngradedToolCalls)
	}
	if len(normalized) != 3 || len(normalized[1].ToolCalls) != 1 {
		t.Fatalf("expected structured tool call to survive normalization, got %+v", normalized)
	}
	if strings.Contains(normalized[1].Content, "[Imported tool call") {
		t.Fatalf("did not expect imported tool marker in normalized content: %q", normalized[1].Content)
	}

	anthropicMessages := convertMessages(normalized)
	if len(anthropicMessages) != 3 {
		t.Fatalf("len(anthropicMessages)=%d, want 3", len(anthropicMessages))
	}
	assistantBlocks, ok := anthropicMessages[1].Content.([]anthropicContent)
	if !ok {
		t.Fatalf("assistant content type = %T, want []anthropicContent", anthropicMessages[1].Content)
	}
	var foundToolUse bool
	for _, block := range assistantBlocks {
		if block.Type == "tool_use" && block.ID == "call_1" && block.Name == "shell" && string(block.Input) == `{"command":"git status --short"}` {
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Fatalf("expected Anthropic tool_use block, got %+v", assistantBlocks)
	}
	resultBlocks, ok := anthropicMessages[2].Content.([]anthropicContent)
	if !ok || len(resultBlocks) != 1 || resultBlocks[0].Type != "tool_result" || resultBlocks[0].ToolUseID != "call_1" {
		t.Fatalf("expected matching Anthropic tool_result block, got %#v", anthropicMessages[2].Content)
	}
}

func TestNormalizeMessagesForPoolTarget_DropsThinkingForOpenAITarget(t *testing.T) {
	provider := NewProviderConfig("openai-main", config.ProviderConfig{Type: config.ProviderTypeResponses}, nil)
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     &message.MessageProvenance{Source: "import:claude", WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "gpt-5"}, RequestTuning{})
	if len(out[0].ThinkingBlocks) != 0 {
		t.Fatalf("thinking should be dropped for OpenAI target: %+v", out[0])
	}
	if rep.DroppedThinkingBlocks != 1 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 1", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeMessagesForPoolTarget_ResponsesConversionReplaysReasoningForStructuredToolHistory(t *testing.T) {
	provider := NewProviderConfig("openai-main", config.ProviderConfig{Type: config.ProviderTypeResponses}, nil)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "inspect the repo"},
		{
			Role:             message.RoleAssistant,
			ReasoningContent: "hidden reasoning",
			ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
			Provenance:       &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "/tmp/project\n"},
	}

	normalized, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "gpt-5"}, RequestTuning{})
	if rep.DowngradedToolCalls != 0 {
		t.Fatalf("DowngradedToolCalls=%d, want 0", rep.DowngradedToolCalls)
	}
	if rep.DowngradedReasoning != 0 {
		t.Fatalf("DowngradedReasoning=%d, want 0", rep.DowngradedReasoning)
	}
	if len(normalized) != 3 {
		t.Fatalf("len(normalized)=%d, want 3", len(normalized))
	}
	if normalized[1].ReasoningContent != "hidden reasoning" {
		t.Fatalf("normalized reasoning = %q, want hidden reasoning", normalized[1].ReasoningContent)
	}
	if len(normalized[1].ToolCalls) != 1 {
		t.Fatalf("expected structured tool call preserved before responses conversion, got %+v", normalized[1].ToolCalls)
	}

	items := convertMessagesToResponses("", modelcompat.WireFamilyOpenAIResponses, normalized)
	if len(items) != 4 {
		t.Fatalf("len(items)=%d, want 4", len(items))
	}
	if items[0].Type != "message" || items[0].Role != string(message.RoleUser) {
		t.Fatalf("items[0] = %#v, want user message", items[0])
	}
	if items[1].Type != "message" || items[1].Role != string(message.RoleAssistant) {
		t.Fatalf("items[1] = %#v, want assistant reasoning replay message", items[1])
	}
	blocks, ok := items[1].Content.([]responsesContentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Type != "output_text" || blocks[0].Text != "hidden reasoning" {
		t.Fatalf("items[1].Content = %#v, want single output_text reasoning block", items[1].Content)
	}
	if items[2].Type != "function_call" || items[2].CallID != "call_1" || items[2].Name != "Shell" || items[2].Arguments != `{"command":"pwd"}` {
		t.Fatalf("items[2] = %#v, want structured function_call item", items[2])
	}
	if items[3].Type != "function_call_output" || items[3].CallID != "call_1" || items[3].Output != "/tmp/project\n" {
		t.Fatalf("items[3] = %#v, want tool result output", items[3])
	}
}

func TestNormalizeMessagesForPoolTarget_DowngradesMissingToolResultForAnthropic(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: args}},
		Provenance: &message.MessageProvenance{Source: "import:claude", Imported: true, WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "claude-sonnet"}, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled"}})
	if len(out) != 1 || len(out[0].ToolCalls) != 0 || out[0].Role != message.RoleAssistant {
		t.Fatalf("expected downgrade to assistant text, got %+v", out)
	}
	if rep.DowngradedToolCalls == 0 {
		t.Fatalf("DowngradedToolCalls=%d, want >0", rep.DowngradedToolCalls)
	}
}
