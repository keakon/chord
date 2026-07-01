package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	toolpkg "github.com/keakon/chord/internal/tools"
)

func TestAnthropicBetaHeaderDefaultAndThinking(t *testing.T) {
	got := anthropicBetaHeader(AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024}, 200000)
	want := "claude-code-20250219,interleaved-thinking-2025-05-14"
	if got != want {
		t.Fatalf("anthropicBetaHeader() = %q, want %q", got, want)
	}
}

func TestConvertMessagesMarksInterruptedAssistant(t *testing.T) {
	got := convertMessages([]message.Message{{Role: "assistant", Content: "partial", StopReason: "interrupted"}})
	if len(got) != 1 || got[0].Role != "assistant" {
		t.Fatalf("convertMessages() = %#v", got)
	}
	blocks, ok := got[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 1 {
		t.Fatalf("convertMessages() = %#v", got)
	}
	text := blocks[0].Text
	if !strings.Contains(text, "partial") || !strings.Contains(text, "interrupted before completion") {
		t.Fatalf("interrupted assistant text = %q", text)
	}
}

func TestConvertMessagesSkipsEmptyAssistant(t *testing.T) {
	got := convertMessages([]message.Message{
		{Role: "user", Content: "before"},
		{Role: "assistant", StopReason: "max_tokens"},
		{Role: "assistant", ThinkingBlocks: []message.ThinkingBlock{{Thinking: "analysis"}}, StopReason: "max_tokens"},
		{Role: "user", Content: "after"},
	})
	if len(got) != 1 {
		t.Fatalf("convertMessages() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Role != "user" {
		t.Fatalf("empty assistant message was not skipped: %#v", got)
	}
	blocks, ok := got[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 || blocks[0].Text != "before" || blocks[1].Text != "after" {
		t.Fatalf("adjacent users were not merged after skip: %#v", got[0].Content)
	}
}

func TestStableAnthropicMetadataUserIDPayload_JSONShape(t *testing.T) {
	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{}, nil)
	payload := stableAnthropicMetadataUserIDPayload(provider)
	if payload == "" {
		t.Fatal("stableAnthropicMetadataUserIDPayload() = empty, want JSON payload")
	}
	var got struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if got.DeviceID == "" {
		t.Fatal("device_id = empty, want stable routing value")
	}
	if got.SessionID == "" {
		t.Fatal("session_id = empty, want stable routing value")
	}
	if got.AccountUUID != "" {
		t.Fatalf("account_uuid = %q, want empty string", got.AccountUUID)
	}
}

func TestAnthropicProvider_DefaultMessagesAuthUsesXAPIKey(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			"event: message_start",
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
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
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
			"",
			"event: message_stop",
			"data: {\"type\":\"message_stop\"}",
			"",
		}, "\n"))
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("anthropic", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: server.URL + "/v1/messages",
	}, []string{"test-key"})
	provider := &AnthropicProvider{provider: providerCfg, client: server.Client()}

	_, err := provider.CompleteStream(
		context.Background(), "test-key", "claude-test", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotHeaders.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key = %q, want test-key", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	if got := gotHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
	}
}

func TestAnthropicProvider_AuthSchemeBearerUsesAuthorizationHeader(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			"event: message_start",
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
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
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}",
			"",
			"event: message_stop",
			"data: {\"type\":\"message_stop\"}",
			"",
		}, "\n"))
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("sensenova", config.ProviderConfig{
		Type:       config.ProviderTypeMessages,
		APIURL:     server.URL + "/v1/messages",
		AuthScheme: config.AuthSchemeBearer,
	}, []string{"test-key"})
	provider := &AnthropicProvider{provider: providerCfg, client: server.Client()}

	_, err := provider.CompleteStream(
		context.Background(), "test-key", "glm-5.2", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 0, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer test-key", got)
	}
	if got := gotHeaders.Get("x-api-key"); got != "" {
		t.Fatalf("x-api-key = %q, want empty", got)
	}
	if got := gotHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
	}
}

