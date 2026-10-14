package llm

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

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
