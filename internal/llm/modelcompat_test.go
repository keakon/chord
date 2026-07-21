package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func findResponsesToolItems(items []responsesInputItem, callID string) (call, output *responsesInputItem) {
	for i := range items {
		item := &items[i]
		switch {
		case item.Type == "function_call" && item.CallID == callID:
			call = item
		case item.Type == "function_call_output" && item.CallID == callID:
			output = item
		}
	}
	return call, output
}

func responsesItemsContainText(items []responsesInputItem, text string) bool {
	for _, item := range items {
		blocks, ok := item.Content.([]responsesContentBlock)
		if !ok {
			continue
		}
		for _, block := range blocks {
			if strings.Contains(block.Text, text) {
				return true
			}
		}
	}
	return false
}

func anthropicBlocksContainToolUse(blocks []anthropicContent, id string) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" && block.ID == id {
			return true
		}
	}
	return false
}

func anthropicBlocksContainText(blocks []anthropicContent, text string) bool {
	for _, block := range blocks {
		if block.Type == "text" && strings.Contains(block.Text, text) {
			return true
		}
	}
	return false
}

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
		Provenance:       &message.MessageProvenance{Source: "chord", ProviderID: "glm-main", ModelID: "glm-5.2", WireFamily: modelcompat.WireFamilyOpenAIChat},
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
	if items[0].Type != "message" || items[0].Role != string(message.RoleUser) {
		t.Fatalf("items[0] = %#v, want user message", items[0])
	}
	call, output := findResponsesToolItems(items, "call_1")
	if call == nil || call.Name != "Shell" || call.Arguments != `{"command":"pwd"}` {
		t.Fatalf("function_call = %#v, want structured item", call)
	}
	if output == nil || output.Output != "/tmp/project\n" {
		t.Fatalf("function_call_output = %#v, want tool result", output)
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

func TestNormalizeMessagesForPoolTarget_ResponsesToMessagesPreservesToolFacts(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	source := []message.Message{
		{Role: message.RoleUser, Content: "inspect"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque", Summary: []message.ResponsesReasoningSummary{{Type: "summary_text", Text: "public reasoning summary"}}},
				{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: `{"path":"README.md"}`},
			},
			ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"README.md"}`)}},
			Provenance: &message.MessageProvenance{ProviderID: "openai", ModelID: "gpt-5", WireFamily: modelcompat.WireFamilyOpenAIResponses},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "README contents"},
	}

	normalized, report := normalizeMessagesForPoolTarget(source, FallbackModel{ProviderConfig: provider, ModelID: "claude-sonnet"}, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive"}})
	if len(normalized) != 3 || len(normalized[1].ResponsesOutput) != 0 || len(normalized[1].ToolCalls) != 1 || normalized[2].ToolCallID != "call_1" {
		t.Fatalf("Responses history lost portable tool facts: %+v (report %+v)", normalized, report)
	}

	converted := convertMessages(normalized)
	if len(converted) != 3 {
		t.Fatalf("Anthropic messages = %#v, want user/assistant/tool-result", converted)
	}
	assistantBlocks, ok := converted[1].Content.([]anthropicContent)
	if !ok || !anthropicBlocksContainToolUse(assistantBlocks, "call_1") || !anthropicBlocksContainText(assistantBlocks, "public reasoning summary") {
		t.Fatalf("Anthropic tool_use = %#v", converted[1].Content)
	}
	resultBlocks, ok := converted[2].Content.([]anthropicContent)
	if !ok || len(resultBlocks) != 1 || resultBlocks[0].Type != "tool_result" || resultBlocks[0].ToolUseID != "call_1" {
		t.Fatalf("Anthropic tool_result = %#v", converted[2].Content)
	}
}

func TestNormalizeMessagesForPoolTarget_MessagesToResponsesPreservesToolFacts(t *testing.T) {
	provider := NewProviderConfig("openai-main", config.ProviderConfig{Type: config.ProviderTypeResponses}, nil)
	source := []message.Message{
		{Role: message.RoleUser, Content: "inspect"},
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "unsigned provider reasoning"}},
			ToolCalls:      []message.ToolCall{{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"README.md"}`)}},
			Provenance:     &message.MessageProvenance{ProviderID: "deepseek", ModelID: "deepseek-v4-pro", WireFamily: modelcompat.WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "README contents"},
	}

	normalized, report := normalizeMessagesForPoolTarget(source, FallbackModel{ProviderConfig: provider, ModelID: "gpt-5"}, RequestTuning{})
	if len(normalized) != 3 || len(normalized[1].ThinkingBlocks) != 0 || len(normalized[1].ToolCalls) != 1 || normalized[2].ToolCallID != "call_1" {
		t.Fatalf("Messages history lost portable tool facts: %+v (report %+v)", normalized, report)
	}

	converted := convertMessagesToResponses("", normalized)
	call, output := findResponsesToolItems(converted, "call_1")
	if call == nil || call.Name != "read" {
		t.Fatalf("Responses function_call = %#v", call)
	}
	if output == nil || output.Output != "README contents" {
		t.Fatalf("Responses function_call_output = %#v", output)
	}
	if !responsesItemsContainText(converted, "unsigned provider reasoning") {
		t.Fatalf("Responses input lost portable reasoning context: %#v", converted)
	}
}