func TestAnthropicBetaHeaderAdaptiveDoesNotInjectInterleaved(t *testing.T) {
	got := anthropicBetaHeader(AnthropicTuning{ThinkingType: "adaptive"}, 200000)
	want := "claude-code-20250219"
	if got != want {
		t.Fatalf("anthropicBetaHeader() = %q, want %q", got, want)
	}
}

func TestAnthropicBetaHeaderEffortOnlyWhenConfigured(t *testing.T) {
	if got := anthropicBetaHeader(AnthropicTuning{}, 200000); strings.Contains(got, "effort-2025-11-24") {
		t.Fatalf("anthropicBetaHeader() = %q, should not inject effort without a configured level", got)
	}
	got := anthropicBetaHeader(AnthropicTuning{ThinkingType: "adaptive", ThinkingEffort: "high"}, 200000)
	if !strings.Contains(got, "effort-2025-11-24") {
		t.Fatalf("anthropicBetaHeader() = %q, want effort beta when ThinkingEffort is set", got)
	}
}

func TestAnthropicBetaHeaderDoesNotDefaultStructuredOutputs(t *testing.T) {
	got := anthropicBetaHeader(AnthropicTuning{}, 200000)
	if strings.Contains(got, "structured-outputs-2025-12-15") {
		t.Fatalf("anthropicBetaHeader() = %q, should not inject structured outputs without output_format", got)
	}
}

func TestAnthropicBetaHeaderContext1MOnlyForMillionTokenModels(t *testing.T) {
	// Below the 1M window: no context-1m beta.
	if got := anthropicBetaHeader(AnthropicTuning{}, 200000); strings.Contains(got, "context-1m-2025-08-07") {
		t.Fatalf("anthropicBetaHeader(200K) = %q, should not inject context-1m", got)
	}
	// Exactly 1M: opt in.
	got := anthropicBetaHeader(AnthropicTuning{}, 1000000)
	if !strings.Contains(got, "context-1m-2025-08-07") {
		t.Fatalf("anthropicBetaHeader(1M) = %q, want context-1m", got)
	}
	// Unknown/zero limit: stay conservative and omit it.
	if got := anthropicBetaHeader(AnthropicTuning{}, 0); strings.Contains(got, "context-1m-2025-08-07") {
		t.Fatalf("anthropicBetaHeader(0) = %q, should not inject context-1m", got)
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

func countCacheControls(messages []anthropicMessage) int {
	total := 0
	for _, msg := range messages {
		blocks, ok := msg.Content.([]anthropicContent)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b.CacheControl != nil {
				total++
			}
		}
	}
	return total
}

func cacheControlRoles(messages []anthropicMessage) []int {
	var idxs []int
	for i, msg := range messages {
		blocks, ok := msg.Content.([]anthropicContent)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b.CacheControl != nil {
				idxs = append(idxs, i)
				break
			}
		}
	}
	return idxs
}

func cacheControlBlockTexts(messages []anthropicMessage) []string {
	var texts []string
	for _, msg := range messages {
		blocks, ok := msg.Content.([]anthropicContent)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b.CacheControl != nil {
				texts = append(texts, b.Text)
			}
		}
	}
	return texts
}

func TestResolveAnthropicCacheBoundaryAfterUserMessageMerging(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Content: "session reminder"},
		{Role: "user", Content: "turn overlay"},
		{Role: "user", Content: "stable-prefix-end"},
		{Role: "assistant", Content: "old assistant"},
		{Role: "user", Content: "last user"},
	}
	apiMessages, messageMap := convertMessagesWithMap(msgs)
	boundary := resolveAnthropicCacheBoundary(AnthropicCacheBoundary{MessageIndex: 2, Valid: true}, messageMap)
	if !boundary.Valid || boundary.MessageIndex != 0 || boundary.BlockIndex != 2 {
		t.Fatalf("resolved boundary = %+v, want message 0 block 2", boundary)
	}

	system := []anthropicContent{{Type: "text", Text: "system"}}
	applyCacheBreakpoints(system, apiMessages, boundary, 0)
	texts := cacheControlBlockTexts(apiMessages)
	if len(texts) == 0 || texts[0] != "stable-prefix-end" {
		t.Fatalf("cache_control texts = %#v, want boundary block first", texts)
	}
}

