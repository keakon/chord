package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/modelcompat"
)

func extraBodyWith(cfg openAIGoogleThinkingConfig) *openAIExtraBody {
	return &openAIExtraBody{Google: &openAIGoogleExtraBody{ThinkingConfig: &cfg}}
}

func TestResolveNativeThinkingDialect(t *testing.T) {
	cases := []struct {
		name     string
		modelID  string
		selector string
		want     nativeThinkingDialect
		wantErr  bool
	}{
		{name: "gemini model", modelID: "gemini-3.8-flash", want: nativeThinkingGemini},
		{name: "vertex gemini alias", modelID: "google/gemini-2.5-pro", want: nativeThinkingGemini},
		{name: "claude model", modelID: "claude-fable-5.1", want: nativeThinkingAnthropic},
		{name: "anthropic model", modelID: "anthropic/claude-opus-4.6", want: nativeThinkingAnthropic},
		{name: "deepseek model", modelID: "deepseek-v4.1-flash", want: nativeThinkingObject},
		{name: "glm model", modelID: "glm-5.2", want: nativeThinkingObject},
		{name: "kimi model", modelID: "kimi-k2.6", want: nativeThinkingObject},
		{name: "doubao model", modelID: "doubao-seed-1.8", want: nativeThinkingObject},
		{name: "qwen model", modelID: "qwen3.7-plus", want: nativeThinkingQwen},
		{name: "unknown model stays off", modelID: "my-gateway-model", want: nativeThinkingOff},
		{name: "selector off for a gemini model", modelID: "gemini-3.8-flash", selector: "off", want: nativeThinkingOff},
		{name: "selector forces a dialect for an alias", modelID: "my-gateway-model", selector: "gemini", want: nativeThinkingGemini},
		{name: "selector pins gemini 3 for an alias", modelID: "my-gateway-model", selector: "gemini-3", want: nativeThinkingGemini3},
		{name: "selector accepts a family name", modelID: "my-gateway-model", selector: "kimi", want: nativeThinkingObject},
		{name: "selector accepts auto", modelID: "deepseek-v4.1-flash", selector: "auto", want: nativeThinkingObject},
		{name: "selector wins over inference", modelID: "gemini-3.8-flash", selector: "qwen", want: nativeThinkingQwen},
		{name: "unknown selector fails", modelID: "gemini-3.8-flash", selector: "deepsek", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveNativeThinkingDialect(tc.modelID, tc.selector)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveNativeThinkingDialect(%q, %q) = %q, want error", tc.modelID, tc.selector, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveNativeThinkingDialect(%q, %q): %v", tc.modelID, tc.selector, err)
			}
			if got != tc.want {
				t.Fatalf("dialect = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGeminiThinkingExtraBody(t *testing.T) {
	budget := 8192
	zero := 0
	includeFalse := false
	cases := []struct {
		name string
		in   GeminiTuning
		want *openAIExtraBody
	}{
		{
			name: "empty tuning omits the block",
			in:   GeminiTuning{},
			want: nil,
		},
		{
			name: "level implies include_thoughts",
			in:   GeminiTuning{ThinkingLevel: "high"},
			want: extraBodyWith(openAIGoogleThinkingConfig{ThinkingLevel: "high", IncludeThoughts: new(true)}),
		},
		{
			name: "budget implies include_thoughts",
			in:   GeminiTuning{ThinkingBudget: &budget},
			want: extraBodyWith(openAIGoogleThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: new(true)}),
		},
		{
			name: "level wins over budget",
			in:   GeminiTuning{ThinkingBudget: &budget, ThinkingLevel: "low"},
			want: extraBodyWith(openAIGoogleThinkingConfig{ThinkingLevel: "low", IncludeThoughts: new(true)}),
		},
		{
			name: "zero budget disables thinking without include_thoughts",
			in:   GeminiTuning{ThinkingBudget: &zero},
			want: extraBodyWith(openAIGoogleThinkingConfig{ThinkingBudget: &zero}),
		},
		{
			name: "explicit include_thoughts false is preserved",
			in:   GeminiTuning{ThinkingLevel: "medium", IncludeThoughts: &includeFalse},
			want: extraBodyWith(openAIGoogleThinkingConfig{ThinkingLevel: "medium", IncludeThoughts: &includeFalse}),
		},
		{
			name: "include_thoughts alone is sent",
			in:   GeminiTuning{IncludeThoughts: new(true)},
			want: extraBodyWith(openAIGoogleThinkingConfig{IncludeThoughts: new(true)}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := geminiThinkingExtraBody(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extra body = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestApplyNativeThinkingShapes(t *testing.T) {
	cases := []struct {
		name    string
		dialect nativeThinkingDialect
		tuning  RequestTuning
		want    openAIRequest
	}{
		{
			name:    "off emits nothing",
			dialect: nativeThinkingOff,
			tuning:  RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}},
			want:    openAIRequest{},
		},
		{
			name:    "gemini emits the google block",
			dialect: nativeThinkingGemini,
			tuning:  RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}},
			want:    openAIRequest{ExtraBody: extraBodyWith(openAIGoogleThinkingConfig{ThinkingLevel: "high", IncludeThoughts: new(true)})},
		},
		{
			name:    "gemini 3 emits the google block",
			dialect: nativeThinkingGemini3,
			tuning:  RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}},
			want:    openAIRequest{ExtraBody: extraBodyWith(openAIGoogleThinkingConfig{ThinkingLevel: "high", IncludeThoughts: new(true)})},
		},
		{
			name:    "gemini without knobs emits nothing",
			dialect: nativeThinkingGemini,
			tuning:  RequestTuning{},
			want:    openAIRequest{},
		},
		{
			name:    "anthropic emits type and budget",
			dialect: nativeThinkingAnthropic,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 4096}},
			want:    openAIRequest{Thinking: &anthropicThinking{Type: "enabled", BudgetTokens: 4096}},
		},
		{
			name:    "anthropic budget alone means manual mode",
			dialect: nativeThinkingAnthropic,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingBudget: 4096}},
			want:    openAIRequest{Thinking: &anthropicThinking{Type: "enabled", BudgetTokens: 4096}},
		},
		{
			name:    "anthropic adaptive drops the budget",
			dialect: nativeThinkingAnthropic,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive", ThinkingBudget: 4096, ThinkingDisplay: "summarized"}},
			want:    openAIRequest{Thinking: &anthropicThinking{Type: "adaptive", Display: "summarized"}},
		},
		{
			name:    "native object maps adaptive to enabled",
			dialect: nativeThinkingObject,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "adaptive"}},
			want:    openAIRequest{Thinking: &anthropicThinking{Type: "enabled"}},
		},
		{
			name:    "native object keeps disabled",
			dialect: nativeThinkingObject,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "disabled"}},
			want:    openAIRequest{Thinking: &anthropicThinking{Type: "disabled"}},
		},
		{
			name:    "native object without modes emits nothing",
			dialect: nativeThinkingObject,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingEffort: "high"}},
			want:    openAIRequest{},
		},
		{
			name:    "qwen emits the boolean flag",
			dialect: nativeThinkingQwen,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingBudget: 2048}},
			want:    openAIRequest{EnableThinking: new(true)},
		},
		{
			name:    "qwen disabled emits false",
			dialect: nativeThinkingQwen,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "disabled"}},
			want:    openAIRequest{EnableThinking: new(false)},
		},
		{
			name:    "qwen without modes emits nothing",
			dialect: nativeThinkingQwen,
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingEffort: "high"}},
			want:    openAIRequest{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req openAIRequest
			applyNativeThinking(&req, tc.dialect, tc.tuning)
			if !reflect.DeepEqual(req, tc.want) {
				t.Fatalf("request thinking fields = %#v, want %#v", req, tc.want)
			}
		})
	}
}

