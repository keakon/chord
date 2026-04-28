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
	"sync"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestConvertMessagesToResponses(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt string
		messages     []message.Message
		wantLen      int
		wantFirst    string // expected type of first item
	}{
		{
			name:         "system prompt only",
			systemPrompt: "You are a helpful assistant",
			messages:     []message.Message{},
			wantLen:      1,
			wantFirst:    "message",
		},
		{
			name:         "user message",
			systemPrompt: "You are helpful",
			messages: []message.Message{
				{Role: "user", Content: "Hello"},
			},
			wantLen:   2,
			wantFirst: "message",
		},
		{
			name:         "assistant with tool call",
			systemPrompt: "You are helpful",
			messages: []message.Message{
				{Role: "user", Content: "Read file"},
				{
					Role:    "assistant",
					Content: "I'll read the file",
					ToolCalls: []message.ToolCall{
						{ID: "call_1", Name: "Read", Args: json.RawMessage(`{"path":"test.go"}`)},
					},
				},
			},
			wantLen: 4, // system + user + assistant text + function_call
		},
		{
			name:         "tool result",
			systemPrompt: "You are helpful",
			messages: []message.Message{
				{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
			},
			wantLen: 2, // system + function_call_output
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertMessagesToResponses(tt.systemPrompt, tt.messages)
			if len(items) != tt.wantLen {
				t.Errorf("convertMessagesToResponses() returned %d items, want %d", len(items), tt.wantLen)
			}
			if tt.wantFirst != "" && len(items) > 0 {
				if items[0].Type != tt.wantFirst {
					t.Errorf("first item type = %q, want %q", items[0].Type, tt.wantFirst)
				}
			}
		})
	}
}

