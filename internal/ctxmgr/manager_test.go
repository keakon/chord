package ctxmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestEstimateMessageTokensCountsOpaqueReasoningState(t *testing.T) {
	msg := message.Message{
		Role:           message.RoleAssistant,
		ThinkingBlocks: []message.ThinkingBlock{{Data: strings.Repeat("r", 300)}},
		ResponsesOutput: []message.ResponsesOutputItem{{
			Type:             "reasoning",
			ID:               strings.Repeat("i", 30),
			EncryptedContent: strings.Repeat("e", 600),
			Summary:          []message.ResponsesReasoningSummary{{Type: "summary_text", Text: strings.Repeat("s", 90)}},
		}},
		GeminiParts: []message.GeminiReplayPart{{Type: "text", ThoughtSignature: strings.Repeat("g", 120)}},
	}
	if got := EstimateMessageTokens(msg); got < 380 {
		t.Fatalf("EstimateMessageTokens = %d, want opaque reasoning state included", got)
	}
	if got := messageContextBytes([]message.Message{msg}); got < 1100 {
		t.Fatalf("messageContextBytes = %d, want opaque reasoning bytes included", got)
	}
}

func TestRepairOrphanToolMessagesInPlace(t *testing.T) {
	m := NewManager(1000, 0)
	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "ok", Name: "Read", Args: json.RawMessage(`{}`)}}})
	m.Append(message.Message{Role: "tool", ToolCallID: "ok", Content: "kept"})
	m.Append(message.Message{Role: "tool", ToolCallID: "missing", Content: "dropped"})

	if got := m.RepairOrphanToolMessagesInPlace(); got != 1 {
		t.Fatalf("RepairOrphanToolMessagesInPlace = %d, want 1", got)
	}
	snap := m.Snapshot()
	if len(snap) != 2 || snap[1].ToolCallID != "ok" {
		t.Fatalf("snapshot after repair = %+v", snap)
	}
	if got := m.RepairOrphanToolMessagesInPlace(); got != 0 {
		t.Fatalf("second repair = %d, want 0", got)
	}
}

func TestRepairOrphanToolMessagesInPlaceClearsTrackedTokensWhenRepairEmptiesHistory(t *testing.T) {
	m := NewManager(1000, 0)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 123, CacheWriteTokens: 11})
	m.Append(message.Message{Role: "tool", ToolCallID: "ghost", Content: "orphan"})

	if got := m.RepairOrphanToolMessagesInPlace(); got != 1 {
		t.Fatalf("RepairOrphanToolMessagesInPlace = %d, want 1", got)
	}
	if got := m.MessageCount(); got != 0 {
		t.Fatalf("MessageCount() = %d, want 0", got)
	}
	if got := m.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens() = %d, want 0", got)
	}
	if got := m.LastTotalContextTokens(); got != 0 {
		t.Fatalf("LastTotalContextTokens() = %d, want 0", got)
	}
}

func TestAnyAssistantDeclaresToolCallID(t *testing.T) {
	m := NewManager(1000, 0)
	m.Append(message.Message{Role: "user", Content: "hello"})
	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "Read", Args: json.RawMessage(`{}`)}}})
	if !m.AnyAssistantDeclaresToolCallID("call-1") {
		t.Fatal("expected call-1 to be declared")
	}
	if m.AnyAssistantDeclaresToolCallID("missing") {
		t.Fatal("did not expect missing call to be declared")
	}
	if m.AnyAssistantDeclaresToolCallID("") {
		t.Fatal("empty call id should not be declared")
	}
}

// Dropping messages must drop the tool-call declarations they carried; a stale
// index entry would let a synthetic tool result persist against an API that
// rejects an output no assistant message declares.
func TestDropMessagesRebuildDeclaredToolCallIDIndex(t *testing.T) {
	m := NewManager(1000, 0)
	m.Append(message.Message{Role: "user", Content: "hello"})
	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{
		{ID: "call-1", Name: "Read", Args: json.RawMessage(`{}`)},
		{ID: "call-2", Name: "Read", Args: json.RawMessage(`{}`)},
	}})
	if !m.AnyAssistantDeclaresToolCallID("call-1") || !m.AnyAssistantDeclaresToolCallID("call-2") {
		t.Fatal("both declared calls should be indexed")
	}

	// DropLastMessage removes the declaring assistant message.
	m.DropLastMessage()
	if m.AnyAssistantDeclaresToolCallID("call-1") || m.AnyAssistantDeclaresToolCallID("call-2") {
		t.Fatal("declarations survived DropLastMessage")
	}

	// DropLastMessages must also re-index when tool results trail the
	// declaring assistant message.
	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-3", Name: "Read", Args: json.RawMessage(`{}`)}}})
	m.Append(message.Message{Role: "tool", ToolCallID: "call-3", Content: "ok"})
	m.Append(message.Message{Role: "tool", ToolCallID: "call-3", Content: "more"})
	m.DropLastMessages(3)
	if m.AnyAssistantDeclaresToolCallID("call-3") {
		t.Fatal("declaring assistant message survived DropLastMessages(3)")
	}
}

