package modelcompat

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNormalizeForTarget_PreservesAnthropicThinkingWhenEnabled(t *testing.T) {
	msgs := []message.Message{{
		Role:           "assistant",
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     &message.MessageProvenance{Source: "import:claude", WireFamily: WireFamilyAnthropic},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ThinkingReplayEnabled: true}, NormalizeOptions{})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 1 {
		t.Fatalf("thinking stripped unexpectedly: %+v", out)
	}
	if rep.DroppedThinkingBlocks != 0 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 0", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeForTarget_DropsAnthropicThinkingWithoutReplayEnable(t *testing.T) {
	msgs := []message.Message{{
		Role:           "assistant",
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     &message.MessageProvenance{Source: "chord", ProviderID: "deepseek", WireFamily: WireFamilyAnthropic},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic}, NormalizeOptions{})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 0 {
		t.Fatalf("thinking should be stripped when replay is not enabled: %+v", out)
	}
	if rep.DroppedThinkingBlocks != 1 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 1", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeForTarget_DropsReasoningContentForAnthropicTarget(t *testing.T) {
	msgs := []message.Message{{
		Role:             "assistant",
		ReasoningContent: "hidden reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: WireFamilyOpenAIChat},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ThinkingReplayEnabled: true}, NormalizeOptions{})
	if len(out) != 1 || out[0].ReasoningContent != "" {
		t.Fatalf("reasoning should be dropped for anthropic target: %+v", out)
	}
	if rep.DowngradedReasoning != 1 {
		t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
	}
}

func TestNormalizeForTarget_DropsAnthropicThinkingForOpenAI(t *testing.T) {
	msgs := []message.Message{{
		Role:           "assistant",
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     &message.MessageProvenance{Source: "import:claude", WireFamily: WireFamilyAnthropic},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyOpenAIChat}, NormalizeOptions{})
	if len(out[0].ThinkingBlocks) != 0 {
		t.Fatalf("thinking should be dropped: %+v", out[0])
	}
	if rep.DroppedThinkingBlocks != 1 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 1", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeForTarget_DropsThinkingWithoutProvenance(t *testing.T) {
	msgs := []message.Message{{
		Role:           "assistant",
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     nil,
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ThinkingReplayEnabled: true}, NormalizeOptions{})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 0 {
		t.Fatalf("thinking should be dropped without provenance: %+v", out[0])
	}
	if rep.DroppedThinkingBlocks != 1 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 1", rep.DroppedThinkingBlocks)
	}
	if len(rep.Warnings) == 0 {
		t.Fatalf("expected warning when dropping thinking without provenance")
	}
}

func TestNormalizeForTarget_DowngradesStructuredToolsWhenDisabled(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{
		{Role: "assistant", Content: "", ToolCalls: []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: args}}},
		{Role: "tool", ToolCallID: "toolu_1", Content: "ok"},
	}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic}, NormalizeOptions{StructuredTools: false})
	if len(out) != 1 {
		t.Fatalf("len(out)=%d, want 1 after assistant merge", len(out))
	}
	if out[0].Role != "assistant" || len(out[0].ToolCalls) != 0 {
		t.Fatalf("expected assistant text downgrade, got %+v", out[0])
	}
	if rep.DowngradedToolCalls == 0 {
		t.Fatalf("DowngradedToolCalls=%d, want >0", rep.DowngradedToolCalls)
	}
}

func TestNormalizeForTarget_DoesNotMutateInput(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{{
		Role:           "assistant",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		ToolCalls:      []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: args}},
	}}
	_, _ = NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyOpenAIChat}, NormalizeOptions{StructuredTools: false})
	if len(msgs[0].ThinkingBlocks) != 1 || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("input mutated: %+v", msgs[0])
	}
}
