package llm

import (
	"fmt"
	"testing"
)

func TestResponsesTextTerminalAuthority(t *testing.T) {
	delta := func(item, part int, text string) string {
		return fmt.Sprintf(`{"type":"response.output_text.delta","output_index":%d,"content_index":%d,"delta":%q}`, item, part, text)
	}
	done := func(item, part int, text string) string {
		return fmt.Sprintf(`{"type":"response.output_text.done","output_index":%d,"content_index":%d,"text":%q}`, item, part, text)
	}
	itemDone := func(item int, text string) string {
		return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"message","content":[{"type":"output_text","text":%q}]}}`, item, text)
	}
	for _, tc := range []struct {
		name   string
		events []string
		want   string
	}{
		{"interleaved items", []string{delta(0, 0, "A"), delta(1, 0, "B"), delta(0, 0, "C")}, "ACB"},
		{"reverse first arrival", []string{delta(2, 0, "C"), delta(1, 0, "B"), delta(0, 0, "A")}, "ABC"},
		{"interleaved parts", []string{delta(0, 1, "B"), delta(0, 0, "A"), delta(0, 1, "C")}, "ABC"},
		{"part done then EOF", []string{delta(0, 0, "damaged"), done(0, 0, "clean")}, "clean"},
		{"part isolation", []string{delta(0, 0, "damaged"), delta(0, 1, "tail"), done(0, 0, "clean")}, "cleantail"},
		{"partial item confirmation", []string{delta(1, 0, "tail"), delta(0, 0, "damaged"), itemDone(0, "clean")}, "cleantail"},
		{"part empty", []string{delta(0, 0, "remove"), done(0, 0, ""), delta(0, 1, "keep")}, "keep"},
		{"item empty", []string{delta(0, 0, "remove"), itemDone(0, ""), delta(1, 0, "keep")}, "keep"},
		{"item outranks part", []string{done(0, 0, "part"), itemDone(0, "item"), done(0, 0, "late part"), delta(0, 0, "late delta")}, "item"},
		{"part outranks delta", []string{done(0, 0, "clean"), delta(0, 0, "late")}, "clean"},
		{"duplicate done", []string{done(0, 0, "clean"), done(0, 0, "clean"), itemDone(0, "clean"), itemDone(0, "clean")}, "clean"},
		{"missing part text", []string{delta(0, 0, "keep"), `{"type":"response.output_text.done","output_index":0,"content_index":0}`}, "keep"},
		{"missing item content", []string{done(0, 0, "keep"), `{"type":"response.output_item.done","output_index":0,"item":{"type":"message"}}`}, "keep"},
		{"literal escapes", []string{delta(0, 0, `\u5B`), delta(0, 0, "8D")}, `\u5B8D`},
		{"literal replacement rune", []string{done(0, 0, "text \ufffd")}, "text \ufffd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(tc.events), nil, nil, nil, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Content != tc.want {
				t.Fatalf("Content=%q, want %q", resp.Content, tc.want)
			}
		})
	}
}

func TestResponsesTextExplicitEmptyAndAbsent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
	}{
		{"part", []string{`{"type":"response.output_text.delta","delta":"remove"}`, `{"type":"response.output_text.done","text":""}`}},
		{"item", []string{`{"type":"response.output_text.delta","delta":"remove"}`, `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":""}]}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// [DONE] returns an explicit empty response; EOF alone may instead reject
			// an empty incomplete response under the parser's recovery policy.
			events := append(tc.events, `[DONE]`)
			resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(buildSSEStream(events), nil, nil, nil, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Content != "" {
				t.Fatalf("Content=%q", resp.Content)
			}
		})
	}
}
