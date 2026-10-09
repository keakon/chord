package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/toolname"
)

func exportNativeReceipt() *message.NativeToolHistory {
	return &message.NativeToolHistory{
		Target: "sample/test-model", Protocol: "responses", APIURL: "https://example.invalid/v1/responses",
		RequestIDs: []string{"request-1"}, Authorization: message.NativeToolAuthorization{Tool: toolname.WebSearch, Contract: "sample.contract", Constraints: json.RawMessage(`{"max_uses":2}`)},
		Items: []json.RawMessage{json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"Sample fact","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"A","start_index":0,"end_index":6}]}]}`), json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque-replay"}`)},
		Calls: []message.HostedCall{{ID: "search-1", Name: toolname.WebSearch, Input: json.RawMessage(`{"query":"sample"}`), Result: json.RawMessage(`[]`)}},
	}
}

func TestExportNativeReceiptRoundTripAndIsolation(t *testing.T) {
	original := exportNativeReceipt()
	exported, err := Export([]message.Message{{Role: message.RoleAssistant, NativeTools: original}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(exported)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ExportedSession
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.ToMessages()[0].NativeTools, original) {
		t.Fatal("JSON roundtrip lost the native receipt")
	}
	exported.Messages[0].NativeTools.RequestIDs[0] = "changed"
	exported.Messages[0].NativeTools.Items[0][0] = 'X'
	exported.Messages[0].NativeTools.Calls[0].Input[0] = 'X'
	if original.RequestIDs[0] != "request-1" || original.Items[0][0] != '{' || original.Calls[0].Input[0] != '{' {
		t.Fatal("export shares mutable receipt data with canonical history")
	}
	restored := decoded.ToMessages()
	restored[0].NativeTools.Authorization.Constraints[0] = 'X'
	restored[0].NativeTools.Calls[0].Result[0] = 'X'
	if decoded.Messages[0].NativeTools.Authorization.Constraints[0] != '{' || decoded.Messages[0].NativeTools.Calls[0].Result[0] != '[' {
		t.Fatal("restoration shares mutable receipt data with the export")
	}
}

func TestMarkdownExportIncludesNativeEvidence(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		native := exportNativeReceipt()
		native.OutcomeUnknown = unknown
		native.Calls = append(native.Calls, message.HostedCall{ID: "search-2", Error: "search limit reached"}, message.HostedCall{ID: "search-3", Input: json.RawMessage(`{"query":"pending"}`)})
		exported, err := Export([]message.Message{{Role: message.RoleAssistant, Content: "Sample fact", NativeTools: native}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		markdown := ExportToMarkdown(exported)
		for _, want := range []string{"sample", "https://example.invalid/a", "Sample", "search limit reached"} {
			if !strings.Contains(markdown, want) {
				t.Fatalf("missing %q in %s", want, markdown)
			}
		}
		state := "pending"
		if unknown {
			state = "Outcome unknown"
		}
		if !strings.Contains(markdown, state) || strings.Contains(markdown, "opaque-replay") || strings.Contains(markdown, "encrypted_content") {
			t.Fatalf("incorrect native evidence projection: %s", markdown)
		}
	}
}