func TestResolveAnthropicCacheBoundaryAfterToolResultMerging(t *testing.T) {
	msgs := []message.Message{
		{Role: "assistant", Content: "call", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "call-1", Content: "first tool"},
		{Role: "tool", ToolCallID: "call-2", Content: "stable tool"},
		{Role: "assistant", Content: "done"},
	}
	_, messageMap := convertMessagesWithMap(msgs)
	boundary := resolveAnthropicCacheBoundary(AnthropicCacheBoundary{MessageIndex: 2, Valid: true}, messageMap)
	if !boundary.Valid || boundary.MessageIndex != 1 || boundary.BlockIndex != 1 {
		t.Fatalf("resolved tool boundary = %+v, want message 1 block 1", boundary)
	}
}

func TestApplyCacheBreakpointsPrioritizesStablePrefixBoundary(t *testing.T) {
	system := []anthropicContent{{Type: "text", Text: "system"}}
	messages := []anthropicMessage{
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "frozen-prefix-end"}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "old-assistant"}}},
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "older-tail-user"}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "last-assistant"}}},
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "last-user"}}},
	}

	applyCacheBreakpoints(system, messages, AnthropicCacheBoundary{MessageIndex: 0, Valid: true}, 0)

	if system[len(system)-1].CacheControl == nil {
		t.Fatalf("expected system[-1] cache_control")
	}
	idxs := cacheControlRoles(messages)
	// Boundary (index 0), last user (index 4), and last assistant (index 3) are
	// the three message breakpoints, replacing the older tail user marker.
	want := map[int]bool{0: true, 3: true, 4: true}
	for _, i := range idxs {
		if !want[i] {
			t.Fatalf("unexpected cache_control at message %d (have %v)", i, idxs)
		}
	}
	if len(idxs) != len(want) {
		t.Fatalf("expected %d message breakpoints, got %d (%v)", len(want), len(idxs), idxs)
	}
	if total := 1 + countCacheControls(messages); total != 4 {
		t.Fatalf("expected 4 total breakpoints, got %d", total)
	}
}

func TestApplyCacheBreakpointsFallsBackToTailWhenNoBoundary(t *testing.T) {
	system := []anthropicContent{{Type: "text", Text: "system"}}
	messages := []anthropicMessage{
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "u1"}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "a1"}}},
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "last-user"}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "last-assistant"}}},
	}

	applyCacheBreakpoints(system, messages, AnthropicCacheBoundary{}, 0)

	if system[len(system)-1].CacheControl == nil {
		t.Fatalf("expected system[-1] cache_control")
	}
	idxs := cacheControlRoles(messages)
	want := map[int]bool{2: true, 3: true}
	for _, i := range idxs {
		if !want[i] {
			t.Fatalf("unexpected cache_control at message %d (have %v)", i, idxs)
		}
	}
	if len(idxs) != len(want) {
		t.Fatalf("expected last user + last assistant breakpoints, got %v", idxs)
	}
}

func TestApplyCacheBreakpointsRespectsFourBreakpointLimitWithCachedTools(t *testing.T) {
	system := []anthropicContent{{Type: "text", Text: "system"}}
	messages := []anthropicMessage{
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "boundary"}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "assistant"}}},
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "last-user"}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "last-assistant"}}},
	}

	// One tool breakpoint already placed; only three message/system slots remain.
	applyCacheBreakpoints(system, messages, AnthropicCacheBoundary{MessageIndex: 0, Valid: true}, 1)

	total := 0
	if system[len(system)-1].CacheControl != nil {
		total++
	}
	total += countCacheControls(messages)
	if total != 3 {
		t.Fatalf("expected 3 remaining breakpoints after one tool breakpoint, got %d", total)
	}
}