func TestSafeKeepBoundaryAndManagerWrapper(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "result"},
		{Role: "user", Content: "u2"},
	}
	if got := SafeKeepBoundary(msgs, 2); got != 1 {
		t.Fatalf("SafeKeepBoundary at tool = %d, want 1", got)
	}
	if got := SafeKeepBoundary(msgs, 3); got != 3 {
		t.Fatalf("SafeKeepBoundary at user = %d, want 3", got)
	}
	if got := SafeKeepBoundary(msgs, -1); got != 0 {
		t.Fatalf("SafeKeepBoundary negative = %d, want 0", got)
	}
	if got := SafeKeepBoundary(msgs, len(msgs)+1); got != len(msgs) {
		t.Fatalf("SafeKeepBoundary beyond end = %d, want %d", got, len(msgs))
	}

	m := NewManager(1000, 0)
	m.RestoreMessages(msgs)
	if got := m.ComputeSafeKeepBoundary(2); got != 1 {
		t.Fatalf("ComputeSafeKeepBoundary = %d, want 1", got)
	}
}

func TestSafeKeepBoundaryKeepsRecentIncompleteToolCallInTail(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{
			{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)},
			{ID: "b", Name: "Read", Args: json.RawMessage(`{}`)},
		}},
		{Role: "tool", ToolCallID: "a", Content: "result-a"},
	}
	if got := SafeKeepBoundary(msgs, len(msgs)); got != 1 {
		t.Fatalf("SafeKeepBoundary with recent pending tool call = %d, want 1", got)
	}
}

func TestSafeKeepBoundaryArchivesStaleIncompleteToolCall(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "missing", Name: "Read", Args: json.RawMessage(`{}`)}}},
	}
	for i := 0; i <= incompleteToolCallProtectedTailMessages; i++ {
		msgs = append(msgs, message.Message{Role: "user", Content: fmt.Sprintf("new-%d", i)})
	}
	if got := SafeKeepBoundary(msgs, len(msgs)); got != len(msgs) {
		t.Fatalf("SafeKeepBoundary with stale pending tool call = %d, want %d", got, len(msgs))
	}
}

func TestSafeKeepBoundaryArchivesCompletedToolCalls(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "result-a"},
	}
	if got := SafeKeepBoundary(msgs, len(msgs)); got != len(msgs) {
		t.Fatalf("SafeKeepBoundary with completed tool call = %d, want %d", got, len(msgs))
	}
}

func TestSafeKeepBoundaryProtectsEarliestInWindowOnly(t *testing.T) {
	// Two pending assistants — one stale (outside the protected window) and one
	// recent (inside the window). The split must land just before the recent
	// pending assistant; the stale pending is intentionally not re-protected
	// once the boundary moves back.
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "stale", Name: "Read", Args: json.RawMessage(`{}`)}}},
	}
	// 12 filler user turns push the stale pending outside the 10-message window.
	for i := range 12 {
		msgs = append(msgs, message.Message{Role: "user", Content: fmt.Sprintf("filler-%d", i)})
	}
	recentIdx := len(msgs)
	msgs = append(msgs, message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "recent", Name: "Read", Args: json.RawMessage(`{}`)}}})
	msgs = append(msgs, message.Message{Role: "user", Content: "follow-up"})

	if got := SafeKeepBoundary(msgs, len(msgs)); got != recentIdx {
		t.Fatalf("SafeKeepBoundary = %d, want %d (split just before the recent pending assistant)", got, recentIdx)
	}
}

func TestCompressForTarget(t *testing.T) {
	m := NewManager(1000, 0)
	msgs := []message.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "older message that should be dropped"},
		{Role: "assistant", Content: "recent assistant"},
		{Role: "user", Content: "recent user"},
	}
	got := m.CompressForTarget(msgs, 15)
	if len(got) != 4 {
		t.Fatalf("compressed len = %d, want 4: %+v", len(got), got)
	}
	if got[0].Role != "system" || got[1].Role != "user" || !strings.Contains(got[1].Content, "Context was compressed") || got[3].Content != "recent user" {
		t.Fatalf("unexpected compressed messages: %+v", got)
	}
	if got := m.CompressForTarget(msgs[:2], 15); got != nil {
		t.Fatalf("CompressForTarget short history = %+v, want nil", got)
	}
	if got := m.CompressForTarget(msgs, 0); got != nil {
		t.Fatalf("CompressForTarget zero target = %+v, want nil", got)
	}
}

func TestCompressForTargetCountsMultipartPayload(t *testing.T) {
	m := NewManager(1000, 0)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "task"},
		{Role: message.RoleUser, Parts: []message.ContentPart{{Type: message.ContentPartText, Text: strings.Repeat("large ", 1000)}}},
		{Role: message.RoleAssistant, Content: "recent"},
	}
	got := m.CompressForTarget(msgs, 100)
	if len(got) != 3 {
		t.Fatalf("compressed len = %d, want first + checkpoint + recent: %+v", len(got), got)
	}
	if len(got[1].Parts) != 0 || !strings.Contains(got[1].Content, "Context was compressed") || got[2].Content != "recent" {
		t.Fatalf("unexpected multipart compression result: %+v", got)
	}
}

