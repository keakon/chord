package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestApplyResponsesCompletionPayload(t *testing.T) {
	t.Run("tool_calls_stop_reason_and_usage", func(t *testing.T) {
		var resp message.Response
		truncated := false
		applyResponsesCompletionPayload(&resp, responsesCompletedPayload{
			ID:     "resp-1",
			Output: []responsesOutputEntry{{Type: "function_call", ID: "fc-1", CallID: "call-1", Name: "Shell", Arguments: `{"command":"pwd"}`}},
			Usage: &responsesUsagePayload{
				InputTokens:  10,
				OutputTokens: 20,
				InputTokensDetails: &struct {
					CachedTokens     int `json:"cached_tokens"`
					CacheWriteTokens int `json:"cache_write_tokens"`
				}{CachedTokens: 3, CacheWriteTokens: 4},
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
		if resp.Usage == nil || resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 20 || resp.Usage.CacheReadTokens != 3 || resp.Usage.CacheWriteTokens != 4 || resp.Usage.ReasoningTokens != 5 {
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
	var events []string
	recoverResponsesToolCallsFromOutput(&resp, []responsesOutputEntry{
		{Type: "message", ID: "m1"},
		{Type: "function_call", ID: "fc-1", CallID: "call-1", Name: "Shell", Arguments: `{"command":"echo hi"}`},
		{Type: "function_call", ID: "fc-empty-name", Arguments: `{"command":"echo skipped"}`},
		{Type: "function_call", ID: "fc-2", Name: "Read", Arguments: `{"path":"a.go"}`},
	}, func(delta message.StreamDelta) {
		if delta.ToolCall != nil {
			events = append(events, delta.Type+":"+delta.ToolCall.ID+":"+delta.ToolCall.Name)
		}
	})
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call-1" || resp.ToolCalls[0].Name != "Shell" {
		t.Fatalf("first tool call = %#v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[1].ID != "fc-2" || resp.ToolCalls[1].Name != "Read" {
		t.Fatalf("second tool call = %#v", resp.ToolCalls[1])
	}
	wantEvents := []string{
		"tool_use_start:call-1:Shell",
		"tool_use_end:call-1:Shell",
		"tool_use_start:fc-2:Read",
		"tool_use_end:fc-2:Read",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("tool callbacks = %#v, want %#v", events, wantEvents)
	}
}

func TestParseResponsesSSEEmitsProgressDeltas(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"id":"resp-test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`[DONE]`,
	})
	var progress []message.StreamProgressDelta
	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.Progress != nil {
			progress = append(progress, *delta.Progress)
		}
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"Shell"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"echo hi"}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_abc","name":"Shell","status":"completed"}}`,
			"[DONE]",
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.ID != "call_abc" || tc.Name != "Shell" {
			t.Errorf("tool call id=%q name=%q, want call_abc Shell", tc.ID, tc.Name)
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
		// API sends empty or partial deltas but done event has full item.arguments (e.g. a proxy that buffers deltas).
		// Parser should use done's arguments as final args.
		stream := buildSSEStream([]string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_xyz","name":"Shell"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_xyz","name":"Shell","arguments":"{\"command\":\"ls -la\"}","status":"completed"}}`,
			"[DONE]",
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		tc := resp.ToolCalls[0]
		if tc.Name != "Shell" {
			t.Errorf("tool name = %q, want Shell", tc.Name)
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
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Shell"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"first\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Shell","status":"completed"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Read"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":\"a.go\"}"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Read","status":"completed"}}`,
			"[DONE]",
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		if len(resp.ToolCalls) != 2 {
			t.Fatalf("got %d tool calls, want 2", len(resp.ToolCalls))
		}
		if resp.ToolCalls[0].Name != "Shell" || resp.ToolCalls[0].ID != "c0" {
			t.Errorf("first tool: name=%q id=%q, want Shell c0", resp.ToolCalls[0].Name, resp.ToolCalls[0].ID)
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
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Shell"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"pwd\"}"}`,
			`{"type":"response.output_item.done","index":1,"output_index":0,"item":{"type":"function_call","id":"i1","call_id":"c1","name":"Shell","status":"completed"}}`,
			"[DONE]",
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Shell"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"i0","call_id":"c0","name":"Shell","arguments":"{\"command\":\"echo 1\"}","status":"completed"}}`,
			"[DONE]",
		})
		resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if err != nil {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
		}
		// Args must be unwrapped to object so json.Unmarshal into struct works (e.g. tools.shellArgs).
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

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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

	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(cr, nil, nil, nil, "", false, false, false)
	if err == nil {
		t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState err = nil, want truncated SSE event error")
	}
	if !strings.Contains(err.Error(), "truncated SSE event") {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState err = %v, want truncated SSE event", err)
	}
	if strings.Contains(err.Error(), "parse output_text.delta") {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState err = %v, should not be reported as output_text.delta parse failure", err)
	}
}

func TestParseResponsesSSE_EarlyCloseAfterTextDeltaReturnsInterruptedPartialResponse(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","content_index":0,"delta":"partial","item_id":"msg_1","logprobs":[],"output_index":0}`,
		"",
		"",
	}, "\n")

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "partial" || resp.StopReason != "interrupted" || resp.Usage != nil {
		t.Fatalf("partial response = content %q stop %q usage %#v, want partial/interrupted/nil usage", resp.Content, resp.StopReason, resp.Usage)
	}
}