func TestApplyCacheBreakpointsSkipsAlreadyMarkedBlocks(t *testing.T) {
	system := []anthropicContent{{Type: "text", Text: "system", CacheControl: &anthropicCacheCtrl{Type: "ephemeral"}}}
	messages := []anthropicMessage{
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "boundary", CacheControl: &anthropicCacheCtrl{Type: "ephemeral"}}}},
		{Role: "assistant", Content: []anthropicContent{{Type: "text", Text: "last-assistant"}}},
		{Role: "user", Content: []anthropicContent{{Type: "text", Text: "last-user"}}},
	}

	applyCacheBreakpoints(system, messages, AnthropicCacheBoundary{MessageIndex: 0, Valid: true}, 0)

	if got := countCacheControls(messages); got != 3 {
		t.Fatalf("expected 3 marked message blocks (boundary reused + assistant + user), got %d", got)
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

func TestConvertToolsWithCacheStableAcrossRegistryConstructionOrder(t *testing.T) {
	regA := toolpkg.NewRegistry()
	regA.Register(toolpkg.GlobTool{})
	regA.Register(toolpkg.ReadTool{})

	regB := toolpkg.NewRegistry()
	regB.Register(toolpkg.ReadTool{})
	regB.Register(toolpkg.GlobTool{})

	defsA := regA.ListDefinitions()
	defsB := regB.ListDefinitions()
	// Registry.ListTools() sorts by name, so registration order doesn't affect output
	if !reflect.DeepEqual(defsA, defsB) {
		t.Fatalf("registry definitions should be stable across construction order:\nA=%#v\nB=%#v", defsA, defsB)
	}

	gotA := convertToolsWithCache(defsA, AnthropicTuning{PromptCacheMode: "explicit", CacheTools: true, PromptCacheTTL: "1h"})
	gotB := convertToolsWithCache(defsB, AnthropicTuning{PromptCacheMode: "explicit", CacheTools: true, PromptCacheTTL: "1h"})
	if !reflect.DeepEqual(gotA, gotB) {
		t.Fatalf("convertToolsWithCache should be stable across registry construction order:\nA=%#v\nB=%#v", gotA, gotB)
	}
	if len(gotA) != len(defsA) || gotA[len(gotA)-1].CacheControl == nil {
		t.Fatalf("expected last tool cache marker to be preserved, got %#v", gotA)
	}
}

func TestConvertToolsWithCache_PreservesInputOrderAndCachesLast(t *testing.T) {
	// convertToolsWithCache no longer sorts; it preserves input order from Registry
	tools := []message.ToolDefinition{
		{Name: "read", Description: "read files", InputSchema: map[string]any{"type": "object"}},
		{Name: "write", Description: "write files", InputSchema: map[string]any{"type": "object"}},
	}
	got := convertToolsWithCache(tools, AnthropicTuning{PromptCacheMode: "explicit", CacheTools: true, PromptCacheTTL: "1h"})
	if len(got) != 2 || got[0].Name != "read" || got[1].Name != "write" {
		t.Fatalf("expected tools in input order: %#v", got)
	}
	if got[0].CacheControl != nil || got[1].CacheControl == nil {
		t.Fatalf("cache_control should be on the last tool: %#v", got)
	}
}

func TestAnthropicCompleteStreamAppliesDefaultTransportHeaders(t *testing.T) {
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
		Type:      config.ProviderTypeMessages,
		APIURL:    srv.URL,
		UserAgent: "ProviderUA/1.0",
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

	if got := captured.Header.Get("anthropic-beta"); got != "claude-code-20250219,interleaved-thinking-2025-05-14" {
		t.Fatalf("anthropic-beta = %q", got)
	}
	if got := captured.Header.Get("User-Agent"); got != "ProviderUA/1.0" {
		t.Fatalf("User-Agent = %q, want ProviderUA/1.0", got)
	}
	if captured.Body.Metadata == nil || captured.Body.Metadata.UserID == "" {
		t.Fatalf("expected metadata.user_id to be present, got %#v", captured.Body.Metadata)
	}
	if len(captured.Body.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(captured.Body.System))
	}
	if got := captured.Body.System[0].Text; got != "base system prompt" {
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

	if got := captured.Header.Get("anthropic-beta"); got != "claude-code-20250219,effort-2025-11-24" {
		t.Fatalf("anthropic-beta = %q, want default Claude Code betas in adaptive mode", got)
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

func TestAnthropicCompleteStreamEncodesToolChoiceAndTemperature(t *testing.T) {
	var captured anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
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

	temperature := 0.2
	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		2048,
		RequestTuning{Anthropic: AnthropicTuning{ToolChoice: "required", Temperature: &temperature}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}

	if captured.ToolChoice == nil || captured.ToolChoice.Type != "any" {
		t.Fatalf("tool_choice = %#v, want type=any", captured.ToolChoice)
	}
	if captured.Temperature == nil || *captured.Temperature != temperature {
		t.Fatalf("temperature = %#v, want %v", captured.Temperature, temperature)
	}
}

func TestAnthropicCompleteStreamOmitsTemperatureWhenThinkingEnabled(t *testing.T) {
	var captured anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
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

	temperature := 0.2
	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		2048,
		RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024, Temperature: &temperature}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.Temperature != nil {
		t.Fatalf("temperature = %#v, want omitted when thinking is enabled", captured.Temperature)
	}
}

func TestAnthropicCompleteStreamOmitsForcedToolChoiceWhenThinkingEnabled(t *testing.T) {
	var captured anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
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

	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		2048,
		RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024, ToolChoice: "required"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.ToolChoice != nil {
		t.Fatalf("tool_choice = %#v, want omitted when thinking is enabled (forced any is rejected by Anthropic)", captured.ToolChoice)
	}
}

func TestAnthropicCompleteStreamKeepsAutoToolChoiceWhenThinkingEnabled(t *testing.T) {
	var captured anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
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

	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"claude-sonnet",
		"base system prompt",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		2048,
		RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 1024, ToolChoice: "auto"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.ToolChoice == nil || captured.ToolChoice.Type != "auto" {
		t.Fatalf("tool_choice = %#v, want type=auto preserved when thinking is enabled", captured.ToolChoice)
	}
}

func TestAnthropicCompleteStreamSetsDefaultUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
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
	_, err = anthropicProvider.CompleteStream(context.Background(), "test-key", "claude-sonnet", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 2048, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != defaultLLMUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, defaultLLMUserAgent())
	}
}

