package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestConvertMessagesToGemini(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Parts: []message.ContentPart{{Type: "text", Text: "hello"}, {Type: "image", MimeType: "image/png", Data: tinyPNG}}},
		{Role: "assistant", Content: "hi", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"BJ"}`)}}},
		{Role: "tool", ToolCallID: "call_1", Content: "sunny", Parts: []message.ContentPart{{Type: "text", Text: "sunny"}, {Type: "image", MimeType: "image/png", Data: tinyPNG, FileName: "weather.png"}}},
		{Role: "tool", ToolCallID: "call_2", Content: "fallback name"},
	}

	got := convertMessagesToGemini(msgs)
	if len(got) != 3 {
		t.Fatalf("convertMessagesToGemini() len = %d, want 3", len(got))
	}
	if got[0].Role != "user" || got[0].Parts[0].Text != "hello" {
		t.Fatalf("first message = %#v", got[0])
	}
	if got[0].Parts[1].InlineData == nil || got[0].Parts[1].InlineData.MimeType != "image/png" || got[0].Parts[1].InlineData.Data != tinyPNGBase64 {
		t.Fatalf("image part = %#v", got[0].Parts[1].InlineData)
	}
	if got[1].Role != "model" || got[1].Parts[0].Text != "hi" {
		t.Fatalf("assistant message = %#v", got[1])
	}
	fc := got[1].Parts[1].FunctionCall
	if fc == nil || fc.Name != "get_weather" || string(fc.Args) != `{"city":"BJ"}` {
		t.Fatalf("functionCall = %#v", fc)
	}
	if got[2].Role != "user" || len(got[2].Parts) != 2 {
		t.Fatalf("tool result message = %#v", got[2])
	}
	if fr := got[2].Parts[0].FunctionResponse; fr == nil || fr.Name != "get_weather" || fr.Response["result"] != "sunny" || len(fr.Parts) != 1 {
		t.Fatalf("first functionResponse = %#v", fr)
	} else if fr.Parts[0].InlineData == nil || fr.Parts[0].InlineData.MimeType != "image/png" || fr.Parts[0].InlineData.Data != tinyPNGBase64 || fr.Parts[0].InlineData.DisplayName != "weather.png" {
		t.Fatalf("first functionResponse parts = %#v", fr.Parts)
	}
	if fr := got[2].Parts[1].FunctionResponse; fr == nil || fr.Name != "call_2" || fr.Response["result"] != "fallback name" {
		t.Fatalf("second functionResponse = %#v", fr)
	}
}

func TestParseGeminiSSEStreamErrorEventAfterTextReturnsAPIError(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"partial "}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"text"}]}}]}`,
		`data: {"error":{"code":503,"message":"backend failed","status":"UNAVAILABLE"}}`,
		"",
	}, "\n\n")
	_, err := parseGeminiSSEStream(strings.NewReader(stream), nil, nil)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "503" {
		t.Fatalf("err = %T %v, want provider API error (partial stays on screen, no rollback)", err, err)
	}
}