func TestParseResponsesSSE_EarlyCloseAfterTextDoneReturnsInterruptedPartialResponse(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","content_index":0,"delta":"complete","item_id":"msg_1","logprobs":[],"output_index":0}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","status":"completed"}}`,
		"",
	}, "\n")

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "complete" || resp.StopReason != "interrupted" || resp.Usage != nil {
		t.Fatalf("partial response = content %q stop %q usage %#v, want complete/interrupted/nil usage", resp.Content, resp.StopReason, resp.Usage)
	}
}

func TestParseResponsesSSE_ReadErrorAfterTextDeltaReturnsInterruptedPartialResponse(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","content_index":0,"delta":"partial","item_id":"msg_1","logprobs":[],"output_index":0}`,
		"",
		"",
	}, "\n")
	reader := &errorAfterReader{payload: []byte(raw), err: errors.New("network reset")}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "partial" || resp.StopReason != "interrupted" || resp.Usage != nil {
		t.Fatalf("partial response = content %q stop %q usage %#v, want partial/interrupted/nil usage", resp.Content, resp.StopReason, resp.Usage)
	}
}

func TestParseResponsesSSE_ReadErrorAfterTextDoneReturnsInterruptedPartialResponse(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","content_index":0,"delta":"complete","item_id":"msg_1","logprobs":[],"output_index":0}`,
		"",
		"event: response.output_item.done",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","status":"completed"}}`,
		"",
		"",
	}, "\n")
	reader := &errorAfterReader{payload: []byte(raw), err: errors.New("network reset")}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "complete" || resp.StopReason != "interrupted" || resp.Usage != nil {
		t.Fatalf("partial response = content %q stop %q usage %#v, want complete/interrupted/nil usage", resp.Content, resp.StopReason, resp.Usage)
	}
}

func TestParseResponsesSSE_EarlyCloseBeforeCompletedWithNoOutputReturnsError(t *testing.T) {
	raw := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"status":"in_progress"}}`,
		"",
	}, "\n")

	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
	if err == nil {
		t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState err = nil, want incomplete stream error")
	}
	if !strings.Contains(err.Error(), "stream closed before response.completed") {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState err = %v, want stream closed before response.completed", err)
	}
}

func TestParseResponsesSSE_EarlyCloseAfterToolDoneReturnsRecoveredToolCall(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"echo recovered\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell","status":"completed"}}`,
	})

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.StopReason != "tool_calls" {
		t.Fatalf("StopReason = %q, want tool_calls", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	var args map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("tool args invalid: %v", err)
	}
	if args["command"] != "echo recovered" {
		t.Fatalf("args[command] = %v, want echo recovered", args["command"])
	}
	if resp.Usage != nil {
		t.Fatalf("Usage = %#v, want nil", resp.Usage)
	}
}

