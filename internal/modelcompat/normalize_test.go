package modelcompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNormalizeForTarget_PreservesAnthropicThinkingWhenEnabled(t *testing.T) {
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     &message.MessageProvenance{Source: "import:claude", WireFamily: WireFamilyAnthropic},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks}, NormalizeOptions{})
	if len(out) != 1 || len(out[0].ThinkingBlocks) != 1 {
		t.Fatalf("thinking stripped unexpectedly: %+v", out)
	}
	if rep.DroppedThinkingBlocks != 0 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 0", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeForTargetDeepCopiesCompactionFileRevisions(t *testing.T) {
	msgs := []message.Message{{
		Role:                    message.RoleUser,
		Content:                 "[Context Summary]\nsummary",
		IsCompactionSummary:     true,
		CompactionFileRevisions: map[string]string{"key.go": "abc123"},
	}}
	out, _ := NormalizeForTarget(msgs, TargetModel{}, NormalizeOptions{})
	if len(out) != 1 || out[0].CompactionFileRevisions["key.go"] != "abc123" {
		t.Fatalf("normalized checkpoint = %#v", out)
	}
	out[0].CompactionFileRevisions["key.go"] = "mutated"
	if got := msgs[0].CompactionFileRevisions["key.go"]; got != "abc123" {
		t.Fatalf("source revision changed through normalized alias: %q", got)
	}
}

func TestNormalizeForTarget_DropsAnthropicThinkingWithoutReplayEnable(t *testing.T) {
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
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
		Role:             message.RoleAssistant,
		ReasoningContent: "hidden reasoning",
		Provenance:       &message.MessageProvenance{WireFamily: WireFamilyOpenAIChat},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks}, NormalizeOptions{})
	if len(out) != 0 {
		t.Fatalf("reasoning should be dropped for anthropic target: %+v", out)
	}
	if rep.DowngradedReasoning != 1 {
		t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
	}
}

func TestNormalizeForTarget_ConvertsOpenAIReasoningToUnsignedAnthropicThinking(t *testing.T) {
	msgs := []message.Message{
		{
			Role:             message.RoleAssistant,
			Content:          "calling tool",
			ReasoningContent: "portable reasoning",
			ToolCalls:        []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}},
			Provenance:       &message.MessageProvenance{ProviderID: "source-chat", ModelID: "glm-5.2", WireFamily: WireFamilyOpenAIChat},
		},
		{Role: message.RoleTool, ToolCallID: "call-1", Content: "result"},
	}
	target := TargetModel{
		ProviderID:              "target-messages",
		ModelID:                 "glm-5.2",
		WireFamily:              WireFamilyAnthropic,
		ReasoningContinuityMode: ReasoningContinuityAnthropicUnsigned,
		ToolResultEncoding:      ToolResultEncodingAnthropicUserBlock,
		SupportsStructuredTools: true,
	}

	out, report := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out) != 2 || out[0].ReasoningContent != "" || len(out[0].ThinkingBlocks) != 1 {
		t.Fatalf("converted messages = %+v", out)
	}
	if got := out[0].ThinkingBlocks[0]; got.Thinking != "portable reasoning" || got.Signature != "" || got.Data != "" {
		t.Fatalf("converted thinking block = %+v", got)
	}
	if out[0].Content != "calling tool" || strings.Contains(out[0].Content, portableReasoningMarker) || len(out[0].ToolCalls) != 1 {
		t.Fatalf("visible/tool content changed during conversion: %+v", out[0])
	}
	if report.ConvertedReasoning != 1 || report.DowngradedReasoning != 0 || report.TextifiedReasoning != 0 {
		t.Fatalf("report = %+v", report)
	}
	if msgs[0].ReasoningContent != "portable reasoning" || len(msgs[0].ThinkingBlocks) != 0 {
		t.Fatalf("input mutated: %+v", msgs[0])
	}

	degraded, degradedReport := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(degraded) != 2 || degraded[0].ReasoningContent != "" || len(degraded[0].ThinkingBlocks) != 0 || len(degraded[0].ToolCalls) != 1 {
		t.Fatalf("synthesized fallback = %+v", degraded)
	}
	if strings.Contains(degraded[0].Content, portableReasoningMarker) || strings.Contains(degraded[0].Content, "portable reasoning") {
		t.Fatalf("synthesized fallback leaked reasoning into content: %q", degraded[0].Content)
	}
	if degradedReport.ConvertedReasoning != 0 || degradedReport.DowngradedReasoning != 1 || degradedReport.TextifiedReasoning != 0 {
		t.Fatalf("synthesized report = %+v", degradedReport)
	}
}

func TestNormalizeForTarget_DropsAnthropicThinkingForOpenAI(t *testing.T) {
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
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
		Role:           message.RoleAssistant,
		Content:        "hello",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		Provenance:     nil,
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks}, NormalizeOptions{})
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

