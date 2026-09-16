package modelcompat

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestModelNativeFamily(t *testing.T) {
	cases := []struct {
		modelID string
		want    string
	}{
		{modelID: "", want: NativeFamilyUnknown},
		{modelID: "gemini-3-pro", want: NativeFamilyGemini},
		{modelID: "google/gemini-2.5-flash", want: NativeFamilyGemini},
		{modelID: "vertex/gemini-3-flash", want: NativeFamilyGemini},
		{modelID: "claude-fable-5.1", want: NativeFamilyAnthropic},
		{modelID: "anthropic/claude-opus-4.6", want: NativeFamilyAnthropic},
		{modelID: "gpt-5.2", want: NativeFamilyOpenAI},
		{modelID: "openai/o3-codex", want: NativeFamilyOpenAI},
		{modelID: "deepseek-v4.1-flash", want: NativeFamilyUnknown},
		{modelID: "gateway-alias", want: NativeFamilyUnknown},
	}
	for _, tc := range cases {
		if got := ModelNativeFamily(tc.modelID); got != tc.want {
			t.Fatalf("ModelNativeFamily(%q) = %q, want %q", tc.modelID, got, tc.want)
		}
	}
}

func TestWireNativeFamily(t *testing.T) {
	cases := map[string]string{
		WireFamilyAnthropic:       NativeFamilyAnthropic,
		WireFamilyGemini:          NativeFamilyGemini,
		WireFamilyOpenAIResponses: NativeFamilyOpenAI,
		WireFamilyOpenAIChat:      NativeFamilyUnknown,
		WireFamilyUnknown:         NativeFamilyUnknown,
	}
	for wire, want := range cases {
		if got := WireNativeFamily(wire); got != want {
			t.Fatalf("WireNativeFamily(%q) = %q, want %q", wire, got, want)
		}
	}
}

