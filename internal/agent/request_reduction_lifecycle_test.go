package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_WrapUpGracePrunesWhenSavingsAreWorthIt(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background(), projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  1,
		StaleAgeTurns:        99,
		StaleOutputBytes:     1,
		MinToolResultsPrune:  1,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1,
		MinIncrementalTokens: 64,
	}}}}
	a.newTurn("")
	a.providerModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 1
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 100)},
		{Role: "user", Content: "u2"},
	}

	a.beginContextReductionWrapUpGrace()
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request") {
		t.Fatalf("wrap-up grace should still prune worthwhile savings, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if stats.Protected || stats.Messages == 0 || stats.TokensSaved < 64 {
		t.Fatalf("stats = %+v, want unprotected worthwhile reduction", stats)
	}

	prepared = a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request") {
		t.Fatalf("wrap-up grace should have been consumed after one request, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_WrapUpGraceSkipsLowValueReduction(t *testing.T) {
	a := &MainAgent{parentCtx: context.Background(), projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ReadLikeAgeTurns:     1,
		ReadLikeOutputBytes:  1,
		StaleAgeTurns:        99,
		StaleOutputBytes:     1,
		MinToolResultsPrune:  1,
		ShellSuccessAgeTurns: 99,
		ShellSuccessBytes:    1,
		MinIncrementalTokens: 4096,
	}}}}
	a.newTurn("")
	a.providerModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 1
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("small read output ", 20)},
		{Role: "user", Content: "u2"},
	}

	a.beginContextReductionWrapUpGrace()
	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content != msgs[2].Content {
		t.Fatalf("wrap-up grace should preserve low-value reduction, got %q", prepared[2].Content)
	}
	stats := a.GetContextReductionStats()
	if !stats.Protected || stats.ProtectReason != contextProtectReasonWrapUpGrace || stats.ReusedStable {
		t.Fatalf("stats = %+v, want protected low-value no-op", stats)
	}

	a.resetLLMModelRun()
	prepared = a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request") {
		t.Fatalf("wrap-up grace should be consumed after one low-value skip, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_WrapUpGraceDoesNotProtectWithQueuedUserMessage(t *testing.T) {
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
	a.pendingUserMessages = []pendingUserMessage{{Content: "queued follow-up"}}
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 100)},
		{Role: "user", Content: "u2"},
	}

	a.beginContextReductionWrapUpGrace()
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request") {
		t.Fatalf("queued user message should bypass wrap-up grace protection, got %q", prepared[2].Content)
	}
	if stats := a.GetContextReductionStats(); stats.Protected || stats.ProtectReason == contextProtectReasonWrapUpGrace {
		t.Fatalf("stats = %+v, want unprotected reduction", stats)
	}
}

func TestPrepareMessagesForLLM_WrapUpGraceReusesPreviouslyReducedPrefix(t *testing.T) {
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
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeReadOutput},
		{Role: "user", Content: "u2"},
	}

	first := a.prepareMessagesForLLM(msgs)
	if first[2].Content == largeReadOutput || !strings.Contains(first[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request") {
		t.Fatalf("expected first request to reduce old read output, got %q", first[2].Content)
	}
	if stats := a.GetContextReductionStats(); stats.Bytes <= 0 || stats.Messages <= 0 {
		t.Fatalf("first reduction stats = %+v, want saved bytes", stats)
	}

	a.beginContextReductionWrapUpGrace()
	withTail := append(append([]message.Message{}, msgs...), message.Message{Role: "assistant", Content: "final answer draft"})
	second := a.prepareMessagesForLLM(withTail)
	if second[2].Content == largeReadOutput {
		t.Fatal("wrap-up grace restored a previously reduced tool output")
	}
	if second[2].Content != first[2].Content {
		t.Fatalf("wrap-up grace should reuse the previously reduced prefix, got %q want %q", second[2].Content, first[2].Content)
	}
	if second[len(second)-1].Content != "final answer draft" {
		t.Fatalf("wrap-up grace should preserve new tail message, got %q", second[len(second)-1].Content)
	}
	stats := a.GetContextReductionStats()
	if !stats.Protected || stats.ProtectReason != contextProtectReasonWrapUpGrace || !stats.ReusedStable {
		t.Fatalf("stats = %+v, want protected wrap-up stable reuse", stats)
	}
	if stats.Bytes <= 0 || stats.CurrentBytes >= ctxmgr.MessagePayloadBytes(withTail) {
		t.Fatalf("stats = %+v, want retained prefix savings and smaller current surface", stats)
	}
}
