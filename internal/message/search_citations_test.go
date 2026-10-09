package message

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeCitationsPreserveTextAndReplay(t *testing.T) {
	cases := []struct {
		name, item, content, want string
	}{
		{"responses", `{"type":"message","content":[{"type":"output_text","text":"Sample fact.","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"A","start_index":0,"end_index":6}]}]}`, "Sample fact.", "Sample [A](<https://example.invalid/a>) fact."},
		{"unicode", `{"type":"message","content":[{"type":"output_text","text":"🌍你好 fact","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"A","start_index":1,"end_index":3}]}]}`, "🌍你好 fact", "🌍你好 [A](<https://example.invalid/a>) fact"},
		{"anthropic", `{"type":"text","text":"Sample fact.","citations":[{"type":"web_search_result_location","url":"https://example.invalid/a","title":"A","cited_text":"Original fact"}]}`, "Sample fact.", "Sample fact. [A](<https://example.invalid/a>)"},
		{"invalid_offsets", `{"type":"message","content":[{"type":"output_text","text":"Sample fact.","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"A","start_index":-1,"end_index":90}]}]}`, "Sample fact.", "Sample fact. [A](<https://example.invalid/a>)"},
		{"partial_capture", `{"type":"text","text":"Sample fact.","citations":[{"type":"web_search_result_location","url":"https://example.invalid/a","title":"A"}]}`, "Final answer", "Final answer [A](<https://example.invalid/a>)"},
		{"unsafe_link", `{"type":"text","text":"Sample fact.","citations":[{"type":"web_search_result_location","url":"javascript:alert(1)","title":"A"}]}`, "Sample fact.", "Sample fact."},
		{"escaping", `{"type":"text","text":"Sample fact.","citations":[{"type":"web_search_result_location","url":"https://example.invalid/a(b)","title":"A [B]"}]}`, "Sample fact.", `Sample fact. [A \[B\]](<https://example.invalid/a(b)>)`},
		{"marker", `{"type":"message","content":[{"type":"output_text","text":"Fact.citeturn0search0","annotations":[{"type":"url_citation","url":"https://example.invalid/a","title":"A","start_index":5,"end_index":24}]}]}`, "Fact.citeturn0search0", "Fact. [A](<https://example.invalid/a>)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history := &NativeToolHistory{Items: []json.RawMessage{json.RawMessage(tc.item)}}
			if got := history.CitedContent(tc.content); got != tc.want {
				t.Fatalf("content = %q, want %q", got, tc.want)
			}
			if string(history.Items[0]) != tc.item {
				t.Fatal("citation projection mutated raw replay data")
			}
		})
	}
}

func TestNativeCitationsAcrossBlocksAndContinuations(t *testing.T) {
	history := &NativeToolHistory{Items: []json.RawMessage{
		json.RawMessage(`{"type":"text","text":"First. ","citations":[{"type":"web_search_result_location","url":"https://example.invalid/a","title":"A","cited_text":"A passage"}]}`),
		json.RawMessage(`{"type":"thinking","thinking":"Hidden","signature":"opaque"}`),
		json.RawMessage(`{"type":"text","text":"Second.","citations":[{"type":"web_search_result_location","url":"https://example.invalid/b","title":"B","cited_text":"B passage"}]}`),
	}}
	got := history.CitedContent("First. Second.")
	if got != "First. [A](<https://example.invalid/a>) Second. [B](<https://example.invalid/b>)" {
		t.Fatalf("content = %q", got)
	}
	citations := WebSearchCitations(history.Items)
	if len(citations) != 2 || citations[0].Text != "A passage" || citations[1].Text != "B passage" || strings.Contains(got, "opaque") {
		t.Fatalf("citations = %+v, content = %s", citations, got)
	}
}
