package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_StableReuseHonorsDueFrontier(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		MinToolResultsPrune:  1,
		StaleAgeTurns:        2,
		StaleOutputBytes:     40,
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  40,
		ShellSuccessAgeTurns: 9,
		ShellSuccessBytes:    1 << 20,
		MinIncrementalTokens: 1 << 20,
	}}}
	a.newTurn("")
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: "READ_RESULT lines=1-40 total=40\n" + strings.Repeat("read line\n", 80)},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "generic", Name: "custom_tool", Args: json.RawMessage(`{"q":"x"}`)}}},
		{Role: message.RoleTool, ToolCallID: "generic", ToolStatus: "success", Content: strings.Repeat("generic output ", 30)},
	}
	setTestRequestBatch(a, msgs, 2)
	first := a.prepareMessagesForLLM(msgs)
	if !hasReductionSavings(a.GetContextReductionStats()) || first[4].Content != msgs[4].Content {
		t.Fatalf("fixture did not preserve the not-yet-due generic output: %+v", a.GetContextReductionStats())
	}
	setTestRequestBatch(a, msgs, 3)
	second := a.prepareMessagesForLLM(msgs)
	if second[4].Content == msgs[4].Content {
		t.Fatalf("due generic output stayed full after review frontier: %+v", a.GetContextReductionStats())
	}
}

func TestPrepareMessagesForLLM_SkipsStableReuseWhenProvenanceDiffers(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background(), projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  1,
		StaleAgeTurns:        99,
		StaleOutputBytes:     1,
		MinToolResultsPrune:  1,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1,
	}}}}
	a.newTurn("")
	a.providerModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 1
	largeReadOutput := strings.Repeat("large read output ", 100)
	previous := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}, Provenance: &message.MessageProvenance{Source: "chord", WireFamily: "anthropic"}},
		{Role: "tool", ToolCallID: "tc1", Content: largeReadOutput, Provenance: &message.MessageProvenance{Source: "chord", WireFamily: "anthropic"}},
	}
	_ = a.prepareMessagesForLLM(previous)

	a.beginContextReductionWrapUpGrace()
	current := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}, Provenance: &message.MessageProvenance{Source: "import:claude", WireFamily: "anthropic", Imported: true}},
		{Role: "tool", ToolCallID: "tc1", Content: largeReadOutput, Provenance: &message.MessageProvenance{Source: "import:claude", WireFamily: "anthropic", Imported: true}},
	}
	prepared := a.prepareMessagesForLLM(current)
	if got := prepared[1].Provenance; got == nil || got.Source != "import:claude" || !got.Imported {
		t.Fatalf("provenance mismatch should preserve current assistant provenance, got %#v", got)
	}
	if got := prepared[2].Provenance; got == nil || got.Source != "import:claude" || !got.Imported {
		t.Fatalf("provenance mismatch should preserve current tool provenance, got %#v", got)
	}
	if stats := a.GetContextReductionStats(); stats.ReusedStable {
		t.Fatalf("stats = %+v, provenance mismatch should not reuse stable prefix", stats)
	}
}

func TestPrepareMessagesForLLM_SkipsIncompatibleStableReuse(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background(), projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  1,
		StaleAgeTurns:        99,
		StaleOutputBytes:     1,
		MinToolResultsPrune:  1,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1,
	}}}}
	a.newTurn("")
	a.providerModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 1
	previous := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 100)},
	}
	_ = a.prepareMessagesForLLM(previous)

	a.beginContextReductionWrapUpGrace()
	current := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameTodoWrite, Args: json.RawMessage(`{"todos":[]}`)}}},
		{Role: "tool", ToolCallID: "tc2", Content: "todos updated"},
		{Role: "assistant", Content: "done"},
	}
	prepared := a.prepareMessagesForLLM(current)
	foundToolResult := false
	for _, msg := range prepared {
		if msg.Role == message.RoleTool && msg.ToolCallID == "tc2" {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatalf("incompatible stable prefix reuse dropped current tool result: %#v", prepared)
	}
	if _, dropped := message.RepairOrphanToolResults(prepared); dropped != 0 {
		t.Fatalf("prepared request still contains orphan tool results dropped=%d messages=%#v", dropped, prepared)
	}
	if stats := a.GetContextReductionStats(); stats.ReusedStable {
		t.Fatalf("stats = %+v, incompatible stable prefix should not be reused", stats)
	}
}

