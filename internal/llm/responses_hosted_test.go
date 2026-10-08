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

// sampleResponsesDeclaration is an arbitrary raw declaration: the transport
// must place it verbatim without interpreting its fields.
const sampleResponsesDeclaration = `{"type":"sample_tool","sample_option":true,"filters":{"allowed_domains":["example.invalid"]}}`

// TestResponsesHostedRequestBodyShape pins the sub-request wire shape and
// verifies hosted bodies bypass the body-reuse cache: the hosted call reuses
// the plain call's message slice, so a cache read would return the plain body
// without the hosted declaration.
func TestResponsesHostedRequestBodyShape(t *testing.T) {
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
		Type:   config.ProviderTypeResponses,
		APIURL: srv.URL + "/responses",
	}, []string{"test-key"})
	impl, err := NewResponsesProvider(provider, "")
	if err != nil {
		t.Fatalf("NewResponsesProvider: %v", err)
	}

	messages := []message.Message{{Role: "user", Content: "find the sample docs"}}
	call := func(tuning RequestTuning) {
		t.Helper()
		if _, err := impl.CompleteStream(context.Background(), "test-key", "gpt-5.5", "search system", messages, nil, 1024, tuning, func(message.StreamDelta) {}); err == nil {
			t.Fatal("expected forced server error")
		}
	}
	call(RequestTuning{})
	call(RequestTuning{OpenAI: OpenAITuning{ServiceTier: "flex"}, HostedTool: &HostedToolRequest{
		Name:        "sample_tool",
		Declaration: json.RawMessage(sampleResponsesDeclaration),
		Force:       json.RawMessage(`"required"`),
		Include:     []string{"sample_tool_call.action.sources"},
		Headers:     map[string]string{"x-sample-hosted": "enabled"},
	}})

	if len(captures) != 2 {
		t.Fatalf("captured %d bodies, want 2", len(captures))
	}
	var plain map[string]any
	if err := json.Unmarshal(captures[0], &plain); err != nil {
		t.Fatalf("unmarshal plain body: %v", err)
	}
	if tools, _ := plain["tools"].([]any); len(tools) != 0 {
		t.Fatalf("plain request must not declare tools: %v", plain["tools"])
	}
	if got := headers[1].Get("x-sample-hosted"); got != "enabled" {
		t.Fatalf("hosted header = %q, want enabled", got)
	}
	if got := headers[0].Get("x-sample-hosted"); got != "" {
		t.Fatalf("plain request must not carry hosted headers: %q", got)
	}

	var body struct {
		Model        string   `json:"model"`
		Instructions *string  `json:"instructions"`
		Stream       bool     `json:"stream"`
		Store        *bool    `json:"store"`
		ToolChoice   string   `json:"tool_choice"`
		ServiceTier  string   `json:"service_tier"`
		Include      []string `json:"include"`
		Input        []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(captures[1], &body); err != nil {
		t.Fatalf("unmarshal hosted body: %v", err)
	}
	if body.Model != "gpt-5.5" || !body.Stream {
		t.Fatalf("model/stream = %q/%v", body.Model, body.Stream)
	}
	if body.Instructions == nil || *body.Instructions != "search system" {
		t.Fatalf("instructions = %v", body.Instructions)
	}
	if len(body.Input) != 1 || body.Input[0].Type != "message" || body.Input[0].Role != "user" ||
		len(body.Input[0].Content) != 1 || body.Input[0].Content[0].Type != "input_text" || body.Input[0].Content[0].Text != "find the sample docs" {
		t.Fatalf("input = %#v, want the last user message as the query", body.Input)
	}
	if body.ToolChoice != "required" {
		t.Fatalf("tool_choice = %q, want required", body.ToolChoice)
	}
	if body.ServiceTier != "flex" {
		t.Fatalf("service_tier = %q, want flex", body.ServiceTier)
	}
	if len(body.Include) != 1 || body.Include[0] != "sample_tool_call.action.sources" {
		t.Fatalf("include = %#v", body.Include)
	}
	if len(body.Tools) != 1 {
		t.Fatalf("tools = %#v, want one hosted declaration", body.Tools)
	}
	if body.Tools[0]["type"] != "sample_tool" || body.Tools[0]["sample_option"] != true {
		t.Fatalf("hosted declaration must be placed verbatim: %#v", body.Tools[0])
	}
	filters, _ := body.Tools[0]["filters"].(map[string]any)
	allowed, _ := filters["allowed_domains"].([]any)
	if len(allowed) != 1 || allowed[0] != "example.invalid" {
		t.Fatalf("filters = %#v", body.Tools[0]["filters"])
	}
	for _, forbidden := range []string{"name", "description", "parameters"} {
		if _, ok := body.Tools[0][forbidden]; ok {
			t.Fatalf("hosted tool must not carry %q: %#v", forbidden, body.Tools[0])
		}
	}
}

// TestResponsesHostedRequestHintOnlyOmitsToolChoice pins the degraded shape:
// the hosted tool stays declared, but nothing forces the call.
func TestResponsesHostedRequestHintOnlyOmitsToolChoice(t *testing.T) {
	req, err := newResponsesHostedRequest("gpt-5", "system", "sample query", &HostedToolRequest{
		Name:        "sample_tool",
		Declaration: json.RawMessage(sampleResponsesDeclaration),
	}, 1024, true, nil)
	if err != nil {
		t.Fatalf("newResponsesHostedRequest: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal hosted request: %v", err)
	}
	if strings.Contains(string(raw), "tool_choice") {
		t.Fatalf("hint-only request still carries tool_choice: %s", raw)
	}
	if !strings.Contains(string(raw), "sample_tool") {
		t.Fatalf("hint-only request must keep the hosted declaration: %s", raw)
	}
}

