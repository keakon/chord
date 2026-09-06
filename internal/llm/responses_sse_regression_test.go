package llm

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

// These tests lock in the Responses SSE recovery behaviors pi fixed separately:
// encrypted_content that only appears in the terminal event, null message
// content before tool calls, and out-of-order output_item.done events.

func TestApplyResponsesCompletionPayloadKeepsTerminalOnlyEncryptedReasoning(t *testing.T) {
	resp := &message.Response{}
	payload := responsesCompletedPayload{
		Output: []responsesOutputEntry{
			{Type: "reasoning", ID: "rs_1", EncryptedContent: "opaque-encrypted"},
			{Type: "message", ID: "msg_1", Role: "assistant", Content: []responsesContentBlock{{Type: "output_text", Text: "done"}}},
		},
	}
	applyResponsesCompletionPayload(resp, payload, nil)
	if len(resp.ResponsesOutput) != 2 {
		t.Fatalf("ResponsesOutput = %+v, want reasoning + message", resp.ResponsesOutput)
	}
	if resp.ResponsesOutput[0].Type != "reasoning" || resp.ResponsesOutput[0].EncryptedContent != "opaque-encrypted" {
		t.Fatalf("terminal-only encrypted reasoning not preserved: %+v", resp.ResponsesOutput[0])
	}
	if resp.StopReason != "stop" {
		t.Fatalf("StopReason = %q, want stop", resp.StopReason)
	}
}

func TestApplyResponsesCompletionPayloadToleratesNullMessageContentBeforeToolCall(t *testing.T) {
	resp := &message.Response{}
	payload := responsesCompletedPayload{
		Output: []responsesOutputEntry{
			{Type: "message", ID: "msg_1", Role: "assistant"},
			{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "Read", Arguments: "{}"},
		},
	}
	applyResponsesCompletionPayload(resp, payload, nil)
	if resp.StopReason != "tool_calls" {
		t.Fatalf("StopReason = %q, want tool_calls", resp.StopReason)
	}
	if len(resp.ResponsesOutput) != 1 || resp.ResponsesOutput[0].Type != "function_call" {
		t.Fatalf("ResponsesOutput = %+v, want only function_call", resp.ResponsesOutput)
	}
	if resp.Content != "" {
		t.Fatalf("Content = %q, want empty", resp.Content)
	}
}

func TestParseResponsesSSEOutOfOrderItemDoneKeepsTerminalOrder(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":"{}"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Read","arguments":"{}"}]}}`,
	})
	resp, err := parseResponsesSSE(stream, nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ResponsesOutput) != 2 {
		t.Fatalf("ResponsesOutput = %+v, want 2 items", resp.ResponsesOutput)
	}
	if resp.ResponsesOutput[0].Type != "reasoning" || resp.ResponsesOutput[0].EncryptedContent != "opaque" {
		t.Fatalf("reasoning order/content = %+v", resp.ResponsesOutput[0])
	}
	if resp.ResponsesOutput[1].Type != "function_call" || resp.ResponsesOutput[1].Name != "Read" {
		t.Fatalf("function_call order = %+v", resp.ResponsesOutput[1])
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "Read" {
		t.Fatalf("ToolCalls = %+v", resp.ToolCalls)
	}
}

func TestParseResponsesSSECustomToolCallEmitsInputDeltas(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","name":"apply_patch"}}`,
		`{"type":"response.custom_tool_call_input.delta","item_id":"item_1","delta":"*** Begin Patch\n"}`,
		`{"type":"response.custom_tool_call_input.delta","item_id":"item_1","delta":"*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"item_1","name":"apply_patch","input":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"custom_tool_call","id":"item_1","name":"apply_patch","input":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}]}}`,
	})
	var got []string
	resp, err := parseResponsesSSE(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseDelta && delta.ToolCall != nil {
			got = append(got, delta.ToolCall.InputText)
		}
	}, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	want := []string{
		"*** Begin Patch\n",
		"*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch",
	}
	if len(got) != len(want) {
		t.Fatalf("input delta count = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("input delta %d = %q, want %q", i, got[i], want[i])
		}
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	if string(resp.ToolCalls[0].Args) != `{"patch":"*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** End Patch"}` {
		t.Fatalf("final tool args = %q, want complete canonical patch", resp.ToolCalls[0].Args)
	}
}

// TestParseResponsesSSEEmitsReasoningItemDeltaOnDone verifies that a finalized
// reasoning item (response.output_item.done with encrypted_content) is surfaced
// via a StreamDeltaReasoningItem delta even when response.completed never
// arrives. Streaming does not fold reasoning into resp.ResponsesOutput until the
// terminal event, so without this delta an interrupted turn loses the reasoning
// and the next request sees an orphan message (Responses API 400).
func TestParseResponsesSSEEmitsReasoningItemDeltaOnDone(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-reasoning-blob"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque-reasoning-blob"}}`,
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"partial reply"}`,
		`{"type":"[DONE]"}`,
	})
	var got []message.ResponsesOutputItem
	if _, err := parseResponsesSSE(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaReasoningItem && delta.ReasoningItem != nil {
			got = append(got, *delta.ReasoningItem)
		}
	}, nil); err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reasoning_item delta count = %d, want 1 (%+v)", len(got), got)
	}
	item := got[0]
	if item.Type != "reasoning" || item.ID != "rs_1" || item.EncryptedContent != "opaque-reasoning-blob" {
		t.Fatalf("reasoning_item delta = %+v, want reasoning/rs_1/opaque-reasoning-blob", item)
	}
}

// A reasoning item with no encrypted payload carries nothing replay can use:
// the summary and reasoning_text this event does not parse are human-readable
// annotations, and an id-only item would replay as a reference to state the
// target may never have stored. It must not be surfaced.
func TestParseResponsesSSESkipsReasoningItemWithoutEncryptedContent(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought about it"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"partial reply"}`,
		`{"type":"[DONE]"}`,
	})
	var got []message.ResponsesOutputItem
	if _, err := parseResponsesSSE(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaReasoningItem && delta.ReasoningItem != nil {
			got = append(got, *delta.ReasoningItem)
		}
	}, nil); err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("reasoning_item deltas = %+v, want none for an item with no encrypted payload", got)
	}
}

// TestParseResponsesSSEEmitsReasoningItemDeltaPerFinalizedItem verifies each
// finalized reasoning item emits exactly one delta (one per output_item.done),
// and output_item.added does not emit one.
func TestParseResponsesSSEEmitsReasoningItemDeltaPerFinalizedItem(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"a"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"reasoning","id":"rs_2","encrypted_content":"b"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"a"}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"rs_2","encrypted_content":"b"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"a"},{"type":"reasoning","id":"rs_2","encrypted_content":"b"}]}}`,
	})
	var got []string
	if _, err := parseResponsesSSE(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaReasoningItem && delta.ReasoningItem != nil {
			got = append(got, delta.ReasoningItem.ID)
		}
	}, nil); err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	want := []string{"rs_1", "rs_2"}
	if len(got) != len(want) {
		t.Fatalf("reasoning_item delta ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reasoning_item delta %d = %q, want %q", i, got[i], want[i])
		}
	}
}
