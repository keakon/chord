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

func TestConvertToolsToGemini(t *testing.T) {
	tools := []message.ToolDefinition{{
		Name:        "search",
		Description: "search docs",
		InputSchema: map[string]any{
			"type":     "object",
			"nullable": true,
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "default": "x"},
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
	props := params["properties"].(map[string]any)
	query := props["query"].(map[string]any)
	if query["type"] != "STRING" {
		t.Fatalf("query type = %#v", query["type"])
	}
	if _, ok := query["default"]; ok {
		t.Fatalf("default should be omitted: %#v", query)
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
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"world"},{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20,"totalTokenCount":30,"thoughtsTokenCount":3}}`,
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
	if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 20 || resp.Usage.ReasoningTokens != 3 {
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
