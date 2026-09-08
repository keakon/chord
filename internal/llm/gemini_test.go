package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestConvertMessagesToGemini(t *testing.T) {
	msgs := []message.Message{
		{Role: "user", Parts: []message.ContentPart{{Type: "text", Text: "hello"}, {Type: "image", MimeType: "image/png", Data: []byte("png")}}},
		{Role: "assistant", Content: "hi", ToolCalls: []message.ToolCall{{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"BJ"}`)}}},
		{Role: "tool", ToolCallID: "call_1", Content: "sunny", Parts: []message.ContentPart{{Type: "text", Text: "sunny"}, {Type: "image", MimeType: "image/png", Data: []byte("png"), FileName: "weather.png"}}},
		{Role: "tool", ToolCallID: "call_2", Content: "fallback name"},
	}

	got := convertMessagesToGemini(msgs)
	if len(got) != 3 {
		t.Fatalf("convertMessagesToGemini() len = %d, want 3", len(got))
	}
	if got[0].Role != "user" || got[0].Parts[0].Text != "hello" {
		t.Fatalf("first message = %#v", got[0])
	}
	if got[0].Parts[1].InlineData == nil || got[0].Parts[1].InlineData.MimeType != "image/png" || got[0].Parts[1].InlineData.Data != "cG5n" {
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
	} else if fr.Parts[0].InlineData == nil || fr.Parts[0].InlineData.MimeType != "image/png" || fr.Parts[0].InlineData.Data != "cG5n" || fr.Parts[0].InlineData.DisplayName != "weather.png" {
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
	}, "\n")
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
	}, "\n")

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
	for _, ev := range events {
		switch ev.Type {
		case message.StreamDeltaThinkingEnd:
			sawThinkingEnd = true
		case message.StreamDeltaText:
			sawText = true
		case message.StreamDeltaToolUseEnd:
			sawToolEnd = true
		}
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
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/models"}, []string{"test-key"})
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
		APIURL: srv.URL + "/models",
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

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/models"}, []string{"test-key"})
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

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/models", UserAgent: "ProviderUA/1.0"}, []string{"test-key"})
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
	got := geminiStreamURL("https://generativelanguage.googleapis.com/v1beta/models/", "/gemini-2.5-flash")
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	if got != want {
		t.Fatalf("geminiStreamURL() = %q, want %q", got, want)
	}
}

func TestGeminiStreamURLPreservesConfiguredQuery(t *testing.T) {
	got := geminiStreamURL("https://example.invalid/v1beta/models?region=test", "test-model")
	want := "https://example.invalid/v1beta/models/test-model:streamGenerateContent?alt=sse&region=test"
	if got != want {
		t.Fatalf("geminiStreamURL() = %q, want %q", got, want)
	}
}

func TestValidateGeminiAPIURLIgnoresQuery(t *testing.T) {
	if err := validateGeminiAPIURL("https://example.invalid/v1beta/models?region=test"); err != nil {
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

	provider := NewProviderConfig("sample", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/models"}, []string{"test-key"})
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
		{Type: "image", MimeType: "image/png", Data: []byte("png")},
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

func TestGeminiUserParts_AddsEmptyTextPartForEmptyTextOnlyMessage(t *testing.T) {
	got := geminiUserParts(message.Message{Parts: []message.ContentPart{{Type: "text", Text: ""}, {Type: "text", Text: ""}}})
	if len(got) != 1 || got[0].Text != "" || got[0].InlineData != nil {
		t.Fatalf("parts = %#v, want one empty text part", got)
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

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/models"}, []string{"test-key"})
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

// TestGeminiRequestDropsUnrepresentableSchemaKeywords covers the whole path a
// tool schema takes onto the wire. Gemini parses the request as proto-JSON,
// where a field its Schema message does not have fails the entire request, so
// a "not" nested inside an anyOf branch — the shape the completion and notify
// tools use — must not survive conversion, and the branches around it must.
func TestGeminiRequestDropsUnrepresentableSchemaKeywords(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"forced","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

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
				"required": []string{"summary"},
				"not": map[string]any{
					"anyOf": []map[string]any{
						{"required": []string{"result_type"}},
						{"required": []string{"result"}},
					},
				},
			},
			{"required": []string{"summary", "result_type"}},
		},
	}

	provider := NewProviderConfig("gemini", config.ProviderConfig{Type: config.ProviderTypeGenerateContent, APIURL: srv.URL + "/models"}, []string{"test-key"})
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
		[]message.ToolDefinition{{Name: "task_complete", Description: "finish", InputSchema: schema}},
		128,
		RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected forced server error")
	}
	if len(rawBody) == 0 {
		t.Fatal("server captured no request body")
	}
	if bytes.Contains(rawBody, []byte(`"not"`)) {
		t.Fatalf("request body still carries a \"not\" keyword: %s", rawBody)
	}
	// The surviving alternatives must still be there: dropping the keyword must
	// not take the branch that carried it with it.
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
	params := body.Tools[0].FunctionDeclarations[0].Parameters
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
