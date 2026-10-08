package llm

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
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
			name: "following heading carries an underscore and parentheses",
			in:   "**Checking the loader****Reviewing native_thinking flag (off) handling**",
			want: "**Checking the loader**\n\n**Reviewing native_thinking flag (off) handling**",
		},
		{
			name: "following heading carries a slash and a code span",
			in:   "**Auditing (v2) naming****Checking path/legacy `cfg.X` names**",
			want: "**Auditing (v2) naming**\n\n**Checking path/legacy `cfg.X` names**",
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
			name: "glued CJK headings split",
			in:   "**检查配置****检查加载器**",
			want: "**检查配置**\n\n**检查加载器**",
		},
		{
			name: "two-rune Latin heading is long enough as the prefix side",
			in:   "**AI****Ops**",
			want: "**AI**\n\n**Ops**",
		},
		{
			name: "very short following heading stays one run",
			in:   "**AI****OK**",
			want: "**AI****OK**",
		},
		{
			name: "following heading mixed CJK and Latin stays one run",
			in:   "**检查配置****关于 API 设计**",
			want: "**检查配置****关于 API 设计**",
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
			name: "inline span with an underscore stays inline",
			in:   "set the **draft_mode** flag first",
			want: "set the **draft_mode** flag first",
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
			if got := NormalizeReasoningSummaryHeadings(tt.in); got != tt.want {
				t.Fatalf("NormalizeReasoningSummaryHeadings(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeReasoningSummaryHeadings_ShortChainTerminatesUnchanged pins the
// fixpoint bound: a chain too short to match any heading rule returns as-is
// instead of looping.
func TestNormalizeReasoningSummaryHeadings_ShortChainTerminatesUnchanged(t *testing.T) {
	in := "**A****B****C**"
	if got := NormalizeReasoningSummaryHeadings(in); got != in {
		t.Fatalf("NormalizeReasoningSummaryHeadings(%q) = %q, want unchanged", in, got)
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

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(raw), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ThinkingBlocks) != 1 {
		t.Fatalf("ThinkingBlocks = %+v, want one block", resp.ThinkingBlocks)
	}
	want := "**Reviewing the config**\n\n**Checking the loader**"
	if got := resp.ThinkingBlocks[0].Thinking; got != want {
		t.Fatalf("thinking = %q, want %q", got, want)
	}
}

// TestParseResponsesSSE_KeepsRawReasoningTextVerbatim pins the storage side of
// the raw reasoning_text channel: glued generated headings stay as the backend
// sent them, so replay forwards the original and renderers add the breaks.
func TestParseResponsesSSE_KeepsRawReasoningTextVerbatim(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"**Reviewing the config**"}`,
		`data: {"type":"response.reasoning_text.delta","delta":"**Checking native_thinking handling**"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"id":"resp_raw_reasoning","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`,
	}, "\n\n") + "\n\n"

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(raw), nil, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ThinkingBlocks) != 0 {
		t.Fatalf("ThinkingBlocks = %+v, want none from the raw reasoning channel", resp.ThinkingBlocks)
	}
	want := "**Reviewing the config****Checking native_thinking handling**"
	if got := resp.ReasoningContent; got != want {
		t.Fatalf("reasoning = %q, want %q", got, want)
	}
}

