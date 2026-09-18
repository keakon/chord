package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

// The Chat Completions wire has two thinking-state carriers: Gemini rides on
// the first function call of a step (extra_content.google.thought_signature),
// Claude on the message (thinking_blocks). The converter writes the carrier the
// resolved dialect expects and nothing for the other dialects.
func TestConvertMessagesToOpenAIWithOptions_ChatThinkingCarriers(t *testing.T) {
	messages := []message.Message{
		{Role: message.RoleUser, Content: "hello"},
		{
			Role: message.RoleAssistant,
			ToolCalls: []message.ToolCall{
				{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)},
				{ID: "call-2", Name: "write", Args: json.RawMessage(`{}`), ThoughtSignature: "sig-step"},
			},
			ThinkingBlocks: []message.ThinkingBlock{
				{Thinking: "plan", Signature: "sig-block"},
				{Thinking: "unsigned"},
				{Data: "redacted-payload"},
			},
		},
	}

	gemini := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, "", messages, openAIConvertOptions{chatNativeThinking: nativeThinkingGemini})
	if len(gemini) != 2 {
		t.Fatalf("converted messages = %#v, want 2", gemini)
	}
	first := gemini[1].ToolCalls[0]
	if first.ExtraContent == nil || first.ExtraContent.Google == nil || first.ExtraContent.Google.ThoughtSignature != "sig-step" {
		t.Fatalf("first tool call = %#v, want the step signature on the first call", first)
	}
	if len(gemini[1].ThinkingBlocks) != 0 {
		t.Fatalf("gemini carrier leaked thinking blocks: %#v", gemini[1].ThinkingBlocks)
	}

	claude := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, "", messages, openAIConvertOptions{chatNativeThinking: nativeThinkingAnthropic})
	blocks := claude[1].ThinkingBlocks
	if len(blocks) != 2 {
		t.Fatalf("thinking blocks = %#v, want the signed and redacted blocks", blocks)
	}
	if blocks[0].Type != "thinking" || blocks[0].Signature != "sig-block" || blocks[1].Type != "redacted_thinking" || blocks[1].Data != "redacted-payload" {
		t.Fatalf("thinking blocks = %#v, want the replayable blocks verbatim", blocks)
	}
	if claude[1].ToolCalls[0].ExtraContent != nil {
		t.Fatalf("claude carrier leaked a gemini signature: %#v", claude[1].ToolCalls[0].ExtraContent)
	}

	off := convertMessagesToOpenAIWithOptions("", modelcompat.WireFamilyOpenAIChat, "", messages, openAIConvertOptions{})
	if off[1].ToolCalls[0].ExtraContent != nil || len(off[1].ThinkingBlocks) != 0 {
		t.Fatalf("carriers emitted without a dialect: %#v", off[1])
	}
}

func TestOpenAIToolCallThoughtSignatureShapes(t *testing.T) {
	cases := []struct {
		name string
		call openAIToolCall
		want string
	}{
		{
			name: "extra content",
			call: openAIToolCall{ExtraContent: &openAIToolCallExtraContent{Google: &openAIGoogleThoughtSignature{ThoughtSignature: "sig"}}},
			want: "sig",
		},
		{name: "top level", call: openAIToolCall{ThoughtSignature: "sig"}, want: "sig"},
		{name: "provider specific fields", call: openAIToolCall{ProviderSpecificFields: &openAIProviderSpecificFields{ThoughtSignature: "sig"}}, want: "sig"},
		{name: "none", call: openAIToolCall{}, want: ""},
	}
	for _, tc := range cases {
		if got := openAIToolCallThoughtSignature(tc.call); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEnsureChatGeminiActiveLoopSignatures(t *testing.T) {
	cases := []struct {
		name    string
		current string
		want    string
	}{
		{name: "missing signature is filled", want: geminiSkipThoughtSignatureValidator},
		{name: "a real signature is kept", current: "sig-real", want: "sig-real"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			messages := []openAIMessage{
				{Role: "user", Content: "first"},
				{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "old", Type: "function"}}},
				{Role: "tool", Content: "ok", ToolCallID: "old"},
				{Role: "user", Content: "second"},
				{Role: "assistant", ToolCalls: []openAIToolCall{
					{ID: "call-1", Type: "function", ExtraContent: extraContentWith(tc.current)},
					{ID: "call-2", Type: "function"},
				}},
			}
			ensureChatGeminiActiveLoopSignatures(messages)
			if got := openAIToolCallThoughtSignature(messages[1].ToolCalls[0]); got != "" {
				t.Fatalf("completed turn signature = %q, want it untouched", got)
			}
			if got := openAIToolCallThoughtSignature(messages[4].ToolCalls[0]); got != tc.want {
				t.Fatalf("active step signature = %q, want %q", got, tc.want)
			}
			if got := openAIToolCallThoughtSignature(messages[4].ToolCalls[1]); got != "" {
				t.Fatalf("second call signature = %q, want the step carrier only on the first call", got)
			}
		})
	}
}

