package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestCollectResponsesOutputPreservesProviderOrder(t *testing.T) {
	resp := &message.Response{}
	output := []responsesOutputEntry{
		{Type: "reasoning", ID: "rs_1", Content: []responsesContentBlock{{Type: "reasoning_text", Text: "visible reasoning"}}, EncryptedContent: "enc-1", Summary: []responsesReasoningSummaryPayload{{Type: "summary_text", Text: "thinking about it"}}},
		{Type: "message", ID: "msg_1", Role: "assistant", Phase: "final_answer", Content: []responsesContentBlock{{Type: "output_text", Text: "working"}}},
		{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: "{}"},
		{Type: "reasoning", ID: "rs_2", EncryptedContent: "enc-2"},
		{Type: "reasoning", ID: "rs_missing_payload"}, // summary-only reasoning remains replayable
	}
	collectResponsesOutput(resp, output)

	if len(resp.ResponsesOutput) != 5 {
		t.Fatalf("expected 5 output items, got %d", len(resp.ResponsesOutput))
	}
	if got := []string{resp.ResponsesOutput[0].Type, resp.ResponsesOutput[1].Type, resp.ResponsesOutput[2].Type, resp.ResponsesOutput[3].Type}; strings.Join(got, ",") != "reasoning,message,function_call,reasoning" {
		t.Fatalf("unexpected output order: %+v", got)
	}
	if len(resp.ResponsesOutput[0].Summary) != 1 || resp.ResponsesOutput[0].Summary[0].Text != "thinking about it" {
		t.Fatalf("unexpected summary: %+v", resp.ResponsesOutput[0].Summary)
	}
	if got := resp.ResponsesOutput[0].Content; len(got) != 1 || got[0].Type != "reasoning_text" || got[0].Text != "visible reasoning" {
		t.Fatalf("plaintext reasoning content was not retained: %+v", got)
	}
	if resp.ResponsesOutput[1].Phase != "final_answer" || resp.ResponsesOutput[1].Content[0].Text != "working" {
		t.Fatalf("unexpected message item: %+v", resp.ResponsesOutput[1])
	}

	// Recollection must replace, not append (completed after incomplete).
	collectResponsesOutput(resp, output)
	if len(resp.ResponsesOutput) != 5 {
		t.Fatalf("expected recollection to replace items, got %d", len(resp.ResponsesOutput))
	}
}