func TestAnthropicCompleteStreamSetsProviderUserAgentAndDefaultMetadata(t *testing.T) {
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
		Type:      config.ProviderTypeMessages,
		APIURL:    srv.URL,
		UserAgent: "ProviderUA/1.0",
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

	if got := captured.Header.Get("anthropic-beta"); got != "claude-code-20250219" {
		t.Fatalf("anthropic-beta = %q, want default Claude Code betas", got)
	}
	if got := captured.Header.Get("User-Agent"); got != "ProviderUA/1.0" {
		t.Fatalf("User-Agent = %q, want ProviderUA/1.0", got)
	}
	if captured.Body.Metadata == nil || captured.Body.Metadata.UserID == "" {
		t.Fatalf("expected metadata.user_id to be present by default, got %#v", captured.Body.Metadata)
	}
	// Now always returns JSON format
	if !strings.HasPrefix(captured.Body.Metadata.UserID, `{"device_id":"chord_`) {
		t.Fatalf("metadata.user_id = %q, want JSON format with chord_ device_id", captured.Body.Metadata.UserID)
	}
	if len(captured.Body.System) != 1 || captured.Body.System[0].Text != "base system prompt" {
		t.Fatalf("unexpected system block: %#v", captured.Body.System)
	}
}

func TestAnthropicCompleteStreamInjectsContext1MForMillionTokenModel(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", "/tmp/chord-config")
	t.Setenv("USER", "tester")

	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("anthropic-main", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: srv.URL,
		Models: map[string]config.ModelConfig{
			"claude-sonnet-1m": {Limit: config.ModelLimit{Context: 1000000}},
			"claude-sonnet-2h": {Limit: config.ModelLimit{Context: 200000}},
		},
	}, []string{"test-key"})

	anthropicProvider, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	send := func(model string) string {
		_, _ = anthropicProvider.CompleteStream(
			context.Background(),
			"test-key",
			model,
			"sys",
			[]message.Message{{Role: "user", Content: "hello"}},
			nil,
			2048,
			RequestTuning{},
			func(message.StreamDelta) {},
		)
		return captured.Get("anthropic-beta")
	}

	if got := send("claude-sonnet-1m"); !strings.Contains(got, "context-1m-2025-08-07") {
		t.Fatalf("1M model anthropic-beta = %q, want context-1m present", got)
	}
	if got := send("claude-sonnet-2h"); strings.Contains(got, "context-1m-2025-08-07") {
		t.Fatalf("200K model anthropic-beta = %q, want context-1m absent", got)
	}
}

