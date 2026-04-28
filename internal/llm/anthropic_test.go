package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestAnthropicBetaHeaderMergesCompatAndThinking(t *testing.T) {
	got := anthropicBetaHeader(AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024}, &config.AnthropicTransportCompatConfig{
		ExtraBeta: []string{
			"beta-a",
			"interleaved-thinking-2025-05-14",
			" beta-b ",
			"",
			"beta-a",
		},
	})
	want := "interleaved-thinking-2025-05-14,beta-a,beta-b"
	if got != want {
		t.Fatalf("anthropicBetaHeader() = %q, want %q", got, want)
	}
}

func TestAnthropicBetaHeaderAdaptiveDoesNotInjectInterleaved(t *testing.T) {
	got := anthropicBetaHeader(AnthropicTuning{ThinkingType: "adaptive"}, &config.AnthropicTransportCompatConfig{
		ExtraBeta: []string{"beta-a"},
	})
	if got != "beta-a" {
		t.Fatalf("anthropicBetaHeader() = %q, want beta-a", got)
	}
}

func TestBuildAnthropicThinking(t *testing.T) {
	tests := []struct {
		name string
		in   AnthropicTuning
		want *anthropicThinking
	}{
		{
			name: "budget without explicit type is ignored",
			in:   AnthropicTuning{ThinkingBudget: 2048},
			want: nil,
		},
		{
			name: "adaptive includes display but no budget",
			in:   AnthropicTuning{ThinkingType: "adaptive", ThinkingDisplay: "omitted"},
			want: &anthropicThinking{Type: "adaptive", Display: "omitted"},
		},
		{
			name: "disabled only carries type",
			in:   AnthropicTuning{ThinkingType: "disabled"},
			want: &anthropicThinking{Type: "disabled"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildAnthropicThinking(tc.in)
			if !reflectDeepEqualAnthropicThinking(got, tc.want) {
				t.Fatalf("buildAnthropicThinking(%+v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildAnthropicOutputConfig(t *testing.T) {
	got := buildAnthropicOutputConfig(AnthropicTuning{ThinkingEffort: "medium"})
	if got == nil || got.Effort != "medium" {
		t.Fatalf("buildAnthropicOutputConfig() = %#v, want effort medium", got)
	}
	if got := buildAnthropicOutputConfig(AnthropicTuning{}); got != nil {
		t.Fatalf("buildAnthropicOutputConfig() = %#v, want nil", got)
	}
}

func TestApplyPromptCachingAutoSetsTopLevelCacheControl(t *testing.T) {
	req := &anthropicRequest{}
	if err := applyPromptCaching(AnthropicTuning{PromptCacheMode: "auto", PromptCacheTTL: "1h"}, req); err != nil {
		t.Fatalf("applyPromptCaching: %v", err)
	}
	if req.CacheControl == nil || req.CacheControl.Type != "ephemeral" || req.CacheControl.TTL != "1h" {
		t.Fatalf("cache_control = %#v, want ephemeral ttl=1h", req.CacheControl)
	}
}

func TestConvertToolsWithCacheMarksLastToolInExplicitMode(t *testing.T) {
	tools := []message.ToolDefinition{
		{Name: "a", Description: "A", InputSchema: map[string]any{"type": "object"}},
		{Name: "b", Description: "B", InputSchema: map[string]any{"type": "object"}},
	}
	got := convertToolsWithCache(tools, AnthropicTuning{PromptCacheMode: "explicit", CacheTools: true, PromptCacheTTL: "1h"})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].CacheControl != nil {
		t.Fatalf("first tool cache_control = %#v, want nil", got[0].CacheControl)
	}
	if got[1].CacheControl == nil || got[1].CacheControl.Type != "ephemeral" || got[1].CacheControl.TTL != "1h" {
		t.Fatalf("last tool cache_control = %#v, want ephemeral ttl=1h", got[1].CacheControl)
	}
}

func TestAnthropicCompleteStreamAppliesTransportCompat(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", "/tmp/chord-config")
	t.Setenv("USER", "tester")

	type capturedRequest struct {
		Header http.Header
		Body   anthropicRequest
	}

	var captured capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Header = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: srv.URL,
		Compat: &config.ProviderCompatConfig{
			AnthropicTransport: &config.AnthropicTransportCompatConfig{
				SystemPrefix:   "[proxy-prefix]\n",
				ExtraBeta:      []string{"beta-a", "interleaved-thinking-2025-05-14", "beta-b"},
				UserAgent:      "Chord-Test/1.0",
				MetadataUserID: true,
			},
		},
	}, []string{"test-key"})

	anthropicProvider, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		2048,
		RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}

	if got := captured.Header.Get("anthropic-beta"); got != "interleaved-thinking-2025-05-14,beta-a,beta-b" {
		t.Fatalf("anthropic-beta = %q", got)
	}
	if got := captured.Header.Get("User-Agent"); got != "Chord-Test/1.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if captured.Body.Metadata == nil || captured.Body.Metadata.UserID == "" {
		t.Fatalf("expected metadata.user_id to be present, got %#v", captured.Body.Metadata)
	}
	if len(captured.Body.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(captured.Body.System))
	}
	if got := captured.Body.System[0].Text; got != "[proxy-prefix]\nbase system prompt" {
		t.Fatalf("system block text = %q", got)
	}
}