func TestConvertMessagesToResponses_WithImageParts(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{{
		Role: "user",
		Parts: []message.ContentPart{
			{Type: "text", Text: "what is in this image?"},
			{Type: "image", MimeType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		},
	}})
	if len(items) != 1 {
		t.Fatalf("convertMessagesToResponses() returned %d items, want 1", len(items))
	}
	if items[0].Type != "message" || items[0].Role != "user" {
		t.Fatalf("first item = %#v, want user message", items[0])
	}
	blocks, ok := items[0].Content.([]responsesContentBlock)
	if !ok {
		t.Fatalf("content type = %T, want []responsesContentBlock", items[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "input_text" || blocks[0].Text != "what is in this image?" {
		t.Fatalf("text block = %#v", blocks[0])
	}
	if blocks[1].Type != "input_image" {
		t.Fatalf("image block type = %q, want input_image", blocks[1].Type)
	}
	if !strings.HasPrefix(blocks[1].ImageURL, "data:image/png;base64,") {
		t.Fatalf("image block URL = %q, want data URL prefix", blocks[1].ImageURL)
	}
	if blocks[1].Detail != "auto" {
		t.Fatalf("image block detail = %q, want auto", blocks[1].Detail)
	}
}

func TestConvertToolsToResponses(t *testing.T) {
	tools := []message.ToolDefinition{
		{
			Name:        "Read",
			Description: "Read a file",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
		},
	}

	items := convertToolsToResponses(tools)
	if len(items) != 1 {
		t.Fatalf("convertToolsToResponses() returned %d items, want 1", len(items))
	}
	if items[0].Type != "function" {
		t.Errorf("tool type = %q, want %q", items[0].Type, "function")
	}
	if items[0].Name != "Read" {
		t.Errorf("tool name = %q, want %q", items[0].Name, "Read")
	}
}

func TestApplyResponsesCompletionPayload(t *testing.T) {
	t.Run("tool_calls_stop_reason_and_usage", func(t *testing.T) {
		var resp message.Response
		truncated := false
		applyResponsesCompletionPayload(&resp, responsesCompletedPayload{
			ID:     "resp-1",
			Output: []responsesOutputEntry{{Type: "function_call", ID: "fc-1", CallID: "call-1", Name: "Bash", Arguments: `{"command":"pwd"}`}},
			Usage: &responsesUsagePayload{
				InputTokens:  10,
				OutputTokens: 20,
				InputTokensDetails: &struct {
					CachedTokens int `json:"cached_tokens"`
				}{CachedTokens: 3},
				OutputTokensDetails: &struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				}{ReasoningTokens: 5},
			},
		}, &truncated)
		if resp.ProviderResponseID != "resp-1" {
			t.Fatalf("ProviderResponseID = %q, want resp-1", resp.ProviderResponseID)
		}
		if resp.StopReason != "tool_calls" {
			t.Fatalf("StopReason = %q, want tool_calls", resp.StopReason)
		}
		if truncated {
			t.Fatal("truncated should remain false")
		}
		if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 20 || resp.Usage.CacheReadTokens != 3 || resp.Usage.ReasoningTokens != 5 {
			t.Fatalf("usage = %#v, want populated usage", resp.Usage)
		}
	})

	t.Run("incomplete_sets_length", func(t *testing.T) {
		var resp message.Response
		truncated := false
		applyResponsesCompletionPayload(&resp, responsesCompletedPayload{
			ID: "resp-2",
			IncompleteDetails: &struct {
				Reason string `json:"reason"`
			}{Reason: "max_output_tokens"},
		}, &truncated)
		if resp.StopReason != "length" {
			t.Fatalf("StopReason = %q, want length", resp.StopReason)
		}
		if !truncated {
			t.Fatal("truncated should be true")
		}
	})

	t.Run("no_tool_calls_defaults_to_stop", func(t *testing.T) {
		var resp message.Response
		truncated := false
		applyResponsesCompletionPayload(&resp, responsesCompletedPayload{ID: "resp-3"}, &truncated)
		if resp.StopReason != "stop" {
			t.Fatalf("StopReason = %q, want stop", resp.StopReason)
		}
	})
}

func TestRecoverResponsesToolCallsFromOutput(t *testing.T) {
	var resp message.Response
	var starts []string
	recoverResponsesToolCallsFromOutput(&resp, []responsesOutputEntry{
		{Type: "message", ID: "m1"},
		{Type: "function_call", ID: "fc-1", CallID: "call-1", Name: "Bash", Arguments: `{"command":"echo hi"}`},
		{Type: "function_call", ID: "fc-2", Name: "Read", Arguments: `{"path":"a.go"}`},
	}, func(delta message.StreamDelta) {
		if delta.Type == "tool_use_start" && delta.ToolCall != nil {
			starts = append(starts, delta.ToolCall.ID+":"+delta.ToolCall.Name)
		}
	})
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call-1" || resp.ToolCalls[0].Name != "Bash" {
		t.Fatalf("first tool call = %#v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "fc-2" || resp.ToolCalls[1].Name != "Read" {
		t.Fatalf("second tool call = %#v", resp.ToolCalls[1])
	}
	if len(starts) != 2 || starts[0] != "call-1:Bash" || starts[1] != "fc-2:Read" {
		t.Fatalf("tool_use_start callbacks = %#v", starts)
	}
}

// TestConvertMessagesToResponses_ArgumentsAsString ensures function_call items serialize
// with "arguments" as a JSON string (per Responses API), not an object.
func TestConvertMessagesToResponses_ArgumentsAsString(t *testing.T) {
	items := convertMessagesToResponses("", []message.Message{
		{Role: "user", Content: "run ls"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Bash", Args: json.RawMessage(`{"command":"ls -la"}`)},
			},
		},
	})
	var found bool
	for _, it := range items {
		if it.Type != "function_call" {
			continue
		}
		found = true
		// Arguments must be string so that when marshaled to JSON we get "arguments": "{\"command\":\"ls -la\"}".
		if it.Arguments == "" {
			t.Error("function_call item must have non-empty Arguments")
		}
		if it.Arguments != `{"command":"ls -la"}` {
			t.Errorf("Arguments = %q, want JSON string content", it.Arguments)
		}
		break
	}
	if !found {
		t.Fatal("no function_call item in converted input")
	}
}

func TestConvertMessagesToResponses_EmptyInput(t *testing.T) {
	// Empty system prompt and empty messages should return empty non-nil slice.
	items := convertMessagesToResponses("", []message.Message{})
	if items == nil {
		t.Error("convertMessagesToResponses() returned nil, want empty non-nil slice")
	}
	if len(items) != 0 {
		t.Errorf("convertMessagesToResponses() returned %d items, want 0", len(items))
	}
}

func TestConvertMessagesToResponses_NonNilSlice(t *testing.T) {
	// Verify that the function always returns a non-nil slice.
	tests := []struct {
		name         string
		systemPrompt string
		messages     []message.Message
	}{
		{"empty", "", []message.Message{}},
		{"system only", "You are helpful", []message.Message{}},
		{"messages only", "", []message.Message{{Role: "user", Content: "Hi"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := convertMessagesToResponses(tt.systemPrompt, tt.messages)
			if items == nil {
				t.Error("convertMessagesToResponses() returned nil slice, want non-nil")
			}
		})
	}
}

func TestConvertMessagesToResponses_WhitespaceOnlyToolOutputPreserved(t *testing.T) {
	items := convertMessagesToResponses("system", []message.Message{
		{Role: "user", Content: "run something"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Read", Args: json.RawMessage(`{"path":"input.go","limit":240,"offset":358}`)},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: "   359\t\n"},
	})

	for _, item := range items {
		if item.Type != "function_call_output" {
			continue
		}
		if item.Output == nil {
			t.Fatal("function_call_output missing output pointer")
		}
		if *item.Output != "   359\t\n" {
			t.Fatalf("output = %q, want exact whitespace-only payload preserved", *item.Output)
		}
		return
	}
	t.Fatal("no function_call_output item found")
}

// TestConvertMessagesToResponses_EmptyToolOutput ensures that function_call_output items
// with empty content still include the "output" field in JSON (not omitted by omitempty).
// This prevents API error 400: Missing required parameter: 'input[N].output'.
func TestConvertMessagesToResponses_EmptyToolOutput(t *testing.T) {
	items := convertMessagesToResponses("system", []message.Message{
		{Role: "user", Content: "run something"},
		{
			Role: "assistant",
			ToolCalls: []message.ToolCall{
				{ID: "c1", Name: "Bash", Args: json.RawMessage(`{"command":"git diff --staged"}`)},
			},
		},
		{Role: "tool", ToolCallID: "c1", Content: ""}, // empty tool output
	})

	// Find the function_call_output item and verify it marshals with "output" field.
	for _, item := range items {
		if item.Type != "function_call_output" {
			continue
		}
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal function_call_output: %v", err)
		}
		// The JSON must contain "output" key even when the value is empty.
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal to map: %v", err)
		}
		if _, ok := raw["output"]; !ok {
			t.Fatalf("function_call_output JSON missing 'output' field: %s\n"+
				"This causes API error 400: Missing required parameter: 'input[N].output'", string(data))
		}
		return
	}
	t.Fatal("no function_call_output item found")
}

// buildSSEStream returns an io.Reader that yields SSE lines (event: + data:).
// Each pair is one event; dataLines use official format (full JSON with type, output_index, etc.).
func buildSSEStream(dataLines []string) *bytes.Reader {
	var b strings.Builder
	for _, d := range dataLines {
		// Infer event type from data for standard SSE (event line then data line).
		var ev struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(d), &ev)
		eventType := ev.Type
		if eventType == "" {
			eventType = "unknown"
		}
		b.WriteString("event: ")
		b.WriteString(eventType)
		b.WriteString("\ndata: ")
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	return bytes.NewReader([]byte(b.String()))
}

func TestParseResponsesSSEEmitsProgressDeltas(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"id":"resp-test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`[DONE]`,
	})
	var progress []message.StreamProgressDelta
	_, err := parseResponsesSSE(stream, func(delta message.StreamDelta) {
		if delta.Progress != nil {
			progress = append(progress, *delta.Progress)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress deltas")
	}
	if progress[0].Bytes <= 0 || progress[0].Events != 1 {
		t.Fatalf("first progress = %+v, want positive bytes and 1 event", progress[0])
	}
}

// TestParseResponsesSSE_ToolCall verifies that Responses API SSE tool-call parsing
// correctly produces ToolCalls (from deltas and/or done item.arguments).
func TestParseResponsesSSE_ToolCall(t *testing.T) {
	t.Run("from_delta_only", func(t *testing.T) {
		// Single function_call: args built only from delta chunks (no arguments in done).
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"Bash"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"echo hi"}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"Bash","status":"completed"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.ID != "call_abc" || tc.Name != "Bash" {
			t.Errorf("tool call id=%q name=%q, want call_abc Bash", tc.ID, tc.Name)
		}
		var args map[string]any
		if err := json.Unmarshal(tc.Args, &args); err != nil {
			t.Fatalf("tool args not valid JSON: %v", err)
		}
		if args["command"] != "echo hi" {
			t.Errorf("args[command] = %v, want \"echo hi\"", args["command"])
		}
	})

	t.Run("prefer_done_arguments_when_present", func(t *testing.T) {
		// API sends empty or partial deltas but done event has full item.arguments (e.g. qt decoding).
		// Parser should use done's arguments as final args.
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_xyz","name":"Bash"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_xyz","name":"Bash","arguments":"{\"command\":\"ls -la\"}","status":"completed"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.Name != "Bash" {
			t.Errorf("tool name = %q, want Bash", tc.Name)
		}
		var args map[string]any
		if err := json.Unmarshal(tc.Args, &args); err != nil {
			t.Fatalf("tool args not valid JSON: %v", err)
		}
		if args["command"] != "ls -la" {
			t.Errorf("args[command] = %v, want \"ls -la\" (done.item.arguments should be used)", args["command"])
		}
	})

	t.Run("two_tool_calls_by_output_index", func(t *testing.T) {
		// Two function_calls: output_index 0 and 1; verify routing by output_index.
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Bash"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"first\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Bash","status":"completed"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Read"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":\"a.go\"}"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Read","status":"completed"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 2 {
			t.Fatalf("got %d tool calls, want 2", len(resp.ToolCalls))
		}
		if resp.ToolCalls[0].Name != "Bash" || resp.ToolCalls[0].ID != "c0" {
			t.Errorf("first tool: name=%q id=%q, want Bash c0", resp.ToolCalls[0].Name, resp.ToolCalls[0].ID)
		}
		if resp.ToolCalls[1].Name != "Read" || resp.ToolCalls[1].ID != "c1" {
			t.Errorf("second tool: name=%q id=%q, want Read c1", resp.ToolCalls[1].Name, resp.ToolCalls[1].ID)
		}
		var a0 map[string]any
		_ = json.Unmarshal(resp.ToolCalls[0].Args, &a0)
		if a0["command"] != "first" {
			t.Errorf("first args[command] = %v, want \"first\"", a0["command"])
		}
		var a1 map[string]any
		_ = json.Unmarshal(resp.ToolCalls[1].Args, &a1)
		if a1["path"] != "a.go" {
			t.Errorf("second args[path] = %v, want \"a.go\"", a1["path"])
		}
	})

	t.Run("done_with_index_fallback", func(t *testing.T) {
		// Backward compat: done event has index but no output_index (or output_index 0).
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Bash"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"pwd\"}"}`,
			`{"type":"response.output_item.done","index":1,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Bash","status":"completed"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		var args map[string]any
		_ = json.Unmarshal(resp.ToolCalls[0].Args, &args)
		if args["command"] != "pwd" {
			t.Errorf("args[command] = %v, want \"pwd\"", args["command"])
		}
	})

	t.Run("done_arguments_as_json_string", func(t *testing.T) {
		// API sends arguments as a JSON string (e.g. "{\"command\":\"echo 1\"}"); parser must unwrap so tool gets object.
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Bash"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Bash","arguments":"{\"command\":\"echo 1\"}","status":"completed"}}`,
			"[DONE]",
		})
		resp, err := parseResponsesSSE(stream, nil, nil)
		if err != nil {
			t.Fatalf("parseResponsesSSE: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		// Args must be unwrapped to object so json.Unmarshal into struct works (e.g. tools.bashArgs).
		var args map[string]any
		if err := json.Unmarshal(resp.ToolCalls[0].Args, &args); err != nil {
			t.Fatalf("tool args must be valid object for tools: %v", err)
		}
		if args["command"] != "echo 1" {
			t.Errorf("args[command] = %v, want \"echo 1\"", args["command"])
		}
	})
}

func TestParseResponsesSSE_MultilineDataEvent(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,`,
		`data: "item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","content_index":0,`,
		`data: "delta":"hello","item_id":"msg_1","logprobs":[],"output_index":0}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")

	resp, err := parseResponsesSSE(bytes.NewReader([]byte(raw)), nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("Content = %q, want hello", resp.Content)
	}
	if resp.ProviderResponseID != "resp-1" {
		t.Fatalf("ProviderResponseID = %q, want resp-1", resp.ProviderResponseID)
	}
}

func TestParseResponsesSSE_MidLineTimeoutReturnsTruncatedEvent(t *testing.T) {
	cancelled := make(chan struct{})
	reader := &timeoutPartialReader{
		cancelled: cancelled,
		payload: []byte(strings.Join([]string{
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","content_index":0,"delta":"hel`,
		}, "\n")),
	}
	cancel := func() {
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}

	cr := NewChunkTimeoutReader(reader, 10*time.Millisecond, cancel)
	defer cr.Stop()

	_, err := parseResponsesSSE(cr, nil, nil)
	if err == nil {
		t.Fatal("parseResponsesSSE err = nil, want truncated SSE event error")
	}
	if !strings.Contains(err.Error(), "truncated SSE event") {
		t.Fatalf("parseResponsesSSE err = %v, want truncated SSE event", err)
	}
	if strings.Contains(err.Error(), "parse output_text.delta") {
		t.Fatalf("parseResponsesSSE err = %v, should not be reported as output_text.delta parse failure", err)
	}
}