func TestConvertMessagesReplaysResponsesOutputInProviderOrder(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do the thing"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "reasoning", ID: "rs_1", Content: []message.ResponsesOutputContent{{Type: "reasoning_text", Text: "visible reasoning"}}},
				{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: `{}`},
				{Type: "reasoning", ID: "rs_2", EncryptedContent: "enc-2", Summary: []message.ResponsesReasoningSummary{{Type: "summary_text", Text: "s"}}},
			},
			ToolCalls: []message.ToolCall{{ID: "call_1", Name: "read", Args: json.RawMessage(`{}`)}},
		},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "file contents"},
	}

	items := convertMessagesToResponses("sys", msgs)

	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Type)
	}
	got := strings.Join(kinds, ",")
	want := "message,message,reasoning,function_call,reasoning,function_call_output"
	if got != want {
		t.Fatalf("unexpected item order: got %v want %v", got, want)
	}

	r1 := items[2]
	if r1.ID != "" {
		t.Fatalf("unexpected reasoning item: %+v", r1)
	}
	reasoningContent, ok := r1.Content.([]responsesReasoningContentBlock)
	if !ok || len(reasoningContent) != 1 || reasoningContent[0].Type != "reasoning_text" || reasoningContent[0].Text != "visible reasoning" {
		t.Fatalf("plaintext reasoning content was not replayed: %#v", r1.Content)
	}
	// Empty summary must serialize as [] (not be omitted): API rejects a
	// reasoning input item without a summary field.
	raw, err := json.Marshal(r1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"summary":[]`) {
		t.Fatalf("expected explicit empty summary array, got %s", raw)
	}
	raw2, err := json.Marshal(items[4])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw2), `"summary":[{"type":"summary_text","text":"s"}]`) {
		t.Fatalf("expected summary_text replay, got %s", raw2)
	}
	// Non-reasoning items must not leak a summary field.
	raw3, err := json.Marshal(items[3])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw3), "summary") {
		t.Fatalf("function_call must not carry summary field: %s", raw3)
	}
	withIDs := convertMessagesToResponsesWithItemIDs("", msgs[1:2], true)
	if len(withIDs) != 3 || withIDs[0].ID != "rs_1" || withIDs[1].ID != "fc_1" {
		t.Fatalf("stored replay must preserve item ids: %+v", withIDs)
	}
}

func TestConvertMessagesReplaysResponsesOutputWithOutOfOrderToolResults(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do the thing"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "function_call", ID: "fc_1", CallID: "call_1", Name: "read", Arguments: `{}`},
				{Type: "function_call", ID: "fc_2", CallID: "call_2", Name: "grep", Arguments: `{}`},
			},
			ToolCalls: []message.ToolCall{
				{ID: "call_1", Name: "read", Args: json.RawMessage(`{}`)},
				{ID: "call_2", Name: "grep", Args: json.RawMessage(`{}`)},
			},
		},
		{Role: message.RoleTool, ToolCallID: "call_2", Content: "grep result"},
		{Role: message.RoleTool, ToolCallID: "call_1", Content: "read result"},
	}

	items := convertMessagesToResponses("sys", msgs)
	if len(items) != 6 {
		t.Fatalf("unexpected item count: got %d want 6 (%+v)", len(items), items)
	}
	want := []struct {
		typ    string
		callID string
		output string
	}{
		{typ: "message"},
		{typ: "message"},
		{typ: "function_call", callID: "call_1"},
		{typ: "function_call_output", callID: "call_1", output: "read result"},
		{typ: "function_call", callID: "call_2"},
		{typ: "function_call_output", callID: "call_2", output: "grep result"},
	}
	for i, item := range items {
		if item.Type != want[i].typ || item.CallID != want[i].callID {
			t.Fatalf("item[%d] = %+v, want type=%q call_id=%q", i, item, want[i].typ, want[i].callID)
		}
		if want[i].output != "" && item.Output != want[i].output {
			t.Fatalf("item[%d] output = %q, want %q", i, item.Output, want[i].output)
		}
	}
}

func TestConvertMessagesReplaysResponsesOutputWithInterleavedReasoningAndAdjacentOutputs(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do the thing"},
		{
			Role: message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{
				{Type: "function_call", ID: "fc_1", CallID: "call_A", Name: "read", Arguments: `{}`},
				{Type: "reasoning", ID: "rs_1", Content: []message.ResponsesOutputContent{{Type: "reasoning_text", Text: "thinking"}}},
				{Type: "function_call", ID: "fc_2", CallID: "call_B", Name: "grep", Arguments: `{}`},
			},
		},
		{Role: message.RoleTool, ToolCallID: "call_A", Content: "read result"},
		{Role: message.RoleTool, ToolCallID: "call_B", Content: "grep result"},
	}

	items := convertMessagesToResponses("sys", msgs)
	var outputs []responsesInputItem
	for _, item := range items {
		if item.Type == "function_call_output" {
			outputs = append(outputs, item)
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("function_call_output count = %d, want 2 (%+v)", len(outputs), items)
	}
	if outputs[0].CallID != "call_A" || outputs[0].Output != "read result" {
		t.Fatalf("first output = %+v, want call_A/read result", outputs[0])
	}
	if outputs[1].CallID != "call_B" || outputs[1].Output != "grep result" {
		t.Fatalf("second output = %+v, want call_B/grep result", outputs[1])
	}
}

func TestConvertMessagesLeavesOrphanToolResultForToolBranchAfterNativeReplay(t *testing.T) {
	msgs := []message.Message{
		{Role: message.RoleUser, Content: "do the thing"},
		{
			Role:            message.RoleAssistant,
			ResponsesOutput: []message.ResponsesOutputItem{{Type: "function_call", ID: "fc_1", CallID: "call_A", Name: "read", Arguments: `{}`}},
		},
		{Role: message.RoleTool, ToolCallID: "orphan", Content: "orphan result"},
		{Role: message.RoleTool, ToolCallID: "call_A", Content: "read result"},
	}

	items := convertMessagesToResponses("sys", msgs)
	var outputs []responsesInputItem
	for _, item := range items {
		if item.Type == "function_call_output" {
			outputs = append(outputs, item)
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("function_call_output count = %d, want 2 (%+v)", len(outputs), items)
	}
	if outputs[0].CallID != "orphan" || outputs[0].Output != "orphan result" {
		t.Fatalf("first output = %+v, want orphan/orphan result", outputs[0])
	}
	if outputs[1].CallID != "call_A" || outputs[1].Output != "read result" {
		t.Fatalf("second output = %+v, want call_A/read result", outputs[1])
	}
}

func TestConvertMessagesToResponsesAddsEmptyTextBlockForEmptyUserParts(t *testing.T) {
	msgs := []message.Message{{
		Role: message.RoleUser,
		Parts: []message.ContentPart{
			{Type: "text", Text: ""},
			{Type: "text", Text: ""},
		},
	}}
	items := convertMessagesToResponses("", msgs)
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	content, ok := items[0].Content.([]responsesContentBlock)
	if !ok || len(content) != 1 || content[0].Type != "input_text" || content[0].Text != "" {
		t.Fatalf("responses content = %#v, want one empty input_text block", items[0].Content)
	}
}

func TestResponsesOutputConversionPreservesIncrementalState(t *testing.T) {
	resp := &message.Response{ResponsesOutput: []message.ResponsesOutputItem{
		{Type: "reasoning", ID: "rs-1", EncryptedContent: "enc", Summary: []message.ResponsesReasoningSummary{}},
		{Type: "message", ID: "msg-1", Role: "assistant", Phase: "commentary", Content: []message.ResponsesOutputContent{{Type: "refusal", Refusal: "not allowed"}}},
		{Type: "function_call", ID: "fc-1", CallID: "call-1", Name: "read", Arguments: `{}`},
	}}
	items := responsesResponseToInputItems(resp)
	if len(items) != 3 || items[0].Type != "reasoning" || items[1].Phase != "commentary" || items[2].Type != "function_call" {
		t.Fatalf("incremental output state was not preserved: %+v", items)
	}
	if items[0].ID != "rs-1" || items[1].ID != "msg-1" || items[2].ID != "fc-1" {
		t.Fatalf("incremental item ids were not preserved: %+v", items)
	}
	content, ok := items[1].Content.([]responsesContentBlock)
	if !ok || len(content) != 1 || content[0].Refusal != "not allowed" {
		t.Fatalf("refusal content was not preserved: %#v", items[1].Content)
	}
}

func TestConvertResponsesOutputItemDropsEmptyStatelessReasoning(t *testing.T) {
	item := message.ResponsesOutputItem{Type: "reasoning", ID: "rs-1"}
	if converted, ok := convertResponsesOutputItem(item, false); ok {
		t.Fatalf("empty stateless reasoning must be dropped: %+v", converted)
	}
	if converted, ok := convertResponsesOutputItem(item, true); !ok || converted.ID != "rs-1" {
		t.Fatalf("stored reasoning reference must be kept: %+v ok=%v", converted, ok)
	}
	item.EncryptedContent = "enc"
	if converted, ok := convertResponsesOutputItem(item, false); !ok || converted.ID != "" || converted.EncryptedContent != "enc" {
		t.Fatalf("encrypted stateless reasoning must be kept without id: %+v ok=%v", converted, ok)
	}
	item.EncryptedContent = ""
	item.Content = []message.ResponsesOutputContent{{Type: "reasoning_text", Text: "visible reasoning"}}
	if converted, ok := convertResponsesOutputItem(item, false); !ok {
		t.Fatalf("plaintext stateless reasoning must be kept: %+v ok=%v", converted, ok)
	}
}

func TestApplyResponsesCompletionPayloadExposesRefusal(t *testing.T) {
	resp := &message.Response{}
	applyResponsesCompletionPayload(resp, responsesCompletedPayload{Output: []responsesOutputEntry{{
		Type:    "message",
		Content: []responsesContentBlock{{Type: "refusal", Refusal: "not allowed"}},
	}}}, nil)
	if resp.Content != "not allowed" || len(resp.ResponsesOutput) != 1 || resp.ResponsesOutput[0].Content[0].Refusal != "not allowed" {
		t.Fatalf("refusal was not exposed and retained: %+v", resp)
	}
}

func TestCollectResponsesOutputNormalizesRelayEntries(t *testing.T) {
	resp := &message.Response{}
	collectResponsesOutput(resp, []responsesOutputEntry{
		{Type: "message", ID: "msg_1", Content: []responsesContentBlock{{Type: "output_text", Text: "hi"}}},
		{Type: "function_call", ID: "fc_1", Name: "read", Arguments: "{}"},
	})
	if len(resp.ResponsesOutput) != 2 {
		t.Fatalf("expected both relay entries collected, got %+v", resp.ResponsesOutput)
	}
	if resp.ResponsesOutput[0].Role != "assistant" {
		t.Fatalf("message role was not defaulted: %+v", resp.ResponsesOutput[0])
	}
	// The streaming accumulator falls back to the item id when call_id is
	// missing, so the collected item must too or its output would be orphaned.
	if resp.ResponsesOutput[1].CallID != "fc_1" {
		t.Fatalf("function_call call_id fallback missing: %+v", resp.ResponsesOutput[1])
	}
}

func TestFillResponsesReasoningForReplay(t *testing.T) {
	user := responsesInputItem{Type: "message", Role: "user", Content: []responsesContentBlock{{Type: "input_text", Text: "hi"}}}
	assistant := responsesInputItem{Type: "message", Role: "assistant", Content: []responsesContentBlock{{Type: "output_text", Text: "done"}}}
	call := responsesInputItem{Type: "function_call", Name: "read", CallID: "call_1", Arguments: `{}`}
	output := responsesInputItem{Type: "function_call_output", CallID: "call_1", Output: "result"}
	reasoning := responsesInputItem{Type: "reasoning", Content: []responsesReasoningContentBlock{{Type: "reasoning_text", Text: "think"}}, Summary: &[]responsesReasoningSummaryPayload{}}

	tests := []struct {
		name string
		in   []responsesInputItem
		want []string // item types after fill
	}{
		{
			name: "assistant turn without reasoning gets synthesized item",
			in:   []responsesInputItem{user, assistant},
			want: []string{"message", "reasoning", "message"},
		},
		{
			name: "native reasoning is not duplicated",
			in:   []responsesInputItem{user, reasoning, assistant},
			want: []string{"message", "reasoning", "message"},
		},
		{
			name: "tool-call-only turn gets synthesized item",
			in:   []responsesInputItem{user, call, output, call},
			want: []string{"message", "reasoning", "function_call", "function_call_output", "reasoning", "function_call"},
		},
		{
			name: "consecutive function calls share one turn",
			in:   []responsesInputItem{user, call, call},
			want: []string{"message", "reasoning", "function_call", "function_call"},
		},
		{
			name: "empty input stays empty",
			in:   nil,
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fillResponsesReasoningForReplay(tt.in)
			var kinds []string
			for _, it := range got {
				kinds = append(kinds, it.Type)
			}
			if strings.Join(kinds, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("item types = %v, want %v", kinds, tt.want)
			}
		})
	}

	// Synthesized items must serialize reasoning_text content and an explicit
	// empty summary array, matching the provider's presence contract.
	filled := fillResponsesReasoningForReplay([]responsesInputItem{user, assistant})
	raw, err := json.Marshal(filled[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reasoning_text"`) {
		t.Fatalf("synthesized item missing reasoning_text content: %s", raw)
	}
	if !strings.Contains(string(raw), `"summary":[]`) {
		t.Fatalf("synthesized item missing explicit empty summary: %s", raw)
	}
	if strings.Contains(string(raw), `"id"`) {
		t.Fatalf("synthesized stateless reasoning item must not carry an id: %s", raw)
	}
}