func TestAnthropicCompleteStreamEncodesAdaptiveThinkingAndAutoCache(t *testing.T) {
	type capturedRequest struct {
		Header http.Header
		Body   anthropicRequest
	}

	var captured capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Header = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL}, []string{"test-key"})
	anthropicProvider, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	tools := []message.ToolDefinition{{Name: "search", Description: "Search", InputSchema: map[string]any{"type": "object"}}}
	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		tools,
		2048,
		RequestTuning{Anthropic: AnthropicTuning{
			ThinkingType:    "adaptive",
			ThinkingEffort:  "medium",
			ThinkingDisplay: "omitted",
			PromptCacheMode: "auto",
			PromptCacheTTL:  "1h",
			CacheTools:      true,
		}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}

	if got := captured.Header.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta = %q, want empty in adaptive mode", got)
	}
	if captured.Body.Thinking == nil || captured.Body.Thinking.Type != "adaptive" || captured.Body.Thinking.Display != "omitted" || captured.Body.Thinking.BudgetTokens != 0 {
		t.Fatalf("thinking = %#v, want adaptive omitted without budget", captured.Body.Thinking)
	}
	if captured.Body.OutputConfig == nil || captured.Body.OutputConfig.Effort != "medium" {
		t.Fatalf("output_config = %#v, want effort=medium", captured.Body.OutputConfig)
	}
	if captured.Body.CacheControl == nil || captured.Body.CacheControl.Type != "ephemeral" || captured.Body.CacheControl.TTL != "1h" {
		t.Fatalf("cache_control = %#v, want ephemeral ttl=1h", captured.Body.CacheControl)
	}
	if len(captured.Body.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(captured.Body.Tools))
	}
	if captured.Body.Tools[0].CacheControl != nil {
		t.Fatalf("tool cache_control = %#v, want nil in auto mode", captured.Body.Tools[0].CacheControl)
	}
}

func TestAnthropicCompleteStreamLeavesTransportDefaultsUnchanged(t *testing.T) {
	type capturedRequest struct {
		Header http.Header
		Body   anthropicRequest
	}

	var captured capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Header = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: srv.URL,
	}, []string{"test-key"})

	anthropicProvider, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		2048,
		RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}

	if got := captured.Header.Get("anthropic-beta"); got != "" {
		t.Fatalf("anthropic-beta = %q, want empty", got)
	}
	if captured.Body.Metadata != nil {
		t.Fatalf("expected metadata to be omitted, got %#v", captured.Body.Metadata)
	}
	if len(captured.Body.System) != 1 || captured.Body.System[0].Text != "base system prompt" {
		t.Fatalf("unexpected system block: %#v", captured.Body.System)
	}
}

