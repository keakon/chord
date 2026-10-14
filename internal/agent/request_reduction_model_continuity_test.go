package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestPrepareMessagesForLLM_WrapUpGraceDoesNotProtectAfterModelSwitch(t *testing.T) {
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
	a.providerModelRef = "test/model-b"
	a.lastLLMRequestModelRef = "test/model-a"
	a.llmModelRunLength = 3
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameWebFetch, Args: json.RawMessage(`{"url":"https://example.com/a.go"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: strings.Repeat("large read output ", 100)},
		{Role: "user", Content: "u2"},
	}

	a.beginContextReductionWrapUpGrace()
	prepared := a.prepareMessagesForLLM(msgs)
	if !strings.Contains(prepared[2].Content, "Older "+tools.NameWebFetch+" output truncated for this request") {
		t.Fatalf("model switch should bypass wrap-up grace, got %q", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLM_DoesNotProtectWarmPromptCacheSurfaceOnSameModelRun(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ShellSuccessAgeTurns: 1,
		ShellSuccessBytes:    10,
	}}}
	a.providerModelRef = "test/model"
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "model", 1024, "")
	a.llmMu.Lock()
	a.runningModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 2
	a.llmMu.Unlock()
	largeOutput := strings.Repeat("test output line\n", 20)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm test"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == largeOutput {
		t.Fatal("same-model prompt cache warmup should not protect an otherwise reducible tool output")
	}
	stats := a.GetContextReductionStats()
	if stats.Protected || stats.Messages == 0 || stats.Bytes == 0 || stats.TokensBefore <= stats.TokensAfter {
		t.Fatalf("context reduction stats = %+v, want unprotected reduction", stats)
	}
	if stats.ModelRunLength != 3 {
		t.Fatalf("model run length = %d, want 3", stats.ModelRunLength)
	}
}

func TestPrepareMessagesForLLM_DoesNotProtectWarmPromptCacheSurfaceBeforeThirdSameModelRun(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ShellSuccessAgeTurns: 1,
		ShellSuccessBytes:    10,
	}}}
	a.providerModelRef = "test/model"
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{"model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "model", 1024, "")
	a.llmMu.Lock()
	a.runningModelRef = "test/model"
	a.lastLLMRequestModelRef = "test/model"
	a.llmModelRunLength = 1
	a.llmMu.Unlock()
	largeOutput := strings.Repeat("test output line\n", 20)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm test"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == largeOutput {
		t.Fatalf("expected second same-model request to reduce instead of protecting cache surface")
	}
	stats := a.GetContextReductionStats()
	if stats.Protected || stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("context reduction stats = %+v, want unprotected reduction", stats)
	}
	if stats.ModelRunLength != 2 {
		t.Fatalf("model run length = %d, want 2", stats.ModelRunLength)
	}
}

func TestPrepareMessagesForLLM_DoesNotProtectWarmPromptCacheSurfaceAfterModelSwitch(t *testing.T) {
	a := &MainAgent{}
	a.projectConfig = &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ShellSuccessAgeTurns: 1,
		ShellSuccessBytes:    10,
	}}}
	a.providerModelRef = "test/model-b"
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			"model-b": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "model-b", 1024, "")
	a.llmMu.Lock()
	a.runningModelRef = "test/model-b"
	a.lastLLMRequestModelRef = "test/model-a"
	a.llmModelRunLength = 5
	a.llmMu.Unlock()
	largeOutput := strings.Repeat("test output line\n", 20)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm test"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	prepared := a.prepareMessagesForLLM(msgs)
	if prepared[2].Content == largeOutput {
		t.Fatalf("expected model-switch request to reduce instead of protecting cache surface")
	}
	stats := a.GetContextReductionStats()
	if stats.Protected || stats.Messages == 0 || stats.Bytes == 0 {
		t.Fatalf("context reduction stats = %+v, want unprotected reduction", stats)
	}
	if stats.ModelRunLength != 1 {
		t.Fatalf("model run length = %d, want 1", stats.ModelRunLength)
	}
}

// TestPrepareMessagesForLLM_DoesNotReuseFrozenSurfaceAfterModelSwitch covers the
// incrementalReductionSurface (frozen prefix) path: a stable surface recorded
// under the old model must not be frozen-and-reused after a model switch, even
// when tool definitions are unchanged. Otherwise the new model inherits the old
// model's reduction budget and skips its own re-reduction.
func TestPrepareMessagesForLLM_DoesNotReuseFrozenSurfaceAfterModelSwitch(t *testing.T) {
	a := &MainAgent{projectConfig: &config.Config{Context: config.ContextConfig{Reduction: config.ContextReductionConfig{
		ShellSuccessAgeTurns: 1,
		ShellSuccessBytes:    10,
	}}}}
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"model-a": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			"model-b": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"test-key"})
	a.llmClient = llm.NewClient(providerCfg, stubProvider{}, "model-b", 1024, "")
	largeOutput := strings.Repeat("test output line\n", 40)
	msgs := []message.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "tc1", Name: tools.NameShell, Args: json.RawMessage(`{"command":"npm test"}`)}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeOutput},
		{Role: "user", Content: "u2"},
		{Role: "user", Content: "u3"},
	}

	// First request on model-a, with a long prior run length so a stable
	// reduction surface (frozen prefix) gets recorded carrying the reduced
	// shell output.
	a.llmMu.Lock()
	a.runningModelRef = "test/model-a"
	a.lastLLMRequestModelRef = "test/model-a"
	a.llmModelRunLength = 5
	a.llmMu.Unlock()
	first := a.prepareMessagesForLLM(msgs)
	if first[2].Content == largeOutput {
		t.Fatalf("first request should reduce the shell output")
	}

	// Second request on model-b: tool definitions are identical, so without a
	// model check the frozen prefix from model-a would be reused verbatim. The
	// ProjectedModelRunLength must drop to 1, invalidating the frozen surface so
	// model-b re-reduces instead of inheriting model-a's compressed prefix.
	a.llmMu.Lock()
	a.runningModelRef = "test/model-b"
	a.lastLLMRequestModelRef = "test/model-a"
	a.llmModelRunLength = 5
	a.llmMu.Unlock()
	second := a.prepareMessagesForLLM(msgs)
	if second[2].Content == largeOutput {
		t.Fatalf("model switch should re-reduce instead of freezing the old surface")
	}
	stats := a.GetContextReductionStats()
	if stats.ModelRunLength != 1 {
		t.Fatalf("model run length = %d, want 1 after switch", stats.ModelRunLength)
	}
}

func TestApplyAnthropicCacheHintsCountsMetaPrefixAndDurableTail(t *testing.T) {
	newAnthropicAgent := func(t *testing.T) (*MainAgent, *recordingLoopTuningProvider) {
		t.Helper()
		a := newTestMainAgent(t, t.TempDir())
		provider := &recordingLoopTuningProvider{}
		providerCfg := llm.NewProviderConfig("anthropic", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"claude": {Limit: config.ModelLimit{Context: 200000, Output: 8192}},
			},
		}, []string{"test-key"})
		a.llmClient = llm.NewClient(providerCfg, provider, "claude", 8192, "sys")
		a.providerModelRef = "anthropic/claude"
		a.llmMu.Lock()
		a.runningModelRef = "anthropic/claude"
		a.llmMu.Unlock()
		return a, provider
	}

	readTuning := func(t *testing.T, a *MainAgent, provider *recordingLoopTuningProvider) llm.AnthropicTuning {
		t.Helper()
		if _, err := a.llmClient.CompleteStream(context.Background(), nil, nil, nil); err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
		provider.mu.Lock()
		defer provider.mu.Unlock()
		if len(provider.tunes) != 1 {
			t.Fatalf("len(tunes) = %d, want 1", len(provider.tunes))
		}
		return provider.tunes[0].Anthropic
	}
	readBoundary := func(t *testing.T, a *MainAgent, provider *recordingLoopTuningProvider) llm.AnthropicCacheBoundary {
		t.Helper()
		return readTuning(t, a, provider).CacheBoundary
	}

	t.Run("meta prefix shifts boundary", func(t *testing.T) {
		a, provider := newAnthropicAgent(t)
		// stableLen=4 stable-prefix messages, plus 1 reminder + 2 meta messages
		// prepended before the first user message.
		a.applyAnthropicCacheHints(4, 3, 0)
		got := readBoundary(t, a, provider)
		if !got.Valid {
			t.Fatal("expected a valid cache boundary on an Anthropic explicit-cache client")
		}
		if got.MessageIndex != 4-1+3 {
			t.Fatalf("boundary index = %d, want %d (stableLen-1+metaPrefixCount)", got.MessageIndex, 4-1+3)
		}
	})

	t.Run("zero meta prefix keeps boundary at stable tail", func(t *testing.T) {
		a, provider := newAnthropicAgent(t)
		a.applyAnthropicCacheHints(4, 0, 0)
		got := readBoundary(t, a, provider)
		if got.MessageIndex != 3 {
			t.Fatalf("boundary index = %d, want 3 (stableLen-1) when no meta prefix", got.MessageIndex)
		}
	})

	// The newest breakpoint must land on the last durable message so the cache
	// entry it writes is still readable next request; transient tail overlays
	// are excluded from durableLen by the caller.
	t.Run("latest boundary excludes transient tail overlays", func(t *testing.T) {
		a, provider := newAnthropicAgent(t)
		// 10 messages on the wire, the last 2 being transient overlays.
		a.applyAnthropicCacheHints(4, 0, 8)
		got := readTuning(t, a, provider)
		if !got.CacheLatestBoundary.Valid {
			t.Fatal("expected a valid latest cache boundary")
		}
		if got.CacheLatestBoundary.MessageIndex != 7 {
			t.Fatalf("latest boundary index = %d, want 7 (durableLen-1)", got.CacheLatestBoundary.MessageIndex)
		}
	})

	t.Run("no frozen prefix still hints the latest boundary", func(t *testing.T) {
		a, provider := newAnthropicAgent(t)
		a.applyAnthropicCacheHints(0, 0, 6)
		got := readTuning(t, a, provider)
		if got.CacheBoundary.Valid {
			t.Fatalf("expected no frozen-prefix boundary, got %+v", got.CacheBoundary)
		}
		if !got.CacheLatestBoundary.Valid || got.CacheLatestBoundary.MessageIndex != 5 {
			t.Fatalf("latest boundary = %+v, want index 5", got.CacheLatestBoundary)
		}
	})

	t.Run("ignored for non-anthropic provider", func(t *testing.T) {
		a := newTestMainAgent(t, t.TempDir())
		provider := &recordingLoopTuningProvider{}
		providerCfg := llm.NewProviderConfig("openai", config.ProviderConfig{
			Type: config.ProviderTypeChatCompletions,
			Models: map[string]config.ModelConfig{
				"gpt": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
			},
		}, []string{"test-key"})
		a.llmClient = llm.NewClient(providerCfg, provider, "gpt", 4096, "sys")
		a.providerModelRef = "openai/gpt"
		a.llmMu.Lock()
		a.runningModelRef = "openai/gpt"
		a.llmMu.Unlock()
		a.applyAnthropicCacheHints(4, 3, 8)
		got := readBoundary(t, a, provider)
		if got.Valid {
			t.Fatalf("expected no cache boundary for a non-Anthropic provider, got %+v", got)
		}
	})
}