// Image parts are charged a per-image allowance, never their base64 length: a
// 300 KB screenshot would otherwise add ~100K phantom tokens to the estimate.
func TestEstimateMessageTokensChargesImagesPerImage(t *testing.T) {
	text := strings.Repeat("a", 3000)
	textOnly := message.Message{Role: message.RoleUser, Content: text}
	textPart := message.ContentPart{Type: message.ContentPartText, Text: text}
	for _, size := range []int{1000, 300_000} {
		msg := message.Message{Role: message.RoleUser, Parts: []message.ContentPart{
			textPart,
			{Type: message.ContentPartImage, Data: make([]byte, size)},
		}}
		if got, want := EstimateMessageTokens(msg), EstimateMessageTokens(textOnly)+imagePartEstimateTokens; got != want {
			t.Fatalf("EstimateMessageTokens(image %d bytes) = %d, want %d (text estimate plus per-image allowance)", size, got, want)
		}
	}
}

func TestShouldAutoCompact(t *testing.T) {
	m := NewManager(1000, 0.8)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 799})
	if m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to stay false below 80%")
	}

	m.UpdateFromUsage(message.TokenUsage{InputTokens: 800})
	if !m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to become true at 80%")
	}
}

func TestShouldAutoCompactUsesInputBudgetWhenConfigured(t *testing.T) {
	m := NewManagerWithInputBudget(400000, 272000, 0, 0.8)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 217599})
	if m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to stay false below 80% of input budget")
	}
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 217600})
	if !m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to become true at 80% of input budget")
	}
}

func TestSetThresholdUpdatesDecisionAndBumpsEpoch(t *testing.T) {
	m := NewManagerWithInputBudget(400000, 272000, 0, 0.8)
	epochBefore := m.TokenBudgetsEpoch()
	// Same value is a no-op for the epoch (no budget change).
	m.SetThreshold(0.8)
	if m.TokenBudgetsEpoch() != epochBefore {
		t.Fatal("same threshold must not bump the epoch")
	}
	// A real change re-derives the threshold and starts a fresh claim window.
	m.SetThreshold(0.3)
	if m.TokenBudgetsEpoch() != epochBefore+1 {
		t.Fatalf("epoch = %d, want %d after threshold change", m.TokenBudgetsEpoch(), epochBefore+1)
	}
	if m.Threshold() != 0.3 {
		t.Fatalf("threshold = %v, want 0.3", m.Threshold())
	}
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 217600})
	if !m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to become true at 30% of input budget")
	}
	m.SetThreshold(0)
	if m.ShouldAutoCompact() {
		t.Fatal("expected zero threshold to disable auto-compaction")
	}
}

func TestShouldAutoCompactUsesUsableInputBudgetWhenReserved(t *testing.T) {
	m := NewManagerWithInputBudget(400000, 272000, 20000, 0.8)
	if got := m.GetUsableInputBudget(); got != 252000 {
		t.Fatalf("GetUsableInputBudget() = %d, want 252000", got)
	}
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 201599})
	if m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to stay false below 80% of usable input budget")
	}
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 201600})
	if !m.ShouldAutoCompact() {
		t.Fatal("expected threshold check to become true at 80% of usable input budget")
	}
}

// Some OpenAI-compatible relays report only the uncached remainder in
// input_tokens even though the wire flags say the cached prefix is included; a
// warm-cache request must not report a near-zero prompt to the threshold.
func TestShouldAutoCompactNormalizesUncachedOnlyRelayUsage(t *testing.T) {
	m := NewManagerWithInputBudget(400000, 272000, 0, 0.8)
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:            2,
		CacheReadTokens:        230000,
		InputSemanticsKnown:    true,
		InputIncludesCacheRead: true,
	})
	if got := m.LastInputTokens(); got != 230002 {
		t.Fatalf("LastInputTokens() = %d, want normalized full prompt 230002", got)
	}
	if !m.ShouldAutoCompact() {
		t.Fatal("expected normalized full prompt to cross the threshold")
	}
}

// Anthropic-style wires report cache-read and cache-write prefixes outside
// input_tokens (semantics flags false): the full prompt is their sum.
func TestShouldAutoCompactAddsCachePrefixesForAnthropicStyleUsage(t *testing.T) {
	m := NewManagerWithInputBudget(400000, 272000, 0, 0.8)
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:      100000,
		CacheReadTokens:  100000,
		CacheWriteTokens: 30000,
	})
	if got := m.LastInputTokens(); got != 230000 {
		t.Fatalf("LastInputTokens() = %d, want 230000", got)
	}
	if !m.ShouldAutoCompact() {
		t.Fatal("expected summed cache prefixes to cross the threshold")
	}
}

