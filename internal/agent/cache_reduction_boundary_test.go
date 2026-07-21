package agent

import (
	"context"
	"encoding/json"
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

	a.clearLoopReductionCache(false)
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