func TestParseGeminiSSEStreamPreservesStatuslessErrorEnvelope(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"error":{"code":503,"message":"backend failed","status":"UNAVAILABLE"}}`,
		"",
	}, "\n")

	_, err := parseGeminiSSEStream(strings.NewReader(stream), nil, nil)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Origin != APIErrorOriginSSEEvent || apiErr.StatusCode != 0 || apiErr.Code != "503" || apiErr.Type != "UNAVAILABLE" {
		t.Fatalf("err = %T %v, want status-less Gemini SSE APIError", err, err)
	}
}

func TestConvertMessagesToGeminiMarksInterruptedAssistant(t *testing.T) {
	got := convertMessagesToGemini([]message.Message{{Role: "assistant", Content: "partial", StopReason: "interrupted"}})
	if len(got) != 1 || got[0].Role != "model" || len(got[0].Parts) != 1 {
		t.Fatalf("convertMessagesToGemini() = %#v", got)
	}
	text := got[0].Parts[0].Text
	if !strings.Contains(text, "partial") || !strings.Contains(text, "interrupted before completion") {
		t.Fatalf("interrupted assistant text = %q", text)
	}
}

func TestConvertMessagesToGeminiSkipsReasoningOnlyAssistant(t *testing.T) {
	got := convertMessagesToGemini([]message.Message{
		{Role: "user", Content: "before"},
		{Role: "assistant", ReasoningContent: "hidden"},
		{Role: "user", Content: "after"},
	})
	if len(got) != 1 {
		t.Fatalf("convertMessagesToGemini() len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Role != "user" || len(got[0].Parts) != 2 || got[0].Parts[0].Text != "before" || got[0].Parts[1].Text != "after" {
		t.Fatalf("unexpected merged user content after skip: %#v", got)
	}
}

func TestConvertToolsToGemini(t *testing.T) {
	tools := []message.ToolDefinition{{
		Name:        "search",
		Description: "search docs",
		InputSchema: map[string]any{
			"type":             "object",
			"nullable":         true,
			"coerceFromObject": true,
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "default": "x", "coerceFromString": true},
				"limit": map[string]any{"type": "integer"},
				"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []any{"query"},
		},
	}}

	got := convertToolsToGemini(tools)
	if len(got) != 1 || len(got[0].FunctionDeclarations) != 1 {
		t.Fatalf("convertToolsToGemini() = %#v", got)
	}
	params := got[0].FunctionDeclarations[0].Parameters
	if params["type"] != "OBJECT" {
		t.Fatalf("top-level type = %#v", params["type"])
	}
	if _, ok := params["nullable"]; ok {
		t.Fatalf("nullable should be omitted: %#v", params)
	}
	if _, ok := params["coerceFromObject"]; ok {
		t.Fatalf("coerceFromObject should be omitted: %#v", params)
	}
	props := params["properties"].(map[string]any)
	query := props["query"].(map[string]any)
	if query["type"] != "STRING" {
		t.Fatalf("query type = %#v", query["type"])
	}
	if _, ok := query["default"]; ok {
		t.Fatalf("default should be omitted: %#v", query)
	}
	if _, ok := query["coerceFromString"]; ok {
		t.Fatalf("coerceFromString should be omitted: %#v", query)
	}
	items := props["tags"].(map[string]any)["items"].(map[string]any)
	if items["type"] != "STRING" {
		t.Fatalf("array item type = %#v", items["type"])
	}
}

func TestParseGeminiSSEStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello "}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"world"},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"cachedContentTokenCount":4,"totalTokenCount":30,"thoughtsTokenCount":3}}`,
		``,
	}, "\n\n")

	var events []message.StreamDelta
	resp, err := parseGeminiSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		events = append(events, delta)
	}, nil)
	if err != nil {
		t.Fatalf("parseGeminiSSEStream() error = %v", err)
	}
	if resp.Content != "hello world" || resp.ReasoningContent != "thinking" || resp.StopReason != "STOP" {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 23 || resp.Usage.CacheReadTokens != 4 || resp.Usage.ReasoningTokens != 3 {
		t.Fatalf("usage = %#v", resp.Usage)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" || string(resp.ToolCalls[0].Args) != `{"q":"x"}` {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
	var sawThinkingEnd, sawText, sawToolEnd bool
	var streamedArgs strings.Builder
	for _, ev := range events {
		switch ev.Type {
		case message.StreamDeltaThinkingEnd:
			sawThinkingEnd = true
		case message.StreamDeltaText:
			sawText = true
		case message.StreamDeltaToolUseDelta:
			streamedArgs.WriteString(ev.ToolCall.Input)
		case message.StreamDeltaToolUseEnd:
			sawToolEnd = true
			if ev.ToolCall.Input != "" {
				t.Fatalf("tool_use_end Input = %q, want the args delivered as fragments only", ev.ToolCall.Input)
			}
		}
	}
	if got := streamedArgs.String(); got != `{"q":"x"}` {
		t.Fatalf("streamed argument fragments = %q, want the final args exactly once", got)
	}
	if !sawThinkingEnd || !sawText || !sawToolEnd {
		t.Fatalf("missing expected stream events: thinking_end=%v text=%v tool_end=%v events=%#v", sawThinkingEnd, sawText, sawToolEnd, events)
	}
}

func TestParseGeminiSSEStreamSkipsFunctionCallWithEmptyName(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"","args":{"q":"x"}}}]}}, {"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"y"}}}]}}, {"finishReason":"STOP"}]}`,
		``,
	}, "\n")

	var starts []message.ToolCallDelta
	resp, err := parseGeminiSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseStart && delta.ToolCall != nil {
			starts = append(starts, *delta.ToolCall)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseGeminiSSEStream() error = %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one valid call", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "lookup" || string(resp.ToolCalls[0].Args) != `{"q":"y"}` {
		t.Fatalf("tool call = %#v, want lookup", resp.ToolCalls[0])
	}
	if len(starts) != 1 || starts[0].Name != "lookup" {
		t.Fatalf("tool_use_start callbacks = %+v, want only lookup", starts)
	}
}