// The next request replays the last prompt plus the generated output, so the
// threshold compares the post-response context baseline, not the prompt alone.
func TestShouldAutoCompactIncludesGeneratedOutput(t *testing.T) {
	m := NewManagerWithInputBudget(400000, 272000, 0, 0.8)
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:            210000,
		OutputTokens:           10000,
		InputSemanticsKnown:    true,
		InputIncludesCacheRead: true,
	})
	if !m.ShouldAutoCompact() {
		t.Fatal("expected prompt+output context baseline to cross the threshold")
	}
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:            210000,
		InputSemanticsKnown:    true,
		InputIncludesCacheRead: true,
	})
	if m.ShouldAutoCompact() {
		t.Fatal("expected prompt alone below threshold to stay false")
	}
}

// A lower local byte-calibrated estimate must never cancel a compaction the
// provider-reported usage already triggered: provider usage stays authoritative
// and the estimate can only raise the effective input, never lower it.
func TestShouldAutoCompactUsageAuthorityNotCanceledByLowerEstimate(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: "user", Content: strings.Repeat("a", 300)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 800})

	decision := m.AutoCompactDecision()
	if decision.EstimatedInputTokens >= decision.LastInputTokens {
		t.Fatalf("EstimatedInputTokens = %d, want below LastInputTokens = %d for this scenario",
			decision.EstimatedInputTokens, decision.LastInputTokens)
	}
	if got := decision.EffectiveInputTokens; got != 800 {
		t.Fatalf("EffectiveInputTokens = %d, want 800 from authoritative usage", got)
	}
	if !decision.ShouldCompact {
		t.Fatal("expected usage-triggered compaction to survive a lower local estimate")
	}
}

func TestShouldAutoCompactUsesPayloadByteCalibrationWhenUsageMissing(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: "user", Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})

	m.Append(message.Message{Role: "tool", Content: strings.Repeat("b", 150)})
	m.UpdateFromUsage(message.TokenUsage{})

	decision := m.AutoCompactDecision()
	if got := decision.LastInputTokens; got != 0 {
		t.Fatalf("LastInputTokens = %d, want 0 for latest zero-usage response", got)
	}
	if got := decision.EstimatedInputTokens; got != 1000 {
		t.Fatalf("EstimatedInputTokens = %d, want 1000", got)
	}
	if got := decision.EffectiveInputTokens; got != 1000 {
		t.Fatalf("EffectiveInputTokens = %d, want 1000", got)
	}
	if !decision.ShouldCompact {
		t.Fatal("expected byte-calibrated estimate to trigger automatic compaction")
	}
}

func TestEffectiveContextTokensMatchesAutoCompactDecision(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: "user", Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})

	// Before post-response growth the effective reading is the provider
	// baseline, identical to the decision's effective input.
	if got, want := m.EffectiveContextTokens(), m.AutoCompactDecision().EffectiveInputTokens; got != want {
		t.Fatalf("EffectiveContextTokens() = %d, want decision EffectiveInputTokens %d", got, want)
	}
	// Growth past the last provider sample raises the shared reading through
	// the calibrated estimate, so a context gauge fed by this getter cannot
	// lag the auto-compaction trigger.
	m.Append(message.Message{Role: "tool", Content: strings.Repeat("b", 150)})
	m.UpdateFromUsage(message.TokenUsage{})

	if got, want := m.EffectiveContextTokens(), m.AutoCompactDecision().EffectiveInputTokens; got != want {
		t.Fatalf("EffectiveContextTokens() after growth = %d, want decision value %d", got, want)
	}
	if got := m.EffectiveContextTokens(); got != 1000 {
		t.Fatalf("EffectiveContextTokens() = %d, want the calibrated estimate 1000", got)
	}
}

func TestShouldAutoCompactUsesContextByteCalibrationForToolCalls(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: "user", Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})

	m.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{Args: json.RawMessage(strings.Repeat("b", 150))}}})
	m.UpdateFromUsage(message.TokenUsage{})

	decision := m.AutoCompactDecision()
	if got := decision.EstimatedInputTokens; got != 1000 {
		t.Fatalf("EstimatedInputTokens = %d, want 1000 from tool-call context bytes", got)
	}
	if !decision.ShouldCompact {
		t.Fatal("expected tool-call context bytes to trigger automatic compaction")
	}
}

// Image payloads are not token-proportional, so a large screenshot appended
// after the provider sample must not be extrapolated into the byte-calibrated
// estimate: byte scaling multiplied the whole context by the image's ~300 KB
// and showed (and triggered compaction at) a phantom 100K+ tokens.
func TestPayloadByteCalibrationIgnoresImagePayloadBytes(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})

	m.Append(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type: message.ContentPartImage,
		Data: make([]byte, 300_000),
	}}})

	decision := m.AutoCompactDecision()
	if got := decision.EstimatedInputTokens; got != 0 {
		t.Fatalf("EstimatedInputTokens = %d, want 0: image bytes must not grow the byte-calibrated estimate", got)
	}
	if decision.ShouldCompact {
		t.Fatal("image payload must not trigger automatic compaction")
	}
	if got := m.EffectiveContextTokens(); got != 400 {
		t.Fatalf("EffectiveContextTokens() = %d, want the last provider sample 400", got)
	}
}