func TestParseResponsesSSE_ChunkTimeoutAfterAllOutputItemsDoneReturnsRecoveredToolCalls(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"echo one\"}"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell","status":"completed"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"item_2","call_id":"call_2","name":"grep"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"pattern\":\"TODO\",\"path\":\"internal\"}"}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item_2","call_id":"call_2","name":"grep","status":"completed"}}`,
	}, "\n\n") + "\n\n"
	reader := &chunkTimeoutAfterPayloadReader{payload: []byte(raw)}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.StopReason != "tool_calls" {
		t.Fatalf("StopReason = %q, want tool_calls", resp.StopReason)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "shell" || resp.ToolCalls[1].Name != "grep" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Usage != nil {
		t.Fatalf("Usage = %#v, want nil when response.completed is missing", resp.Usage)
	}
}

// TestParseResponsesSSE_ChunkTimeoutWithReasoningItemRecoversToolCalls guards a
// regression where a reasoning output item left openOutputItems non-empty: the
// done branch only marked function_call/message items done, so the reasoning
// entry from output_item.added persisted, outputItemsComplete() stayed false,
// and partial recovery after a truncated response.completed discarded the
// already-complete tool call. gpt-5 reasoning models always emit such an item,
// so this is the real-world shape of the 60s "truncated response.completed"
// stalls. It must recover the tool call rather than failing.
func TestParseResponsesSSE_ChunkTimeoutWithReasoningItemRecoversToolCalls(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"echo recovered\"}"}`,
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell","status":"completed"}}`,
	}, "\n\n") + "\n\n"
	reader := &chunkTimeoutAfterPayloadReader{payload: []byte(raw)}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.StopReason != "tool_calls" {
		t.Fatalf("StopReason = %q, want tool_calls", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "shell" {
		t.Fatalf("tool call = %+v, want shell", resp.ToolCalls[0])
	}
	var args map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("tool args invalid: %v", err)
	}
	if args["command"] != "echo recovered" {
		t.Fatalf("args[command] = %v, want echo recovered", args["command"])
	}
}

func TestParseResponsesSSE_xAIReasoningText(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"thinking "}`,
		`data: {"type":"response.reasoning_text.delta","delta":"deeply"}`,
		`data: {"type":"response.reasoning_text.done","text":"thinking deeply"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"id":"resp_xai","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`,
	}, "\n\n") + "\n\n"
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(raw), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ReasoningContent != "thinking deeply" || resp.Content != "answer" {
		t.Fatalf("response = %+v, want xAI reasoning and answer", resp)
	}
}

// TestResponsesOutputItemDone_ReasoningArmsTerminalDrain verifies that once all
// output items (reasoning + the final tool call) are done, the parser shortens
// the per-chunk timeout to the terminal drain budget instead of waiting a full
// DefaultChunkTimeout for the trailing response.completed. The reasoning item
// must not keep the drain from arming.
func TestResponsesOutputItemDone_ReasoningArmsTerminalDrain(t *testing.T) {
	phaser := &recordingPhaser{}
	state := responsesEventState{
		resp:           &message.Response{},
		textItems:      &responsesItemTexts{},
		toolCalls:      make(map[int]*responsesToolAccumulator),
		finalizedCalls: make(map[string]bool),
		truncated:      new(bool),
		outputItems:    &[]responsesInputItem{},
		partial:        &responsesPartialCompletionState{openOutputItems: make(map[int]struct{})},
		phaser:         phaser,
	}
	flush := func() {}

	events := []struct{ eventType, data string }{
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`},
		{"response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`},
		{"response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"echo hi\"}"}`},
		{"response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell","status":"completed"}}`},
	}
	for _, ev := range events {
		if _, _, _, err := processResponsesEventPayload(state, ev.eventType, []byte(ev.data), flush); err != nil {
			t.Fatalf("processResponsesEventPayload(%s): %v", ev.eventType, err)
		}
	}

	if !phaser.drainSet {
		t.Fatal("terminal drain timeout was never armed; reasoning item likely left openOutputItems non-empty")
	}
	if phaser.lastDrain != TerminalDrainChunkTimeout {
		t.Fatalf("drain timeout = %v, want %v", phaser.lastDrain, TerminalDrainChunkTimeout)
	}
}

