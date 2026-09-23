package modelcompat

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// syntheticUserKinds are the user-role messages Chord appends itself. They are
// not user turns: hook feedback, background results, stream continuations, loop
// notices, SubAgent mailbox deliveries and context notices are appended in the
// middle of a turn, and turn overlays ride the request tail. Counting one as the
// last user message moves the reasoning-strip / validation window past the
// current turn.
func syntheticUserKinds() []string {
	return []string{
		message.KindTurnOverlay,
		message.KindHookFeedback,
		message.KindBackgroundResult,
		message.KindStreamContinue,
		message.KindLoopNotice,
		message.KindSubAgentMailbox,
		message.KindContextNotice,
	}
}

func TestLastUserMessageIndexIgnoresSyntheticUserMessages(t *testing.T) {
	for _, kind := range syntheticUserKinds() {
		t.Run(kind, func(t *testing.T) {
			msgs := []message.Message{
				{Role: message.RoleUser, Content: "the real request"},
				{Role: message.RoleAssistant, Content: "working"},
				{Role: message.RoleUser, Content: "synthetic", Kind: kind},
			}
			if got := LastUserMessageIndex(msgs); got != 0 {
				t.Fatalf("LastUserMessageIndex = %d, want 0 (a %s message is not a user turn)", got, kind)
			}
		})
	}
}

func TestLastUserMessageIndexFollowsARealUserMessage(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "first"},
		{Role: message.RoleAssistant, Content: "working"},
		{Role: message.RoleUser, Content: "hook", Kind: message.KindHookFeedback},
		{Role: message.RoleUser, Content: "second"},
	}
	if got := LastUserMessageIndex(msgs); got != 3 {
		t.Fatalf("LastUserMessageIndex = %d, want 3", got)
	}
	// A compaction checkpoint is harness text, not a user turn either.
	msgs[3].IsCompactionSummary = true
	if got := LastUserMessageIndex(msgs); got != 0 {
		t.Fatalf("LastUserMessageIndex = %d, want 0 once the last user-role message is a checkpoint", got)
	}
}

// A synthetic user-role message appended mid-turn must not move the
// current_turn replay window: the tool loop it interrupts is still the turn the
// backend validates, and its signed thinking blocks are what Anthropic requires
// back with the tool results.
func TestNormalizeForTarget_CurrentTurnKeepsToolLoopReasoningAfterSyntheticUserMessage(t *testing.T) {
	prov := &message.MessageProvenance{Source: "chord", ProviderID: "sample", ModelID: "test-model", WireFamily: WireFamilyAnthropic, NativeFamily: NativeFamilyAnthropic}
	for _, kind := range syntheticUserKinds() {
		t.Run(kind, func(t *testing.T) {
			msgs := []message.Message{
				{Role: message.RoleUser, Content: "q1"},
				{Role: message.RoleAssistant, Content: "t1", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "plan", Signature: "sig-1"}}, ToolCalls: []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`)}}, Provenance: prov},
				{Role: message.RoleTool, ToolCallID: "c1", Content: "ok"},
				{Role: message.RoleUser, Content: "synthetic", Kind: kind},
			}
			target := TargetModel{ProviderID: "sample", ModelID: "test-model", WireFamily: WireFamilyAnthropic, NativeFamily: NativeFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks, ReasoningReplay: ReasoningReplayCurrentTurn, SupportsStructuredTools: true, ToolResultEncoding: ToolResultEncodingAnthropicUserBlock}
			out, rep := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
			if len(out[1].ThinkingBlocks) != 1 || out[1].ThinkingBlocks[0].Signature != "sig-1" {
				t.Fatalf("current-turn signed thinking after a %s message = %+v, want it preserved", kind, out[1].ThinkingBlocks)
			}
			if rep.StrippedHistoricalReasoning != 0 {
				t.Fatalf("StrippedHistoricalReasoning = %d, want 0 (nothing is outside the current turn)", rep.StrippedHistoricalReasoning)
			}
			if !toolPairingIntact(out, "c1") {
				t.Fatalf("tool pairing broken after reasoning strip: %+v", out)
			}
		})
	}
}

// The window still has to strip a completed turn: a real user message after the
// tool loop is what moves the boundary.
func TestNormalizeForTarget_CurrentTurnStripsCompletedTurnReasoning(t *testing.T) {
	prov := &message.MessageProvenance{Source: "chord", ProviderID: "sample", ModelID: "test-model", WireFamily: WireFamilyAnthropic, NativeFamily: NativeFamilyAnthropic}
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "q1"},
		{Role: message.RoleAssistant, Content: "t1", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "old plan", Signature: "sig-old"}}, ToolCalls: []message.ToolCall{{ID: "c1", Name: "Read", Args: json.RawMessage(`{}`)}}, Provenance: prov},
		{Role: message.RoleTool, ToolCallID: "c1", Content: "ok"},
		{Role: message.RoleUser, Content: "q2"},
	}
	target := TargetModel{ProviderID: "sample", ModelID: "test-model", WireFamily: WireFamilyAnthropic, NativeFamily: NativeFamilyAnthropic, ReasoningContinuityMode: ReasoningContinuityAnthropicBlocks, ReasoningReplay: ReasoningReplayCurrentTurn, SupportsStructuredTools: true, ToolResultEncoding: ToolResultEncodingAnthropicUserBlock}
	out, rep := NormalizeForTarget(msgs, target, NormalizeOptions{StructuredTools: true})
	if len(out[1].ThinkingBlocks) != 0 {
		t.Fatalf("completed-turn signed thinking survived: %+v", out[1].ThinkingBlocks)
	}
	if rep.StrippedHistoricalReasoning != 1 {
		t.Fatalf("StrippedHistoricalReasoning = %d, want 1", rep.StrippedHistoricalReasoning)
	}
}
