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
		{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-1", Summary: []responsesReasoningSummaryPayload{{Type: "summary_text", Text: "thinking about it"}}},
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
				{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-1"},
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
	if r1.ID != "" || r1.EncryptedContent != "enc-1" {
		t.Fatalf("unexpected reasoning item: %+v", r1)
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
