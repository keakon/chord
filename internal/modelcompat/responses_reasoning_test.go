package modelcompat

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func responsesOutputMsg(providerID, modelID string) message.Message {
	return message.Message{
		Role: message.RoleAssistant,
		ResponsesOutput: []message.ResponsesOutputItem{
			{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-1"},
			{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: `{}`},
		},
		ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
		Provenance: &message.MessageProvenance{
			WireFamily: WireFamilyOpenAIResponses,
			ProviderID: providerID,
			ModelID:    modelID,
		},
	}
}

func TestNormalizeKeepsResponsesOutputForSameResponsesModel(t *testing.T) {
	msgs := []message.Message{
		responsesOutputMsg("openai", "gpt-5.6-sol"),
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	target := TargetModel{
		ProviderID:              "openai",
		WireFamily:              WireFamilyOpenAIResponses,
		ModelID:                 "gpt-5.6-sol",
		ToolResultEncoding:      ToolResultEncodingOpenAIToolRole,
		SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) == 0 || len(out[0].ResponsesOutput) != 2 {
		t.Fatalf("expected output kept, got %+v (report %+v)", out, report)
	}
}

func TestNormalizeDropsResponsesOutputOnProvenanceMismatch(t *testing.T) {
	cases := []struct {
		name   string
		msg    message.Message
		target TargetModel
	}{
		{
			name: "non-responses target",
			msg:  responsesOutputMsg("openai", "gpt-5.6-sol"),
			target: TargetModel{
				ProviderID: "anthropic", WireFamily: WireFamilyAnthropic, ModelID: "gpt-5.6-sol",
				ToolResultEncoding: ToolResultEncodingAnthropicUserBlock, SupportsStructuredTools: true,
			},
		},
		{
			name: "missing provenance",
			msg: message.Message{
				Role:            message.RoleAssistant,
				ResponsesOutput: []message.ResponsesOutputItem{{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-1"}},
				ToolCalls:       []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
			},
			target: TargetModel{
				ProviderID: "openai", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.6-sol",
				ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []message.Message{tc.msg, {Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"}}
			out, report := NormalizeForTarget(msgs, tc.target, NormalizeOptions{StructuredTools: true})
			for _, m := range out {
				if len(m.ResponsesOutput) > 0 {
					t.Fatalf("expected output dropped, got %+v", m.ResponsesOutput)
				}
			}
			if report.DowngradedReasoning == 0 {
				t.Fatal("expected DowngradedReasoning to be reported")
			}
		})
	}
}

func TestNormalizeClearsNativeTrajectoryWhenToolResultsAreMissing(t *testing.T) {
	msg := responsesOutputMsg("openai", "gpt-5.6-sol")
	msg.Content = ""
	target := TargetModel{
		ProviderID: "openai", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.6-sol",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget([]message.Message{msg}, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 0 {
		t.Fatalf("orphan native tool trajectory must be removed, got %+v", out)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("tool-call removal was not reported: %+v", report)
	}
}

func TestNormalizeDeepCopyDoesNotShareResponsesOutput(t *testing.T) {
	src := []message.Message{responsesOutputMsg("openai", "gpt-5.6-sol"), {Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"}}
	target := TargetModel{
		ProviderID: "openai", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.6-sol",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, _ := NormalizeForTarget(src, target, NormalizeOptions{StructuredTools: true})
	out[0].ResponsesOutput[0].EncryptedContent = "mutated"
	if src[0].ResponsesOutput[0].EncryptedContent != "enc-1" {
		t.Fatal("normalize must not mutate the durable transcript")
	}
}

func TestNormalizeVisibleReasoningForeignReplayLadder(t *testing.T) {
	msgs := []message.Message{
		{
			Role:             message.RoleAssistant,
			ReasoningContent: "native reasoning",
			ToolCalls:        []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
			Provenance:       &message.MessageProvenance{ProviderID: "deepseek", ModelID: "deepseek-reasoner", WireFamily: WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	target := TargetModel{
		ProviderID: "glm", ModelID: "glm-5.2", WireFamily: WireFamilyOpenAIChat,
		ReasoningContinuityMode: ReasoningContinuityOpenAIVisible,
		ToolResultEncoding:      ToolResultEncodingOpenAIToolRole,
		SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || out[0].ReasoningContent != "native reasoning" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("cross-provider chat-native reasoning must be kept optimistically: %+v (report %+v)", out, report)
	}
	if report.ForeignNativeReplays != 1 {
		t.Fatalf("ForeignNativeReplays = %d, want 1", report.ForeignNativeReplays)
	}

	out, report = NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(out) != 2 || out[0].ReasoningContent != "" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("synthesized level must drop reasoning while keeping the tool trajectory, got %+v", out)
	}
	if report.DowngradedReasoning == 0 || report.DroppedToolCalls != 0 {
		t.Fatalf("report = %+v, want reasoning downgrade without tool deletion", report)
	}

	target.ProviderID = "deepseek"
	target.ModelID = "deepseek-v4"
	out, report = NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(out) != 2 || out[0].ReasoningContent != "native reasoning" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("same-provider native reasoning must survive model upgrades even at synthesized level: %+v (report %+v)", out, report)
	}
}

func TestNormalizeUnsignedAnthropicThinkingKeepsToolTrajectoryUntilStrict(t *testing.T) {
	msgs := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "provider-visible reasoning"}},
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{"path":"AGENTS.md"}`)}},
			Provenance:     &message.MessageProvenance{Source: "chord", ProviderID: "relay-a", ModelID: "deepseek-v4-pro", WireFamily: WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "file contents"},
	}
	target := TargetModel{
		ProviderID:              "relay-b",
		ModelID:                 "deepseek-v4-pro",
		WireFamily:              WireFamilyAnthropic,
		ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
		SupportsStructuredTools: true,
	}

	for _, level := range []int{ReplayCompatNative, ReplayCompatSynthesized} {
		out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: level})
		if len(out) != 2 || len(out[0].ThinkingBlocks) != 0 || len(out[0].ToolCalls) != 1 || out[1].Role != message.RoleTool {
			t.Fatalf("level %d must preserve paired tool history without unsigned thinking: %+v (report %+v)", level, out, report)
		}
		if report.DroppedToolCalls != 0 || report.DroppedToolResults != 0 {
			t.Fatalf("level %d unexpectedly dropped tool history: %+v", level, report)
		}
	}

	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatStrict})
	if len(out) != 1 || out[0].Role != message.RoleAssistant || len(out[0].ToolCalls) != 0 || !strings.Contains(out[0].Content, "[Previous tool call: read]") || !strings.Contains(out[0].Content, "[Previous tool result for call-1]") {
		t.Fatalf("strict level must textify paired tool history, got %+v (report %+v)", out, report)
	}
	if report.DroppedToolCalls != 0 || report.DroppedToolResults != 0 || len(report.Warnings) == 0 {
		t.Fatalf("strict textification report = %+v", report)
	}
}

func TestNormalizeAnthropicUnsignedContinuityReplayAndDegrade(t *testing.T) {
	msgs := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "provider-visible reasoning"}},
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
			Provenance:     &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", ModelID: "deepseek-v4-pro", WireFamily: WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
	}
	target := TargetModel{
		ProviderID: "deepseek", ModelID: "deepseek-v4-pro", WireFamily: WireFamilyAnthropic,
		ReasoningContinuityMode: ReasoningContinuityAnthropicUnsigned,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
		SupportsStructuredTools: true,
	}

	native, nativeReport := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatNative})
	if len(native) != 2 || len(native[0].ThinkingBlocks) != 1 || native[0].ThinkingBlocks[0].Thinking != "provider-visible reasoning" || nativeReport.DroppedThinkingBlocks != 0 {
		t.Fatalf("native unsigned replay = %+v (report %+v)", native, nativeReport)
	}

	synthesized, synthesizedReport := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(synthesized) != 2 || len(synthesized[0].ThinkingBlocks) != 0 || len(synthesized[0].ToolCalls) != 1 || !strings.Contains(synthesized[0].Content, "provider-visible reasoning") {
		t.Fatalf("synthesized unsigned fallback = %+v (report %+v)", synthesized, synthesizedReport)
	}

	foreignTarget := target
	foreignTarget.ProviderID = "other-relay"
	foreign, foreignReport := NormalizeForTarget(msgs, foreignTarget, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatNative})
	if len(foreign) != 2 || len(foreign[0].ThinkingBlocks) != 0 || !strings.Contains(foreign[0].Content, "provider-visible reasoning") || foreignReport.DroppedThinkingBlocks != 1 {
		t.Fatalf("cross-provider unsigned replay must degrade: %+v (report %+v)", foreign, foreignReport)
	}
}

func TestNormalizeClaudeThinkingFallbackTextifiesCompletedToolTrajectory(t *testing.T) {
	msgs := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "signed reasoning", Signature: "sig"}},
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
			Provenance:     &message.MessageProvenance{Source: "import:claude", Imported: true, ProviderID: "claude-a", ModelID: "claude", WireFamily: WireFamilyAnthropic},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
	}
	target := TargetModel{
		ProviderID:              "claude-b",
		ModelID:                 "claude",
		WireFamily:              WireFamilyAnthropic,
		ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
		SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(out) != 1 || out[0].Role != message.RoleAssistant || len(out[0].ToolCalls) != 0 || !strings.Contains(out[0].Content, "[Previous tool call: read]") || !strings.Contains(out[0].Content, "[Previous tool result for call-1]") {
		t.Fatalf("Claude fallback must preserve completed action history, got %+v (report %+v)", out, report)
	}
}

func TestNormalizeStripsGeminiThoughtSignaturesOnMismatch(t *testing.T) {
	msg := message.Message{
		Role:        message.RoleAssistant,
		Content:     "working",
		GeminiParts: []message.GeminiReplayPart{{Type: "text", Text: "working", ThoughtSignature: "sig-text"}, {Type: "function_call", ToolCallID: "gemini_0", ThoughtSignature: "sig-fc"}},
		ToolCalls:   []message.ToolCall{{ID: "gemini_0", Name: "read", Args: []byte(`{}`), ThoughtSignature: "sig-fc"}},
		Provenance:  &message.MessageProvenance{ProviderID: "google", WireFamily: WireFamilyGemini, ModelID: "gemini-3-pro"},
	}
	msgs := []message.Message{msg, {Role: message.RoleTool, ToolCallID: "gemini_0", Content: "ok"}}

	// Same gemini model: signatures kept.
	sameTarget := TargetModel{
		ProviderID: "google", WireFamily: WireFamilyGemini, ModelID: "gemini-3-pro",
		ToolResultEncoding: ToolResultEncodingGeminiUserParts, SupportsStructuredTools: true,
	}
	out, _ := NormalizeForTarget(msgs, sameTarget, NormalizeOptions{StructuredTools: true})
	if len(out[0].GeminiParts) != 2 || out[0].GeminiParts[0].ThoughtSignature != "sig-text" || out[0].ToolCalls[0].ThoughtSignature != "sig-fc" {
		t.Fatalf("expected signatures kept for same gemini model: %+v", out[0])
	}

	// Different gemini model: kept optimistically at the native level,
	// stripped once the ladder reaches the synthesized level.
	crossModelTarget := TargetModel{
		ProviderID: "google", WireFamily: WireFamilyGemini, ModelID: "gemini-2.5-pro",
		ToolResultEncoding: ToolResultEncodingGeminiUserParts, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, crossModelTarget, NormalizeOptions{StructuredTools: true})
	if len(out[0].GeminiParts) != 2 || out[0].ToolCalls[0].ThoughtSignature != "sig-fc" {
		t.Fatalf("expected signatures kept optimistically for cross-model gemini target: %+v", out[0])
	}
	if report.ForeignNativeReplays != 1 {
		t.Fatalf("ForeignNativeReplays = %d, want 1", report.ForeignNativeReplays)
	}
	out, report = NormalizeForTarget(msgs, crossModelTarget, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(out[0].GeminiParts) != 0 || out[0].ToolCalls[0].ThoughtSignature != "" {
		t.Fatalf("expected signatures stripped at synthesized level: %+v", out[0])
	}
	if report.DowngradedReasoning == 0 {
		t.Fatal("expected DowngradedReasoning reported")
	}

	// Non-gemini target: signatures stripped at every level.
	nonGeminiTarget := TargetModel{
		ProviderID: "openai", WireFamily: WireFamilyOpenAIResponses, ModelID: "gemini-3-pro",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, report = NormalizeForTarget(msgs, nonGeminiTarget, NormalizeOptions{StructuredTools: true})
	if len(out[0].GeminiParts) != 0 || out[0].ToolCalls[0].ThoughtSignature != "" {
		t.Fatalf("expected signatures stripped for non-gemini target: %+v", out[0])
	}
	if report.DowngradedReasoning == 0 {
		t.Fatal("expected DowngradedReasoning reported")
	}
}

func TestNormalizeGeminiToAnthropicPreservesVisibleThoughtAndToolFacts(t *testing.T) {
	msgs := []message.Message{
		{
			Role: message.RoleAssistant,
			GeminiParts: []message.GeminiReplayPart{
				{Type: "thought", Text: "public Gemini thought", ThoughtSignature: "sig-thought"},
				{Type: "function_call", ToolCallID: "gemini_0", ThoughtSignature: "sig-call"},
			},
			ToolCalls:  []message.ToolCall{{ID: "gemini_0", Name: "read", Args: []byte(`{}`), ThoughtSignature: "sig-call"}},
			Provenance: &message.MessageProvenance{ProviderID: "google", ModelID: "gemini", WireFamily: WireFamilyGemini},
		},
		{Role: message.RoleTool, ToolCallID: "gemini_0", Content: "contents"},
	}
	target := TargetModel{
		ProviderID: "anthropic", ModelID: "claude", WireFamily: WireFamilyAnthropic,
		ToolResultEncoding: ToolResultEncodingAnthropicUserBlock, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || len(out[0].GeminiParts) != 0 || len(out[0].ToolCalls) != 1 || out[0].ToolCalls[0].ThoughtSignature != "" || !strings.Contains(out[0].Content, "public Gemini thought") {
		t.Fatalf("Gemini cross-wire normalization lost portable context: %+v (report %+v)", out, report)
	}
	if report.TextifiedReasoning != 1 || report.DroppedToolCalls != 0 || report.DroppedToolResults != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestNormalizeStrictTextifiesCompletedTrajectoryAcrossWireFamilies(t *testing.T) {
	tests := []struct {
		name   string
		msg    message.Message
		target TargetModel
	}{
		{
			name: "messages to responses",
			msg: message.Message{
				Role:           message.RoleAssistant,
				ThinkingBlocks: []message.ThinkingBlock{{Thinking: "messages reasoning", Signature: "sig"}},
				ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
				Provenance:     &message.MessageProvenance{ProviderID: "anthropic", ModelID: "claude", WireFamily: WireFamilyAnthropic},
			},
			target: TargetModel{ProviderID: "openai", ModelID: "gpt", WireFamily: WireFamilyOpenAIResponses, ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true},
		},
		{
			name: "chat to messages",
			msg: message.Message{
				Role:             message.RoleAssistant,
				ReasoningContent: "chat reasoning",
				ToolCalls:        []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
				Provenance:       &message.MessageProvenance{ProviderID: "deepseek", ModelID: "deepseek", WireFamily: WireFamilyOpenAIChat},
			},
			target: TargetModel{ProviderID: "anthropic", ModelID: "claude", WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks, ToolResultEncoding: ToolResultEncodingAnthropicUserBlock, SupportsStructuredTools: true},
		},
		{
			name: "responses to messages",
			msg: message.Message{
				Role: message.RoleAssistant,
				ResponsesOutput: []message.ResponsesOutputItem{
					{Type: "reasoning", ID: "rs-1", EncryptedContent: "opaque", Summary: []message.ResponsesReasoningSummary{{Type: "summary_text", Text: "responses summary"}}},
					{Type: "function_call", CallID: "call-1", Name: "read", Arguments: `{}`},
				},
				ToolCalls:  []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
				Provenance: &message.MessageProvenance{ProviderID: "openai", ModelID: "gpt", WireFamily: WireFamilyOpenAIResponses},
			},
			target: TargetModel{ProviderID: "anthropic", ModelID: "claude", WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks, ToolResultEncoding: ToolResultEncodingAnthropicUserBlock, SupportsStructuredTools: true},
		},
		{
			name: "gemini to chat",
			msg: message.Message{
				Role: message.RoleAssistant,
				GeminiParts: []message.GeminiReplayPart{
					{Type: "thought", Text: "gemini thought", ThoughtSignature: "sig-thought"},
					{Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "sig-call"},
				},
				ToolCalls:  []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`), ThoughtSignature: "sig-call"}},
				Provenance: &message.MessageProvenance{ProviderID: "google", ModelID: "gemini", WireFamily: WireFamilyGemini},
			},
			target: TargetModel{ProviderID: "openai", ModelID: "gpt", WireFamily: WireFamilyOpenAIChat, ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []message.Message{tc.msg, {Role: message.RoleTool, ToolCallID: "call-1", Content: "result"}}
			out, report := NormalizeForTarget(msgs, tc.target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatStrict})
			if len(out) != 1 || out[0].Role != message.RoleAssistant || len(out[0].ToolCalls) != 0 || !strings.Contains(out[0].Content, "[Previous tool call: read]") || !strings.Contains(out[0].Content, "[Previous tool result for call-1]") {
				t.Fatalf("strict cross-wire fallback lost completed history: %+v (report %+v)", out, report)
			}
			if report.DroppedToolCalls != 0 || report.DroppedToolResults != 0 {
				t.Fatalf("strict cross-wire fallback dropped tool facts: %+v", report)
			}
		})
	}
}

func TestNormalizeKeepsRedactedThinkingForAnthropicTarget(t *testing.T) {
	msg := message.Message{
		Role:           message.RoleAssistant,
		Content:        "done",
		ThinkingBlocks: []message.ThinkingBlock{{Data: "enc-redacted"}},
		Provenance:     &message.MessageProvenance{WireFamily: WireFamilyAnthropic, ModelID: "claude-fable-5"},
	}
	target := TargetModel{
		WireFamily: WireFamilyAnthropic, ModelID: "claude-fable-5",
		ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget([]message.Message{msg}, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 1 || out[0].ThinkingBlocks[0].Data != "enc-redacted" {
		t.Fatalf("expected redacted block kept, got %+v (report %+v)", out, report)
	}
	if report.DroppedThinkingBlocks != 0 {
		t.Fatalf("unexpected drops: %+v", report)
	}
}

func TestNormalizeKeepsOmittedThinkingToolTrajectory(t *testing.T) {
	msgs := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Signature: "sig-omitted"}},
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
			Provenance:     &message.MessageProvenance{WireFamily: WireFamilyAnthropic, ProviderID: "anthropic", ModelID: "claude"},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
	}
	target := TargetModel{
		WireFamily: WireFamilyAnthropic, ProviderID: "anthropic", ModelID: "claude",
		ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
		SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || len(out[0].ThinkingBlocks) != 1 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("signature-only thinking trajectory was dropped: %+v (report %+v)", out, report)
	}
}