func TestAnthropicCompleteStreamDefaultsMetadata(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", "/tmp/chord-config")
	t.Setenv("USER", "tester")

	var captured anthropicRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
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
	if captured.Metadata == nil || captured.Metadata.UserID == "" {
		t.Fatalf("expected metadata.user_id to be present by default, got %#v", captured.Metadata)
	}
}

func TestAnthropicCompleteStreamReplaysThinkingBlocksInAssistantHistory(t *testing.T) {
	type capturedMessage struct {
		Role    string             `json:"role"`
		Content []anthropicContent `json:"content"`
	}

	var captured struct {
		Messages []capturedMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("deepseek", config.ProviderConfig{Type: config.ProviderTypeMessages, APIURL: srv.URL}, []string{"test-key"})
	anthropicProvider, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	_, err = anthropicProvider.CompleteStream(
		context.Background(),
		"test-key",
		"deepseek-v4-pro",
		"base system prompt",
		[]message.Message{
			{Role: "user", Content: "hi"},
			{
				Role:           "assistant",
				ThinkingBlocks: []message.ThinkingBlock{{Thinking: "analyzing", Signature: "sig-1"}},
				ToolCalls:      []message.ToolCall{{ID: "toolu_1", Name: "Shell", Args: json.RawMessage(`{"command":"pwd"}`)}},
			},
			{Role: "tool", ToolCallID: "toolu_1", Content: "/tmp\n"},
		},
		nil,
		2048,
		RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if len(captured.Messages) < 2 {
		t.Fatalf("captured messages = %#v, want assistant history present", captured.Messages)
	}
	assistant := captured.Messages[1]
	blocks := assistant.Content
	if len(blocks) < 2 {
		t.Fatalf("assistant content blocks = %#v, want thinking + tool_use", blocks)
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking != "analyzing" || blocks[0].Signature != "sig-1" {
		t.Fatalf("first assistant block = %#v, want replayed thinking block", blocks[0])
	}
	if blocks[1].Type != "tool_use" || blocks[1].ID != "toolu_1" {
		t.Fatalf("second assistant block = %#v, want tool_use", blocks[1])
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
	if got := resp.Usage.InputTokens; got != 107 {
		t.Fatalf("InputTokens = %d, want 107", got)
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
	if got := resp.Usage.CacheWrite1hTokens; got != 13 {
		t.Fatalf("CacheWrite1hTokens = %d, want 13", got)
	}
}

func TestParseSSEStreamSkipsToolUseWithEmptyName(t *testing.T) {
	stream := strings.Join([]string{
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_empty\",\"name\":\"\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_read\",\"name\":\"read\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":1}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}",
		"",
		"event: message_stop",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	resp, err := parseSSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one valid call", resp.ToolCalls)
	}
	if resp.ToolCalls[0].ID != "toolu_read" || resp.ToolCalls[0].Name != "read" {
		t.Fatalf("tool call = %#v, want read", resp.ToolCalls[0])
	}
}

func TestParseSSEStreamDoesNotEmitStartOrDeltaForEmptyNameToolUse(t *testing.T) {
	stream := strings.Join([]string{
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_empty\",\"name\":\"\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_read\",\"name\":\"read\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":1}",
		"",
		"event: message_stop",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	var starts, deltas, ends []message.ToolCallDelta
	_, err := parseSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		switch delta.Type {
		case message.StreamDeltaToolUseStart:
			starts = append(starts, *delta.ToolCall)
		case message.StreamDeltaToolUseDelta:
			deltas = append(deltas, *delta.ToolCall)
		case message.StreamDeltaToolUseEnd:
			ends = append(ends, *delta.ToolCall)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	// The empty-name block must emit no start/delta/end; only the valid read tool does.
	if len(starts) != 1 || starts[0].Name != "read" {
		t.Fatalf("tool_use_start callbacks = %+v, want one read start", starts)
	}
	if len(ends) != 1 || ends[0].Name != "read" {
		t.Fatalf("tool_use_end callbacks = %+v, want one read end", ends)
	}
	for _, d := range deltas {
		if d.Name != "read" {
			t.Fatalf("tool_use_delta callback for non-read tool: %+v", d)
		}
	}
}

func TestParseSSEStreamDoesNotEmitToolEndForMalformedEOFToolUse(t *testing.T) {
	stream := strings.Join([]string{
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_empty\",\"name\":\"\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}",
		"",
	}, "\n")

	var toolEnds int
	_, _ = parseSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseEnd {
			toolEnds++
		}
	}, nil)
	if toolEnds != 0 {
		t.Fatalf("tool_use_end callbacks = %d, want 0", toolEnds)
	}
}

// TestParseSSEStreamAdoptsMessageDeltaInputUsage covers Anthropic-compatible
// gateways (e.g. ModelGate) that report input/cache usage only in message_delta
// and send input_tokens=0 in message_start.
func TestParseSSEStreamAdoptsMessageDeltaInputUsage(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}",
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
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":90,\"output_tokens\":27,\"cache_read_input_tokens\":768,\"cache_creation_input_tokens\":0}}",
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
	// InputTokens normalizes to input_tokens + cache_read_input_tokens.
	if got := resp.Usage.InputTokens; got != 858 {
		t.Fatalf("InputTokens = %d, want 858", got)
	}
	if got := resp.Usage.OutputTokens; got != 27 {
		t.Fatalf("OutputTokens = %d, want 27", got)
	}
	if got := resp.Usage.CacheReadTokens; got != 768 {
		t.Fatalf("CacheReadTokens = %d, want 768", got)
	}
}

func TestParseSSEStreamMessageDeltaZeroUsageDoesNotClobberStartUsage(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0,\"cache_read_input_tokens\":7,\"cache_creation\":{\"ephemeral_5m_input_tokens\":11,\"ephemeral_1h_input_tokens\":13}}}}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}",
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
	if got := resp.Usage.InputTokens; got != 107 {
		t.Fatalf("InputTokens = %d, want 107", got)
	}
	if got := resp.Usage.CacheReadTokens; got != 7 {
		t.Fatalf("CacheReadTokens = %d, want 7", got)
	}
	if got := resp.Usage.CacheWriteTokens; got != 24 {
		t.Fatalf("CacheWriteTokens = %d, want 24", got)
	}
	if got := resp.Usage.CacheWrite1hTokens; got != 13 {
		t.Fatalf("CacheWrite1hTokens = %d, want 13", got)
	}
}

func TestParseSSEStreamKeepsInterruptedTextWithoutMessageStop(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hello\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}",
		"",
	}, "\n")

	resp, err := parseSSEStream(strings.NewReader(stream), nil, nil)
	if err == nil {
		if resp == nil || resp.Content != "hello" || resp.StopReason != "interrupted" {
			t.Fatalf("resp = %#v, want interrupted partial text", resp)
		}
		return
	}
	t.Fatalf("parseSSEStream returned error: %v", err)
}