func TestParseGeminiSSEStreamInterruptedTextWithoutFinish(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"partial thought"},{"text":"visible text"},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}]}`,
		``,
	}, "\n")

	var sawToolEnd bool
	resp, err := parseGeminiSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseEnd {
			sawToolEnd = true
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseGeminiSSEStream() error = %v", err)
	}
	if resp == nil || resp.Content != "visible text" || resp.StopReason != "interrupted" {
		t.Fatalf("response = %#v, want interrupted partial text", resp)
	}
	if len(resp.ToolCalls) != 0 || resp.ReasoningContent != "" {
		t.Fatalf("unsafe partial context retained: tool_calls=%#v reasoning=%q", resp.ToolCalls, resp.ReasoningContent)
	}
	if sawToolEnd {
		t.Fatal("unexpected ToolUseEnd for interrupted partial Gemini tool call")
	}
}

func TestGeminiCompleteStreamEncodesToolChoice(t *testing.T) {
	var captured geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("unexpected Gemini request URL: %s", r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/"}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(
		context.Background(),
		"test-key",
		"gemini-test",
		"",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		128,
		RequestTuning{Gemini: GeminiTuning{ToolChoice: "required"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.ToolConfig == nil || captured.ToolConfig.FunctionCallingConfig == nil {
		t.Fatalf("toolConfig = %#v, want functionCallingConfig", captured.ToolConfig)
	}
	if got := captured.ToolConfig.FunctionCallingConfig.Mode; got != "ANY" {
		t.Fatalf("functionCallingConfig.mode = %q, want ANY", got)
	}
}

func TestGeminiCompleteStreamOmitsForcedToolChoiceWhenAutoOnly(t *testing.T) {
	var captured geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{
		Type:   config.ProviderTypeGenerateContent,
		APIURL: srv.URL + "/v1beta/",
		Models: map[string]config.ModelConfig{
			"gemini-test": {
				Compat: &config.ModelCompatConfig{
					ForcedToolChoice: &config.ForcedToolChoiceCompatConfig{AutoOnly: new(true)},
				},
			},
		},
	}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(
		context.Background(),
		"test-key",
		"gemini-test",
		"",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		128,
		RequestTuning{Gemini: GeminiTuning{ToolChoice: "required"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.ToolConfig != nil {
		t.Fatalf("toolConfig = %#v, want omitted when forced choice is disabled", captured.ToolConfig)
	}
}

func TestGeminiCompleteStreamSuppressesForcedToolChoiceInThinking(t *testing.T) {
	cases := []struct {
		name        string
		compat      *config.ForcedToolChoiceCompatConfig
		tuning      GeminiTuning
		wantToolCfg bool
	}{
		{
			name:        "thinking active without compat keeps ANY",
			tuning:      GeminiTuning{ToolChoice: "required", ThinkingLevel: "high"},
			wantToolCfg: true,
		},
		{
			name:        "suppress_in_thinking drops ANY while thinking",
			compat:      &config.ForcedToolChoiceCompatConfig{SuppressInThinking: new(true)},
			tuning:      GeminiTuning{ToolChoice: "required", ThinkingLevel: "high"},
			wantToolCfg: false,
		},
		{
			name:        "suppress_in_thinking keeps ANY when budget zero",
			compat:      &config.ForcedToolChoiceCompatConfig{SuppressInThinking: new(true)},
			tuning:      GeminiTuning{ToolChoice: "required", ThinkingBudget: new(0)},
			wantToolCfg: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured geminiRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
			}))
			defer srv.Close()

			provider := NewProviderConfig("gemini", config.ProviderConfig{
				Type:   config.ProviderTypeGenerateContent,
				APIURL: srv.URL + "/v1beta/",
				Models: map[string]config.ModelConfig{
					"gemini-test": {
						Compat: &config.ModelCompatConfig{ForcedToolChoice: tc.compat},
					},
				},
			}, []string{"test-key"})
			geminiProvider, err := NewGeminiProvider(provider, "")
			if err != nil {
				t.Fatalf("NewGeminiProvider: %v", err)
			}
			_, err = geminiProvider.CompleteStream(
				context.Background(),
				"test-key",
				"gemini-test",
				"",
				[]message.Message{{Role: "user", Content: "hello"}},
				[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
				128,
				RequestTuning{Gemini: tc.tuning},
				func(message.StreamDelta) {},
			)
			if err == nil {
				t.Fatal("expected forced server error")
			}
			if !tc.wantToolCfg {
				if captured.ToolConfig != nil {
					t.Fatalf("toolConfig = %#v, want omitted while thinking is active", captured.ToolConfig)
				}
				return
			}
			if captured.ToolConfig == nil || captured.ToolConfig.FunctionCallingConfig == nil {
				t.Fatalf("toolConfig = %#v, want functionCallingConfig", captured.ToolConfig)
			}
			if got := captured.ToolConfig.FunctionCallingConfig.Mode; got != "ANY" {
				t.Fatalf("functionCallingConfig.mode = %q, want ANY", got)
			}
		})
	}
}

func TestGeminiCompleteStreamOmitsForcedToolChoiceAfterAdaptiveMemo(t *testing.T) {
	var captured geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/"}, []string{"test-key"})
	provider.markForcedChoiceRefused("gemini-test")
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(
		context.Background(),
		"test-key",
		"gemini-test",
		"",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "done", Description: "Finish", InputSchema: map[string]any{"type": "object"}}},
		128,
		RequestTuning{Gemini: GeminiTuning{ToolChoice: "required"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.ToolConfig != nil {
		t.Fatalf("toolConfig = %#v, want omitted after the adaptive refusal memo", captured.ToolConfig)
	}
}

func TestGeminiCompleteStreamAppliesRequestOverrides(t *testing.T) {
	var gotBody map[string]any
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	custom := "custom"
	provider := NewProviderConfig("gemini", config.ProviderConfig{
		Type:   config.ProviderTypeGenerateContent,
		APIURL: srv.URL + "/v1beta/",
		Compat: &config.ProviderCompatConfig{RequestOverrides: &config.RequestOverridesConfig{
			Body: map[string]any{
				"generationConfig": map[string]any{"responseMimeType": "application/json"},
			},
			Headers: map[string]*string{"x-goog-api-key": nil, "x-custom": &custom},
		}},
	}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(context.Background(), "test-key", "gemini-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}

	generationConfig, ok := gotBody["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig = %#v, want object", gotBody["generationConfig"])
	}
	if got := generationConfig["maxOutputTokens"]; got != float64(128) {
		t.Fatalf("generationConfig.maxOutputTokens = %#v, want 128", got)
	}
	if got := generationConfig["responseMimeType"]; got != "application/json" {
		t.Fatalf("generationConfig.responseMimeType = %#v, want application/json", got)
	}
	if got := gotHeaders.Get("x-goog-api-key"); got != "" {
		t.Fatalf("x-goog-api-key = %q, want removed", got)
	}
	if got := gotHeaders.Get("x-custom"); got != "custom" {
		t.Fatalf("x-custom = %q, want custom", got)
	}
}

func TestGeminiCompleteStreamSetsDefaultUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/"}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(context.Background(), "test-key", "gemini-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != defaultLLMUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, defaultLLMUserAgent())
	}
}

func TestGeminiCompleteStreamSetsProviderUserAgent(t *testing.T) {
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/", UserAgent: "ProviderUA/1.0"}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(context.Background(), "test-key", "gemini-test", "", []message.Message{{Role: "user", Content: "hello"}}, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if gotUserAgent != "ProviderUA/1.0" {
		t.Fatalf("User-Agent = %q, want ProviderUA/1.0", gotUserAgent)
	}
}

func TestParseGeminiHTTPErrorFromBytes(t *testing.T) {
	header := http.Header{"Retry-After": []string{"2"}}
	err := parseGeminiHTTPErrorFromBytes(400, header, []byte(`{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`))
	if err.StatusCode != 400 || err.Message != "bad request" || err.Code != "INVALID_ARGUMENT" || err.Type != "INVALID_ARGUMENT" {
		t.Fatalf("error = %#v", err)
	}
	if err.RetryAfter == 0 {
		t.Fatalf("RetryAfter was not parsed")
	}
}

func TestGeminiStreamURL(t *testing.T) {
	got := geminiStreamURL("https://generativelanguage.googleapis.com/v1beta/", "/gemini-2.5-flash")
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	if got != want {
		t.Fatalf("geminiStreamURL() = %q, want %q", got, want)
	}
}

func TestGeminiStreamURLPreservesConfiguredQuery(t *testing.T) {
	got := geminiStreamURL("https://example.invalid/v1beta/?region=test", "test-model")
	want := "https://example.invalid/v1beta/models/test-model:streamGenerateContent?alt=sse&region=test"
	if got != want {
		t.Fatalf("geminiStreamURL() = %q, want %q", got, want)
	}
}

func TestValidateGeminiAPIURLIgnoresQuery(t *testing.T) {
	if err := validateGeminiAPIURL("https://example.invalid/v1beta/?region=test"); err != nil {
		t.Fatalf("validateGeminiAPIURL() error = %v", err)
	}
}

func TestGeminiCompleteStreamOmitsToolConfigWithoutTools(t *testing.T) {
	var captured geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/"}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(
		context.Background(),
		"test-key",
		"test-model",
		"",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{Gemini: GeminiTuning{ToolChoice: "required"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.ToolConfig != nil {
		t.Fatalf("toolConfig = %#v, want omitted without tools", captured.ToolConfig)
	}
}

func TestGeminiUserParts_MergesAdjacentTextParts(t *testing.T) {
	got := geminiUserParts(message.Message{Parts: []message.ContentPart{
		{Type: "text", Text: "Is directory a git repo: yes\n\nGit branch: main"},
		{Type: "text", Text: "fix the test"},
		{Type: "text", Text: "verify"},
	}})
	if len(got) != 1 {
		t.Fatalf("parts = %#v, want one merged text part", got)
	}
	want := "Is directory a git repo: yes\n\nGit branch: main\nfix the test\nverify"
	if got[0].Text != want {
		t.Fatalf("merged text = %q, want %q", got[0].Text, want)
	}
}

func TestGeminiUserParts_MergeKeepsImagePart(t *testing.T) {
	got := geminiUserParts(message.Message{Parts: []message.ContentPart{
		{Type: "text", Text: "before"},
		{Type: "text", Text: "and after"},
		{Type: "image", MimeType: "image/png", Data: tinyPNG},
		{Type: "text", Text: "see this"},
		{Type: "text", Text: "then fix"},
	}})
	if len(got) != 3 {
		t.Fatalf("parts = %d, want 3 (merged text + image + merged text)", len(got))
	}
	if got[0].Text != "before\nand after" {
		t.Fatalf("leading part = %#v", got[0])
	}
	if got[1].InlineData == nil || got[1].InlineData.MimeType != "image/png" {
		t.Fatalf("image part = %#v", got[1])
	}
	if got[2].Text != "see this\nthen fix" {
		t.Fatalf("trailing part = %#v", got[2])
	}
}

func TestGeminiUserParts_LabelsEmptyTextOnlyMessage(t *testing.T) {
	got := geminiUserParts(message.Message{Parts: []message.ContentPart{{Type: "text", Text: ""}, {Type: "text", Text: ""}}})
	if len(got) != 1 || got[0].Text != emptyWireTextPlaceholder || got[0].InlineData != nil {
		t.Fatalf("parts = %#v, want one labeled text part", got)
	}
}

func TestGeminiThinkingLevelDropsLegacyBudget(t *testing.T) {
	// Gemini answers 400 when a request carries both thinkingBudget and
	// thinkingLevel. Normalization is deliberately deferred to the single wire
	// point — normalizeGeminiThinking, called from the Gemini request body
	// construction — so the tuning builders and merge layers keep both knobs
	// exactly as configured and no future merge path can ship a level-bearing
	// request without passing through the collapse.
	budget := -1
	norm := normalizeGeminiThinking(GeminiTuning{ThinkingBudget: &budget, ThinkingLevel: "high"})
	if norm.ThinkingLevel != "high" {
		t.Fatalf("thinkingLevel = %q, want high", norm.ThinkingLevel)
	}
	if norm.ThinkingBudget != nil {
		t.Fatalf("thinkingBudget = %d, want nil when a level is set", *norm.ThinkingBudget)
	}

	// A budget-only config keeps the legacy knob untouched.
	only := normalizeGeminiThinking(GeminiTuning{ThinkingBudget: &budget})
	if only.ThinkingBudget == nil || *only.ThinkingBudget != -1 {
		t.Fatalf("thinkingBudget = %v, want -1 preserved", only.ThinkingBudget)
	}

	// The builders preserve config facts: a variant that only sets a level
	// overlays the level while the inherited budget survives until the wire
	// normalization collapses the pair.
	base := tuningFromModel(config.ModelConfig{Thinking: &config.ThinkingConfig{Budget: -1}}, "", nil, nil)
	if base.Gemini.ThinkingBudget == nil || *base.Gemini.ThinkingBudget != -1 {
		t.Fatalf("base thinkingBudget = %v, want -1", base.Gemini.ThinkingBudget)
	}
	merged := mergeVariantTuning(base, config.ModelVariant{Thinking: &config.ThinkingConfig{Level: "low"}})
	if merged.Gemini.ThinkingLevel != "low" {
		t.Fatalf("thinkingLevel = %q, want low", merged.Gemini.ThinkingLevel)
	}
	if merged.Gemini.ThinkingBudget == nil || *merged.Gemini.ThinkingBudget != -1 {
		t.Fatalf("thinkingBudget = %v, want -1 kept by the variant overlay", merged.Gemini.ThinkingBudget)
	}
	final := normalizeGeminiThinking(merged.Gemini)
	if final.ThinkingLevel != "low" || final.ThinkingBudget != nil {
		t.Fatalf("normalized = %+v, want level low and no budget", final)
	}
}

func TestGeminiCompleteStreamOmitsThinkingBudgetWhenLevelSet(t *testing.T) {
	var captured geminiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/"}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	budget := -1
	_, err = geminiProvider.CompleteStream(
		context.Background(),
		"test-key",
		"gemini-test",
		"",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil,
		128,
		RequestTuning{Gemini: GeminiTuning{ThinkingBudget: &budget, ThinkingLevel: "high"}},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if captured.GenerationConfig == nil || captured.GenerationConfig.ThinkingConfig == nil {
		t.Fatalf("thinkingConfig = %#v, want one", captured.GenerationConfig)
	}
	cfg := captured.GenerationConfig.ThinkingConfig
	if cfg.ThinkingLevel != "high" {
		t.Fatalf("thinkingLevel = %q, want high", cfg.ThinkingLevel)
	}
	if cfg.ThinkingBudget != nil {
		t.Fatalf("thinkingBudget = %d, want omitted alongside thinkingLevel", *cfg.ThinkingBudget)
	}
}

// TestGeminiActiveLoopSignatureContract covers the native Gemini wire: the
// documented placeholder repairs current-turn calls for native Gemini 3/4
// model IDs or an explicitly selected signature contract.
func TestGeminiActiveLoopSignatureContract(t *testing.T) {
	capture := func(t *testing.T, model, contract string) geminiRequest {
		t.Helper()
		var captured geminiRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
		}))
		t.Cleanup(srv.Close)

		modelCfg := config.ModelConfig{}
		if contract != "" {
			modelCfg.Compat = &config.ModelCompatConfig{ReasoningContinuity: &config.ReasoningContinuityCompatConfig{Contract: contract}}
		}
		provider := NewProviderConfig("gemini", config.ProviderConfig{
			Type:   config.ProviderTypeGenerateContent,
			APIURL: srv.URL + "/v1beta/",
			Models: map[string]config.ModelConfig{model: modelCfg},
		}, []string{"test-key"})
		geminiProvider, err := NewGeminiProvider(provider, "")
		if err != nil {
			t.Fatalf("NewGeminiProvider: %v", err)
		}
		_, err = geminiProvider.CompleteStream(
			context.Background(),
			"test-key",
			model,
			"",
			[]message.Message{
				{Role: message.RoleUser, Content: "hello"},
				{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "call-1", Name: "read", Args: json.RawMessage(`{}`)}}},
				{Role: message.RoleTool, ToolCallID: "call-1", Content: "ok"},
			},
			nil,
			128,
			RequestTuning{},
			func(message.StreamDelta) {},
		)
		if err == nil {
			t.Fatal("expected forced server error")
		}
		return captured
	}

	firstSignature := func(t *testing.T, req geminiRequest) string {
		t.Helper()
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if part.FunctionCall != nil {
					return part.ThoughtSignature
				}
			}
		}
		t.Fatal("request carries no function call")
		return ""
	}

	cases := []struct {
		name     string
		model    string
		contract string
		want     string
	}{
		{name: "Gemini 4 selects the signature contract", model: "gemini-4-argon", want: geminiSkipThoughtSignatureValidator},
		{name: "Gemini 4 resource ID selects the signature contract", model: "models/Gemini-4-Argon", want: geminiSkipThoughtSignatureValidator},
		{name: "explicit none opts Gemini 4 out", model: "gemini-4-argon", contract: config.ReasoningContractNone},
		{name: "gemini-3 model ID selects the contract", model: "gemini-3.8-flash", want: geminiSkipThoughtSignatureValidator},
		{name: "resource-form gemini-3 model ID selects the contract", model: "models/Gemini-3-pro-preview", want: geminiSkipThoughtSignatureValidator},
		{name: "explicit none opts a gemini-3 model out", model: "gemini-3.8-flash", contract: config.ReasoningContractNone},
		{name: "explicit gemini-3 opts in an alias", model: "deployment-a", contract: config.ReasoningContractGemini3, want: geminiSkipThoughtSignatureValidator},
		{name: "a contract for another endpoint keeps the model-ID inference", model: "gemini-3.8-flash", contract: config.ReasoningContractDeepSeek, want: geminiSkipThoughtSignatureValidator},
		{name: "earlier generation stays without the contract", model: "gemini-2.5-pro"},
		{name: "alias without a contract stays without it", model: "deployment-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstSignature(t, capture(t, tc.model, tc.contract)); got != tc.want {
				t.Fatalf("signature = %q, want %q", got, tc.want)
			}
		})
	}
}

var geminiSchemaTypes = map[string]bool{
	"STRING": true, "NUMBER": true, "INTEGER": true, "BOOLEAN": true,
	"ARRAY": true, "OBJECT": true, "NULL": true,
}

// assertGeminiSchemaValid walks a converted declaration and checks the
// constraints Gemini enforces beyond proto-JSON parsing. A keyword
// disappearing is not proof that what remains is a legal Schema: the previous
// regression test pinned anyOf branches carrying only "required", which strict
// backends reject. Every wire assertion should go through here.
func assertGeminiSchemaValid(t *testing.T, node map[string]any, path string) {
	t.Helper()
	for key := range node {
		if !geminiSchemaFields[key] {
			t.Errorf("%s: field %q is not on the Gemini Schema proto message", path, key)
		}
	}
	if typ, ok := node["type"]; ok {
		s, ok := typ.(string)
		if !ok || !geminiSchemaTypes[s] {
			t.Errorf("%s: type = %#v, want one proto enum value", path, typ)
		}
	} else if _, hasProps := node["properties"]; hasProps {
		t.Errorf("%s: node has properties but no type", path)
	} else if _, hasRequired := node["required"]; hasRequired {
		t.Errorf("%s: node has required but no type", path)
	} else if branches, _ := node["anyOf"].([]any); len(branches) == 0 {
		t.Errorf("%s: node has neither type nor a non-empty anyOf", path)
	}
	if _, ok := node["required"]; ok {
		if node["type"] != "OBJECT" {
			t.Errorf("%s: required on a non-OBJECT node (type=%v)", path, node["type"])
		}
		props, _ := node["properties"].(map[string]any)
		for _, name := range RequiredFields(node) {
			if _, ok := props[name]; !ok {
				t.Errorf("%s: required %q has no property on this node", path, name)
			}
		}
	}
	if enum, ok := node["enum"].([]any); ok {
		for i, item := range enum {
			if _, ok := item.(string); !ok {
				t.Errorf("%s: enum[%d] = %#v, want a string", path, i, item)
			}
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for name, raw := range props {
			child, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s/properties/%s: property is not a schema", path, name)
				continue
			}
			assertGeminiSchemaValid(t, child, path+"/properties/"+name)
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		assertGeminiSchemaValid(t, items, path+"/items")
	}
	if branches, ok := node["anyOf"].([]any); ok {
		for i, raw := range branches {
			child, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("%s/anyOf[%d]: branch is not a schema", path, i)
				continue
			}
			assertGeminiSchemaValid(t, child, fmt.Sprintf("%s/anyOf[%d]", path, i))
		}
	}
}

// captureGeminiToolParameters sends one tool declaration to a local server and
// returns the parameters that reached the wire.
func captureGeminiToolParameters(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/v1beta/"}, []string{"test-key"})
	geminiProvider, err := NewGeminiProvider(provider, "")
	if err != nil {
		t.Fatalf("NewGeminiProvider: %v", err)
	}
	_, err = geminiProvider.CompleteStream(
		context.Background(), "test-key", "gemini-test", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		[]message.ToolDefinition{{Name: "sample_tool", Description: "finish", InputSchema: schema}},
		128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if len(rawBody) == 0 {
		t.Fatal("server captured no request body")
	}
	var body struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Parameters map[string]any `json:"parameters"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(body.Tools) != 1 || len(body.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %#v", body.Tools)
	}
	return body.Tools[0].FunctionDeclarations[0].Parameters
}