// The gate cannot rely on the model name alone: an aliased Gemini 3 is only
// identified by an explicitly pinned gemini dialect.
func TestChatGeminiRequiresSignaturePlaceholder(t *testing.T) {
	unset := &config.ChatCompletionsCompatConfig{}
	pinnedGemini := &config.ChatCompletionsCompatConfig{NativeThinking: "gemini"}
	cases := []struct {
		name    string
		model   string
		compat  *config.ChatCompletionsCompatConfig
		dialect nativeThinkingDialect
		want    bool
	}{
		{name: "gemini 3 by name", model: "gemini-3-pro", compat: unset, dialect: nativeThinkingGemini, want: true},
		{name: "gemini 2 keeps no placeholder", model: "gemini-2.5-pro", compat: unset, dialect: nativeThinkingGemini},
		{name: "aliased gemini 3 with a pinned dialect", model: "deployment-a", compat: pinnedGemini, dialect: nativeThinkingGemini, want: true},
		{name: "alias without a pin stays unnamed", model: "deployment-a", compat: unset, dialect: nativeThinkingGemini},
		{name: "non-gemini dialect", model: "deployment-a", compat: pinnedGemini, dialect: nativeThinkingAnthropic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatGeminiRequiresSignaturePlaceholder(tc.model, tc.compat, tc.dialect); got != tc.want {
				t.Fatalf("chatGeminiRequiresSignaturePlaceholder(%q, %+v, %q) = %v, want %v", tc.model, tc.compat, tc.dialect, got, tc.want)
			}
		})
	}
}

// An unset selector means auto, so it must not count as a pinned dialect: the
// family resolution would otherwise read a gpt-* gateway as an unknown backend.
func TestPinnedNativeThinkingDialectTreatsUnsetAsAuto(t *testing.T) {
	cases := []struct {
		name   string
		compat *config.ChatCompletionsCompatConfig
		want   bool
	}{
		{name: "no compat block", compat: nil},
		{name: "unset selector", compat: &config.ChatCompletionsCompatConfig{}},
		{name: "auto selector", compat: &config.ChatCompletionsCompatConfig{NativeThinking: "auto"}},
		{name: "pinned gemini", compat: &config.ChatCompletionsCompatConfig{NativeThinking: "gemini"}, want: true},
		{name: "pinned off", compat: &config.ChatCompletionsCompatConfig{NativeThinking: "off"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinnedNativeThinkingDialect(tc.compat); got != tc.want {
				t.Fatalf("pinnedNativeThinkingDialect(%+v) = %v, want %v", tc.compat, got, tc.want)
			}
		})
	}
}

func TestParseOpenAISSEStreamChatThinkingState(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read","arguments":"{}"},"extra_content":{"google":{"thought_signature":"sig-step"}}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call-2","type":"function","function":{"name":"write","arguments":"{}"},"provider_specific_fields":{"thought_signature":"sig-second"}}]}}]}`,
		`data: {"choices":[{"delta":{"thinking_blocks":[{"type":"thinking","thinking":"plan","signature":"sig-block"},{"type":"redacted_thinking","data":"payload"}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n")

	resp, err := parseOpenAISSEStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseOpenAISSEStream: %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want 2", resp.ToolCalls)
	}
	if got := resp.ToolCalls[0].ThoughtSignature; got != "sig-step" {
		t.Fatalf("first tool call signature = %q, want %q", got, "sig-step")
	}
	if got := resp.ToolCalls[1].ThoughtSignature; got != "sig-second" {
		t.Fatalf("second tool call signature = %q, want the provider_specific_fields mirror", got)
	}
	if len(resp.ThinkingBlocks) != 2 || resp.ThinkingBlocks[0].Signature != "sig-block" || resp.ThinkingBlocks[1].Data != "payload" {
		t.Fatalf("thinking blocks = %#v, want the carrier blocks verbatim", resp.ThinkingBlocks)
	}
}

// A Claude-backed chat endpoint replays its thinking through thinking_blocks, so
// a current-turn step that still carries replayable blocks keeps the reasoning
// controls; a step without them degrades as before.
func TestReplayCompatibleRequestTuningKeepsReplayableBlocks(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"claude-fable-5.1": {
			Thinking: &config.ThinkingConfig{Type: "enabled"},
		}},
	}, []string{"key"})
	target := FallbackModel{ProviderConfig: cfg, ModelID: "claude-fable-5.1"}
	tuning := tuningForPoolTarget(target)

	messages := []message.Message{{
		Role:           message.RoleAssistant,
		ToolCalls:      []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
		ThinkingBlocks: []message.ThinkingBlock{{Thinking: "plan", Signature: "sig"}},
	}}
	if got := replayCompatibleRequestTuning(tuning, messages, target); got.DisableReasoning {
		t.Fatalf("tuning = %+v, want the reasoning controls kept while blocks are replayable", got)
	}
	messages[0].ThinkingBlocks = []message.ThinkingBlock{{Thinking: "unsigned"}}
	messages[0].ReasoningContent = "visible reasoning without a signature"
	if got := replayCompatibleRequestTuning(tuning, messages, target); !got.DisableReasoning {
		t.Fatalf("tuning = %+v, want DisableReasoning without replayable blocks", got)
	}
}