func TestNormalizeAnthropicThinkingForeignReplayLadder(t *testing.T) {
	msgs := []message.Message{
		{
			Role:           message.RoleAssistant,
			ThinkingBlocks: []message.ThinkingBlock{{Thinking: "reasoning", Signature: "sig-1"}},
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{}`)}},
			Provenance:     &message.MessageProvenance{WireFamily: WireFamilyAnthropic, ProviderID: "relay-a", ModelID: "claude"},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
	}
	target := TargetModel{
		WireFamily: WireFamilyAnthropic, ProviderID: "relay-b", ModelID: "claude",
		ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
		SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || len(out[0].ThinkingBlocks) != 1 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("cross-provider anthropic thinking must be kept optimistically: %+v (report %+v)", out, report)
	}
	if report.ForeignNativeReplays != 1 {
		t.Fatalf("ForeignNativeReplays = %d, want 1", report.ForeignNativeReplays)
	}

	out, report = NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	for _, m := range out {
		if len(m.ThinkingBlocks) > 0 {
			t.Fatalf("synthesized level must strip foreign thinking blocks, got %+v", out)
		}
	}
	if report.DroppedThinkingBlocks == 0 {
		t.Fatalf("report = %+v, want dropped thinking blocks", report)
	}
}

func TestNormalizeKeepsCrossProviderResponsesToolTrajectory(t *testing.T) {
	msgs := []message.Message{
		responsesOutputMsg("sharedchat", "gpt-5.6-sol"),
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	target := TargetModel{
		ProviderID: "jianzhile", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.6-sol",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 {
		t.Fatalf("messages = %d, want assistant turn and tool result kept: %+v", len(out), out)
	}
	if len(out[0].ResponsesOutput) != 2 {
		t.Fatalf("native items = %+v, want kept at optimistic level for same model", out[0].ResponsesOutput)
	}
	if len(out[0].ToolCalls) != 1 || out[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v, want kept for cross-provider responses target", out[0].ToolCalls)
	}
	if out[1].Role != message.RoleTool || out[1].ToolCallID != "call_1" {
		t.Fatalf("tool result = %+v, want kept", out[1])
	}
	if report.ForeignNativeReplays != 1 {
		t.Fatalf("ForeignNativeReplays = %d, want 1", report.ForeignNativeReplays)
	}
}

func TestNormalizeKeepsForeignNativeReplayAcrossModels(t *testing.T) {
	msgs := []message.Message{
		responsesOutputMsg("sharedchat", "gpt-5.6-sol"),
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	target := TargetModel{
		ProviderID: "jianzhile", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.5",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || len(out[0].ResponsesOutput) != 2 {
		t.Fatalf("native items must be kept optimistically across models, got %+v", out)
	}
	if report.ForeignNativeReplays != 1 {
		t.Fatalf("ForeignNativeReplays = %d, want 1", report.ForeignNativeReplays)
	}

	out, report = NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(out) != 2 || len(out[0].ResponsesOutput) != 0 {
		t.Fatalf("synthesized level must strip cross-model native items, got %+v", out)
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want kept in synthesized form", out[0].ToolCalls)
	}
	if report.ForeignNativeReplays != 0 {
		t.Fatalf("ForeignNativeReplays = %d, want 0", report.ForeignNativeReplays)
	}
}

func TestNormalizeSynthesizedLevelStripsForeignNativeItems(t *testing.T) {
	msgs := []message.Message{
		responsesOutputMsg("sharedchat", "gpt-5.6-sol"),
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	target := TargetModel{
		ProviderID: "jianzhile", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.6-sol",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(out) != 2 {
		t.Fatalf("messages = %d, want turn kept: %+v", len(out), out)
	}
	if len(out[0].ResponsesOutput) != 0 {
		t.Fatalf("native items = %+v, want stripped", out[0].ResponsesOutput)
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want kept", out[0].ToolCalls)
	}
	if report.DowngradedReasoning == 0 {
		t.Fatal("expected native item stripping to be reported")
	}
}

func TestNormalizeStrictReplayTextifiesCrossProviderResponsesToolTrajectory(t *testing.T) {
	msgs := []message.Message{
		responsesOutputMsg("sharedchat", "gpt-5.6-sol"),
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	target := TargetModel{
		ProviderID: "jianzhile", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.6-sol",
		ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
	}
	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatStrict})
	if len(out) != 1 || out[0].Role != message.RoleAssistant || len(out[0].ToolCalls) > 0 || !strings.Contains(out[0].Content, "[Previous tool call: read]") || !strings.Contains(out[0].Content, "[Previous tool result for call_1]") {
		t.Fatalf("strict mode must textify the completed trajectory, got %+v", out)
	}
	if len(report.Warnings) == 0 {
		t.Fatalf("strict drop was not reported: %+v", report)
	}
}