// TestGeminiRequestDropsUnrepresentableSchemaKeywords covers the whole path a
// tool schema takes onto the wire. A "not" nested inside an anyOf branch must
// not survive conversion, and the branches around it must. The branches are
// complete Schemas here: pinning a required-only branch would make the test
// assert an invalid wire shape.
func TestGeminiRequestDropsUnrepresentableSchemaKeywords(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":     map[string]any{"type": "string"},
			"result_type": map[string]any{"type": "string"},
			"result":      map[string]any{"type": "object"},
		},
		"required": []string{"summary"},
		"not": map[string]any{
			"required": []string{"result", "result_type"},
		},
		"anyOf": []map[string]any{
			{
				"type":       "object",
				"properties": map[string]any{"summary": map[string]any{"type": "string"}},
				"required":   []string{"summary"},
				"not": map[string]any{
					"anyOf": []map[string]any{
						{"required": []string{"result_type"}},
						{"required": []string{"result"}},
					},
				},
			},
			{
				"type": "object",
				"properties": map[string]any{
					"summary":     map[string]any{"type": "string"},
					"result_type": map[string]any{"type": "string"},
				},
				"required": []string{"summary", "result_type"},
			},
		},
	}

	params := captureGeminiToolParameters(t, schema)
	if _, ok := params["not"]; ok {
		t.Fatalf("request body still carries a \"not\" keyword: %#v", params)
	}
	assertGeminiSchemaValid(t, params, "parameters")
	branches, ok := params["anyOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("anyOf = %#v, want two surviving branches", params["anyOf"])
	}
	for i, branch := range branches {
		if _, ok := branch.(map[string]any)["required"]; !ok {
			t.Fatalf("anyOf[%d] lost its required clause: %#v", i, branch)
		}
	}
}