func TestNormalizeMessagesForPoolTarget_ChatToMessagesDropsOnlyReasoning(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	source := []message.Message{
		{Role: message.RoleUser, Content: "inspect"},
		{
			Role:             message.RoleAssistant,
			ReasoningContent: "visible chat reasoning",
			ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"README.md"}`)}},
			Provenance:       &message.MessageProvenance{ProviderID: "deepseek", ModelID: "deepseek-v4-pro", WireFamily: modelcompat.WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "README contents"},
	}

	normalized, report := normalizeMessagesForPoolTarget(source, FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-pro"}, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive"}})
	if len(normalized) != 3 || normalized[1].ReasoningContent != "" || len(normalized[1].ToolCalls) != 1 || report.DowngradedReasoning != 1 {
		t.Fatalf("Chat reasoning should be dropped while tool facts survive: %+v (report %+v)", normalized, report)
	}
	converted := convertMessages(normalized)
	blocks, ok := converted[1].Content.([]anthropicContent)
	if !ok || !anthropicBlocksContainToolUse(blocks, "call_1") || !anthropicBlocksContainText(blocks, "visible chat reasoning") {
		t.Fatalf("Anthropic conversion lost tool_use: %#v", converted[1].Content)
	}
}

func TestNormalizeMessagesForPoolTarget_MessagesToChatDropsOnlyThinking(t *testing.T) {
	provider := NewProviderConfig("chat-main", config.ProviderConfig{Type: config.ProviderTypeChatCompletions}, nil)
	source := []message.Message{
		{Role: message.RoleUser, Content: "inspect"},
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "signed or unsigned messages reasoning", Signature: "sig"}},
			ToolCalls:      []message.ToolCall{{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"README.md"}`)}},
			Provenance:     &message.MessageProvenance{ProviderID: "anthropic-main", ModelID: "claude", WireFamily: modelcompat.WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "README contents"},
	}

	normalized, report := normalizeMessagesForPoolTarget(source, FallbackModel{ProviderConfig: provider, ModelID: "deepseek-v4-pro"}, RequestTuning{})
	if len(normalized) != 3 || len(normalized[1].ThinkingBlocks) != 0 || len(normalized[1].ToolCalls) != 1 || report.DroppedThinkingBlocks != 1 {
		t.Fatalf("Messages thinking should be dropped while tool facts survive: %+v (report %+v)", normalized, report)
	}
	converted := convertMessagesToOpenAI("", modelcompat.WireFamilyOpenAIChat, modelcompat.ReasoningContinuityNone, normalized)
	var foundCall, foundResult bool
	for _, msg := range converted {
		if msg.Role == "assistant" && len(msg.ToolCalls) == 1 && msg.ToolCalls[0].ID == "call_1" {
			foundCall = true
		}
		if msg.Role == "tool" && msg.ToolCallID == "call_1" && msg.Content == "README contents" {
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("OpenAI conversion lost portable tool facts: %#v", converted)
	}
}
