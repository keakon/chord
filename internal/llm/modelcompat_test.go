package llm

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func TestNormalizeMessagesForPoolTarget_PreservesAnthropicThinkingForAnthropicTarget(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	msgs := []message.Message{{
		Role:           "assistant",
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

func TestNormalizeMessagesForPoolTarget_DropsThinkingForOpenAITarget(t *testing.T) {
	provider := NewProviderConfig("openai-main", config.ProviderConfig{Type: config.ProviderTypeResponses}, nil)
	msgs := []message.Message{{
		Role:           "assistant",
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

func TestNormalizeMessagesForPoolTarget_DowngradesMissingToolResultForAnthropic(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{{
		Role:       "assistant",
		ToolCalls:  []message.ToolCall{{ID: "toolu_1", Name: "Bash", Args: args}},
		Provenance: &message.MessageProvenance{Source: "import:claude", WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	out, rep := normalizeMessagesForPoolTarget(msgs, FallbackModel{ProviderConfig: provider, ModelID: "claude-sonnet"}, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled"}})
	if len(out) != 1 || len(out[0].ToolCalls) != 0 || out[0].Role != "assistant" {
		t.Fatalf("expected downgrade to assistant text, got %+v", out)
	}
	if rep.DowngradedToolCalls == 0 {
		t.Fatalf("DowngradedToolCalls=%d, want >0", rep.DowngradedToolCalls)
	}
}