func TestParseSSEStreamAggregatesAnthropicCacheUsage(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0,\"cache_read_input_tokens\":7,\"cache_creation\":{\"ephemeral_5m_input_tokens\":11,\"ephemeral_1h_input_tokens\":13}}}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hello\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":23}}",
		"",
		"event: message_stop",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	resp, err := parseSSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if resp == nil || resp.Usage == nil {
		t.Fatalf("resp/usage = %#v, want non-nil", resp)
	}
	if got := resp.Usage.InputTokens; got != 100 {
		t.Fatalf("InputTokens = %d, want 100", got)
	}
	if got := resp.Usage.OutputTokens; got != 23 {
		t.Fatalf("OutputTokens = %d, want 23", got)
	}
	if got := resp.Usage.CacheReadTokens; got != 7 {
		t.Fatalf("CacheReadTokens = %d, want 7", got)
	}
	if got := resp.Usage.CacheWriteTokens; got != 24 {
		t.Fatalf("CacheWriteTokens = %d, want 24", got)
	}
}

func TestParseSSEStreamEmitsProgressDeltas(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
		"",
		"event: message_stop",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	var progress []message.StreamProgressDelta
	_, err := parseSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.Progress != nil {
			progress = append(progress, *delta.Progress)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress deltas")
	}
	if progress[0].Bytes <= 0 || progress[0].Events != 1 {
		t.Fatalf("first progress = %+v, want positive bytes and 1 event", progress[0])
	}
}

func TestStableAnthropicMetadataUserID(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", "/tmp/chord-config")
	t.Setenv("USER", "tester")

	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{Type: config.ProviderTypeMessages}, nil)
	compat := &config.AnthropicTransportCompatConfig{MetadataUserID: true}
	got1 := stableAnthropicMetadataUserID(provider, compat)
	got2 := stableAnthropicMetadataUserID(provider, compat)

	if got1 == "" {
		t.Fatal("expected stable metadata user id")
	}
	if got1 != got2 {
		t.Fatalf("expected stable metadata user id, got %q and %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "chord_") {
		t.Fatalf("expected chord_ prefix, got %q", got1)
	}
}

func TestValidateAnthropicTuningRejectsEnabledWithoutBudget(t *testing.T) {
	_, err := validateAnthropicTuning(AnthropicTuning{ThinkingType: "enabled"})
	if err == nil {
		t.Fatal("expected error for enabled thinking without budget")
	}
	if !strings.Contains(err.Error(), "requires thinking.budget > 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAnthropicTuningRejectsAdaptiveBudgetMix(t *testing.T) {
	_, err := validateAnthropicTuning(AnthropicTuning{ThinkingType: "adaptive", ThinkingBudget: 1024})
	if err == nil {
		t.Fatal("expected error for adaptive thinking with budget")
	}
	if !strings.Contains(err.Error(), `does not support thinking.budget`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAnthropicTuningRejectsEnabledEffortMix(t *testing.T) {
	_, err := validateAnthropicTuning(AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024, ThinkingEffort: "high"})
	if err == nil {
		t.Fatal("expected error for enabled thinking with effort")
	}
	if !strings.Contains(err.Error(), `does not support thinking.effort`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAnthropicTuningRejectsDisabledDisplayMix(t *testing.T) {
	_, err := validateAnthropicTuning(AnthropicTuning{ThinkingType: "disabled", ThinkingDisplay: "omitted"})
	if err == nil {
		t.Fatal("expected error for disabled thinking with display")
	}
	if !strings.Contains(err.Error(), `does not support thinking.display`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAnthropicTuningRejectsUnknownPromptCacheMode(t *testing.T) {
	_, err := validateAnthropicTuning(AnthropicTuning{PromptCacheMode: "hybrid"})
	if err == nil {
		t.Fatal("expected error for unsupported prompt_cache.mode")
	}
	if !strings.Contains(err.Error(), "unsupported anthropic prompt_cache.mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func reflectDeepEqualAnthropicThinking(a, b *anthropicThinking) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Type == b.Type && a.BudgetTokens == b.BudgetTokens && a.Display == b.Display
}