func familyFixture(provenance *message.MessageProvenance, blocks []message.ThinkingBlock, parts []message.GeminiReplayPart, signature string) []message.Message {
	return []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{
			Role:           message.RoleAssistant,
			ToolCalls:      []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`), ThoughtSignature: signature}},
			ThinkingBlocks: blocks,
			GeminiParts:    parts,
			Provenance:     provenance,
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
	}
}

func chatTarget(providerID, modelID, nativeFamily string) TargetModel {
	return TargetModel{
		ProviderID:              providerID,
		ModelID:                 modelID,
		WireFamily:              WireFamilyOpenAIChat,
		NativeFamily:            nativeFamily,
		SupportsStructuredTools: true,
		ToolResultEncoding:      ToolResultEncodingOpenAIToolRole,
	}
}

func geminiTarget(providerID, modelID string) TargetModel {
	return TargetModel{
		ProviderID:              providerID,
		ModelID:                 modelID,
		WireFamily:              WireFamilyGemini,
		NativeFamily:            ModelNativeFamily(modelID),
		SupportsStructuredTools: true,
		ToolResultEncoding:      ToolResultEncodingGeminiUserParts,
	}
}

func anthropicTarget(providerID, modelID string) TargetModel {
	return TargetModel{
		ProviderID:              providerID,
		ModelID:                 modelID,
		WireFamily:              WireFamilyAnthropic,
		NativeFamily:            ModelNativeFamily(modelID),
		ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks,
		SupportsStructuredTools: true,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
	}
}

// A thinking block produced through a Messages-wire gateway fronting Gemini
// keeps its signature when the next request reaches the same backend through
// the native Gemini wire; the block becomes the thought part the native wire
// serializes.
func TestNormalizeForTarget_ConvertsBlocksToGeminiParts(t *testing.T) {
	msgs := familyFixture(
		&message.MessageProvenance{Source: "chord", ProviderID: "messages-gw", ModelID: "gemini-3-pro", WireFamily: WireFamilyAnthropic},
		[]message.ThinkingBlock{{Thinking: "plan", Signature: "gemini-sig"}},
		nil,
		"",
	)
	out, report := NormalizeForTarget(msgs, geminiTarget("native-gemini", "gemini-3-pro"), NormalizeOptions{StructuredTools: true})
	got := out[1].GeminiParts
	if len(got) != 1 || got[0].Type != "thought" || got[0].Text != "plan" || got[0].ThoughtSignature != "gemini-sig" {
		t.Fatalf("GeminiParts = %#v, want one signed thought part", got)
	}
	if report.ConvertedReasoning == 0 {
		t.Fatalf("ConvertedReasoning = 0, want the carrier conversion reported")
	}
}

// The reverse direction: signatures captured through the native Gemini wire are
// replayed to a Messages-wire gateway fronting the same family. Only thought
// parts have a Messages carrier; the per-call signature has no slot there.
func TestNormalizeForTarget_ConvertsGeminiThoughtPartsToBlocks(t *testing.T) {
	msgs := familyFixture(
		&message.MessageProvenance{Source: "chord", ProviderID: "native-gemini", ModelID: "gemini-3-pro", WireFamily: WireFamilyGemini},
		nil,
		[]message.GeminiReplayPart{
			{Type: "thought", Text: "plan", ThoughtSignature: "thought-sig"},
			{Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "call-sig"},
		},
		"",
	)
	out, _ := NormalizeForTarget(msgs, anthropicTarget("messages-gw", "gemini-3-pro"), NormalizeOptions{StructuredTools: true})
	got := out[1].ThinkingBlocks
	if len(got) != 1 || got[0].Thinking != "plan" || got[0].Signature != "thought-sig" {
		t.Fatalf("ThinkingBlocks = %#v, want the signed thought block", got)
	}
}

// A Chat Completions gateway fronting Gemini carries the step signature on the
// first function call, so a native-wire capture collapses onto that slot.
func TestNormalizeForTarget_ConvertsGeminiPartsToChatCarrier(t *testing.T) {
	msgs := familyFixture(
		&message.MessageProvenance{Source: "chord", ProviderID: "native-gemini", ModelID: "gemini-3-pro", WireFamily: WireFamilyGemini},
		nil,
		[]message.GeminiReplayPart{
			{Type: "thought", Text: "plan", ThoughtSignature: "thought-sig"},
			{Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "call-sig"},
		},
		"",
	)
	out, _ := NormalizeForTarget(msgs, chatTarget("chat-gw", "gemini-3-pro", NativeFamilyGemini), NormalizeOptions{StructuredTools: true})
	if got := out[1].ToolCalls[0].ThoughtSignature; got != "call-sig" {
		t.Fatalf("ToolCalls[0].ThoughtSignature = %q, want the step signature", got)
	}
}

// A Claude writing through a Messages wire and a Claude served by a Chat
// Completions gateway are the same family: the signed blocks survive the wire
// change and the request emits them as thinking_blocks.
func TestNormalizeForTarget_KeepsClaudeBlocksForChatTarget(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "messages-claude", ModelID: "claude-fable-5.1", WireFamily: WireFamilyAnthropic}
	msgs := familyFixture(provenance, []message.ThinkingBlock{{Thinking: "plan", Signature: "claude-sig"}}, nil, "")

	out, report := NormalizeForTarget(msgs, chatTarget("chat-gateway", "claude-fable-5.1", NativeFamilyAnthropic), NormalizeOptions{StructuredTools: true})
	got := out[1].ThinkingBlocks
	if len(got) != 1 || got[0].Signature != "claude-sig" {
		t.Fatalf("ThinkingBlocks = %#v, want the block kept for the chat carrier", got)
	}
	if report.ForeignNativeReplays == 0 {
		t.Fatalf("ForeignNativeReplays = 0, want the cross-provider family replay reported")
	}
}

// The same blocks never reach a Gemini-family target, even on the same wire.
func TestNormalizeForTarget_StripsBlocksAcrossFamilies(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "messages-claude", ModelID: "claude-fable-5.1", WireFamily: WireFamilyAnthropic}
	msgs := familyFixture(provenance, []message.ThinkingBlock{{Thinking: "plan", Signature: "claude-sig"}}, nil, "")

	out, report := NormalizeForTarget(msgs, chatTarget("chat-gateway", "gemini-3-pro", NativeFamilyGemini), NormalizeOptions{StructuredTools: true})
	if len(out[1].ThinkingBlocks) != 0 {
		t.Fatalf("ThinkingBlocks = %#v, want them stripped for another family", out[1].ThinkingBlocks)
	}
	if report.DroppedThinkingBlocks == 0 {
		t.Fatalf("DroppedThinkingBlocks = 0, want the strip reported")
	}
}

func TestNormalizeForTarget_StripsGeminiSignaturesAcrossFamilies(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "native-gemini", ModelID: "gemini-3-pro", WireFamily: WireFamilyGemini}
	msgs := familyFixture(provenance, nil, []message.GeminiReplayPart{{Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "gemini-sig"}}, "gemini-sig")

	out, report := NormalizeForTarget(msgs, chatTarget("chat-gateway", "claude-fable-5.1", NativeFamilyAnthropic), NormalizeOptions{StructuredTools: true})
	if len(out[1].GeminiParts) != 0 || out[1].ToolCalls[0].ThoughtSignature != "" {
		t.Fatalf("signatures survived a cross-family switch: parts=%#v signature=%q", out[1].GeminiParts, out[1].ToolCalls[0].ThoughtSignature)
	}
	if report.DowngradedReasoning == 0 {
		t.Fatalf("DowngradedReasoning = 0, want the strip reported")
	}
}

// An unresolved family on either side keeps the blob stripped: replaying a
// signature the target is not known to validate only produces a rejection.
func TestNormalizeForTarget_StripsSignaturesWhenFamilyIsUnknown(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "native-gemini", ModelID: "gemini-3-pro", WireFamily: WireFamilyGemini}
	msgs := familyFixture(provenance, nil, []message.GeminiReplayPart{{Type: "function_call", ToolCallID: "call-1", ThoughtSignature: "gemini-sig"}}, "gemini-sig")

	out, _ := NormalizeForTarget(msgs, chatTarget("alias-gateway", "gateway-alias", NativeFamilyUnknown), NormalizeOptions{StructuredTools: true})
	if len(out[1].GeminiParts) != 0 || out[1].ToolCalls[0].ThoughtSignature != "" {
		t.Fatalf("signatures survived an unknown target family: parts=%#v signature=%q", out[1].GeminiParts, out[1].ToolCalls[0].ThoughtSignature)
	}
}

// The replay ladder still escalates: once a target rejected the family-matched
// payload, the degraded levels stop replaying foreign blobs.
func TestNormalizeForTarget_FamilyReplayOnlyAtNativeLevel(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "native-gemini", ModelID: "gemini-3-pro", WireFamily: WireFamilyGemini}
	msgs := familyFixture(provenance, nil, nil, "gemini-sig")

	out, _ := NormalizeForTarget(
		msgs,
		chatTarget("chat-gateway", "gemini-3-pro", NativeFamilyGemini),
		NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized},
	)
	if out[1].ToolCalls[0].ThoughtSignature != "" {
		t.Fatalf("signature replayed at the synthesized level: %q", out[1].ToolCalls[0].ThoughtSignature)
	}
}

// A Messages-wire gateway fronting Gemini must replay signed blocks even when
// the request does not enable thinking, because Gemini 3 validates
// function-call history regardless of the request's thinking setting.
func TestNormalizeForTarget_KeepsSignedBlocksWithoutThinkingMode(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "messages-gw", ModelID: "gemini-3-pro", WireFamily: WireFamilyAnthropic}
	msgs := familyFixture(provenance, []message.ThinkingBlock{{Thinking: "plan", Signature: "gemini-sig"}}, nil, "")

	target := anthropicTarget("messages-gw", "gemini-3-pro")
	target.ReasoningContinuityMode = ReasoningContinuityNone
	out, _ := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if got := out[1].ThinkingBlocks; len(got) != 1 || got[0].Signature != "gemini-sig" {
		t.Fatalf("ThinkingBlocks = %#v, want the signed block kept", got)
	}
}

// A Messages-wire gateway that declared it only verifies unsigned thinking
// keeps rejecting signature blobs, even when the family matches.
func TestNormalizeForTarget_UnsignedModeKeepsRejectingForeignSignedBlocks(t *testing.T) {
	provenance := &message.MessageProvenance{Source: "chord", ProviderID: "messages-other", ModelID: "gemini-3-pro", WireFamily: WireFamilyAnthropic}
	msgs := familyFixture(provenance, []message.ThinkingBlock{{Thinking: "plan", Signature: "gemini-sig"}}, nil, "")

	target := anthropicTarget("messages-gw", "gemini-3-pro")
	target.ReasoningContinuityMode = ReasoningContinuityAnthropicUnsigned
	out, _ := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	for _, block := range out[1].ThinkingBlocks {
		if block.Signature != "" {
			t.Fatalf("signed block replayed to an anthropic_unsigned target: %#v", block)
		}
	}
}