func TestParseResponsesSSE_VisibleSummaryEmitsPartBoundaryButRawReasoningDoesNot(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"**Visible summary**"}`,
		`data: {"type":"response.reasoning_summary_text.done","text":"**Visible summary**"}`,
		`data: {"type":"response.reasoning_text.delta","delta":"**hidden raw reasoning**"}`,
		`data: {"type":"response.reasoning_text.done","text":"**hidden raw reasoning**"}`,
		`data: {"type":"response.completed","response":{"id":"resp_summary_boundary","status":"completed","output":[]}}`,
	}, "\n\n") + "\n\n"
	var events []message.StreamDelta
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(raw), func(delta message.StreamDelta) {
		events = append(events, delta)
	}, nil, nil, "", false, false, false)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	var summaryDeltas, partEnds, rawDeltas int
	for _, event := range events {
		switch {
		case event.Type == message.StreamDeltaThinking && event.ThinkingMode == message.StreamThinkingModeSummary:
			summaryDeltas++
		case event.Type == message.StreamDeltaThinkingPartEnd:
			partEnds++
		case event.Type == message.StreamDeltaThinking:
			rawDeltas++
		}
	}
	if summaryDeltas != 1 || partEnds != 1 || rawDeltas != 0 {
		t.Fatalf("events = %#v, want one visible summary delta, one part boundary, no raw reasoning delta", events)
	}
	if resp.ReasoningContent != "**hidden raw reasoning**" {
		t.Fatalf("ReasoningContent = %q, want raw reasoning preserved for replay", resp.ReasoningContent)
	}
}

func TestNormalizeStreamingReasoningSummaryHeadings(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "trailing cjk bold waits for its terminator", in: "检查**配置**", want: "检查**配置**"},
		{name: "cjk bold followed by text stays inline", in: "检查**配置**文件", want: "检查**配置**文件"},
		{name: "cjk heading closed by a newline splits", in: "检查**配置**\n", want: "检查\n\n**配置**\n"},
		{name: "trailing glued heading waits", in: "**First****Second**", want: "**First****Second**"},
		{name: "glued heading closed by a newline splits", in: "**First****Second**\n", want: "**First**\n\n**Second**\n"},
		{name: "last heading of a chain waits", in: "Done.**Planning the next step**", want: "Done.**Planning the next step**"},
		{name: "heading closed by the next heading splits", in: "Done.**Planning the next step****Checking**", want: "Done.\n\n**Planning the next step****Checking**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeStreamingReasoningSummaryHeadings(tt.in); got != tt.want {
				t.Fatalf("NormalizeStreamingReasoningSummaryHeadings(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestNormalizeStreamingReasoningSummaryHeadings_BreaksNeverRetract pins the
// streaming property the TUI relies on: feeding the text rune by rune, every
// paragraph boundary produced for a prefix stays in place once more text
// arrives, so the settled part of a streaming thinking card never regresses.
func TestNormalizeStreamingReasoningSummaryHeadings_BreaksNeverRetract(t *testing.T) {
	samples := []string{
		"检查**配置**文件，然后继续。**检查加载器**\n正文**第二节**\n\n结尾",
		"**Reviewing the config****Checking the loader****Planning next**\nBody text.\n**Wrapping up**",
		"Nothing left to do.\n**Planning the next step**\n\nKeep the **important** part.**API**Returns",
		"这是**重要**内容。这是**重要**\n**AI****Ops**\n",
	}
	for _, sample := range samples {
		prevSettled := ""
		for i := range sample {
			if i == 0 {
				continue
			}
			got := NormalizeStreamingReasoningSummaryHeadings(sample[:i])
			if !strings.HasPrefix(got, prevSettled) {
				t.Fatalf("prefix %q: normalized %q dropped settled prefix %q", sample[:i], got, prevSettled)
			}
			if cut := strings.LastIndex(got, "\n\n"); cut >= 0 {
				prevSettled = got[:cut+2]
			}
		}
		final := NormalizeReasoningSummaryHeadings(sample)
		if !strings.HasPrefix(final, prevSettled) {
			t.Fatalf("final %q dropped settled prefix %q", final, prevSettled)
		}
	}
}

// TestParseResponsesSSE_ProjectsVisibleRawReasoning pins the opt-in
// openai_visible shape: reasoning_text that the target exposes as user-visible
// reasoning must land in ThinkingBlocks so restore and export show what the
// live stream showed. The raw text stays on ReasoningContent for replay.
func TestParseResponsesSSE_ProjectsVisibleRawReasoning(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"Reviewing the config"}`,
		`data: {"type":"response.reasoning_text.done","text":"Reviewing the config"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"id":"resp_visible_reasoning","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`,
	}, "\n\n") + "\n\n"

	var visible strings.Builder
	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(raw), func(delta message.StreamDelta) {
		if delta.Type == message.StreamDeltaThinking {
			visible.WriteString(delta.Text)
		}
	}, nil, nil, "", false, false, true)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if got := visible.String(); got != "Reviewing the config" {
		t.Fatalf("visible reasoning = %q, want the streamed text", got)
	}
	if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Thinking != "Reviewing the config" {
		t.Fatalf("ThinkingBlocks = %+v, want the visible raw reasoning", resp.ThinkingBlocks)
	}
	if resp.ReasoningContent != "Reviewing the config" {
		t.Fatalf("reasoning = %q, want the raw text kept for replay", resp.ReasoningContent)
	}
}

// TestParseResponsesSSE_ProjectsVisibleRawReasoningWithoutDone covers streams
// that only send deltas: the final projection still carries the part instead
// of dropping it on restore.
func TestParseResponsesSSE_ProjectsVisibleRawReasoningWithoutDone(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"Reviewing"}`,
		`data: {"type":"response.reasoning_text.delta","delta":" the config"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed","response":{"id":"resp_visible_reasoning_delta","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"answer"}]}]}}`,
	}, "\n\n") + "\n\n"

	resp, _, err := parseResponsesSSEWithOutputItemsAndTurnState(strings.NewReader(raw), nil, nil, nil, "", false, false, true)
	if err != nil {
		t.Fatalf("parseResponsesSSEWithOutputItemsAndTurnState: %v", err)
	}
	if len(resp.ThinkingBlocks) != 1 || resp.ThinkingBlocks[0].Thinking != "Reviewing the config" {
		t.Fatalf("ThinkingBlocks = %+v, want the delta text projected at stream end", resp.ThinkingBlocks)
	}
}
