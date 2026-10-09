package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNativeToolDisplaysTerminalAndUnknownReceipts(t *testing.T) {
	native := &message.NativeToolHistory{Target: "sample/test-model", RequestIDs: []string{"request-1"}, Calls: []message.HostedCall{
		{ID: "search-1", Name: NameWebSearch, Input: json.RawMessage(`{"query":"sample"}`), Result: json.RawMessage(`[]`)},
		{ID: "search-2", Name: NameWebSearch, Error: "request rejected"},
		{ID: "search-3", Name: NameWebSearch},
	}}
	got := NativeToolDisplays(native)
	if len(got) != 2 || got[0].Status != message.ToolStatusSuccess || got[1].Status != message.ToolStatusError {
		t.Fatalf("displays=%+v", got)
	}
	native.OutcomeUnknown = true
	got = NativeToolDisplays(native)
	if len(got) != 3 || got[2].Status != message.ToolStatusError || !strings.Contains(got[2].Result, "unknown") {
		t.Fatalf("unknown=%+v", got)
	}
	native.Calls = nil
	got = NativeToolDisplays(native)
	if len(got) != 1 || !strings.Contains(got[0].Result, "outcome unknown") {
		t.Fatalf("request failure=%+v", got)
	}
}

func TestNativeUnknownRequestRemainsVisibleAfterCompletedCalls(t *testing.T) {
	native := &message.NativeToolHistory{Target: "sample/test-model", RequestIDs: []string{"request-1"}, OutcomeUnknown: true, Calls: []message.HostedCall{{ID: "search-1", Result: json.RawMessage(`[]`)}}}
	got := NativeToolDisplays(native)
	if len(got) != 2 || got[0].Status != message.ToolStatusSuccess || got[1].Status != message.ToolStatusError || !strings.Contains(got[1].Result, "outcome unknown") || got[0].ID == got[1].ID {
		t.Fatalf("displays=%+v", got)
	}
}

func TestNativeToolDisplaysAnnotationOnlySources(t *testing.T) {
	native := &message.NativeToolHistory{Authorization: message.NativeToolAuthorization{Tool: NameWebSearch}, Target: "sample/test-model", RequestIDs: []string{"request-1"},
		Calls: []message.HostedCall{{ID: "search-1", Result: json.RawMessage(`{"action":{"sources":[]}}`)}},
		Items: []json.RawMessage{json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"Sample fact","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"Sample","start_index":0,"end_index":6}]}]}`)},
	}
	got := NativeToolDisplays(native)
	if len(got) != 1 || !strings.Contains(got[0].Result, "https://example.invalid/a") || !strings.Contains(got[0].Result, "Cited passages:") || strings.Contains(got[0].Result, "no search results") {
		t.Fatalf("displays = %+v", got)
	}
}

func TestNativeToolDisplaysPreserveToolIdentity(t *testing.T) {
	native := &message.NativeToolHistory{Target: "sample/test-model", RequestIDs: []string{"request-1"}, Authorization: message.NativeToolAuthorization{Tool: "sample_tool", Contract: "sample.contract"}, Calls: []message.HostedCall{{ID: "call-1", Name: "sample_tool", Result: json.RawMessage(`{"value":"sample output"}`)}}}
	got := NativeToolDisplays(native)
	if len(got) != 1 || got[0].Name != "sample_tool" || !strings.Contains(got[0].Result, "sample output") || strings.Contains(got[0].Result, "no search results") {
		t.Fatalf("display=%+v", got)
	}
	native.Calls = nil
	native.OutcomeUnknown = true
	got = NativeToolDisplays(native)
	if len(got) != 1 || got[0].Name != "sample_tool" || !strings.Contains(got[0].Result, "outcome unknown") {
		t.Fatalf("failure=%+v", got)
	}
}