func TestPrepareMessagesForLLM_SkipsStableReuseWhenToolCallIDIsReusedForDifferentCall(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background(), projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  1,
		StaleAgeTurns:        99,
		StaleOutputBytes:     1,
		MinToolResultsPrune:  1,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1,
	}}}}
	a.newTurn("")
	a.providerModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 1
	previous := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 100)},
	}
	_ = a.prepareMessagesForLLM(previous)

	a.beginContextReductionWrapUpGrace()
	current := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameTodoWrite, Args: json.RawMessage(`{"todos":[]}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: "todos updated"},
		{Role: "assistant", Content: "done"},
	}
	prepared := a.prepareMessagesForLLM(current)
	if prepared[1].ToolCalls[0].Name != tools.NameTodoWrite || prepared[2].Content != "todos updated" {
		t.Fatalf("incompatible stable prefix reuse replaced current tool chain: %#v", prepared)
	}
	if _, dropped := message.RepairOrphanToolResults(prepared); dropped != 0 {
		t.Fatalf("prepared request still contains orphan tool results dropped=%d messages=%#v", dropped, prepared)
	}
	if stats := a.GetContextReductionStats(); stats.ReusedStable {
		t.Fatalf("stats = %+v, reused tool_call_id with different call should not reuse stable prefix", stats)
	}
}

func TestPrepareMessagesForLLM_ReusesStableReductionSurfaceForSmallLowPressureIncrease(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context:   config.ContextConfig{Reduction: config.ContextReductionConfig{MinIncrementalTokens: 4096}},
		Providers: map[string]config.ProviderConfig{"codex": {Preset: config.ProviderPresetCodex}},
	}
	a.providerModelRef = "codex/gpt-5.5"
	providerCfg := llm.NewProviderConfig("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.5": {Limit: config.ModelLimit{Context: 65536, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "gpt-5.5", 4096, "")
	a.llmMu.Lock()
	a.runningModelRef = "codex/gpt-5.5"
	a.llmMu.Unlock()
	a.newTurn("")

	largeOutput := strings.Repeat("first output line\n", 900)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "ack"},
		{Role: "user", Content: "u3"},
	}
	for i := range 14 {
		msgs = append(msgs,
			message.Message{Role: "assistant", Content: fmt.Sprintf("ack %d", i)},
			message.Message{Role: "user", Content: fmt.Sprintf("continue %d", i)},
		)
	}
	a.prepareMessagesForLLM(msgs)
	firstStats := a.GetContextReductionStats()
	if firstStats.TokensSaved <= 0 {
		t.Fatalf("expected first request to reduce, stats=%+v", firstStats)
	}

	second := append(append([]message.Message(nil), msgs...),
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameApplyPatch, Args: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n*** End Patch"}`)}}},
		message.Message{Role: "tool", ToolCallID: "tc2", Content: "Replaced 1 occurrence\n\nDiagnostics:\n" + strings.Repeat("[E] 1:1 small diagnostic\n", 40)},
		message.Message{Role: "assistant", Content: "ack2"},
		message.Message{Role: "user", Content: "u4"},
	)
	prepared := a.prepareMessagesForLLM(second)
	stats := a.GetContextReductionStats()
	if !stats.ReusedStable {
		t.Fatalf("expected stable reduction surface reuse, stats=%+v", stats)
	}
	if stats.ReuseReason != contextReuseReasonBelowIncrementalMin {
		t.Fatalf("expected reuse reason %q, got %+v", contextReuseReasonBelowIncrementalMin, stats)
	}
	if stats.SavedDelta <= 0 || stats.SavedDelta >= 4096 {
		t.Fatalf("expected saved delta in reuse hysteresis range, got %+v", stats)
	}
	if stats.CurrentMessages != len(prepared) || stats.CurrentBytes == 0 {
		t.Fatalf("reused stable stats current surface = (%d messages, %d bytes), want prepared surface (%d messages, >0 bytes)", stats.CurrentMessages, stats.CurrentBytes, len(prepared))
	}
	if stats.TokensSaved != firstStats.TokensSaved || stats.Messages != firstStats.Messages || stats.ByToolAndRule == nil {
		t.Fatalf("stats = %+v, want first stats marked reused", stats)
	}
	if len(prepared) != len(second) {
		t.Fatalf("prepared len = %d, want current request len %d", len(prepared), len(second))
	}
	if prepared[len(prepared)-1].Content != "u4" {
		t.Fatalf("expected current tail to be preserved, got last message %+v", prepared[len(prepared)-1])
	}
}