// Text growth after the sample is scaled by the byte ratio, and the images in
// the current context are charged the per-image allowance on top instead of
// being folded into that growth factor.
func TestPayloadByteCalibrationAddsPerImageAllowance(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})

	m.Append(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type: message.ContentPartImage,
		Data: make([]byte, 300_000),
	}}})
	m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("b", 50)})
	m.UpdateFromUsage(message.TokenUsage{})

	// Text bytes 100 -> 150 scale the 400-token sample to 600; the image adds
	// one per-image allowance on top.
	if got := m.AutoCompactDecision().EstimatedInputTokens; got != 600+imagePartEstimateTokens {
		t.Fatalf("EstimatedInputTokens = %d, want %d (scaled text share plus per-image allowance)", got, 600+imagePartEstimateTokens)
	}
}

// A sample taken while images were in context carries their provider-priced
// share; it is removed before scaling so the text growth factor is not inflated
// by image tokens.
func TestPayloadByteCalibrationDiscountsSampleImageShare(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Content: strings.Repeat("a", 300)}})
	m.Append(message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type: message.ContentPartImage,
		Data: make([]byte, 100),
	}}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 2000})

	m.Append(message.Message{Role: message.RoleUser, Content: strings.Repeat("b", 100)})
	m.UpdateFromUsage(message.TokenUsage{})

	// Text bytes 300 -> 400 scale the sample's text share (2000 - 1600) to 533;
	// the image adds its allowance back.
	if got := m.AutoCompactDecision().EstimatedInputTokens; got != 533+imagePartEstimateTokens {
		t.Fatalf("EstimatedInputTokens = %d, want %d (sample image share discounted, current allowance added)", got, 533+imagePartEstimateTokens)
	}
}

func TestShouldAutoCompactPayloadByteCalibrationHonorsDisabledThreshold(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0)
	m.RestoreMessages([]message.Message{{Role: "user", Content: strings.Repeat("a", 100)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 400})
	m.Append(message.Message{Role: "tool", Content: strings.Repeat("b", 150)})
	m.UpdateFromUsage(message.TokenUsage{})

	decision := m.AutoCompactDecision()
	if got := decision.EstimatedInputTokens; got != 1000 {
		t.Fatalf("EstimatedInputTokens = %d, want 1000", got)
	}
	if decision.ShouldCompact {
		t.Fatal("expected byte-calibrated estimate not to trigger when automatic compaction is disabled")
	}
}

func TestReplacePrefixAtomicClearsPayloadByteCalibration(t *testing.T) {
	m := NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: "user", Content: strings.Repeat("a", 250)}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 800})

	err := m.ReplacePrefixAtomic(1, []message.Message{{Role: "assistant", Content: "summary"}}, nil)
	if err != nil {
		t.Fatalf("ReplacePrefixAtomic: %v", err)
	}
	m.UpdateFromUsage(message.TokenUsage{})

	decision := m.AutoCompactDecision()
	if got := decision.EstimatedInputTokens; got != 0 {
		t.Fatalf("EstimatedInputTokens = %d, want 0 after durable history replacement", got)
	}
	if decision.ShouldCompact {
		t.Fatal("expected durable replacement to clear stale byte-calibrated auto-compaction signal")
	}
}

func TestUpdateFromUsageTracksTrueContextBurden(t *testing.T) {
	m := NewManager(1000, 0)
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:      100,
		OutputTokens:     40,
		CacheReadTokens:  60,
		CacheWriteTokens: 20,
		ReasoningTokens:  10,
	})
	// Without semantics flags the wire is treated as Anthropic-style: cached
	// prefixes live outside input_tokens, so the full prompt is their sum.
	if got := m.LastInputTokens(); got != 180 {
		t.Fatalf("LastInputTokens() = %d, want 180 (input + cache_read + cache_write)", got)
	}
	if got := m.LastTotalContextTokens(); got != 220 {
		t.Fatalf("LastTotalContextTokens() = %d, want 220 (full prompt + output)", got)
	}
}

func TestClearLastTokenUsagePreservesCumulativeStats(t *testing.T) {
	m := NewManager(1000, 0)
	m.UpdateFromUsage(message.TokenUsage{
		InputTokens:      100,
		OutputTokens:     40,
		CacheWriteTokens: 20,
	})

	m.ClearLastTokenUsage()

	if got := m.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens() = %d, want 0", got)
	}
	if got := m.LastTotalContextTokens(); got != 0 {
		t.Fatalf("LastTotalContextTokens() = %d, want 0", got)
	}
	stats := m.GetStats()
	if stats.InputTokens != 100 || stats.OutputTokens != 40 || stats.CacheWriteTokens != 20 {
		t.Fatalf("GetStats() = %+v, want cumulative usage preserved", stats)
	}
}