func TestResponsesHostedRequestRejectsMissingInputs(t *testing.T) {
	if _, err := newResponsesHostedRequest("gpt-5", "system", "sample query", &HostedToolRequest{Name: "sample_tool"}, 1024, true, nil); err == nil {
		t.Fatal("missing declaration must fail before any request is sent")
	}
	if _, err := newResponsesHostedRequest("gpt-5", "system", "", &HostedToolRequest{Name: "sample_tool", Declaration: json.RawMessage(sampleResponsesDeclaration)}, 1024, true, nil); err == nil {
		t.Fatal("missing user message must fail before any request is sent")
	}
}

func TestParseResponsesSSECapturesHostedToolCall(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"sample_tool_call","id":"call_1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"sample_tool_call","id":"call_1","status":"completed","action":{"type":"search","query":"golang generics","sources":[{"type":"url","url":"https://example.invalid/a","title":"Result A"},{"type":"url","url":"https://example.invalid/b","title":"Result B"}]}}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"sample_tool_call","id":"call_1","status":"completed","action":{"type":"search","query":"golang generics","sources":[{"type":"url","url":"https://example.invalid/a","title":"Result A"},{"type":"url","url":"https://example.invalid/b","title":"Result B"}]}},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Search summary."}]}]}}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "Search summary." {
		t.Fatalf("content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("hosted calls must not become client tool calls: %#v", resp.ToolCalls)
	}
	obs := resp.Hosted
	if obs == nil {
		t.Fatal("Hosted observation missing")
	}
	if len(obs.Calls) != 1 || obs.PendingCalls() != 0 || !obs.HasCompleteResult() {
		t.Fatalf("observation calls = %#v", obs.Calls)
	}
	call := obs.Calls[0]
	if call.ID != "call_1" || call.Name != "sample_tool" || call.Kind != "sample_tool_call" || call.Status != "completed" {
		t.Fatalf("call identity = %#v", call)
	}
	if !strings.Contains(string(call.Input), `"query":"golang generics"`) {
		t.Fatalf("call input = %s", call.Input)
	}
	if !strings.Contains(string(call.Result), `"Result A"`) || !strings.Contains(string(call.Result), `"Result B"`) {
		t.Fatalf("call result = %s", call.Result)
	}
	if obs.Summary != "Search summary." {
		t.Fatalf("summary = %q", obs.Summary)
	}
}

func TestParseResponsesSSECapturesHostedToolFailureAndPending(t *testing.T) {
	t.Run("failed call", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"sample_tool_call","id":"call_2","status":"in_progress"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"sample_tool_call","id":"call_2","status":"failed"}}`,
			`{"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[{"type":"sample_tool_call","id":"call_2","status":"failed"}]}}`,
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || obs.PendingCalls() != 0 || obs.HasCompleteResult() {
			t.Fatalf("observation = %#v", obs)
		}
		if errs := obs.CallErrors(); len(errs) != 1 || errs[0] != "sample_tool_call: failed" {
			t.Fatalf("errors = %#v, want the failed item status", errs)
		}
	})

	t.Run("pending call", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"sample_tool_call","id":"call_3","status":"in_progress"}}`,
			"[DONE]",
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || obs.PendingCalls() != 1 || obs.HasCompleteResult() {
			t.Fatalf("observation = %#v, want one pending hosted call", obs)
		}
	})

	t.Run("terminal payload only", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"response.completed","response":{"id":"resp_4","status":"completed","output":[{"type":"sample_tool_call","id":"call_4","status":"completed","action":{"type":"search","query":"q"}}]}}`,
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, true, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		obs := resp.Hosted
		if obs == nil || len(obs.Calls) != 1 || !obs.HasCompleteResult() {
			t.Fatalf("observation = %#v, want the terminal payload captured", obs)
		}
		if string(obs.Calls[0].Result) == "" || obs.Calls[0].Status != "completed" {
			t.Fatalf("call = %#v", obs.Calls[0])
		}
	})

	t.Run("client calls and capture disabled", func(t *testing.T) {
		events := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{}"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"resp_5","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"sample_tool","arguments":"{}"}]}}`,
		}
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), nil, nil, nil, "", false, true, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		if resp.Hosted == nil || len(resp.Hosted.Calls) != 0 || len(resp.Hosted.Items) != 1 {
			t.Fatalf("client function calls must remain native items without hosted calls: %#v", resp.Hosted)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("client function call must stay a client tool call: %#v", resp.ToolCalls)
		}

		hostedEvents := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"sample_tool_call","id":"call_6","status":"in_progress"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"sample_tool_call","id":"call_6","status":"completed"}}`,
			`{"type":"response.completed","response":{"id":"resp_6","status":"completed","output":[{"type":"sample_tool_call","id":"call_6","status":"completed"}]}}`,
		}
		resp, _, err = parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(hostedEvents), nil, nil, nil, "", false, false, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		if resp.Hosted != nil {
			t.Fatalf("capture-disabled parse must not build an observation: %#v", resp.Hosted)
		}
	})
}