func TestParseResponsesSSE_EarlyCloseBeforeCompletedReturnsError(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","content_index":0,"delta":"partial","item_id":"msg_1","logprobs":[],"output_index":0}`,
		"",
	}, "\n")

	_, err := parseResponsesSSE(bytes.NewReader([]byte(raw)), nil, nil)
	if err == nil {
		t.Fatal("parseResponsesSSE err = nil, want incomplete stream error")
	}
	if !strings.Contains(err.Error(), "stream closed before response.completed") {
		t.Fatalf("parseResponsesSSE err = %v, want stream closed before response.completed", err)
	}
}

// TestParseResponsesSSE_DuplicateToolCallFromProxy verifies that when a proxy (e.g. qt)
// replays the same tool call events after they've already been finalized, the parser
// deduplicates them — producing exactly 1 tool call, not 2.
func TestParseResponsesSSE_DuplicateToolCallFromProxy(t *testing.T) {
	// Simulate qt behavior: normal sequence, then duplicate with triple-encoded args.
	stream := buildSSEStream([]string{
		// First (normal) sequence
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"echo hi\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"Bash","arguments":"{\"command\":\"echo hi\"}","status":"completed"}}`,
		// Duplicate replay from proxy (same call_id, same output_index, triple-encoded args)
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"Bash","status":"in_progress","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"\"{\\\"command\\\":\\\"echo hi\\\"}\"","call_id":"call_abc","item_id":"fc_1"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"Bash","arguments":"\"{\\\"command\\\":\\\"echo hi\\\"}\"","status":"completed"}}`,
		// Completion
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})

	var toolUseStarts int
	cb := func(delta message.StreamDelta) {
		if delta.Type == "tool_use_start" {
			toolUseStarts++
		}
	}

	resp, err := parseResponsesSSE(stream, cb, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1 (duplicate should be skipped)", len(resp.ToolCalls))
	}
	if toolUseStarts != 1 {
		t.Errorf("got %d tool_use_start callbacks, want 1 (duplicate should not trigger TUI block)", toolUseStarts)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Name != "Bash" {
		t.Errorf("tool call id=%q name=%q, want call_abc Bash", tc.ID, tc.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("tool args not valid JSON: %v", err)
	}
	if args["command"] != "echo hi" {
		t.Errorf("args[command] = %v, want \"echo hi\"", args["command"])
	}
}

// TestParseResponsesSSE_ExecuteBashTool runs the full flow: parse SSE → UnwrapToolArgs → Registry.Execute(Bash).
// It verifies that parsed tool-call args are valid for real tool execution (no "cannot unmarshal string" etc.).
func TestParseResponsesSSE_ExecuteBashTool(t *testing.T) {
	// Simulate API stream: one Bash tool call with command "echo chord-ok".
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"echo chord-ok"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"Bash","status":"completed"}}`,
		"[DONE]",
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "Bash" {
		t.Fatalf("tool name = %q, want Bash", tc.Name)
	}

	// Same path as agent: UnwrapToolArgs then Execute.
	args := UnwrapToolArgs(tc.Args)
	reg := tools.NewRegistry()
	reg.Register(tools.NewBashTool("bash"))
	ctx := context.Background()
	out, execErr := reg.Execute(ctx, tc.Name, args)
	if execErr != nil {
		t.Fatalf("Bash Execute failed (args may be wrong type for tools.bashArgs): %v", execErr)
	}
	if !strings.Contains(out, "chord-ok") {
		t.Errorf("Bash output = %q, want to contain \"chord-ok\"", out)
	}
}

// TestParseResponsesSSE_ExecuteBashTool_DoneArgumentsAsString runs the same flow when the API
// sends arguments only in done event as a JSON string (e.g. qt). Execute must still succeed.
func TestParseResponsesSSE_ExecuteBashTool_DoneArgumentsAsString(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_2","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_2","name":"Bash","arguments":"{\"command\":\"echo done-args\"}","status":"completed"}}`,
		"[DONE]",
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	args := UnwrapToolArgs(tc.Args)
	reg := tools.NewRegistry()
	reg.Register(tools.NewBashTool("bash"))
	ctx := context.Background()
	out, execErr := reg.Execute(ctx, tc.Name, args)
	if execErr != nil {
		t.Fatalf("Bash Execute failed (done.arguments as string must unwrap to object): %v", execErr)
	}
	if !strings.Contains(out, "done-args") {
		t.Errorf("Bash output = %q, want to contain \"done-args\"", out)
	}
}