// TestPrepareMessagesForLLM_IncrementalFreezesAlreadyReducedPrefix verifies the
// 3-part incremental reduction model: already-reduced tool results in the prefix
// are frozen (their marker content is preserved verbatim), while not-yet-reduced
// prefix messages and the new tail are re-examined. This keeps the frozen prefix
// cache-stable across turns within an agent loop.
func TestPrepareMessagesForLLM_IncrementalFreezesAlreadyReducedPrefix(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			ShellSuccessAgeTurns: 9,
			ShellSuccessBytes:    1 << 20,
			MinIncrementalTokens: 1, // disable hysteresis reuse; force full scan each time
		}},
		Providers: map[string]config.ProviderConfig{"codex": {Preset: config.ProviderPresetCodex}},
	}
	a.providerModelRef = "codex/gpt-5.5"
	providerCfg := llm.NewProviderConfig("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.5": {Limit: config.ModelLimit{Context: 65536, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "gpt-5.5", 4096, "")
	a.llmMu.Lock()
	a.runningModelRef = "codex/gpt-5.5"
	a.llmMu.Unlock()
	a.newTurn("")

	// First request: two large read outputs that will be reduced.
	largeRead1 := strings.Repeat("line from file A\n", 100)
	largeRead2 := strings.Repeat("line from file B\n", 100)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeRead1},
		{Role: "user", Content: "u2"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/b.go"}`)}}},
		{Role: "tool", ToolCallID: "tc2", Content: largeRead2},
		{Role: "user", Content: "u3"},
	}
	firstPrepared := a.prepareMessagesForLLM(msgs)
	// Both tool results should be reduced (read-like, age >= 1, > 80 bytes).
	if firstPrepared[2].Content == largeRead1 {
		t.Fatalf("expected tc1 to be reduced, got original content")
	}
	if firstPrepared[5].Content == largeRead2 {
		t.Fatalf("expected tc2 to be reduced, got original content")
	}
	firstMarker1 := firstPrepared[2].Content
	firstMarker2 := firstPrepared[5].Content
	firstStats := a.GetContextReductionStats()

	// Second request: add a new tail with another large read.
	// The prefix (msgs) hasn't changed, so incremental reduction should freeze
	// the already-reduced tc1 and tc2 markers verbatim.
	second := append(append([]message.Message(nil), msgs...),
		message.Message{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc3", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/c.go"}`)}}},
		message.Message{Role: "tool", ToolCallID: "tc3", Content: strings.Repeat("line from file C\n", 100)},
		message.Message{Role: "user", Content: "u4"},
	)
	secondPrepared := a.prepareMessagesForLLM(second)

	// Frozen prefix markers must be identical (cache-stable).
	if secondPrepared[2].Content != firstMarker1 {
		t.Fatalf("frozen prefix tc1 marker changed:\n got %q\n want %q", secondPrepared[2].Content, firstMarker1)
	}
	if secondPrepared[5].Content != firstMarker2 {
		t.Fatalf("frozen prefix tc2 marker changed:\n got %q\n want %q", secondPrepared[5].Content, firstMarker2)
	}
	// New tail tc3 should be reduced (it's a new large read).
	if secondPrepared[8].Content == "line from file C\n"+strings.Repeat("line from file C\n", 99) {
		t.Fatalf("expected new tail tc3 to be reduced, got original content")
	}
	// Verify the frozen_reduced skip was recorded.
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipFrozenReduced] < 2 {
		t.Fatalf("expected at least 2 frozen_reduced skips, got %d (stats=%+v)", stats.SkippedByReason[contextReductionSkipFrozenReduced], stats)
	}
	wantSaved := 0
	wantReducedMessages := 0
	for i := range second {
		saved := contextContributorBytes(second[i]) - contextContributorBytes(secondPrepared[i])
		if saved <= 0 {
			continue
		}
		wantSaved += saved
		wantReducedMessages++
	}
	if stats.Bytes != wantSaved || stats.Messages != wantReducedMessages {
		t.Fatalf("current request reduction = (%d messages, %d bytes), want (%d messages, %d bytes)", stats.Messages, stats.Bytes, wantReducedMessages, wantSaved)
	}
	frozenSaved := len(largeRead1) - len(firstMarker1) + len(largeRead2) - len(firstMarker2)
	if stats.Bytes <= firstStats.Bytes || stats.Bytes <= frozenSaved {
		t.Fatalf("current request bytes = %d, want frozen prefix savings %d plus new tail savings (first request %d)", stats.Bytes, frozenSaved, firstStats.Bytes)
	}
}

// TestPrepareMessagesForLLM_IncrementalReExaminesNotYetReducedPrefix verifies that
// a tool result in the prefix that was NOT reduced in the first pass (because its
// age was below the threshold) gets re-examined and reduced in the second pass
// when its age crosses the threshold. This is the user's "part 2" of the 3-part model.
func TestPrepareMessagesForLLM_IncrementalReExaminesNotYetReducedPrefix(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     2, // need 2 user turns before reduction
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1, // force full scan each time
		}},
		Providers: map[string]config.ProviderConfig{"codex": {Preset: config.ProviderPresetCodex}},
	}
	a.providerModelRef = "codex/gpt-5.5"
	providerCfg := llm.NewProviderConfig("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.5": {Limit: config.ModelLimit{Context: 65536, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "gpt-5.5", 4096, "")
	a.llmMu.Lock()
	a.runningModelRef = "codex/gpt-5.5"
	a.llmMu.Unlock()
	a.newTurn("")

	largeRead := strings.Repeat("line from file\n", 100)
	// First request: tc1 is age 0 (same user turn), so NOT reduced.
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeRead},
	}
	firstPrepared := a.prepareMessagesForLLM(msgs)
	if firstPrepared[2].Content != largeRead {
		t.Fatalf("tc1 should NOT be reduced at age 0, got %q", firstPrepared[2].Content)
	}

	// Second request: add a new user turn. tc1 is now age 1 (still < 2), not reduced.
	second := append(append([]message.Message(nil), msgs...),
		message.Message{Role: "user", Content: "u2"},
	)
	secondPrepared := a.prepareMessagesForLLM(second)
	if secondPrepared[2].Content != largeRead {
		t.Fatalf("tc1 should NOT be reduced at age 1, got %q", secondPrepared[2].Content)
	}

	// Third request: another user turn. tc1 is now age 2, should be reduced.
	third := append(append([]message.Message(nil), second...),
		message.Message{Role: "user", Content: "u3"},
	)
	thirdPrepared := a.prepareMessagesForLLM(third)
	if thirdPrepared[2].Content == largeRead {
		t.Fatalf("tc1 should be reduced at age 2, got original content")
	}
	reducedMarker := thirdPrepared[2].Content

	// Fourth request: the now-reduced tc1 should be frozen.
	fourth := append(append([]message.Message(nil), third...),
		message.Message{Role: "user", Content: "u4"},
	)
	fourthPrepared := a.prepareMessagesForLLM(fourth)
	if fourthPrepared[2].Content != reducedMarker {
		t.Fatalf("reduced tc1 should be frozen (stable marker), got %q want %q", fourthPrepared[2].Content, reducedMarker)
	}
}

// TestPrepareMessagesForLLM_IncrementalDisabledOnToolDefChange verifies that when
// the tool-definition surface changes, the frozen prefix is invalidated and a full
// re-reduction occurs (the user's "tool update → full re-reduction" requirement).
func TestPrepareMessagesForLLM_IncrementalDisabledOnToolDefChange(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1,
		}},
		Providers: map[string]config.ProviderConfig{"codex": {Preset: config.ProviderPresetCodex}},
	}
	a.providerModelRef = "codex/gpt-5.5"
	providerCfg := llm.NewProviderConfig("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.5": {Limit: config.ModelLimit{Context: 65536, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "gpt-5.5", 4096, "")
	a.llmMu.Lock()
	a.runningModelRef = "codex/gpt-5.5"
	a.llmMu.Unlock()
	a.newTurn("")

	largeRead := strings.Repeat("line from file\n", 100)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeRead},
		{Role: "user", Content: "u2"},
	}
	firstPrepared := a.prepareMessagesForLLM(msgs)
	if firstPrepared[2].Content == largeRead {
		t.Fatalf("expected tc1 to be reduced in first pass")
	}
	firstMarker := firstPrepared[2].Content

	// Simulate a tool-definition change by installing a different frozen tool surface.
	// This changes the toolDefHash, invalidating the incremental path.
	a.frozenToolDefs.Store(&[]message.ToolDefinition{
		{Name: "new_tool", Description: "A different tool surface"},
	})

	second := append(append([]message.Message(nil), msgs...),
		message.Message{Role: "user", Content: "u3"},
	)
	secondPrepared := a.prepareMessagesForLLM(second)
	// With tool defs changed, incremental is disabled. The full scan re-reduces tc1.
	// The marker should still be a reduction marker (re-reduced from original),
	// and the frozen_reduced skip should NOT be recorded for this message.
	if secondPrepared[2].Content == largeRead {
		t.Fatalf("expected tc1 to still be reduced after tool change (full re-reduction)")
	}
	// The marker may differ because it's re-reduced from original, not frozen.
	// The key check: no frozen_reduced skips (incremental was disabled).
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipFrozenReduced] > 0 {
		t.Fatalf("expected no frozen_reduced skips when tool defs changed, got %d", stats.SkippedByReason[contextReductionSkipFrozenReduced])
	}
	_ = firstMarker // marker may or may not be identical after re-reduction
}

func TestPrepareMessagesForLLM_IncrementalDisabledOnInputSchemaChange(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1,
		}},
		Providers: map[string]config.ProviderConfig{"codex": {Preset: config.ProviderPresetCodex}},
	}
	a.providerModelRef = "codex/gpt-5.5"
	providerCfg := llm.NewProviderConfig("codex", config.ProviderConfig{
		Preset: config.ProviderPresetCodex,
		Type:   config.ProviderTypeResponses,
		Models: map[string]config.ModelConfig{"gpt-5.5": {Limit: config.ModelLimit{Context: 65536, Output: 4096}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "gpt-5.5", 4096, "")
	a.llmMu.Lock()
	a.runningModelRef = "codex/gpt-5.5"
	a.llmMu.Unlock()
	a.newTurn("")

	a.frozenToolDefs.Store(&[]message.ToolDefinition{
		{Name: tools.NameRead, Description: "Read a file", InputSchema: map[string]any{"type": "object"}},
	})

	largeRead := strings.Repeat("line from file\n", 100)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeRead},
		{Role: "user", Content: "u2"},
	}
	if firstPrepared := a.prepareMessagesForLLM(msgs); firstPrepared[2].Content == largeRead {
		t.Fatalf("expected tc1 to be reduced in first pass")
	}

	// Change only the InputSchema; name and description are unchanged. The
	// incremental frozen-prefix path must be invalidated by the hash.
	a.frozenToolDefs.Store(&[]message.ToolDefinition{
		{Name: tools.NameRead, Description: "Read a file", InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"limit": map[string]any{"type": "integer"}},
		}},
	})

	second := append(append([]message.Message(nil), msgs...),
		message.Message{Role: "user", Content: "u3"},
	)
	secondPrepared := a.prepareMessagesForLLM(second)
	if secondPrepared[2].Content == largeRead {
		t.Fatalf("expected tc1 to still be reduced after schema change (full re-reduction)")
	}
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipFrozenReduced] > 0 {
		t.Fatalf("expected no frozen_reduced skips when input schema changed, got %d", stats.SkippedByReason[contextReductionSkipFrozenReduced])
	}
}

// TestStableSurfaceReviewSkipsAlreadyReducedRepeatedOutputs pins the reuse
// path against a permanent-review loop: repeated detection runs over the
// immutable original history, so the earlier duplicate stays flagged forever.
// Once that index has been reduced to the repeated marker there is nothing
// left to trim, and the frozen surface must stay reusable.
func TestStableSurfaceReviewSkipsAlreadyReducedRepeatedOutputs(t *testing.T) {
	history := []message.Message{
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: message.RoleTool, ToolCallID: "c1", ToolStatus: message.ToolStatusSuccess, Content: "ok\tpkg\t0.1s"},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "c2", Name: "shell", Args: json.RawMessage(`{"command":"go test ./..."}`)}}},
		{Role: message.RoleTool, ToolCallID: "c2", ToolStatus: message.ToolStatusSuccess, Content: "ok\tpkg\t0.1s"},
		{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "c3", Name: "shell", Args: json.RawMessage(`{"command":"go vet ./..."}`)}}},
		{Role: message.RoleTool, ToolCallID: "c3", ToolStatus: message.ToolStatusSuccess, Content: "vet ok"},
	}
	boundary := 4
	surfaceMessages := append([]message.Message(nil), history[:boundary]...)
	surfaceMessages[1].Content = "[repeated tool output reduced]"

	surface := stableReductionSurface{
		Messages:       surfaceMessages,
		ReducedIndices: []bool{false, true, false, false},
		NextReviewAge:  make([]int, boundary),
		ToolResults:    0,
		Policy:         requesttrim.DefaultPolicy(),
	}
	scan := requesttrim.NewScan(history, nil)

	if stableReductionSurfaceNeedsReview(surface, scan, 4, nil) {
		t.Fatal("surface with the repeated index already reduced must stay reusable")
	}

	surface.ReducedIndices[1] = false
	if !stableReductionSurfaceNeedsReview(surface, scan, 4, nil) {
		t.Fatal("an unreduced repeated output must still trigger review")
	}
}

// TestStableSurfaceReviewToleratesMarkerClassDivergence pins the other
// permanent-review loop: review computes validity over ORIGINAL messages
// while the frozen marker was built from the PREPARED view. A later edit can
// overlap the original read range but not the kept head, making the original
// Invalidated while the marker legitimately stays truncated=superseded. That
// divergence must not force a full scan on every subsequent request.
func TestStableSurfaceReviewToleratesMarkerClassDivergence(t *testing.T) {
	history := []message.Message{
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "r1", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", ToolStatus: message.ToolStatusSuccess, Content: "READ_RESULT lines=10-50 total=80\nbody"},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "r2", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r2", ToolStatus: message.ToolStatusSuccess, Content: "READ_RESULT lines=1-80 total=80\nfull body"},
		{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "e1", Name: "edit", Args: json.RawMessage(`{"path":"main.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "e1", ToolStatus: message.ToolStatusSuccess, Content: "ok", FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "main.go", Exists: true, ChangedStart: 20, ChangedEnd: 30}}}},
		{Role: message.RoleAssistant, RequestBatch: 4, ToolCalls: []message.ToolCall{{ID: "s1", Name: "shell", Args: json.RawMessage(`{"command":"true"}`)}}},
		{Role: message.RoleTool, ToolCallID: "s1", ToolStatus: message.ToolStatusSuccess, Content: "ok"},
	}
	boundary := 6
	surfaceMessages := append([]message.Message(nil), history[:boundary]...)
	// The first read was reduced under the PREPARED view as superseded by r2;
	// under the ORIGINAL view the edit at 20-30 also invalidates it.
	surfaceMessages[1].Content = "READ_RESULT lines=10-12 total=80 truncated=superseded\nhead"

	surface := stableReductionSurface{
		Messages:       surfaceMessages,
		ReducedIndices: []bool{false, true, false, false, false, false},
		NextReviewAge:  make([]int, boundary),
		ToolResults:    0,
		Policy:         requesttrim.DefaultPolicy(),
	}
	scan := requesttrim.NewScan(history, nil)
	if stableReductionSurfaceNeedsReview(surface, scan, 5, nil) {
		t.Fatal("marker-class divergence on an already-reduced read must not force review")
	}

	// An already-reduced read whose marker somehow lost its truncated tag
	// still triggers review.
	surface.Messages[1].Content = "READ_RESULT lines=10-12 total=80\nhead"
	if !stableReductionSurfaceNeedsReview(surface, scan, 5, nil) {
		t.Fatal("an unmarked reduced read with drifted validity must still trigger review")
	}
}
