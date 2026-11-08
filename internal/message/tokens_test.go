package message

import (
	"encoding/json"
	"strings"
	"testing"
)

// Image parts are charged a per-image allowance, never their base64 length: a
// 300 KB screenshot would otherwise add ~100K phantom tokens to the estimate.
func TestEstimateMessageTokensChargesImagesPerImage(t *testing.T) {
	text := strings.Repeat("a", 3000)
	textOnly := Message{Role: RoleUser, Content: text}
	textPart := ContentPart{Type: ContentPartText, Text: text}
	for _, size := range []int{1000, 300_000} {
		msg := Message{Role: RoleUser, Parts: []ContentPart{
			textPart,
			{Type: ContentPartImage, Data: make([]byte, size)},
		}}
		if got, want := EstimateMessageTokens(msg), EstimateMessageTokens(textOnly)+ImagePartEstimateTokens; got != want {
			t.Fatalf("EstimateMessageTokens(image %d bytes) = %d, want %d (text estimate plus per-image allowance)", size, got, want)
		}
	}
}

func TestEstimateMessagesTokensCountsToolCallsAndThinking(t *testing.T) {
	msgs := []Message{
		{
			Role:    RoleAssistant,
			Content: "abcd",
			ToolCalls: []ToolCall{
				{ID: "1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)},
			},
			ThinkingBlocks: []ThinkingBlock{
				{Thinking: "reasoning", Signature: "sig"},
			},
			ReasoningContent: "hidden reasoning",
		},
	}

	got := EstimateMessagesTokens(msgs)
	if got <= 1 {
		t.Fatalf("expected token estimate to include tool args and thinking, got %d", got)
	}
}

func TestNativeImageAccountingUsesOnlyReplayedSuccessfulItems(t *testing.T) {
	part := ContentPart{Type: ContentPartImage, DataBytes: 1024}
	native := &NativeToolHistory{
		Items: []json.RawMessage{json.RawMessage(`{"type":"image_generation_call","id":"current","status":"completed"}`), json.RawMessage(`{"type":"image_generation_call","id":"failed","status":"failed"}`)},
		Calls: []HostedCall{
			{ID: "earlier", Kind: HostedCallKindImageGeneration, Parts: []ContentPart{part}},
			{ID: "current", Kind: HostedCallKindImageGeneration, Status: HostedCallStatusCompleted, Parts: []ContentPart{part}, Result: json.RawMessage(`{"images":[{"width":6000,"height":6000}]}`)},
			{ID: "failed", Kind: HostedCallKindImageGeneration, Status: HostedCallStatusFailed, Error: "generation failed"},
		},
	}
	bytes, tokens := NativeImageAccounting(native)
	if bytes != 1368 || tokens != 48000 {
		t.Fatalf("wrong surface or original allowance: bytes=%d tokens=%d", bytes, tokens)
	}
	native.Items = native.Items[1:]
	if bytes, tokens := NativeImageAccounting(native); bytes != 0 || tokens != 0 {
		t.Fatalf("failed item charged for an image: %d, %d", bytes, tokens)
	}
}

func TestContextPromptTokensPrefersWindowObservation(t *testing.T) {
	window := &ContextTokenUsage{Known: true, PromptTokens: 4242}
	usage := TokenUsage{InputTokens: 100, CacheReadTokens: 300, Context: window}
	if got, known := ContextPromptTokens(usage); !known || got != 4242 {
		t.Fatalf("ContextPromptTokens(window) = %d, %v; want 4242, true", got, known)
	}
	unknown := &ContextTokenUsage{Known: false}
	if _, known := ContextPromptTokens(TokenUsage{Context: unknown}); known {
		t.Fatal("unknown window observation reported as known")
	}
	got, known := ContextPromptTokens(TokenUsage{InputTokens: 100, CacheReadTokens: 300})
	if !known || got != 400 {
		t.Fatalf("ContextPromptTokens(flat usage) = %d, %v; want 400, true", got, known)
	}
	if _, known := ContextPromptTokens(TokenUsage{}); known {
		t.Fatal("all-zero usage reported as a real observation")
	}
}
