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
)

// sampleAnthropicDeclaration is an arbitrary raw declaration: the transport
// must place it verbatim without interpreting its fields.
const sampleAnthropicDeclaration = `{"type":"sample_tool_20250101","name":"sample_tool","max_uses":8,"allowed_domains":["example.invalid"]}`

// TestAnthropicHostedRequestBodyShape pins the sub-request wire shape and
// verifies hosted bodies bypass the body-reuse cache: the second (hosted) call
// reuses the first call's message slice and tuning-free identity, so a cache
// read would return the plain body without the hosted declaration.
func TestAnthropicHostedRequestBodyShape(t *testing.T) {
	var captures [][]byte
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		captures = append(captures, raw)
		headers = append(headers, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"forced test error"}}`))
	}))
	defer srv.Close()

	provider := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeMessages,
		APIURL: srv.URL,
		Compat: &config.ProviderCompatConfig{RequestOverrides: &config.RequestOverridesConfig{
			Headers: map[string]*string{"anthropic-beta": new("provider-override")},
		}},
	}, []string{"test-key"})
	impl, err := NewAnthropicProvider(provider, "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	messages := []message.Message{{Role: "user", Content: "find the sample docs"}}
	call := func(tuning RequestTuning) {
		t.Helper()
		if _, err := impl.CompleteStream(context.Background(), "test-key", "claude-sonnet", "search system", messages, nil, 1024, tuning, func(message.StreamDelta) {}); err == nil {
			t.Fatal("expected forced server error")
		}
	}
	call(RequestTuning{})
	call(RequestTuning{HostedTool: &HostedToolRequest{
		Name:        "sample_tool",
		Declaration: json.RawMessage(sampleAnthropicDeclaration),
		Force:       json.RawMessage(`{"type":"tool","name":"sample_tool"}`),
		Headers: map[string]string{
			"x-sample-hosted": "enabled",
			// Overrides the standard Anthropic beta header: hosted headers are applied
			// after the provider defaults.
			"anthropic-beta": "sample-beta-2025",
		},
	}})

	if len(captures) != 2 {
		t.Fatalf("captured %d bodies, want 2", len(captures))
	}
	var plain map[string]any
	if err := json.Unmarshal(captures[0], &plain); err != nil {
		t.Fatalf("unmarshal plain body: %v", err)
	}
	if _, ok := plain["tools"]; ok {
		t.Fatalf("plain request must not declare tools: %v", plain["tools"])
	}
	if got := headers[1].Get("x-sample-hosted"); got != "enabled" {
		t.Fatalf("hosted header = %q, want enabled", got)
	}
	if got := headers[1].Get("anthropic-beta"); got != "sample-beta-2025" {
		t.Fatalf("hosted header must override provider defaults, got %q", got)
	}
	if got := headers[0].Get("x-sample-hosted"); got != "" {
		t.Fatalf("plain request must not carry hosted headers: %q", got)
	}
	if got := headers[0].Get("anthropic-beta"); got == "sample-beta-2025" {
		t.Fatalf("plain request must keep the provider beta header, got %q", got)
	}

	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools      []map[string]any `json:"tools"`
		ToolChoice map[string]any   `json:"tool_choice"`
	}
	if err := json.Unmarshal(captures[1], &body); err != nil {
		t.Fatalf("unmarshal hosted body: %v", err)
	}
	if body.Model != "claude-sonnet" || !body.Stream {
		t.Fatalf("model/stream = %q/%v", body.Model, body.Stream)
	}
	if len(body.System) != 1 || body.System[0].Text != "search system" {
		t.Fatalf("system = %#v, want minimal search system prompt", body.System)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "find the sample docs" {
		t.Fatalf("messages = %#v, want the last user message as the query", body.Messages)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools = %#v, want one hosted declaration", body.Tools)
	}
	if body.Tools[0]["type"] != "sample_tool_20250101" || body.Tools[0]["name"] != "sample_tool" || body.Tools[0]["max_uses"] != float64(8) {
		t.Fatalf("hosted declaration must be placed verbatim: %#v", body.Tools[0])
	}
	allowed, _ := body.Tools[0]["allowed_domains"].([]any)
	if len(allowed) != 1 || allowed[0] != "example.invalid" {
		t.Fatalf("hosted declaration domains = %#v", body.Tools[0]["allowed_domains"])
	}
	if body.ToolChoice["type"] != "tool" || body.ToolChoice["name"] != "sample_tool" {
		t.Fatalf("tool_choice = %#v, want forced hosted tool", body.ToolChoice)
	}
}

// TestAnthropicHostedRequestHintOnlyOmitsToolChoice pins the degraded shape:
// the hosted tool stays declared, but nothing forces the call.
func TestAnthropicHostedRequestHintOnlyOmitsToolChoice(t *testing.T) {
	req, err := newAnthropicHostedRequest("claude-sonnet", "system", "sample query", &HostedToolRequest{
		Name:        "sample_tool",
		Declaration: json.RawMessage(sampleAnthropicDeclaration),
	}, 1024)
	if err != nil {
		t.Fatalf("newAnthropicHostedRequest: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal hosted request: %v", err)
	}
	if strings.Contains(string(raw), "tool_choice") {
		t.Fatalf("hint-only request still carries tool_choice: %s", raw)
	}
	if !strings.Contains(string(raw), "sample_tool_20250101") {
		t.Fatalf("hint-only request must keep the hosted declaration: %s", raw)
	}
}

func TestAnthropicHostedRequestRejectsMissingInputs(t *testing.T) {
	if _, err := newAnthropicHostedRequest("claude-sonnet", "system", "sample query", &HostedToolRequest{Name: "sample_tool"}, 1024); err == nil {
		t.Fatal("missing declaration must fail before any request is sent")
	}
	if _, err := newAnthropicHostedRequest("claude-sonnet", "system", "", &HostedToolRequest{Name: "sample_tool", Declaration: json.RawMessage(sampleAnthropicDeclaration)}, 1024); err == nil {
		t.Fatal("missing user message must fail before any request is sent")
	}
}

func TestParseSSEStreamCapturesHostedToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"sample_tool","input":{}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"golang generics\""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":",\"count\":2}"}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"sample_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"sample_result","url":"https://example.invalid/a","title":"Result A"},{"type":"sample_result","url":"https://example.invalid/b","title":"Result B"}]}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":1}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Search summary."}}`,
		"",
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":2}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25,"server_tool_use":{"web_search_requests":1}}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	var streamed []message.StreamDelta
	resp, err := parseSSEStream(strings.NewReader(stream), func(delta message.StreamDelta) {
		streamed = append(streamed, delta)
	}, nil, false)
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	for _, delta := range streamed {
		switch delta.Type {
		case message.StreamDeltaToolUseStart, message.StreamDeltaToolUseDelta, message.StreamDeltaToolUseEnd:
			t.Fatalf("hosted blocks must not stream as client tool calls: %#v", delta)
		}
	}
	if resp.Content != "Search summary." {
		t.Fatalf("content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("hosted blocks must not become client tool calls: %#v", resp.ToolCalls)
	}
	obs := resp.Hosted
	if obs == nil {
		t.Fatal("Hosted observation missing")
	}
	if len(obs.Calls) != 1 || obs.PendingCalls() != 0 || !obs.HasCompleteResult() {
		t.Fatalf("observation calls = %#v", obs.Calls)
	}
	call := obs.Calls[0]
	if call.ID != "srvtoolu_1" || call.Name != "sample_tool" || call.Kind != anthropicServerToolUseBlock {
		t.Fatalf("call identity = %#v", call)
	}
	if string(call.Input) != `{"query":"golang generics","count":2}` {
		t.Fatalf("accumulated input = %s", call.Input)
	}
	if !strings.Contains(string(call.Result), `"Result A"`) || !strings.Contains(string(call.Result), `"Result B"`) {
		t.Fatalf("result = %s", call.Result)
	}
	if call.Error != "" {
		t.Fatalf("error = %q, want none", call.Error)
	}
	if obs.Summary != "Search summary." {
		t.Fatalf("summary = %q", obs.Summary)
	}
	if obs.Usage == nil || obs.Usage != resp.Usage || obs.Usage.OutputTokens != 25 {
		t.Fatalf("usage = %#v, want assembled response usage", obs.Usage)
	}
}