func TestParseResponsesSSE_ChunkTimeoutBeforeOutputItemDoneStillFails(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"echo unsafe\"}"}`,
	}, "\n\n") + "\n\n"
	reader := &chunkTimeoutAfterPayloadReader{payload: []byte(raw)}

	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
	if err == nil {
		t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState err = nil, want timeout/incomplete error")
	}
	if !strings.Contains(err.Error(), "reading SSE stream") {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState err = %v, want reading SSE stream error", err)
	}
}

func TestParseResponsesSSE_ChunkTimeoutAfterTextOnlyDoneReturnsInterruptedPartialResponse(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"I will run checks next."}`,
		`data: {"type":"response.output_text.done","output_index":0,"content_index":0,"text":"I will run checks next."}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","status":"completed"}}`,
	}, "\n\n") + "\n\n"
	reader := &chunkTimeoutAfterPayloadReader{payload: []byte(raw)}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "I will run checks next." || resp.StopReason != "interrupted" || resp.Usage != nil {
		t.Fatalf("partial response = content %q stop %q usage %#v, want text/interrupted/nil usage", resp.Content, resp.StopReason, resp.Usage)
	}
}

func TestParseResponsesSSE_EarlyCloseBeforeToolDoneReturnsError(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"echo unsafe\"}"}`,
	})

	_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err == nil {
		t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState err = nil, want incomplete stream error")
	}
	if !strings.Contains(err.Error(), "stream closed before response.completed") {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState err = %v, want stream closed before response.completed", err)
	}
}

func TestParseResponsesSSE_ProviderErrorEvents(t *testing.T) {
	t.Run("response_failed_context_length_exceeded", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input is too long for this model","param":"input"}}}`,
		})
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if err == nil {
			t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState err = nil, want APIError")
		}
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok {
			t.Fatalf("err = %T %v, want *APIError", err, err)
		}
		if apiErr.StatusCode != 0 || apiErr.Origin != APIErrorOriginSSEEvent || apiErr.Code != "context_length_exceeded" || apiErr.Param != "input" {
			t.Fatalf("apiErr = %+v, want status unknown with preserved SSE fields", apiErr)
		}
		if !IsContextLengthExceeded(err) {
			t.Fatalf("expected IsContextLengthExceeded(%v) = true", err)
		}
	})

	t.Run("error_event_preserved_over_incomplete_stream", func(t *testing.T) {
		raw := strings.Join([]string{
			"event: error",
			`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"This request exceeds the context window","param":"input"}}`,
			"",
		}, "\n")
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
		if err == nil {
			t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState err = nil, want APIError")
		}
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok {
			t.Fatalf("err = %T %v, want *APIError", err, err)
		}
		if strings.Contains(err.Error(), "incomplete SSE stream") {
			t.Fatalf("err = %v, want provider API error preserved over incomplete SSE", err)
		}
		if apiErr.Origin != APIErrorOriginSSEEvent || apiErr.StatusCode != 0 || apiErr.Code != "context_length_exceeded" || apiErr.Param != "input" {
			t.Fatalf("apiErr = %+v, want status unknown with preserved SSE fields", apiErr)
		}
	})

	t.Run("unknown_stream_error_remains_retryable_without_guessed_status", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"error","error":{"type":"future_gateway_error","code":"future_unknown_v7","message":"stream failed"}}`,
		})
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Origin != APIErrorOriginSSEEvent || apiErr.StatusCode != 0 {
			t.Fatalf("err = %T %v, want status-less SSE APIError", err, err)
		}
		if !isRetriable(err) {
			t.Fatalf("unknown SSE provider error should remain retryable: %v", err)
		}
	})

	t.Run("error_event_after_text_delta_keeps_visible_text_and_returns_api_error", func(t *testing.T) {
		raw := strings.Join([]string{
			"event: response.output_item.added",
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}`,
			"",
			"event: response.output_text.delta",
			`data: {"type":"response.output_text.delta","content_index":0,"delta":"partial","item_id":"msg_1","logprobs":[],"output_index":0}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"upstream_error","code":"upstream_connection_error","message":"Upstream response stream was interrupted"}}`,
			"",
		}, "\n")
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Code != "upstream_connection_error" {
			t.Fatalf("err = %T %v, want provider API error (partial stays on screen, no rollback)", err, err)
		}
	})

	t.Run("error_event_after_incomplete_tool_call_remains_api_error", func(t *testing.T) {
		raw := strings.Join([]string{
			"event: response.output_item.added",
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","status":"in_progress"}}`,
			"",
			"event: response.function_call_arguments.delta",
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"ech"}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"upstream_error","code":"upstream_connection_error","message":"Upstream response stream was interrupted"}}`,
			"",
		}, "\n")
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Code != "upstream_connection_error" {
			t.Fatalf("err = %T %v, want provider API error for half-built tool call", err, err)
		}
	})

	t.Run("error_event_after_complete_tool_call_returns_api_error", func(t *testing.T) {
		raw := strings.Join([]string{
			"event: response.output_item.added",
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","status":"in_progress"}}`,
			"",
			"event: response.function_call_arguments.delta",
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"git status --short\"}"}`,
			"",
			"event: response.function_call_arguments.done",
			`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"command\":\"git status --short\"}"}`,
			"",
			"event: response.output_item.done",
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"{\"command\":\"git status --short\"}","status":"completed"}}`,
			"",
			"event: error",
			`data: {"type":"error","error":{"type":"upstream_error","code":"upstream_connection_error","message":"Upstream response stream was interrupted"}}`,
			"",
		}, "\n")
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(bytes.NewReader([]byte(raw)), nil, nil, nil, "", false, false, false)
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Code != "upstream_connection_error" {
			t.Fatalf("err = %T %v, want provider API error after error event", err, err)
		}
	})

	t.Run("error_event_is_not_swallowed_by_later_completion", func(t *testing.T) {
		stream := buildSSEStream([]string{
			`{"type":"error","error":{"type":"future_gateway_error","code":"upstream_failed","message":"stream failed"}}`,
			`{"type":"response.completed","response":{"status":"completed","output":[]}}`,
		})
		_, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
		if _, ok := errors.AsType[*APIError](err); !ok {
			t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState err = %T %v, want provider API error", err, err)
		}
	})
}

