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
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaToolUseDelta && delta.ToolCall != nil {
			got = append(got, delta.ToolCall.InputText)
		}
	}, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
	if _, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaReasoningItem && delta.ReasoningItem != nil {
			got = append(got, *delta.ReasoningItem)
		}
	}, nil, nil, "", false); err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
	if _, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaReasoningItem && delta.ReasoningItem != nil {
			got = append(got, *delta.ReasoningItem)
		}
	}, nil, nil, "", false); err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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
	if _, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaReasoningItem && delta.ReasoningItem != nil {
			got = append(got, delta.ReasoningItem.ID)
		}
	}, nil, nil, "", false); err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
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

// The terminal reconciliation tests below lock in the fix for relays that
// damage multi-byte text upstream (deltas arrive with U+FFFD already encoded
// in them) while the completed/incomplete payload still carries clean text.

func TestParseResponsesSSECompletedAdoptsCleanTerminalText(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"da"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"ma"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"ged \ufffd text"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"damaged text"}]}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "damaged text" {
		t.Fatalf("Content = %q, want clean terminal text", resp.Content)
	}
}

func TestParseResponsesSSEIncompleteAdoptsCleanTerminalText(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial \ufffd"}`,
		`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"partial clean"}]}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "partial clean" {
		t.Fatalf("Content = %q, want clean terminal text", resp.Content)
	}
	if resp.StopReason != "length" {
		t.Fatalf("StopReason = %q, want length", resp.StopReason)
	}
}

// A terminal message without a content field does not provide text: content
// accumulated from deltas must survive it.
func TestParseResponsesSSECompletedWithoutContentKeepsDeltaText(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"streamed reply"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant"}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "streamed reply" {
		t.Fatalf("Content = %q, want delta accumulation preserved", resp.Content)
	}
}

// An explicit empty output_text part is an authoritative empty, not a missing
// field: the terminal adopts the empty over any damaged delta accumulation.
func TestParseResponsesSSECompletedEmptyOutputTextAdoptsEmpty(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"\ufffd"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}]}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "" {
		t.Fatalf("Content = %q, want explicit terminal empty", resp.Content)
	}
}

// Multiple message items reconcile in structural order regardless of the
// interleaving of their deltas.
func TestParseResponsesSSECompletedMultiItemStructuralOrder(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_2","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_2","output_index":1,"content_index":0,"delta":"second \ufffd"}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"first \ufffd"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"first"}]},{"type":"message","id":"msg_2","role":"assistant","content":[{"type":"output_text","text":"second"}]}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "firstsecond" {
		t.Fatalf("Content = %q, want structural order join", resp.Content)
	}
}

// A stream that dies after output_item.done keeps the done item's captured
// text and the unfinished item's delta accumulation.
func TestParseResponsesSSENoTerminalKeepsDoneCaptureAndDeltas(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_2","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"damaged \ufffd"}`,
		`{"type":"response.output_text.delta","item_id":"msg_2","output_index":1,"content_index":0,"delta":"plain tail"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"clean head"}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "clean headplain tail" {
		t.Fatalf("Content = %q, want done capture for finished item and delta tail for the open one", resp.Content)
	}
}

// response.completed is terminal for the stream: events after it must not
// reopen or mutate content.
func TestParseResponsesSSEEventsAfterCompletedDoNotMutateContent(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"clean"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"clean"}]}]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":" \ufffd after"}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "clean" {
		t.Fatalf("Content = %q, want terminal text unchanged by post-completed events", resp.Content)
	}
}

// The refusal backfill inside applyResponsesCompletionPayload must survive the
// final content flush: a refusal-only response streams no text deltas, so the
// flush would previously wipe the backfilled refusal with an empty builder.
func TestParseResponsesSSERefusalBackfillSurvivesFlush(t *testing.T) {
	stream := buildSSEStream([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"cannot help with that"}]}]}}`,
		`{"type":"[DONE]"}`,
	})
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(stream, nil, nil, nil, "", false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if resp.Content != "cannot help with that" {
		t.Fatalf("Content = %q, want refusal backfill preserved", resp.Content)
	}
}