// TestReplayCompatibleRequestTuningDisablesModelLevelThinking confirms a chat
// model that turns thinking on through the model-level block is treated as a
// thinking request for replay compatibility, not only when it sets
// reasoning.effort or a request override.
func TestReplayCompatibleRequestTuningDisablesModelLevelThinking(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{"deepseek-v4.1-flash": {
			Thinking: &config.ThinkingConfig{Type: "enabled"},
		}},
	}, []string{"key"})
	target := FallbackModel{ProviderConfig: cfg, ModelID: "deepseek-v4.1-flash"}
	tuning := tuningForPoolTarget(target)
	missing := []message.Message{{
		Role:       message.RoleAssistant,
		ToolCalls:  []message.ToolCall{{ID: "call_1", Name: "read", Args: []byte(`{}`)}},
		Provenance: &message.MessageProvenance{WireFamily: modelcompat.WireFamilyAnthropic},
	}}
	if got := replayCompatibleRequestTuning(tuning, missing, target); !got.DisableReasoning {
		t.Fatalf("tuning = %+v, want DisableReasoning", got)
	}
	missing[0].ReasoningContent = "portable reasoning"
	if got := replayCompatibleRequestTuning(tuning, missing, target); got.DisableReasoning {
		t.Fatalf("tuning with replayed reasoning = %+v, want reasoning kept", got)
	}
}

