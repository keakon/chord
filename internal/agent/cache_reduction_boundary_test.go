package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func setTestRequestBatch(a *MainAgent, messages []message.Message, batch uint64) {
	a.requestBatches.mu.Lock()
	a.requestBatches.sessionEpoch = a.sessionEpoch
	a.requestBatches.sequence = batch
	a.requestBatches.mu.Unlock()
	for i := range messages {
		if messages[i].Role == message.RoleAssistant && len(messages[i].ToolCalls) > 0 && messages[i].RequestBatch == 0 {
			messages[i].RequestBatch = batch
			return
		}
	}
}

// TestPrepareMessagesDefersBoundaryReductions verifies that a small reduction
// inside the previously sent prefix is deferred (cache-stability wins) while
// new tail content is still reduced immediately.
func TestPrepareMessagesDefersBoundaryReductions(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")
	setTestRequestBatch(a, nil, 1)

	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: readContent},
		{Role: "assistant", Content: bigTail},
	}

	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatalf("request 1 should not reduce a fresh webfetch result")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: "assistant", Content: "done"},
		message.Message{Role: "user", Content: "u2"},
	)
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatalf("boundary reduction should be deferred for cache stability, got: %.80s", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipDeferredCache] == 0 {
		t.Fatalf("expected deferred_for_cache skip, stats=%+v", stats)
	}

	a.resetLLMModelRun()
	a.recordLLMModelRun("q/m")
	prepared = a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("boundary reduction should flush when the cache is cold")
	}
}

func TestStableReductionSurfaceSurvivesUserTurnBoundary(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	a.clearReductionCache(false)
	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("turn boundary discarded the stable reduction surface")
	}
	if a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache] == 0 {
		t.Fatal("expected boundary reduction to stay deferred across the turn boundary")
	}
}

func TestDeferredBoundaryReductionDoesNotReportOverCompression(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readArgs := json.RawMessage(`{"url":"https://example.com/a"}`)
	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: readArgs}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	a.prepareMessagesForLLM(msgs)

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
		message.Message{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc2", Name: tools.NameWebFetch, Args: readArgs}}},
		message.Message{Role: message.RoleTool, ToolCallID: "tc2", Content: "fresh reread"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("boundary reduction should remain deferred")
	}
	if got := a.GetContextReductionStats().OverCompression[contextReductionOverCompressionReread]; got != 0 {
		t.Fatalf("deferred reduction reported reread over-compression = %d", got)
	}
}

// TestBoundaryReductionIgnoresContextUsage verifies that input usage cannot
// change request-level reduction. A warm cached prefix remains deferred even
// when the current context manager reports an oversized prompt; compaction owns
// pressure-driven size control.
func TestBoundaryReductionIgnoresContextUsage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := strings.Repeat("line content for fetched page\n", 120)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatal("context usage changed the reduction surface")
	}
	if got := a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache]; got == 0 {
		t.Fatal("expected warm boundary reduction to remain deferred")
	}
}

// TestBoundaryReductionFlushesWhenSavingsAmortize pins the amortization flush
// condition: with a warm cache and low pressure, a boundary reduction whose
// pending savings dominate the re-billed tail is applied instead of deferred.
func TestBoundaryReductionFlushesWhenSavingsAmortize(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	// A huge reducible read result followed by a tiny tail: the savings
	// amortize the re-billing well inside the flush horizon
	// (savings*30 >= 9*tail), unlike the deferral tests where an
	// irreducible tail dominates.
	readContent := strings.Repeat("line content for fetched page\n", 3000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: "short reply"},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatal("fresh webfetch result was unexpectedly reduced")
	}

	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("amortized savings should flush the boundary reduction")
	}
	if got := a.GetContextReductionStats().SkippedByReason[contextReductionSkipDeferredCache]; got != 0 {
		t.Fatalf("amortized flush still recorded deferred_for_cache = %d", got)
	}
}

// TestPrepareMessagesDefersExternalReadInvalidation pins the read half of the
// boundary rule: an invalidated read that is still only in the sent prefix is
// a boundary proposal like any other, so cache stability defers it while the
// amortization gate is unsatisfied and a cold cache flushes it. Lazy stat
// validation detects the out-of-band edit; no tool call names it.
func TestPrepareMessagesDefersExternalReadInvalidation(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  40,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)
	bigTail := strings.Repeat("user context that cannot be reduced ", 20000)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "read", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "read", ToolStatus: "success", Content: readContent, FileState: buildReadFileState(path)},
		{Role: message.RoleAssistant, Content: bigTail},
	}
	setTestRequestBatch(a, msgs, 1)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	if prepared := a.prepareMessagesForLLM(msgs); prepared[2].Content != readContent {
		t.Fatalf("a valid read must stay full on request 1: %.80s", prepared[2].Content)
	}

	// Out-of-band write, no new tool call: lazy validation flags the read.
	if err := os.WriteFile(path, []byte("package main\n\nconst value = 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile changed: %v", err)
	}
	setTestRequestBatch(a, nil, 2)
	msgs = append(msgs,
		message.Message{Role: message.RoleAssistant, Content: "done"},
		message.Message{Role: message.RoleUser, Content: "u2"},
	)
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != readContent {
		t.Fatalf("external read invalidation should defer for cache stability, got: %.80s", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.SkippedByReason[contextReductionSkipDeferredCache] == 0 {
		t.Fatalf("expected deferred_for_cache skip, stats=%+v", stats)
	}

	a.resetLLMModelRun()
	a.recordLLMModelRun("q/m")
	prepared = a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "truncated="+tools.ReadTruncatedStale) {
		t.Fatalf("cold cache should render the stale marker, got: %.80s", prepared[2].Content)
	}
}