func TestEstimateMessagesTokensCountsToolCallsAndThinking(t *testing.T) {
	msgs := []message.Message{
		{
			Role:    "assistant",
			Content: "abcd",
			ToolCalls: []message.ToolCall{
				{ID: "1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)},
			},
			ThinkingBlocks: []message.ThinkingBlock{
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

func TestMessagePayloadBytesCountsOnlyContentAndImageData(t *testing.T) {
	msgs := []message.Message{
		{
			Role:    "assistant",
			Content: "abcd",
			ToolCalls: []message.ToolCall{
				{ID: "1", Name: "Read", Args: json.RawMessage(`{"path":"README.md"}`)},
			},
			ThinkingBlocks: []message.ThinkingBlock{
				{Thinking: "reasoning"},
			},
		},
		{
			Role: "user",
			Parts: []message.ContentPart{
				{Type: "text", Text: "hi"},
				{Type: "image", Data: []byte("raw-image")},
			},
			Content: "ignored when parts are present",
		},
	}

	if got := MessagePayloadBytes(msgs); got != len("abcd")+len("hi")+len("raw-image") {
		t.Fatalf("MessagePayloadBytes() = %d, want content plus image data only", got)
	}
	if got, want := MessagePayloadBytes([]message.Message{{Role: "user", Content: "中文"}}), len("中文"); got != want {
		t.Fatalf("MessagePayloadBytes() for UTF-8 content = %d, want %d", got, want)
	}
}

func TestManagerPayloadBytesTracksIncrementalMessageChanges(t *testing.T) {
	m := NewManager(1000, 0)
	m.SetSystemPrompt(message.Message{Role: "system", Content: "sys"})
	m.Append(message.Message{Role: "user", Content: "abcd"})
	m.Append(message.Message{Role: "assistant", Parts: []message.ContentPart{{Type: "text", Text: "hi"}, {Type: "image", Data: []byte("raw")}}})

	if got, want := m.PayloadBytes(), len("abcd")+len("hi")+len("raw"); got != want {
		t.Fatalf("PayloadBytes() = %d, want %d", got, want)
	}
	if got, want := m.ContextPayloadBytes(), len("sys")+len("abcd")+len("hi")+len("raw"); got != want {
		t.Fatalf("ContextPayloadBytes() = %d, want %d", got, want)
	}

	m.DropLastMessage()
	if got, want := m.PayloadBytes(), len("abcd"); got != want {
		t.Fatalf("PayloadBytes() after DropLastMessage = %d, want %d", got, want)
	}

	m.SetSystemPrompt(message.Message{Role: "system", Content: "longer system"})
	if got, want := m.ContextPayloadBytes(), len("longer system")+len("abcd"); got != want {
		t.Fatalf("ContextPayloadBytes() after SetSystemPrompt = %d, want %d", got, want)
	}
}

func TestManagerPayloadBytesRecomputesAfterRestoreAndRepair(t *testing.T) {
	m := NewManager(1000, 0)
	m.RestoreMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "a", Name: "read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "ok"},
		{Role: "tool", ToolCallID: "ghost", Content: "orphan"},
	})
	if got, want := m.PayloadBytes(), len("ok"); got != want {
		t.Fatalf("PayloadBytes() after RestoreMessages repair = %d, want %d", got, want)
	}

	m.Append(message.Message{Role: "user", Content: "tail"})
	m.DropLastMessages(2)
	if got, want := m.PayloadBytes(), 0; got != want {
		t.Fatalf("PayloadBytes() after DropLastMessages = %d, want %d", got, want)
	}
}

// The image-side counters the estimator reads are maintained incrementally, so
// append, drop, wholesale replace and restore must keep them consistent with
// the payload/context byte counters that keep counting the same payloads for
// display and byte budgets.
func TestManagerImageAccountingTracksMessageChanges(t *testing.T) {
	image := message.Message{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type: message.ContentPartImage,
		Data: make([]byte, 300_000),
	}}}
	m := NewManager(1000, 0)

	m.Append(image)
	if m.imagePayloadBytes != 300_000 || m.imageEstimateTokens != imagePartEstimateTokens {
		t.Fatalf("after Append: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}
	if got := m.ContextPayloadBytes(); got != 300_000 {
		t.Fatalf("ContextPayloadBytes() = %d, want the image payload still counted for byte budgets", got)
	}
	m.DropLastMessage()
	if m.imagePayloadBytes != 0 || m.imageEstimateTokens != 0 {
		t.Fatalf("after DropLastMessage: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}

	m.Append(image)
	m.Append(image)
	m.DropLastMessages(2)
	if m.imagePayloadBytes != 0 || m.imageEstimateTokens != 0 {
		t.Fatalf("after DropLastMessages: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}

	m.Append(image)
	if err := m.ReplacePrefixAtomic(m.MessageCount(), []message.Message{{Role: message.RoleAssistant, Content: "summary"}}, nil); err != nil {
		t.Fatalf("ReplacePrefixAtomic: %v", err)
	}
	if m.imagePayloadBytes != 0 || m.imageEstimateTokens != 0 {
		t.Fatalf("after ReplacePrefixAtomic: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}
}

func TestRestoreMessagesDropsOrphanToolResults(t *testing.T) {
	m := NewManager(1000, 0)
	in := []message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "a", Content: "ok"},
		{Role: "tool", ToolCallID: "ghost", Content: "orphan"},
	}
	m.RestoreMessages(in)
	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("len(snap)=%d want 2", len(snap))
	}
	if snap[1].ToolCallID != "a" {
		t.Fatalf("last message tool id = %q", snap[1].ToolCallID)
	}
}

func TestRestoreMessagesRepairsAdjacentOutOfOrderToolResult(t *testing.T) {
	m := NewManager(1000, 0)
	m.RestoreMessages([]message.Message{
		{Role: "user", Content: "task"},
		{Role: "tool", ToolCallID: "esc-1", Content: "Escalation sent"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "esc-1", Name: "escalate", Args: json.RawMessage(`{"reason":"blocked"}`)}}},
	})

	snap := m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("len(snap) = %d, want 3", len(snap))
	}
	if snap[1].Role != message.RoleAssistant || snap[2].Role != message.RoleTool || snap[2].ToolCallID != "esc-1" {
		t.Fatalf("repaired messages = %#v, want user, assistant call, tool result", snap)
	}
}

func TestRestoreMessagesDoesNotReorderUnmatchedFutureToolResult(t *testing.T) {
	m := NewManager(1000, 0)
	m.RestoreMessages([]message.Message{
		{Role: "tool", ToolCallID: "ghost", Content: "orphan"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "other", Name: "read", Args: json.RawMessage(`{}`)}}},
	})

	snap := m.Snapshot()
	if len(snap) != 1 || snap[0].Role != message.RoleAssistant {
		t.Fatalf("repaired messages = %#v, want unmatched orphan dropped", snap)
	}
}

func TestRestoreMessagesClearsTrackedTokensWhenRepairEmptiesHistory(t *testing.T) {
	m := NewManager(1000, 0)
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 123, CacheWriteTokens: 11})

	m.RestoreMessages([]message.Message{{Role: "tool", ToolCallID: "ghost", Content: "orphan"}})

	if got := m.MessageCount(); got != 0 {
		t.Fatalf("MessageCount() = %d, want 0", got)
	}
	if got := m.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens() = %d, want 0", got)
	}
	if got := m.LastTotalContextTokens(); got != 0 {
		t.Fatalf("LastTotalContextTokens() = %d, want 0", got)
	}
}