func extraContentWith(signature string) *openAIToolCallExtraContent {
	if signature == "" {
		return nil
	}
	return &openAIToolCallExtraContent{Google: &openAIGoogleThoughtSignature{ThoughtSignature: signature}}
}

func captureOpenAIChatBody(t *testing.T, model string, compat *config.ProviderCompatConfig, tuning RequestTuning, messages []message.Message) map[string]any {
	t.Helper()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)

	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/chat/completions",
		Compat: compat,
		Models: map[string]config.ModelConfig{
			model: {Limit: config.ModelLimit{Context: 1000000, Output: 64000}},
		},
	}, []string{"test-key"})
	o := &OpenAIProvider{provider: provider, client: server.Client(), responsesProvider: &ResponsesProvider{}}
	if _, err := o.CompleteStream(context.Background(), "test-key", model, "", messages, nil, 128, tuning, func(message.StreamDelta) {}); err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	return body
}

func firstToolCallSignature(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) < 2 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	assistant, _ := msgs[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) == 0 {
		t.Fatalf("assistant message = %#v, want tool calls", assistant)
	}
	call, _ := calls[0].(map[string]any)
	extra, _ := call["extra_content"].(map[string]any)
	google, _ := extra["google"].(map[string]any)
	signature, _ := google["thought_signature"].(string)
	return signature
}

// TestOpenAIProvider_ChatThinkingStateBody confirms the request body carries the
// provider-bound thinking state the dialect expects, including the documented
// Gemini 3 placeholder for a missing signature, and that a degraded request
// ships no Claude blocks.
func TestOpenAIProvider_ChatThinkingStateBody(t *testing.T) {
	toolStep := func() []message.Message {
		return []message.Message{
			{Role: message.RoleUser, Content: "hello"},
			{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}}},
			{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
		}
	}

	t.Run("gemini keeps the captured signature", func(t *testing.T) {
		messages := toolStep()
		messages[1].ToolCalls[0].ThoughtSignature = "sig-real"
		body := captureOpenAIChatBody(t, "gemini-3.8-flash", nil, RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}}, messages)
		if got := firstToolCallSignature(t, body); got != "sig-real" {
			t.Fatalf("signature = %q, want the captured value replayed", got)
		}
	})

	t.Run("gemini 3 fills a missing signature", func(t *testing.T) {
		body := captureOpenAIChatBody(t, "gemini-3.8-flash", nil, RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}}, toolStep())
		if got := firstToolCallSignature(t, body); got != geminiSkipThoughtSignatureValidator {
			t.Fatalf("signature = %q, want the documented placeholder", got)
		}
	})

	t.Run("gemini 2 sends no placeholder", func(t *testing.T) {
		body := captureOpenAIChatBody(t, "gemini-2.5-flash", nil, RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}}, toolStep())
		if got := firstToolCallSignature(t, body); got != "" {
			t.Fatalf("signature = %q, want no placeholder before Gemini 3", got)
		}
	})

	t.Run("claude ships the thinking blocks", func(t *testing.T) {
		messages := toolStep()
		messages[1].ThinkingBlocks = []message.ThinkingBlock{{Thinking: "plan", Signature: "sig-block"}}
		body := captureOpenAIChatBody(t, "claude-fable-5.1", nil, RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 4096}}, messages)
		msgs, _ := body["messages"].([]any)
		assistant, _ := msgs[1].(map[string]any)
		blocks, ok := assistant["thinking_blocks"].([]any)
		if !ok || len(blocks) != 1 {
			t.Fatalf("thinking_blocks = %#v, want the signed block", assistant["thinking_blocks"])
		}
		block, _ := blocks[0].(map[string]any)
		if block["signature"] != "sig-block" || block["type"] != "thinking" {
			t.Fatalf("thinking block = %#v, want the Anthropic shape verbatim", block)
		}
	})

	t.Run("degraded claude request ships no thinking state", func(t *testing.T) {
		messages := toolStep()
		messages[1].ThinkingBlocks = []message.ThinkingBlock{{Thinking: "plan", Signature: "sig-block"}}
		tuning := RequestTuning{DisableReasoning: true, Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 4096}}
		body := captureOpenAIChatBody(t, "claude-fable-5.1", nil, tuning, messages)
		if _, ok := body["thinking"]; ok {
			t.Fatalf("thinking field present in a degraded request: %#v", body["thinking"])
		}
		msgs, _ := body["messages"].([]any)
		assistant, _ := msgs[1].(map[string]any)
		if blocks, ok := assistant["thinking_blocks"]; ok {
			t.Fatalf("thinking_blocks present in a degraded request: %#v", blocks)
		}
	})
}
