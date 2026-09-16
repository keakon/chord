package modelcompat

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNormalizeThinkingBlocksRequiresMatchingFamily(t *testing.T) {
	for _, provider := range []string{"source", "other"} {
		for _, family := range []string{NativeFamilyAnthropic, NativeFamilyUnknown} {
			t.Run(provider+"/"+family, func(t *testing.T) {
				msgs := familyFixture(&message.MessageProvenance{ProviderID: "source", ModelID: "deployment-a", WireFamily: WireFamilyOpenAIChat, NativeFamily: family}, []message.ThinkingBlock{{Thinking: "plan", Signature: "signed-block"}}, nil, "")
				target := anthropicTarget(provider, "gemini-3-test")
				target.ReasoningContinuityMode = ""
				out, _ := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
				if len(out[1].ThinkingBlocks) != 0 {
					t.Fatal("foreign or unknown family retained signed blocks")
				}
			})
		}
	}
	msgs := familyFixture(&message.MessageProvenance{ProviderID: "source", ModelID: "claude-test", WireFamily: WireFamilyAnthropic}, []message.ThinkingBlock{{Thinking: "plan", Signature: "signed-block"}}, nil, "")
	for _, provider := range []string{"source", "other"} {
		target := anthropicTarget(provider, "gemini-3-test")
		target.ReasoningContinuityMode = ""
		out, _ := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
		if len(out[1].ThinkingBlocks) != 0 {
			t.Fatal("Messages wire bypassed family validation")
		}
	}
}

func TestNormalizeAliasedThinkingStateAfterSerialization(t *testing.T) {
	msgs := familyFixture(&message.MessageProvenance{ProviderID: "sample", ModelID: "deployment-a", WireFamily: WireFamilyOpenAIChat, NativeFamily: NativeFamilyAnthropic}, []message.ThinkingBlock{{Thinking: "plan", Signature: "signed-block"}}, nil, "")
	encoded, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	var restored []message.Message
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	out, _ := NormalizeForTarget(restored, chatTarget("sample", "deployment-a", NativeFamilyAnthropic), NormalizeOptions{StructuredTools: true})
	if len(out[1].ThinkingBlocks) != 1 || out[1].ThinkingBlocks[0].Signature != "signed-block" {
		t.Fatal("restored alias lost its signed block")
	}
	degraded, _ := NormalizeForTarget(restored, chatTarget("sample", "deployment-a", NativeFamilyAnthropic), NormalizeOptions{StructuredTools: true, ReplayCompat: ReplayCompatSynthesized})
	if len(degraded[1].ThinkingBlocks) != 0 {
		t.Fatal("rejected Chat signature survived synthesized replay")
	}
}

func TestNormalizeGeminiBlocksToChatSignature(t *testing.T) {
	msgs := familyFixture(&message.MessageProvenance{ProviderID: "messages-gw", ModelID: "gemini-3-test", WireFamily: WireFamilyAnthropic}, []message.ThinkingBlock{{Thinking: "plan", Signature: "signed-block"}}, nil, "")
	out, report := NormalizeForTarget(msgs, chatTarget("chat-gw", "gemini-3-test", NativeFamilyGemini), NormalizeOptions{StructuredTools: true})
	if out[1].ToolCalls[0].ThoughtSignature != "signed-block" || report.ConvertedReasoning == 0 {
		t.Fatalf("missing converted signature: %+v", report)
	}
	if msgs[1].ToolCalls[0].ThoughtSignature != "" {
		t.Fatal("normalization mutated durable history")
	}
}