func TestReplacePrefixAtomic(t *testing.T) {
	t.Run("basic replacement", func(t *testing.T) {
		m := NewManager(1000, 0)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})
		m.Append(message.Message{Role: "user", Content: "u2"})
		m.Append(message.Message{Role: "assistant", Content: "a2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			if len(tail) != 2 {
				t.Fatalf("expected tail len 2, got %d", len(tail))
			}
			result := make([]message.Message, 0, len(prefix)+len(tail))
			result = append(result, prefix...)
			result = append(result, tail...)
			return result, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(snap))
		}
		if snap[0].Content != "summary" {
			t.Fatalf("first message = %q, want summary", snap[0].Content)
		}
		if snap[1].Content != "u2" {
			t.Fatalf("second message = %q, want u2", snap[1].Content)
		}
	})

	t.Run("empty tail", func(t *testing.T) {
		m := NewManager(1000, 0)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			if len(tail) != 0 {
				t.Fatalf("expected empty tail, got %d", len(tail))
			}
			return prefix, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("expected 1 message, got %d", len(snap))
		}
	})

	t.Run("repairs orphan tool result in tail", func(t *testing.T) {
		m := NewManager(1000, 0)
		// head: assistant with tool_call "a"
		m.Append(message.Message{
			Role:      "assistant",
			Content:   "a1",
			ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}},
		})
		m.Append(message.Message{Role: "tool", ToolCallID: "a", Content: "result-a"})
		// tail: tool result "b" without matching tool_call (orphan)
		m.Append(message.Message{Role: "tool", ToolCallID: "b", Content: "orphan-result"})
		m.Append(message.Message{Role: "user", Content: "u2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			// orphan tool result should be removed
			if len(tail) != 1 {
				t.Fatalf("expected tail len 1 (orphan removed), got %d", len(tail))
			}
			if tail[0].Content != "u2" {
				t.Fatalf("tail[0].Content = %q, want u2", tail[0].Content)
			}
			result := make([]message.Message, 0, len(prefix)+len(tail))
			result = append(result, prefix...)
			result = append(result, tail...)
			return result, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(snap))
		}
	})

	t.Run("keeps valid tool result in tail", func(t *testing.T) {
		m := NewManager(1000, 0)
		// head: assistant with tool_call "a"
		m.Append(message.Message{
			Role:      "assistant",
			Content:   "a1",
			ToolCalls: []message.ToolCall{{ID: "a", Name: "Read", Args: json.RawMessage(`{}`)}},
		})
		m.Append(message.Message{Role: "tool", ToolCallID: "a", Content: "result-a"})
		// tail: assistant with tool_call "b" and its result
		m.Append(message.Message{
			Role:      "assistant",
			Content:   "a2",
			ToolCalls: []message.ToolCall{{ID: "b", Name: "Read", Args: json.RawMessage(`{}`)}},
		})
		m.Append(message.Message{Role: "tool", ToolCallID: "b", Content: "result-b"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			// both messages in tail should be kept (tool result "b" has matching tool_call in tail)
			if len(tail) != 2 {
				t.Fatalf("expected tail len 2, got %d", len(tail))
			}
			result := make([]message.Message, 0, len(prefix)+len(tail))
			result = append(result, prefix...)
			result = append(result, tail...)
			return result, nil
		})
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(snap))
		}
	})

	t.Run("callback error aborts", func(t *testing.T) {
		m := NewManager(1000, 0)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})
		m.Append(message.Message{Role: "user", Content: "u2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, func(tail []message.Message) ([]message.Message, error) {
			return nil, fmt.Errorf("callback error")
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		// Original messages should be unchanged
		snap := m.Snapshot()
		if len(snap) != 3 {
			t.Fatalf("expected 3 original messages, got %d", len(snap))
		}
		if snap[0].Content != "u1" {
			t.Fatalf("first message = %q, want u1", snap[0].Content)
		}
	})

	t.Run("nil callback applies prefix and tail directly", func(t *testing.T) {
		m := NewManager(1000, 0)
		m.Append(message.Message{Role: "user", Content: "u1"})
		m.Append(message.Message{Role: "assistant", Content: "a1"})
		m.Append(message.Message{Role: "user", Content: "u2"})

		prefix := []message.Message{
			{Role: "user", Content: "summary"},
		}
		err := m.ReplacePrefixAtomic(2, prefix, nil)
		if err != nil {
			t.Fatalf("ReplacePrefixAtomic failed: %v", err)
		}

		snap := m.Snapshot()
		if len(snap) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(snap))
		}
		if snap[0].Content != "summary" {
			t.Fatalf("first message = %q, want summary", snap[0].Content)
		}
		if snap[1].Content != "u2" {
			t.Fatalf("second message = %q, want u2", snap[1].Content)
		}
	})
}