// TestGeminiRequestNormalizesUnsupportedToolSchemas pins the shapes that
// strict Gemini backends reject: required-only combinator branches, orphan
// required names, keywords the Schema proto does not have, numeric enums,
// nullable type arrays, non-object properties, and tuple or boolean items
// (the proto's items is a single Schema message). MCP servers and hosted
// tools supply these verbatim.
func TestGeminiRequestNormalizesUnsupportedToolSchemas(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string"},
						"header":   map[string]any{"type": "string"},
					},
					"anyOf": []map[string]any{
						{"required": []string{"question", "header"}},
						{"required": []string{"wait_for"}},
					},
					"additionalProperties": false,
				},
			},
			"mode": map[string]any{
				"type": []any{"string", "null"},
				"enum": []any{1, true, "auto"},
			},
			"flag": true, // boolean schema, no proto-JSON representation
			"payload": map[string]any{
				"$ref": "#/$defs/Payload",
				"oneOf": []any{
					map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []string{"a"}},
					map[string]any{"type": "object", "properties": map[string]any{"b": map[string]any{"type": "string"}}, "required": []string{"b"}},
				},
				"allOf":         []any{map[string]any{"type": "object"}},
				"discriminator": map[string]any{"propertyName": "kind"},
			},
			"tuple_items": map[string]any{
				"items": []any{map[string]any{"type": "string"}},
			},
			"bool_items": map[string]any{"items": true},
			"bool_props": map[string]any{"properties": true},
			"empty":      map[string]any{},
		},
		"$defs":    map[string]any{"Payload": map[string]any{"type": "object"}},
		"required": []string{"questions", "missing_one"},
	}

	params := captureGeminiToolParameters(t, schema)
	assertGeminiSchemaValid(t, params, "parameters")
	if got := RequiredFields(params); len(got) != 1 || got[0] != "questions" {
		t.Fatalf("required = %#v, want only the defined property", params["required"])
	}
	props := params["properties"].(map[string]any)
	if _, ok := props["flag"]; ok {
		t.Fatal("boolean property schema survived conversion")
	}
	items := props["questions"].(map[string]any)["items"].(map[string]any)
	if _, ok := items["anyOf"]; ok {
		t.Fatalf("required-only anyOf branches survived: %#v", items["anyOf"])
	}
	mode := props["mode"].(map[string]any)
	if mode["type"] != "STRING" {
		t.Fatalf("mode.type = %#v, want STRING", mode["type"])
	}
	if enum, ok := mode["enum"].([]any); !ok || len(enum) != 3 {
		t.Fatalf("mode.enum = %#v, want three string values", mode["enum"])
	}
	payload := props["payload"].(map[string]any)
	if branches, ok := payload["anyOf"].([]any); !ok || len(branches) != 2 {
		t.Fatalf("payload.anyOf = %#v, want the lowered oneOf alternatives", payload["anyOf"])
	}
	// Tuple and boolean items are dropped, but the node still reads as an
	// array; a dropped or non-object properties keyword still reads as an
	// object; a wholly empty schema becomes an object rather than a type-less
	// node that fails the whole request.
	tupleItems := props["tuple_items"].(map[string]any)
	if tupleItems["type"] != "ARRAY" {
		t.Fatalf("tuple_items.type = %#v, want inferred ARRAY", tupleItems["type"])
	}
	if _, ok := tupleItems["items"]; ok {
		t.Fatalf("tuple items survived conversion: %#v", tupleItems["items"])
	}
	if got := props["bool_items"].(map[string]any)["type"]; got != "ARRAY" {
		t.Fatalf("bool_items.type = %#v, want inferred ARRAY", got)
	}
	if _, ok := props["bool_items"].(map[string]any)["items"]; ok {
		t.Fatal("boolean items survived conversion")
	}
	boolProps := props["bool_props"].(map[string]any)
	if boolProps["type"] != "OBJECT" {
		t.Fatalf("bool_props.type = %#v, want inferred OBJECT", boolProps["type"])
	}
	if _, ok := boolProps["properties"]; ok {
		t.Fatal("non-object properties survived conversion")
	}
	if got := props["empty"].(map[string]any)["type"]; got != "OBJECT" {
		t.Fatalf("empty schema type = %#v, want OBJECT", got)
	}
	for _, forbidden := range []string{"$ref", "$defs", "oneOf", "allOf", "discriminator", "additionalProperties"} {
		if bytes.Contains(mustMarshalGeminiSchema(t, params), []byte(`"`+forbidden+`"`)) {
			t.Errorf("wire still carries an unsupported keyword %q", forbidden)
		}
	}
}