// TestParseResponsesSSE_DuplicateToolCallFromProxy verifies that when a proxy
// replays the same tool call events after they've already been finalized, the parser
// deduplicates them — producing exactly 1 tool call, not 2.
func TestParseResponsesSSE_DuplicateToolCallFromProxy(t *testing.T) {
	// Simulate proxy behavior: normal sequence, then duplicate with triple-encoded args.
	stream := buildSSEStream([]string{
		// First (normal) sequence
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"echo hi\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"{\"command\":\"echo hi\"}","status":"completed"}}`,
		// Duplicate replay from proxy (same call_id, same output_index, triple-encoded args)
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","status":"in_progress","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"\"{\\\"command\\\":\\\"echo hi\\\"}\"","call_id":"call_abc","item_id":"fc_1"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"\"{\\\"command\\\":\\\"echo hi\\\"}\"","status":"completed"}}`,
		// Completion
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})

	var toolUseStarts int
	cb := func(delta message.StreamDelta) {
		if delta.Type == "tool_use_start" {
			toolUseStarts++
		}
	}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, cb, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1 (duplicate should be skipped)", len(resp.ToolCalls))
	}
	if toolUseStarts != 1 {
		t.Errorf("got %d tool_use_start callbacks, want 1 (duplicate should not trigger TUI block)", toolUseStarts)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Name != tools.NameShell {
		t.Errorf("tool call id=%q name=%q, want call_abc %s", tc.ID, tc.Name, tools.NameShell)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("tool args not valid JSON: %v", err)
	}
	if args["command"] != "echo hi" {
		t.Errorf("args[command] = %v, want \"echo hi\"", args["command"])
	}
}