// TestParseResponsesSSE_TruncatedStream verifies that when the API sends response.incomplete
// (output truncation, e.g. max_output_tokens), we do not append in-progress tool calls and set StopReason=length.
// This prevents "review uncommitted code" / large git diff scenarios from being counted as malformed.
func TestParseResponsesSSE_TruncatedStream(t *testing.T) {
	// Simulate: model starts Bash (e.g. git diff), stream is truncated before output_item.done.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"git diff --staged"}`,
		// No output_item.done; stream ends with response.incomplete (truncation).
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":5000,"output_tokens":32000}}}`,
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("truncated stream: got %d tool calls, want 0 (in-progress tool call must be discarded)", len(resp.ToolCalls))
	}
	if resp.StopReason != "length" {
		t.Errorf("StopReason = %q, want \"length\"", resp.StopReason)
	}
}

// TestParseResponsesSSE_DONEWithIncompleteJSON verifies that when the stream ends with [DONE]
// without response.incomplete but accumulated tool args are invalid JSON (truncated mid-call),
// we discard that tool call and set StopReason=length so the agent does not count it as malformed.
func TestParseResponsesSSE_DONEWithIncompleteJSON(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"git "}`,
		// No more deltas, no output_item.done; stream ends with [DONE]. Args are invalid JSON.
		"[DONE]",
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("incomplete JSON at [DONE]: got %d tool calls, want 0 (discard to avoid malformed)", len(resp.ToolCalls))
	}
	if resp.StopReason != "length" {
		t.Errorf("StopReason = %q, want \"length\"", resp.StopReason)
	}
}

// TestParseResponsesSSE_ExtractsProviderResponseID verifies that response.id is captured.
func TestParseResponsesSSE_ExtractsProviderResponseID(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"id":"resp-abc123","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if resp.ProviderResponseID != "resp-abc123" {
		t.Errorf("ProviderResponseID = %q, want resp-abc123", resp.ProviderResponseID)
	}
}

func TestParseResponsesSSEWithOutputItems_CompletedOutputToBaseline(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":"{\"file\":\"README.md\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	})
	resp, outputItems, err := parseResponsesSSEWithOutputItems(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItems: %v", err)
	}
	if resp == nil || resp.ProviderResponseID != "resp-1" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(outputItems) != 2 {
		t.Fatalf("outputItems len = %d, want 2", len(outputItems))
	}
	if outputItems[0].Type != "message" || outputItems[0].Role != "assistant" {
		t.Fatalf("outputItems[0] = %#v, want assistant message", outputItems[0])
	}
	if outputItems[1].Type != "function_call" || outputItems[1].CallID != "call_1" || outputItems[1].Name != "Read" {
		t.Fatalf("outputItems[1] = %#v, want function_call call_1 Read", outputItems[1])
	}
}

func TestParseResponsesSSEWithOutputItems_FallsBackToStreamToolCallsWhenOutputMissing(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"call_1","name":"Read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"file\":\"README.md\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"call_1","name":"Read","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"resp-2","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	})
	resp, outputItems, err := parseResponsesSSEWithOutputItems(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItems: %v", err)
	}
	if resp == nil || len(resp.ToolCalls) != 1 {
		t.Fatalf("unexpected response tool calls: %#v", resp)
	}
	if len(outputItems) != 1 {
		t.Fatalf("outputItems len = %d, want 1", len(outputItems))
	}
	if outputItems[0].Type != "function_call" || outputItems[0].CallID != "call_1" || outputItems[0].Name != "Read" {
		t.Fatalf("outputItems[0] = %#v, want function_call call_1 Read", outputItems[0])
	}
}

func TestResponsesRequestSignatureIgnoresInputButTracksNonInputFields(t *testing.T) {
	reqA := &responsesRequest{
		Model:          "gpt-5",
		Tools:          []responsesTool{{Type: "function", Name: "Read"}},
		Stream:         true,
		Input:          []responsesInputItem{{Type: "message", Role: "user", Content: "one"}},
		PromptCacheKey: "sid",
	}
	reqB := &responsesRequest{
		Model:          "gpt-5",
		Tools:          []responsesTool{{Type: "function", Name: "Read"}},
		Stream:         true,
		Input:          []responsesInputItem{{Type: "message", Role: "user", Content: "two"}},
		PromptCacheKey: "sid",
	}
	if responsesRequestSignature(reqA) != responsesRequestSignature(reqB) {
		t.Fatal("signature should ignore input differences")
	}
	reqB.Tools = []responsesTool{{Type: "function", Name: "Bash"}}
	if responsesRequestSignature(reqA) == responsesRequestSignature(reqB) {
		t.Fatal("signature should change when non-input fields change")
	}
}

func TestResponsesSessionState_Reset(t *testing.T) {
	s := &responsesSessionState{
		lastResponseID:   "resp-1",
		lastModelID:      "gpt-5",
		lastFullInputLen: 3,
		lastFullInputSig: "sig",
		lastKeyHint:      "key",
	}
	s.reset("test_reason")
	if s.lastResponseID != "" || s.lastModelID != "" || s.lastFullInputLen != 0 || s.lastFullInputSig != "" {
		t.Errorf("reset did not clear all fields: %+v", s)
	}
}

func TestResponsesInputSignature_PrefixConsistency(t *testing.T) {
	items := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "assistant", Content: "world"},
		{Type: "message", Role: "user", Content: "more"},
	}
	pfxSig := responsesInputPrefixSignature(items, 2)
	fullSig := responsesInputSignature(items[:2])
	if pfxSig != fullSig {
		t.Errorf("prefix sig = %q, full sig = %q; must match", pfxSig, fullSig)
	}
	pfx1 := responsesInputPrefixSignature(items, 1)
	if pfx1 == pfxSig {
		t.Errorf("prefix sig for len=1 must differ from len=2")
	}
	modified := make([]responsesInputItem, len(items))
	copy(modified, items)
	modified[0].Content = "different"
	if responsesInputPrefixSignature(modified, 2) == pfxSig {
		t.Errorf("modified prefix sig should differ from original")
	}
}

func TestResponsesProvider_IncrementalSessionConditions(t *testing.T) {
	items1 := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
	}
	items2 := []responsesInputItem{
		{Type: "message", Role: "user", Content: "hello"},
		{Type: "message", Role: "assistant", Content: "hi"},
	}

	sig1 := responsesInputSignature(items1)

	// Model match + prefix match + deltaLen>0 => incremental allowed.
	lastResponseID := "resp-1"
	model := "gpt-5"
	lastModelID := "gpt-5"
	lastFullInputLen := 1
	can := lastResponseID != "" &&
		model == lastModelID &&
		lastFullInputLen > 0 &&
		len(items2) >= lastFullInputLen &&
		responsesInputPrefixSignature(items2, lastFullInputLen) == sig1 &&
		len(items2)-lastFullInputLen > 0
	if !can {
		t.Error("expected incremental to be allowed")
	}

	// Model mismatch => not allowed.
	can2 := model == "gpt-4"
	if can2 {
		t.Error("model mismatch should prevent incremental")
	}

	// deltaLen == 0 => not allowed.
	can3 := len(items1)-1 > 0
	if can3 {
		t.Error("deltaLen==0 should prevent incremental")
	}

	// Prefix mismatch => not allowed.
	modified := []responsesInputItem{{Type: "message", Role: "user", Content: "different"}}
	modified = append(modified, items2[1:]...)
	can4 := responsesInputPrefixSignature(modified, 1) == sig1
	if can4 {
		t.Error("prefix mismatch should prevent incremental")
	}
}

func TestOpenAIProvider_PersistsResponsesProvider(t *testing.T) {
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Preset: config.ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}, nil)
	o, err := NewOpenAIProvider(providerCfg, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if o.responsesProvider == nil {
		t.Fatal("OpenAIProvider.responsesProvider should be set in constructor")
	}
	rp1 := o.responsesProvider
	rp2 := o.responsesProvider
	if rp1 != rp2 {
		t.Error("responsesProvider must be the same instance")
	}
}

func TestOpenAIProvider_ResetResponsesSession(t *testing.T) {
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Preset: config.ProviderPresetCodex,
		APIURL: "https://example.com/v1/responses",
	}, nil)
	o, err := NewOpenAIProvider(providerCfg, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	o.responsesProvider.session.mu.Lock()
	o.responsesProvider.session.lastResponseID = "resp-xyz"
	o.responsesProvider.session.lastModelID = "gpt-5"
	o.responsesProvider.session.lastFullInputLen = 5
	o.responsesProvider.session.mu.Unlock()

	o.ResetResponsesSession("test")

	o.responsesProvider.session.mu.Lock()
	id := o.responsesProvider.session.lastResponseID
	o.responsesProvider.session.mu.Unlock()
	if id != "" {
		t.Errorf("after reset, lastResponseID = %q, want empty", id)
	}
}

func TestResponsesProvider_WebsocketMissingResponseIDDoesNotAdvanceChain(t *testing.T) {
	r := &ResponsesProvider{}
	r.codexWSLastRespID = "resp-old"
	r.codexWSLastInpLen = 2
	r.codexWSLastInpSig = "sig-old"

	resp := &message.Response{}
	fullInput := []responsesInputItem{{Type: "message", Role: "user", Content: "hello"}}

	r.codexWSLastInpLen = len(fullInput)
	r.codexWSLastInpSig = responsesInputSignature(fullInput)
	if resp.ProviderResponseID != "" {
		r.codexWSLastRespID = resp.ProviderResponseID
	} else {
		r.codexWSLastRespID = ""
	}

	if r.codexWSLastRespID != "" {
		t.Fatalf("last resp id = %q, want empty when response id missing", r.codexWSLastRespID)
	}
}

func TestResponsesProvider_NoPreviousIDOnFirstRound(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
	}, []string{"test-key"})

	r := &ResponsesProvider{
		provider: providerCfg,
		client:   server.Client(),
	}

	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if _, ok := gotBody["previous_response_id"]; ok {
		t.Error("first request must not include previous_response_id")
	}
	// HTTP path no longer advances previous_response_id session chain.
	r.session.mu.Lock()
	id := r.session.lastResponseID
	r.session.mu.Unlock()
	if id != "" {
		t.Errorf("session.lastResponseID = %q, want empty", id)
	}
}

func TestResponsesProvider_IncrementalOnSecondRound(t *testing.T) {
	var requestBodies []map[string]any
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-2","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
	}, []string{"test-key"})

	// Compute fullInput as CompleteStream does internally (system prompt="", one user message).
	msgs1 := []message.Message{{Role: "user", Content: "hello"}}
	items1 := convertMessagesToResponses("", msgs1)

	r := &ResponsesProvider{
		provider: providerCfg,
		client:   server.Client(),
	}
	// Seed session as if first round already succeeded.
	r.session.lastResponseID = "resp-1"
	r.session.lastModelID = "gpt-5"
	r.session.lastFullInputLen = len(items1)
	r.session.lastFullInputSig = responsesInputSignature(items1)

	// Second round: same first message + one more.
	msgs2 := []message.Message{
		{Role: "user", Content: "hello"},
		{Role: "user", Content: "follow up"},
	}
	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		msgs2, nil, 128, RequestTuning{},
		func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	mu.Lock()
	body := requestBodies[0]
	mu.Unlock()
	if _, ok := body["previous_response_id"]; ok {
		t.Errorf("second round: unexpected previous_response_id = %v", body["previous_response_id"])
	}
	input, _ := body["input"].([]any)
	// HTTP path always sends full input.
	if len(input) != 2 {
		t.Errorf("second round: sent %d input items, want 2 (full)", len(input))
	}
}

func TestResponsesProvider_NoFallbackOnFourXX(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: server.URL + "/v1/responses",
		Store:  &storeTrue,
	}, []string{"test-key"})

	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	_, err := r.CompleteStream(
		context.Background(), "test-key", "gpt-5", "",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err == nil {
		t.Fatal("expected 400 error")
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call without fallback, got %d", callCount)
	}
}

func TestResponsesProvider_CodexOAuth429EmitsRateLimitDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-codex-primary-used-percent", "91")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("x-codex-primary-reset-after-seconds", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer server.Close()

	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		Preset: config.ProviderPresetCodex,
		APIURL: server.URL + "/v1/responses",
		Models: map[string]config.ModelConfig{
			"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
		},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	var deltas []message.StreamDelta
	_, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(delta message.StreamDelta) {
			deltas = append(deltas, delta)
		},
	)
	if err == nil {
		t.Fatal("expected 429 error")
	}

	var got *message.StreamDelta
	for i := range deltas {
		if deltas[i].Type == "rate_limits" && deltas[i].RateLimit != nil {
			got = &deltas[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("did not receive rate_limits delta: %#v", deltas)
	}
	if got.RateLimit.Provider != "openai" {
		t.Fatalf("delta provider = %q, want openai", got.RateLimit.Provider)
	}
	if snap := providerCfg.KeySnapshot("oauth-key"); snap == nil || snap.Primary == nil || snap.Primary.UsedPercent() != 91 {
		t.Fatalf("stored snapshot = %#v, want primary used percent 91", snap)
	}
}

func TestResponsesProvider_CodexWSIncrementalFailureRetriesWSFullThenHTTPFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-http","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:               config.ProviderTypeChatCompletions,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	var wsCalls []bool
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time) (*message.Response, bool, error) {
		call := len(wsCalls)
		wsCalls = append(wsCalls, call == 0)
		if call == 0 {
			return nil, true, fmt.Errorf("previous_response_not_found")
		}
		return nil, false, fmt.Errorf("ws still unavailable")
	}

	resp, err := r.CompleteStream(
		context.Background(), "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{}, func(message.StreamDelta) {},
	)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if resp == nil || resp.ProviderResponseID != "resp-http" {
		t.Fatalf("unexpected response after fallback: %#v", resp)
	}
	if len(wsCalls) != 2 {
		t.Fatalf("ws calls = %d, want 2 (incremental then full)", len(wsCalls))
	}
	if !wsCalls[0] || wsCalls[1] {
		t.Fatalf("ws retry sequence = %#v, want [incremental, full]", wsCalls)
	}
	if httpCalls != 1 {
		t.Fatalf("http calls = %d, want 1 after ws retries", httpCalls)
	}
}

func TestResponsesProvider_CodexWSCancelDoesNotRetryOrFallback(t *testing.T) {
	var httpCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	responsesWSOn := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{
		Type:               config.ProviderTypeChatCompletions,
		Preset:             config.ProviderPresetCodex,
		APIURL:             server.URL + "/v1/responses",
		ResponsesWebsocket: &responsesWSOn,
		Models:             map[string]config.ModelConfig{"gpt-5": {Limit: config.ModelLimit{Context: 8192, Output: 1024}}},
	}, []string{"oauth-key"})
	providerCfg.SetOAuthRefresher(config.OpenAIOAuthTokenURL, config.OpenAIOAuthClientID, "", &config.AuthConfig{}, &sync.Mutex{}, map[string]OAuthKeySetup{"oauth-key": {CredentialIndex: 0, AccountID: "acc-test", Expires: 32503680000000}}, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wsCalls int
	var deltas []message.StreamDelta
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}
	r.codexWSCompleteFn = func(_ context.Context, _ string, _ string, _ string, _ *responsesRequest, _ []responsesInputItem, _ StreamCallback, _ time.Time) (*message.Response, bool, error) {
		wsCalls++
		cancel()
		return nil, true, fmt.Errorf("previous_response_not_found")
	}

	_, err := r.CompleteStream(
		ctx, "oauth-key", "gpt-5", "system",
		[]message.Message{{Role: "user", Content: "hello"}},
		nil, 128, RequestTuning{},
		func(delta message.StreamDelta) {
			deltas = append(deltas, delta)
		},
	)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("CompleteStream err = %v, want context.Canceled", err)
	}
	if wsCalls != 1 {
		t.Fatalf("ws calls = %d, want 1 after cancel", wsCalls)
	}
	if httpCalls != 0 {
		t.Fatalf("http calls = %d, want 0 after cancel", httpCalls)
	}
	if len(deltas) != 0 {
		t.Fatalf("unexpected status deltas after cancel: %#v", deltas)
	}
}

func TestResponsesProvider_IgnoresNullPreviousResponseIDWithoutRollback(t *testing.T) {
	callCount := 0
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-null-prev","previous_response_id":null,"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: server.URL + "/v1/responses", Store: &storeTrue}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	msgs2 := []message.Message{{Role: "user", Content: "hello"}, {Role: "user", Content: "follow up"}}
	rollbackSeen := false
	_, err := r.CompleteStream(context.Background(), "test-key", "gpt-5", "", msgs2, nil, 128, RequestTuning{}, func(delta message.StreamDelta) {
		if delta.Type == "rollback" {
			rollbackSeen = true
		}
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", callCount)
	}
	if rollbackSeen {
		t.Fatal("did not expect rollback delta")
	}
	if _, ok := bodies[0]["previous_response_id"]; ok {
		t.Fatalf("request must not include previous_response_id, got %v", bodies[0]["previous_response_id"])
	}
}

func TestResponsesProvider_AlwaysFullInputOnHTTP(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp-fixed","previous_response_id":null,"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	storeTrue := true
	providerCfg := NewProviderConfig("openai", config.ProviderConfig{Type: config.ProviderTypeChatCompletions, APIURL: server.URL + "/v1/responses", Store: &storeTrue}, []string{"test-key"})
	r := &ResponsesProvider{provider: providerCfg, client: server.Client()}

	for _, msgs := range [][]message.Message{
		{{Role: "user", Content: "m1"}},
		{{Role: "user", Content: "m1"}, {Role: "user", Content: "m2"}},
		{{Role: "user", Content: "m1"}, {Role: "user", Content: "m2"}, {Role: "user", Content: "m3"}},
	} {
		_, err := r.CompleteStream(context.Background(), "test-key", "gpt-5", "", msgs, nil, 128, RequestTuning{}, func(message.StreamDelta) {})
		if err != nil {
			t.Fatalf("CompleteStream: %v", err)
		}
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(bodies))
	}
	for i, b := range bodies {
		if _, ok := b["previous_response_id"]; ok {
			t.Fatalf("request %d unexpectedly includes previous_response_id", i+1)
		}
	}
}

// TestParseResponsesSSE_ReviewUncommittedCode_E2E simulates "review uncommitted code" flow: parse a truncated
// stream (Bash git diff started but output truncated), then ensure we do not produce malformed tool calls
// and StopReason is length so the agent can suggest new conversation / max_output_tokens.
func TestParseResponsesSSE_ReviewUncommittedCode_E2E(t *testing.T) {
	// Scenario: user says "review uncommitted code", model starts Bash with "git diff --staged", output is truncated.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"bash_1","call_id":"call_bash","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"git diff --staged"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`,
		// Simulate truncation before done (e.g. token limit hit). API sends response.incomplete.
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10000,"output_tokens":32000}}}`,
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	// Must not expose any tool call when truncated (agent would treat as malformed otherwise).
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("review-uncommitted truncated: got %d tool calls, want 0", len(resp.ToolCalls))
	}
	if resp.StopReason != "length" {
		t.Errorf("StopReason = %q, want \"length\"", resp.StopReason)
	}
	// Agent path: with 0 tool calls and StopReason=length, MalformedCount is not incremented.
	// So the turn can retry or user can start new conversation / increase max_output_tokens.
}