// TestOpenAIProvider_NativeThinkingBody confirms the Chat Completions body
// carries the dialect-specific thinking field inferred from the model name, that
// compat.chat_completions.native_thinking overrides or disables it, and that a
// replay degradation never ships the field.
func TestOpenAIProvider_NativeThinkingBody(t *testing.T) {
	deepseekTuning := RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 4096}}
	cases := []struct {
		name         string
		model        string
		compat       *config.ProviderCompatConfig
		tuning       RequestTuning
		wantKey      string
		wantThinking map[string]any
	}{
		{
			name:         "deepseek model emits the native object",
			model:        "deepseek-v4.1-flash",
			tuning:       deepseekTuning,
			wantKey:      "thinking",
			wantThinking: map[string]any{"type": "enabled"},
		},
		{
			name:    "claude model emits type and budget",
			model:   "claude-fable-5.1",
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled", ThinkingBudget: 4096}},
			wantKey: "thinking",
			wantThinking: map[string]any{
				"type":          "enabled",
				"budget_tokens": float64(4096),
			},
		},
		{
			name:    "gemini model emits the google block",
			model:   "gemini-3.8-flash",
			tuning:  RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}},
			wantKey: "extra_body",
		},
		{
			name:    "qwen model emits the boolean flag",
			model:   "qwen3.7-plus",
			tuning:  RequestTuning{Anthropic: AnthropicTuning{ThinkingType: "enabled"}},
			wantKey: "enable_thinking",
		},
		{
			name:    "unknown model family emits nothing",
			model:   "web-search-2",
			tuning:  deepseekTuning,
			wantKey: "",
		},
		{
			name:  "explicit selector forces a dialect",
			model: "gateway-alias-1",
			compat: &config.ProviderCompatConfig{
				ChatCompletions: &config.ChatCompletionsCompatConfig{NativeThinking: "gemini"},
			},
			tuning:  RequestTuning{Gemini: GeminiTuning{ThinkingBudget: new(2048)}},
			wantKey: "extra_body",
		},
		{
			name:  "explicit selector disables the conversion",
			model: "gemini-3.8-flash",
			compat: &config.ProviderCompatConfig{
				ChatCompletions: &config.ChatCompletionsCompatConfig{NativeThinking: "off"},
			},
			tuning:  RequestTuning{Gemini: GeminiTuning{ThinkingLevel: "high"}},
			wantKey: "",
		},
		{
			name:    "replay degradation strips the field",
			model:   "deepseek-v4.1-flash",
			tuning:  RequestTuning{DisableReasoning: true, Anthropic: AnthropicTuning{ThinkingType: "enabled"}},
			wantKey: "",
		},
		{
			name:    "no thinking knobs emit nothing",
			model:   "deepseek-v4.1-flash",
			tuning:  RequestTuning{},
			wantKey: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &gotBody)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			provider := NewProviderConfig("sample", config.ProviderConfig{
				Type:   config.ProviderTypeChatCompletions,
				APIURL: server.URL + "/v1/chat/completions",
				Compat: tc.compat,
				Models: map[string]config.ModelConfig{
					tc.model: {Limit: config.ModelLimit{Context: 1000000, Output: 64000}},
				},
			}, []string{"test-key"})
			o := &OpenAIProvider{provider: provider, client: server.Client(), responsesProvider: &ResponsesProvider{}}

			_, err := o.CompleteStream(
				context.Background(), "test-key", tc.model, "",
				[]message.Message{{Role: "user", Content: "hello"}},
				nil, 128, tc.tuning,
				func(message.StreamDelta) {},
			)
			if err != nil {
				t.Fatalf("CompleteStream: %v", err)
			}

			if tc.wantKey == "" {
				for _, key := range []string{"thinking", "enable_thinking", "extra_body"} {
					if got, ok := gotBody[key]; ok {
						t.Fatalf("%s should be omitted, got %#v", key, got)
					}
				}
				return
			}
			raw, ok := gotBody[tc.wantKey]
			if !ok {
				t.Fatalf("%s missing (body keys %v)", tc.wantKey, sortedKeys(gotBody))
			}
			if tc.wantThinking == nil {
				return
			}
			got, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s = %#v, want object", tc.wantKey, raw)
			}
			for key, want := range tc.wantThinking {
				if got[key] != want {
					t.Fatalf("%s.%s = %#v, want %#v", tc.wantKey, key, got[key], want)
				}
			}
			if tc.wantKey == "thinking" && len(got) != len(tc.wantThinking) {
				t.Fatalf("%s = %#v, want exactly %#v", tc.wantKey, got, tc.wantThinking)
			}
		})
	}
}