func TestParseResponsesSSE_DuplicateAddedWithEmptyNameKeepsToolName(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell"}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","name":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"{\"command\":\"git status --short\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"item_id":"fc_1","arguments":"{\"command\":\"git status --short\"}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"{\"command\":\"git status --short\"}","status":"completed"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})

	var starts []message.ToolCallDelta
	var updates []message.ToolCallDelta
	cb := func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		switch delta.Type {
		case message.StreamDeltaToolUseStart:
			starts = append(starts, *delta.ToolCall)
		case message.StreamDeltaToolUseEnd:
			updates = append(updates, *delta.ToolCall)
		}
	}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, cb, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Name != tools.NameShell {
		t.Fatalf("tool call id=%q name=%q, want call_abc %s", tc.ID, tc.Name, tools.NameShell)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("tool args not valid JSON: %v", err)
	}
	if args["command"] != "git status --short" {
		t.Fatalf("args[command] = %v, want git status --short", args["command"])
	}
	if len(starts) != 1 || starts[0].ID != "call_abc" || starts[0].Name != tools.NameShell {
		t.Fatalf("tool_use_start callbacks = %+v, want one shell start", starts)
	}
	if len(updates) != 1 || updates[0].ID != "call_abc" || updates[0].Name != tools.NameShell {
		t.Fatalf("tool_use_end callbacks = %+v, want one shell end", updates)
	}
}

func TestParseResponsesSSE_FirstAddedMalformedEmitsNoToolCallbacks(t *testing.T) {
	// First (and only) output_item.added for this call is malformed: empty name and
	// no id/call_id. It must emit no start/delta/end, and the call must be discarded.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","name":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"","arguments":"{\"command\":\"pwd\"}","status":"completed"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
	})

	var starts, deltas, ends int
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		switch delta.Type {
		case message.StreamDeltaToolUseStart:
			starts++
		case message.StreamDeltaToolUseDelta:
			deltas++
		case message.StreamDeltaToolUseEnd:
			ends++
		}
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if starts != 0 || deltas != 0 || ends != 0 {
		t.Fatalf("malformed first added emitted callbacks: starts=%d deltas=%d ends=%d, want 0/0/0", starts, deltas, ends)
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want malformed call discarded", resp.ToolCalls)
	}
}

func TestParseResponsesSSE_ArgumentsDoneEmitsEarlyToolUseEnd(t *testing.T) {
	// response.function_call_arguments.done is the authoritative per-call
	// completion signal: tool_use_end must be emitted there, before
	// response.output_item.done, and output_item.done must not emit a second end.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":\"echo "}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"hi\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"command\":\"echo hi\"}","call_id":"call_abc","item_id":"fc_1"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"{\"command\":\"echo hi\"}","status":"completed"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})

	var events []string
	cb := func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		switch delta.Type {
		case message.StreamDeltaToolUseStart:
			events = append(events, "start")
		case message.StreamDeltaToolUseDelta:
			events = append(events, "delta")
		case message.StreamDeltaToolUseEnd:
			events = append(events, "end")
		}
	}

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, cb, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(resp.ToolCalls), resp.ToolCalls)
	}
	var args map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("tool args not valid JSON: %v", err)
	}
	if args["command"] != "echo hi" {
		t.Fatalf("args[command] = %v, want echo hi", args["command"])
	}
	want := []string{"start", "delta", "delta", "end"}
	if len(events) != len(want) {
		t.Fatalf("callback events = %v, want %v (single end emitted before output_item.done)", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("callback events = %v, want %v", events, want)
		}
	}
}