func mustMarshalGeminiSchema(t *testing.T, schema map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	return data
}

// TestBuiltInQuestionSchemasProduceValidGeminiWire guards both question tool
// variants: the synchronous copy used by workers keeps only one required-only
// anyOf branch, which is just as invalid as the main agent's two branches.
func TestBuiltInQuestionSchemasProduceValidGeminiWire(t *testing.T) {
	async := tools.NewQuestionTool(nil)
	for _, tc := range []struct {
		name   string
		schema map[string]any
	}{
		{"async", async.Parameters()},
		{"synchronous", async.Synchronous().Parameters()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := mustDecodeGeminiSchema(t, convertSchemaToGemini(tc.schema))
			assertGeminiSchemaValid(t, wire, "parameters")
			items := wire["properties"].(map[string]any)["questions"].(map[string]any)["items"].(map[string]any)
			if _, ok := items["anyOf"]; ok {
				t.Fatalf("required-only anyOf branches survived: %#v", items["anyOf"])
			}
		})
	}
}

// stubSubAgentCreatorForGeminiWire satisfies DelegateTool's schema build,
// which lists the available agent types in the agent_type description.
type stubSubAgentCreatorForGeminiWire struct{}

func (stubSubAgentCreatorForGeminiWire) CreateSubAgent(context.Context, tools.SubAgentRequest) (tools.TaskHandle, error) {
	return tools.TaskHandle{}, nil
}