func TestParseSSEStreamCapturesHostedToolErrorsAndPending(t *testing.T) {
	t.Run("error result", func(t *testing.T) {
		stream := strings.Join([]string{
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"sample_tool","input":{"query":"q"}}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"sample_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"sample_tool_result_error","error_code":"unavailable"}}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":1}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		resp, err := parseSSEStream(strings.NewReader(stream), nil, nil, false)
		if err != nil {
			t.Fatalf("parseSSEStream: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || obs.PendingCalls() != 0 || obs.HasCompleteResult() {
			t.Fatalf("observation = %#v", obs)
		}
		if len(obs.CallErrors()) != 1 || !strings.Contains(obs.CallErrors()[0], "unavailable") {
			t.Fatalf("errors = %#v, want the error object's code", obs.CallErrors())
		}
		if len(obs.Calls[0].Result) != 0 {
			t.Fatalf("failed call must not carry a result: %s", obs.Calls[0].Result)
		}
		// The start event's concrete input is preserved even without deltas.
		if string(obs.Calls[0].Input) != `{"query":"q"}` {
			t.Fatalf("input = %s", obs.Calls[0].Input)
		}
	})

	t.Run("zero hits", func(t *testing.T) {
		stream := strings.Join([]string{
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"sample_tool","input":{"query":"q"}}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"sample_tool_result","tool_use_id":"srvtoolu_1","content":[]}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":1}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		resp, err := parseSSEStream(strings.NewReader(stream), nil, nil, false)
		if err != nil {
			t.Fatalf("parseSSEStream: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || !obs.HasCompleteResult() || len(obs.CallErrors()) != 0 {
			t.Fatalf("observation = %#v, want completed call with an empty result", obs)
		}
		if string(obs.Calls[0].Result) != "[]" {
			t.Fatalf("result = %s, want the empty array", obs.Calls[0].Result)
		}
	})

	t.Run("tool use without result", func(t *testing.T) {
		stream := strings.Join([]string{
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"sample_tool","input":{"query":"q"}}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		resp, err := parseSSEStream(strings.NewReader(stream), nil, nil, false)
		if err != nil {
			t.Fatalf("parseSSEStream: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || obs.PendingCalls() != 1 || obs.HasCompleteResult() {
			t.Fatalf("observation = %#v, want one pending hosted call", obs)
		}
	})

	t.Run("unpaired result", func(t *testing.T) {
		stream := strings.Join([]string{
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"sample_tool_result","tool_use_id":"srvtoolu_9","content":[{"type":"sample_result","url":"https://example.invalid/a"}]}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		resp, err := parseSSEStream(strings.NewReader(stream), nil, nil, false)
		if err != nil {
			t.Fatalf("parseSSEStream: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || !obs.HasCompleteResult() {
			t.Fatalf("observation = %#v, want the unpaired result captured", obs)
		}
		call := obs.Calls[0]
		if call.ID != "srvtoolu_9" || call.Kind != "sample_tool_result" || call.Name != "" {
			t.Fatalf("unpaired call = %#v", call)
		}
	})

	t.Run("no hosted blocks", func(t *testing.T) {
		stream := strings.Join([]string{
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"answer"}}`,
			"",
			"event: content_block_stop",
			`data: {"type":"content_block_stop","index":0}`,
			"",
			"event: message_stop",
			`data: {"type":"message_stop"}`,
			"",
		}, "\n")
		resp, err := parseSSEStream(strings.NewReader(stream), nil, nil, false)
		if err != nil {
			t.Fatalf("parseSSEStream: %v", err)
		}
		if resp.Hosted != nil {
			t.Fatalf("Hosted = %#v, want nil without hosted blocks", resp.Hosted)
		}
	})
}