func TestParseResponsesSSE_CustomInputDoneEmitsEarlyToolUseEnd(t *testing.T) {
	// Same contract for custom_tool_call: custom_tool_call_input.done ends the
	// stream early, output_item.done must not emit a duplicate end, and the
	// final freeform input still comes from output_item.done.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch"}}`,
		`{"type":"response.custom_tool_call_input.delta","item_id":"ct_1","delta":"*** Begin Patch\n*** End Patch"}`,
		`{"type":"response.custom_tool_call_input.done","item_id":"ct_1","input":"*** Begin Patch\n*** End Patch"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"custom_tool_call","id":"ct_1","name":"apply_patch","status":"completed","input":"*** Begin Patch\n*** End Patch"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"custom_tool_call"}],"usage":{"input_tokens":100,"output_tokens":50}}}`,
	})

	var starts, deltas, ends int
	var endIDs []string
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		switch delta.Type {
		case message.StreamDeltaToolUseStart:
			starts++
		case message.StreamDeltaToolUseDelta:
			deltas++
		case message.StreamDeltaToolUseEnd:
			ends++
			endIDs = append(endIDs, delta.ToolCall.ID)
		}
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if starts != 1 || deltas != 1 || ends != 1 {
		t.Fatalf("callbacks: starts=%d deltas=%d ends=%d, want 1/1/1", starts, deltas, ends)
	}
	if len(endIDs) != 1 || endIDs[0] != "ct_1" {
		t.Fatalf("tool_use_end ids = %v, want single ct_1", endIDs)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != tools.NameApplyPatch {
		t.Fatalf("tool calls = %+v, want one apply_patch", resp.ToolCalls)
	}
}

func TestParseResponsesSSE_LateNameEmitsPairedToolCallbacks(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"{\"command\":\"pwd\"}","status":"completed"}}`,
		"[DONE]",
	})

	var events []string
	var toolDeltas []message.ToolCallDelta
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.ToolCall == nil {
			return
		}
		events = append(events, delta.Type)
		toolDeltas = append(toolDeltas, *delta.ToolCall)
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(events) != 2 || events[0] != message.StreamDeltaToolUseStart || events[1] != message.StreamDeltaToolUseEnd {
		t.Fatalf("tool callback events = %+v, want paired start/end only", events)
	}
	for _, delta := range toolDeltas {
		if delta.ID != "fc_1" || delta.Name != tools.NameShell {
			t.Fatalf("tool callback = %+v, want stable fc_1 shell", delta)
		}
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_abc" || resp.ToolCalls[0].Name != tools.NameShell {
		t.Fatalf("final tool calls = %+v, want provider call_id call_abc", resp.ToolCalls)
	}
}

func TestParseResponsesSSE_StartEndUseStableStreamIDWhenDoneAddsCallID(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"shell","arguments":"{\"command\":\"pwd\"}","status":"completed"}}`,
		"[DONE]",
	})

	var starts []message.ToolCallDelta
	var deltas []message.ToolCallDelta
	var ends []message.ToolCallDelta
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
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
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(starts) != 1 || len(deltas) != 1 || len(ends) != 1 {
		t.Fatalf("starts=%+v deltas=%+v ends=%+v", starts, deltas, ends)
	}
	for _, delta := range []message.ToolCallDelta{starts[0], deltas[0], ends[0]} {
		if delta.ID != "fc_1" || delta.Name != tools.NameShell {
			t.Fatalf("stream callback = %+v, want stable fc_1 shell", delta)
		}
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_abc" || resp.ToolCalls[0].Name != tools.NameShell {
		t.Fatalf("final tool calls = %+v, want provider call_id call_abc", resp.ToolCalls)
	}
}

// TestParseResponsesSSE_ExecuteShellTool runs the full flow: parse SSE → UnwrapToolArgs → Registry.Execute(shell).
// It verifies that parsed tool-call args are valid for real tool execution (no "cannot unmarshal string" etc.).
func TestParseResponsesSSE_ExecuteShellTool(t *testing.T) {
	// Simulate API stream: one shell tool call with command "echo chord-ok".
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"echo chord-ok"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell","status":"completed"}}`,
		"[DONE]",
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != tools.NameShell {
		t.Fatalf("tool name = %q, want %s", tc.Name, tools.NameShell)
	}

	// Same path as agent: UnwrapToolArgs then Execute.
	args := UnwrapToolArgs(tc.Args)
	reg := tools.NewRegistry()
	reg.Register(tools.NewShellTool("bash"))
	ctx := context.Background()
	out, execErr := reg.Execute(ctx, tc.Name, args)
	if execErr != nil {
		t.Fatalf("shell Execute failed (args may be wrong type for tools.shellArgs): %v", execErr)
	}
	if !strings.Contains(out, "chord-ok") {
		t.Errorf("shell output = %q, want to contain \"chord-ok\"", out)
	}
}