func (stubSubAgentCreatorForGeminiWire) AvailableSubAgents() []tools.AgentInfo { return nil }

// TestAllBuiltInToolSchemasProduceValidGeminiWire converts every built-in
// declaration — the local tools plus the built-in hosted catalog — and asserts
// the wire stays inside the Schema proto subset. Adding or reshaping a tool
// schema is covered here automatically: run the llm package tests instead of
// guessing which shapes a strict backend accepts. A schema that fails needs
// its lost constraints restated in descriptions and runtime validation, the
// way question.go restates its required pair.
func TestAllBuiltInToolSchemasProduceValidGeminiWire(t *testing.T) {
	schemas := map[string]map[string]any{}
	add := func(tool tools.Tool) {
		schemas[tools.NormalizeName(tool.Name())] = tool.Parameters()
	}
	add(tools.ApplyPatchTool{})
	add(tools.NewCancelTool(nil))
	add(tools.CompleteTool{})
	// compact_context ships two schema surfaces: the main-agent variant and the
	// reduced worker variant selected by the validator surface.
	main := tools.NewCompactContextTool(tools.CompactContextValidator{})
	add(main)
	worker := tools.NewCompactContextTool(tools.CompactContextValidator{WorkerSurface: true})
	schemas[tools.NormalizeName(worker.Name())+"/worker"] = worker.Parameters()
	add(tools.NewDelegateTool(&stubSubAgentCreatorForGeminiWire{}))
	add(tools.DeleteTool{})
	add(tools.DoneTool{})
	add(tools.NewEscalateTool(nil))
	add(&tools.GenerateImageTool{})
	add(tools.GlobTool{})
	add(tools.GrepTool{})
	add(tools.HandoffTool{})
	add(tools.JobKillTool{})
	add(tools.JobListTool{})
	add(tools.JobOutputTool{})
	add(tools.NewNotifyTool(nil, nil, true, true))
	add(tools.NewQuestionTool(nil))
	add(tools.PresentTool{})
	add(tools.ReadArtifactTool{})
	add(tools.ReadTool{})
	add(tools.NewShellTool(""))
	add(tools.NewSkillTool(nil))
	add(tools.NewTodoWriteTool(nil))
	add(tools.NewToolSearchTool(nil))
	add(tools.NewViewImageTool(nil))
	add(tools.NewWebFetchTool(config.WebFetchConfig{}, ""))
	add(tools.WriteTool{})
	add(tools.EditTool{})
	// Hosted declarations reach the same wire through their spec schemas.
	for name, spec := range tools.ResolveHostedToolCatalog(nil) {
		schemas[name] = tools.NewHostedTool(spec, nil).Parameters()
	}

	for name, schema := range schemas {
		t.Run(name, func(t *testing.T) {
			wire := mustDecodeGeminiSchema(t, convertSchemaToGemini(schema))
			assertGeminiSchemaValid(t, wire, name)
		})
	}
}

func mustDecodeGeminiSchema(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(mustMarshalGeminiSchema(t, schema), &wire); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return wire
}