// Restore and orphan repair rebuild the image counters from the message list,
// so they must agree with the byte counters for parts that only exist on disk:
// a part restored from the session file keeps Data empty (the blob resolves
// lazily) and reports its payload through DataBytes.
func TestManagerImageAccountingTracksRestoreAndRepair(t *testing.T) {
	m := NewManager(1000, 0)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{{
		Type:      message.ContentPartImage,
		ImagePath: "shot.png",
		DataBytes: 300_000,
	}}}})
	if m.imagePayloadBytes != 300_000 || m.imageEstimateTokens != imagePartEstimateTokens {
		t.Fatalf("after RestoreMessages: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}
	if got := m.ContextPayloadBytes(); got != 300_000 {
		t.Fatalf("ContextPayloadBytes() = %d, want the lazily resolved image payload counted", got)
	}

	m.Append(message.Message{Role: message.RoleTool, ToolCallID: "ghost", Parts: []message.ContentPart{{
		Type: message.ContentPartImage,
		Data: make([]byte, 4000),
	}}})
	if m.imagePayloadBytes != 304_000 || m.imageEstimateTokens != 2*imagePartEstimateTokens {
		t.Fatalf("after Append: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}

	if got := m.RepairOrphanToolMessagesInPlace(); got != 1 {
		t.Fatalf("RepairOrphanToolMessagesInPlace = %d, want 1", got)
	}
	if m.imagePayloadBytes != 300_000 || m.imageEstimateTokens != imagePartEstimateTokens {
		t.Fatalf("after repair: payload=%d tokens=%d", m.imagePayloadBytes, m.imageEstimateTokens)
	}
	if got := m.MessageCount(); got != 1 {
		t.Fatalf("MessageCount() = %d, want the orphan image message dropped", got)
	}
}

// The byte calibration discounts the image allowance of the current context
// from the provider sample, assuming the request surface carried those images.
// A target that rejects image input reports a prompt without them, so the
// discount still applies and the ratio under-scales as text grows. This test
// pins that known approximation.
func TestPayloadByteCalibrationUsesContextImageAllowance(t *testing.T) {
	m := NewManagerWithInputBudget(100000, 100000, 0, 0.8)
	m.RestoreMessages([]message.Message{{Role: message.RoleUser, Parts: []message.ContentPart{
		{Type: message.ContentPartText, Text: strings.Repeat("a", 30000)},
		{Type: message.ContentPartImage, Data: make([]byte, 4000)},
	}}})
	m.UpdateFromUsage(message.TokenUsage{InputTokens: 10000})
	// (10000 - 1600) text tokens over 30000 text bytes.
	if got, want := m.CalibratedRatio(), 8400.0/30000.0; got != want {
		t.Fatalf("CalibratedRatio() = %v, want %v", got, want)
	}
}