// TestParseResponsesSSE_ExecuteShellTool_DoneArgumentsAsString runs the same flow when the API
// sends arguments only in done event as a JSON string (as some proxies do). Execute must still succeed.
func TestParseResponsesSSE_ExecuteShellTool_DoneArgumentsAsString(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_2","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_2","name":"shell","arguments":"{\"command\":\"echo done-args\"}","status":"completed"}}`,
		"[DONE]",
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	args := UnwrapToolArgs(tc.Args)
	reg := tools.NewRegistry()
	reg.Register(tools.NewShellTool("bash"))
	ctx := context.Background()
	out, execErr := reg.Execute(ctx, tc.Name, args)
	if execErr != nil {
		t.Fatalf("shell Execute failed (done.arguments as string must unwrap to object): %v", execErr)
	}
	if !strings.Contains(out, "done-args") {
		t.Errorf("shell output = %q, want to contain \"done-args\"", out)
	}
}

// TestParseResponsesSSE_TruncatedStream verifies that when the API sends response.incomplete
// (output truncation, e.g. max_output_tokens), we do not append in-progress tool calls and set StopReason=length.
// This prevents "review uncommitted code" / large git diff scenarios from being counted as malformed.
func TestParseResponsesSSE_TruncatedStream(t *testing.T) {
	// Simulate: model starts Shell (e.g. git diff), stream is truncated before output_item.done.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"git diff --staged"}`,
		// No output_item.done; stream ends with response.incomplete (truncation).
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":5000,"output_tokens":32000}}}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"Shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"git "}`,
		// No more deltas, no output_item.done; stream ends with [DONE]. Args are invalid JSON.
		"[DONE]",
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.ProviderResponseID != "resp-abc123" {
		t.Errorf("ProviderResponseID = %q, want resp-abc123", resp.ProviderResponseID)
	}
}

func TestParseResponsesSSE_ReturnsOnTerminalDataLineWithoutBlankDelimiter(t *testing.T) {
	reader := &blockingAfterPayloadReader{payload: []byte(`data: {"type":"response.completed","response":{"id":"resp-no-blank","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")}
	done := make(chan struct{})
	var resp *message.Response
	var err error
	go func() {
		resp, _, err = parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState did not return after terminal response.completed data line")
	}
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp == nil || resp.ProviderResponseID != "resp-no-blank" || resp.StopReason != "stop" {
		t.Fatalf("response = %#v, want resp-no-blank stop", resp)
	}
}

func TestParseResponsesSSE_ReturnsOnStandardTerminalEventWithoutBlankDelimiter(t *testing.T) {
	reader := &blockingAfterPayloadReader{payload: []byte("event: response.completed\n" + `data: {"response":{"id":"resp-standard-no-blank","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n")}
	done := make(chan struct{})
	var resp *message.Response
	var err error
	go func() {
		resp, _, err = parseResponsesSSEWithOutputItemsAndTurnState(reader, nil, nil, nil, "", false, false, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("parseResponsesSSEWithOutputItemsAndTurnState did not return after standard terminal response.completed data line")
	}
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp == nil || resp.ProviderResponseID != "resp-standard-no-blank" || resp.StopReason != "stop" {
		t.Fatalf("response = %#v, want resp-standard-no-blank stop", resp)
	}
}

func TestParseResponsesSSEWithOutputItems_CompletedOutputToBaseline(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":"{\"file\":\"README.md\"}"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	})
	resp, outputItems, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
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
	resp, outputItems, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
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

// TestParseResponsesSSE_ReviewUncommittedCode_E2E simulates "review uncommitted code" flow: parse a truncated
// stream (Shell git diff started but output truncated), then ensure we do not produce malformed tool calls
// and StopReason is length so the agent can suggest new conversation / max_output_tokens.
func TestParseResponsesSSE_ReviewUncommittedCode_E2E(t *testing.T) {
	// Scenario: user says "review uncommitted code", model starts Shell with "git diff --staged", output is truncated.
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"shell_1","call_id":"call_shell","name":"Shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\""}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"git diff --staged"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`,
		// Simulate truncation before done (e.g. token limit hit). API sends response.incomplete.
		`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10000,"output_tokens":32000}}}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
