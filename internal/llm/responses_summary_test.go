package llm

import (
	"strings"
	"testing"
)

func TestNormalizeReasoningSummaryHeadings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "headings concatenated without separator",
			in:   "**Reviewing the config****Checking the loader**",
			want: "**Reviewing the config**\n\n**Checking the loader**",
		},
		{
			name: "heading glued to the previous token",
			in:   "Nothing left to do.**Planning the next step**",
			want: "Nothing left to do.\n\n**Planning the next step**",
		},
		{
			name: "heading behind a single newline",
			in:   "Nothing left to do.\n**Planning the next step**",
			want: "Nothing left to do.\n\n**Planning the next step**",
		},
		{
			name: "several glued headings",
			in:   "**First****Second****Third**",
			want: "**First**\n\n**Second**\n\n**Third**",
		},
		{
			name: "headings already separated",
			in:   "**First**\n\n**Second**",
			want: "**First**\n\n**Second**",
		},
		{
			name: "inline bold keeps its place",
			in:   "Keep the **important** part.",
			want: "Keep the **important** part.",
		},
		{
			name: "lowercase inline span is not a heading",
			in:   "word**bold**",
			want: "word**bold**",
		},
		{
			name: "cjk inline bold stays inline",
			in:   "这是**重要**内容",
			want: "这是**重要**内容",
		},
		{
			name: "cjk heading glued to body is split",
			in:   "这是**重要**",
			want: "这是\n\n**重要**",
		},
		{
			name: "short heading glued to the following word stays inline",
			in:   "**API**Returns",
			want: "**API**Returns",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeReasoningSummaryHeadings(tt.in); got != tt.want {
				t.Fatalf("normalizeReasoningSummaryHeadings(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseResponsesSSE_SeparatesFlattenedSummaryHeadings pins the wiring: a
// backend that puts every summary section in one part with the headings glued
// together must still produce one heading per paragraph in the stored block.
func TestParseResponsesSSE_SeparatesFlattenedSummaryHeadings(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"**Reviewing the config**"}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"**Checking the loader**"}`,
		`data: {"type":"response.reasoning_summary_text.done","text":"**Reviewing the config****Checking the loader**"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"id":"resp_summary","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`,
	}, "\n\n") + "\n\n"

	resp, err := parseResponsesSSE(strings.NewReader(raw), nil, nil)
	if err != nil {
		t.Fatalf("parseResponsesSSE: %v", err)
	}
	if len(resp.ThinkingBlocks) != 1 {
		t.Fatalf("ThinkingBlocks = %+v, want one block", resp.ThinkingBlocks)
	}
	want := "**Reviewing the config**\n\n**Checking the loader**"
	if got := resp.ThinkingBlocks[0].Thinking; got != want {
		t.Fatalf("thinking = %q, want %q", got, want)
	}
}