// TestPrepareMessagesKeepsFrozenReadMarkerWhenSuperseded pins the other read
// branch: a frozen marker that already carries the stale/superseded guidance
// must not be re-rendered when the read flips to superseded, because that
// rewrites cached bytes without changing what the model can act on.
func TestPrepareMessagesKeepsFrozenReadMarkerWhenSuperseded(t *testing.T) {
	projectRoot := t.TempDir()
	path := filepath.Join(projectRoot, "a.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  40,
			MinIncrementalTokens: 1,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")
	a.recordLLMModelRun("p/m")

	readContent := "READ_RESULT lines=1-100 total=100\n" + strings.Repeat("source line\n", 100)
	firstMessages := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "r1", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r1", ToolStatus: "success", Content: readContent, FileState: buildReadFileState(path)},
		{Role: message.RoleAssistant, RequestBatch: 2, ToolCalls: []message.ToolCall{{ID: "r2", Name: tools.NameRead, Args: json.RawMessage(`{"path":"a.go"}`)}}},
		{Role: message.RoleTool, ToolCallID: "r2", ToolStatus: "success", Content: readContent, FileState: buildReadFileState(path)},
	}
	setTestRequestBatch(a, firstMessages, 2)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}
	first := a.prepareMessagesForLLM(firstMessages)
	if !strings.Contains(first[2].Content, "truncated="+tools.ReadTruncatedSuperseded) {
		t.Fatalf("first request did not render the superseded marker: %q", first[2].Content)
	}

	// A later edit invalidates the same read as well. The frozen marker already
	// carries the superseded guidance, so it must not be rewritten to the stale
	// shape: the model cannot act differently on the two, and the rewrite would
	// re-bill the cached prefix.
	messages := append(append([]message.Message(nil), firstMessages...),
		message.Message{Role: message.RoleAssistant, RequestBatch: 3, ToolCalls: []message.ToolCall{{ID: "e1", Name: tools.NameEdit, Args: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)}}},
		message.Message{Role: message.RoleTool, ToolCallID: "e1", ToolStatus: "success", Content: "edited"},
	)
	setTestRequestBatch(a, messages, 3)
	second := a.prepareMessagesForLLM(messages)
	if second[2].Content != first[2].Content {
		t.Fatalf("frozen read marker rewritten after its validity widened:\nfirst:  %q\nsecond: %q", first[2].Content, second[2].Content)
	}
}

// TestContextNoticeRowKeepsStableSurfaceReusable pins the cache side of the
// context-pressure notice policy: the row that records a pressure cycle is
// appended at the tail of the durable history, so its arrival must leave the
// recorded stable surface prefix-compatible. A row inserted before the frozen
// prefix (or a durable rewrite) would fail compatibility and make every later
// request in the same window pay a full reduction scan.
func TestContextNoticeRowKeepsStableSurfaceReusable(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.projectConfig = &config.Config{
		Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
			ReadLikeAgeTurns:     1,
			ReadLikeOutputBytes:  80,
			MinIncrementalTokens: 1 << 20,
		}},
	}
	a.runningModelRef = "p/m"
	a.recordLLMModelRun("p/m")

	readContent := strings.Repeat("line content for fetched page\n", 120)
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "u1"},
		{Role: message.RoleAssistant, RequestBatch: 1, ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a"}`)}}},
		{Role: message.RoleTool, ToolCallID: "tc1", Content: readContent},
		{Role: message.RoleAssistant, Content: "done"},
	}
	setTestRequestBatch(a, nil, 2)
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	a.turn = &Turn{ID: 1, Ctx: turnCtx, Cancel: turnCancel}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == readContent {
		t.Fatal("aged webfetch result should be reduced on request 1")
	}
	a.updatePreparedLLMRequestSurface(a.currentTurnID(), prepared)

	notice := message.Message{
		Role:            message.RoleUser,
		Kind:            message.KindContextNotice,
		NoticeLevel:     contextNoticePressure,
		PressureCycleID: 1,
		Content:         "<system-reminder>\nThe context is approaching the configured automatic-compaction threshold.\n</system-reminder>",
	}
	setTestRequestBatch(a, nil, 3)
	prepared2 := a.prepareMessagesForLLM(append(append([]message.Message(nil), msgs...), notice))
	if stats := a.GetContextReductionStats(); !stats.ReusedStable {
		t.Fatalf("stable surface should be reused after the durable notice row, stats=%+v", stats)
	}
	if prepared2[2].Content != prepared[2].Content {
		t.Fatal("frozen reduced read marker should stay byte-stable across requests")
	}
}
