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

func TestNormalizeMessagesForPoolTarget_DropsOpenAIReasoningForResponsesTarget(t *testing.T) {
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
	if len(out) != 2 || out[0].ReasoningContent != "" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("reasoning should be dropped while tool call survives for responses target: %+v", out)
	}
	if rep.DowngradedToolCalls != 0 {
		t.Fatalf("DowngradedToolCalls=%d, want 0", rep.DowngradedToolCalls)
	}
	if rep.DowngradedReasoning != 1 {
		t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
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

func TestNormalizeMessagesForPoolTarget_PreservesOpenAIVisibleReasoningWhenCompatEnabled(t *testing.T) {
	provider := NewProviderConfig("glm-main", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"glm-5.2": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				},
			},
		},
	}, nil)
	msgs := []message.Message{{
		Role:             message.RoleAssistant,
		ReasoningContent: "preserved reasoning",
		ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
		Provenance:       &message.MessageProvenance{Source: "chord", ProviderID: "glm-main", WireFamily: modelcompat.WireFamilyOpenAIChat},
	}, {Role: message.RoleTool, ToolCallID: "call_1", Content: "/tmp/project\n"}}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "glm-5.2"}, RequestTuning{})
	if len(out) != 2 || out[0].ReasoningContent != "preserved reasoning" {
		t.Fatalf("reasoning should be preserved for openai_visible target: %+v", out)
	}
	if rep.DowngradedReasoning != 0 {
		t.Fatalf("DowngradedReasoning=%d, want 0", rep.DowngradedReasoning)
	}
}

func TestNormalizeMessagesForPoolTarget_DropsNonOpenAIChatReasoningWhenOpenAIVisibleCompatEnabled(t *testing.T) {
	provider := NewProviderConfig("glm-main", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"glm-5.2": {
				Compat: &config.ModelCompatConfig{
					ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
				},
			},
		},
	}, nil)

	tests := []struct {
		name       string
		provenance *message.MessageProvenance
	}{
		{name: "nil provenance"},
		{name: "gemini provenance", provenance: &message.MessageProvenance{Source: "chord", ProviderID: "gemini-main", WireFamily: modelcompat.WireFamilyGemini}},
		{name: "responses provenance", provenance: &message.MessageProvenance{Source: "chord", ProviderID: "openai-main", WireFamily: modelcompat.WireFamilyOpenAIResponses}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []message.Message{{
				Role:             message.RoleAssistant,
				ReasoningContent: "foreign reasoning",
				ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
				Provenance:       tt.provenance,
			}, {Role: message.RoleTool, ToolCallID: "call_1", Content: "/tmp/project\n"}}

			out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "glm-5.2"}, RequestTuning{})
			if len(out) != 2 || out[0].ReasoningContent != "" {
				t.Fatalf("reasoning should be dropped for non-openai-chat provenance: %+v", out)
			}
			if rep.DowngradedReasoning != 1 {
				t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
			}
		})
	}
}

func TestNormalizeMessagesForPoolTarget_IgnoresOpenAIVisibleCompatForResponsesTarget(t *testing.T) {
	sourceMsgs := []message.Message{{
		Role:             message.RoleAssistant,
		ReasoningContent: "preserved reasoning",
		ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
		Provenance:       &message.MessageProvenance{Source: "chord", ProviderID: "glm-main", WireFamily: modelcompat.WireFamilyOpenAIChat},
	}, {Role: message.RoleTool, ToolCallID: "call_1", Content: "/tmp/project\n"}}
	provider := NewProviderConfig("openai-main", config.ProviderConfig{
		Type: config.ProviderTypeResponses,
		Compat: &config.ProviderCompatConfig{
			ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Mode: "openai_visible"},
		},
	}, nil)
	out, rep := normalizeMessagesForPoolTarget(sourceMsgs, FallbackModel{ProviderConfig: provider, ModelID: "gpt-5"}, RequestTuning{})
	if len(out) != 2 || out[0].ReasoningContent != "" {
		t.Fatalf("reasoning should be dropped for responses target even when openai_visible is configured: %+v", out)
	}
	if rep.DowngradedReasoning != 1 {
		t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
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

func TestNormalizeMessagesForPoolTarget_ResponsesConversionDoesNotReplayReasoningForStructuredToolHistory(t *testing.T) {
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
	if rep.DowngradedReasoning != 1 {
		t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
	}
	if len(normalized) != 3 {
		t.Fatalf("len(normalized)=%d, want 3", len(normalized))
	}
	if normalized[1].ReasoningContent != "" {
		t.Fatalf("normalized reasoning = %q, want empty", normalized[1].ReasoningContent)
	}
	if len(normalized[1].ToolCalls) != 1 {
		t.Fatalf("expected structured tool call preserved before responses conversion, got %+v", normalized[1].ToolCalls)
	}

	items := convertMessagesToResponses("", normalized)
	if len(items) != 3 {
		t.Fatalf("len(items)=%d, want 3", len(items))
	}
	if items[0].Type != "message" || items[0].Role != string(message.RoleUser) {
		t.Fatalf("items[0] = %#v, want user message", items[0])
	}
	if items[1].Type != "function_call" || items[1].CallID != "call_1" || items[1].Name != "Shell" || items[1].Arguments != `{"command":"pwd"}` {
		t.Fatalf("items[1] = %#v, want structured function_call item", items[1])
	}
	if items[2].Type != "function_call_output" || items[2].CallID != "call_1" || items[2].Output != "/tmp/project\n" {
		t.Fatalf("items[2] = %#v, want tool result output", items[2])
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