func TestNormalizeForTarget_DropsEmptyAssistantAfterThinkingRemoval(t *testing.T) {
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
		Content:        "",
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: ""}},
		Provenance:     &message.MessageProvenance{Source: "chord", WireFamily: WireFamilyAnthropic},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks}, NormalizeOptions{})
	if len(out) != 0 {
		t.Fatalf("expected empty output after dropping unreplayable assistant, got %+v", out)
	}
	if rep.DroppedThinkingBlocks != 1 {
		t.Fatalf("DroppedThinkingBlocks=%d, want 1", rep.DroppedThinkingBlocks)
	}
}

func TestNormalizeForTarget_DropsReasoningOnlyAssistant(t *testing.T) {
	msgs := []message.Message{{
		Role:             message.RoleAssistant,
		ReasoningContent: "hidden reasoning",
		Provenance:       &message.MessageProvenance{Source: "chord", WireFamily: WireFamilyOpenAIChat},
	}}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyOpenAIChat, ToolResultEncoding: ToolResultEncodingOpenAIToolRole, SupportsStructuredTools: true}, NormalizeOptions{StructuredTools: true})
	if len(out) != 0 {
		t.Fatalf("expected reasoning-only assistant to be dropped, got %+v", out)
	}
	if rep.DowngradedReasoning != 1 {
		t.Fatalf("DowngradedReasoning=%d, want 1", rep.DowngradedReasoning)
	}
}

func TestNormalizeForTarget_DowngradesImportedStructuredToolsWhenDisabled(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{
		{Role: message.RoleAssistant, Content: "", ToolCalls: []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: args}}, Provenance: &message.MessageProvenance{Imported: true}},
		{Role: message.RoleTool, ToolCallID: "toolu_1", Content: "ok", Provenance: &message.MessageProvenance{Imported: true}},
	}
	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic}, NormalizeOptions{StructuredTools: false})
	if len(out) != 1 {
		t.Fatalf("len(out)=%d, want 1 after assistant merge", len(out))
	}
	if out[0].Role != message.RoleAssistant || len(out[0].ToolCalls) != 0 {
		t.Fatalf("expected assistant text downgrade, got %+v", out[0])
	}
	if rep.DowngradedToolCalls == 0 {
		t.Fatalf("DowngradedToolCalls=%d, want >0", rep.DowngradedToolCalls)
	}
}

func TestNormalizeForTarget_DropsNonImportedUnreplayableToolsWithoutImportedMarker(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{
		{
			Role:       message.RoleAssistant,
			Content:    "I will inspect the workspace.",
			ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "shell", Args: args}},
			Provenance: &message.MessageProvenance{Source: "chord", WireFamily: WireFamilyOpenAIChat},
		},
	}

	out, rep := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic, SupportsStructuredTools: true, ToolResultEncoding: ToolResultEncodingAnthropicUserBlock}, NormalizeOptions{StructuredTools: true})
	if len(out) != 1 {
		t.Fatalf("len(out)=%d, want 1", len(out))
	}
	if len(out[0].ToolCalls) != 0 {
		t.Fatalf("tool calls should be dropped from request copy, got %+v", out[0].ToolCalls)
	}
	if out[0].Content != "I will inspect the workspace." {
		t.Fatalf("content=%q", out[0].Content)
	}
	if rep.DowngradedToolCalls != 0 {
		t.Fatalf("DowngradedToolCalls=%d, want 0", rep.DowngradedToolCalls)
	}
	if strings.Contains(out[0].Content, "[Imported tool call") {
		t.Fatalf("non-imported tool call was rendered as imported marker: %q", out[0].Content)
	}
	if len(rep.Warnings) == 0 {
		t.Fatalf("expected warning for dropped non-imported tool call")
	}
}

func TestNormalizeForTarget_TextifiesCompletedToolsWhenStructuredToolsDisabled(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call_1", Name: "shell", Args: args}}},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "ok"},
	}
	out, report := NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyAnthropic}, NormalizeOptions{StructuredTools: false})
	if len(out) != 1 || out[0].Role != message.RoleAssistant || len(out[0].ToolCalls) != 0 || !strings.Contains(out[0].Content, "[Previous tool call: shell]") || !strings.Contains(out[0].Content, "[Previous tool result for call_1]") {
		t.Fatalf("completed tool history should be textified, got %+v (report %+v)", out, report)
	}
}

func TestNormalizeForTarget_DoesNotMutateInput(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"command": "ls"})
	msgs := []message.Message{{
		Role:           message.RoleAssistant,
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "t", Signature: "sig"}},
		ToolCalls:      []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: args}},
	}}
	_, _ = NormalizeForTarget(msgs, TargetModel{WireFamily: WireFamilyOpenAIChat}, NormalizeOptions{StructuredTools: false})
	if len(msgs[0].ThinkingBlocks) != 1 || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("input mutated: %+v", msgs[0])
	}
}