func TestParseSSEStreamInterruptedTextDropsPartialToolAndThinking(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"visible text\"}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"partial thought\"}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"Read\",\"input\":{}}}",
		"",
	}, "\n")

	var sawToolEnd, sawThinkingEnd bool
	resp, err := parseSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		switch delta.Type {
		case message.StreamDeltaToolUseEnd:
			sawToolEnd = true
		case message.StreamDeltaThinkingEnd:
			sawThinkingEnd = true
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseSSEStream returned error: %v", err)
	}
	if resp == nil || resp.Content != "visible text" || resp.StopReason != "interrupted" {
		t.Fatalf("resp = %#v, want interrupted partial text", resp)
	}
	if len(resp.ToolCalls) != 0 || len(resp.ThinkingBlocks) != 0 || resp.ReasoningContent != "" {
		t.Fatalf("unsafe partial context retained: tool_calls=%#v thinking=%#v reasoning=%q", resp.ToolCalls, resp.ThinkingBlocks, resp.ReasoningContent)
	}
	if sawToolEnd || sawThinkingEnd {
		t.Fatalf("unexpected completion callback for dropped partial blocks: tool_end=%v thinking_end=%v", sawToolEnd, sawThinkingEnd)
	}
}

func TestParseSSEStreamAllowsMaxTokensTruncatedContentWithoutMessageStop(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hello\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":10}}",
		"",
	}, "\n")

	resp, err := parseSSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if resp == nil || resp.Content != " world" || resp.StopReason != "max_tokens" {
		t.Fatalf("resp = %#v, want truncated content", resp)
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
	got1 := stableAnthropicMetadataUserIDPayload(provider)
	got2 := stableAnthropicMetadataUserIDPayload(provider)

	if got1 == "" {
		t.Fatal("expected stable metadata user id")
	}
	if got1 != got2 {
		t.Fatalf("expected stable metadata user id, got %q and %q", got1, got2)
	}
	// Always returns JSON format (implicitly enabled like preset: codex)
	if !strings.HasPrefix(got1, `{"device_id":"chord_`) {
		t.Fatalf("expected JSON format with chord_ device_id, got %q", got1)
	}
	if !strings.Contains(got1, `"session_id":"`) {
		t.Fatalf("expected session_id field, got %q", got1)
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

func TestParseSSEStreamParsesThinkingTokens(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"model\":\"claude\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0}}}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Let me think...\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":0}",
		"",
		"event: content_block_start",
		"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"",
		"event: content_block_delta",
		"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Answer\"}}",
		"",
		"event: content_block_stop",
		"data: {\"type\":\"content_block_stop\",\"index\":1}",
		"",
		"event: message_delta",
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":50,\"thinking_tokens\":377}}",
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
	if got := resp.Usage.OutputTokens; got != 50 {
		t.Fatalf("OutputTokens = %d, want 50", got)
	}
	if got := resp.Usage.ReasoningTokens; got != 377 {
		t.Fatalf("ReasoningTokens = %d, want 377", got)
	}
	if len(resp.ThinkingBlocks) != 1 {
		t.Fatalf("len(ThinkingBlocks) = %d, want 1", len(resp.ThinkingBlocks))
	}
	if resp.ThinkingBlocks[0].Thinking != "Let me think..." {
		t.Fatalf("ThinkingBlocks[0].Thinking = %q, want %q", resp.ThinkingBlocks[0].Thinking, "Let me think...")
	}
}
