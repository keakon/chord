package modelcompat

import (
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
			name: "different model",
			msg:  responsesOutputMsg("openai", "gpt-5.6-sol"),
			target: TargetModel{
				ProviderID: "openai", WireFamily: WireFamilyOpenAIResponses, ModelID: "gpt-5.5",
				ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true,
			},
		},
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

func TestNormalizeVisibleReasoningRequiresMatchingProvider(t *testing.T) {
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
	if len(out) != 0 {
		t.Fatalf("cross-model native reasoning tool trajectory must be dropped, got %+v", out)
	}
	if report.DowngradedReasoning == 0 || len(report.Warnings) == 0 {
		t.Fatalf("report = %+v, want reasoning and tool trajectory downgrade", report)
	}

	target.ProviderID = "deepseek"
	target.ModelID = "deepseek-v4"
	out, report = NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || out[0].ReasoningContent != "native reasoning" || len(out[0].ToolCalls) != 1 {
		t.Fatalf("same-provider native reasoning must survive model upgrades: %+v (report %+v)", out, report)
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

	// Different model / non-gemini target: signatures stripped.
	for _, target := range []TargetModel{
		{ProviderID: "google", WireFamily: WireFamilyGemini, ModelID: "gemini-2.5-pro", ToolResultEncoding: ToolResultEncodingGeminiUserParts, SupportsStructuredTools: true},
		{ProviderID: "openai", WireFamily: WireFamilyOpenAIResponses, ModelID: "gemini-3-pro", ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true},
	} {
		out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
		if len(out[0].GeminiParts) != 0 || out[0].ToolCalls[0].ThoughtSignature != "" {
			t.Fatalf("expected signatures stripped for target %+v: %+v", target, out[0])
		}
		if report.DowngradedReasoning == 0 {
			t.Fatal("expected DowngradedReasoning reported")
		}
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
