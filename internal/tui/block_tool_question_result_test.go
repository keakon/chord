package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestQuestionCallFormatsControlReceiptsAndWaitDecisions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   string
		result tools.QuestionResult
		want   []string
	}{
		{name: "accepted", args: `{"questions":[{"header":"Choice","question":"Which option?"}],"wait":false}`, result: tools.QuestionResult{Status: tools.QuestionStatusAccepted, QuestionIDs: []string{"q-1"}}, want: []string{"Which option?", "Questions sent; awaiting answers."}},
		{name: "interrupted wait", args: `{"wait_for":["q-1"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusWaitInterrupted, PendingIDs: []string{"q-1"}}, want: []string{"Waiting interrupted", "1 question still awaiting an answer."}},
		{name: "interrupted without pending IDs", args: `{"wait_for":["q-1"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusWaitInterrupted}, want: []string{"Waiting interrupted."}},
		{name: "nested wait", args: `{"questions":[{"wait_for":["q-1","q-2"]}]}`, result: tools.QuestionResult{Status: tools.QuestionStatusWaitInterrupted, PendingIDs: []string{"q-1", "q-2"}}, want: []string{"2 questions still awaiting answers."}},
		{name: "resolved wait", args: `{"wait_for":["q-1"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusResolved, Answers: []tools.QuestionAnswer{{Header: "Choice", Outcome: tools.QuestionOutcomeAnswered}}}, want: []string{"Choice", "Outcome: answered"}},
		{name: "answerless resolved", args: `{"wait_for":["q-1"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusResolved}, want: []string{"Decisions received."}},
		{name: "mixed result", args: `{"wait_for":["q-1","q-2"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusWaitInterrupted, PendingIDs: []string{"q-2"}, Answers: []tools.QuestionAnswer{{Header: "Choice", Outcome: tools.QuestionOutcomeDefaulted, Selected: []string{"No"}}}}, want: []string{"1 question still awaiting", "default adopted by system (not user consent)", "Selected: No"}},
		{name: "closed without an answer", args: `{"wait_for":["q-1"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusResolved, Answers: []tools.QuestionAnswer{{Header: "Choice", Outcome: tools.QuestionOutcomeNoResponse}}}, want: []string{"closed without an answer (not user consent)"}},
		{name: "free text", args: `{"wait_for":["q-1"]}`, result: tools.QuestionResult{Status: tools.QuestionStatusResolved, Answers: []tools.QuestionAnswer{{Header: "Choice", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"First line\nSecond line"}}}}, want: []string{"First line", "Second line"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatal(err)
			}
			block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion, Content: tc.args, ResultPayload: string(payload), ResultNotes: []string{"Diagnostic note"}, ResultContent: string(payload) + "\nDiagnostic note", ResultDone: true, ResultStatus: message.ToolStatusSuccess}
			view := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
			for _, want := range append(tc.want, "Diagnostic note") {
				if !strings.Contains(view, want) {
					t.Fatalf("view lacks %q:\n%s", want, view)
				}
			}
			for _, raw := range []string{`"status"`, `"pending_ids"`, `"answers"`, `"question_ids"`, "q-1", "q-2"} {
				if strings.Contains(view, raw) {
					t.Fatalf("protocol detail %q leaked:\n%s", raw, view)
				}
			}
			if strings.Count(view, "Diagnostic note") != 1 {
				t.Fatalf("note repeated:\n%s", view)
			}
		})
	}
}

func TestQuestionCallRetainsMalformedResultAndErrors(t *testing.T) {
	for _, result := range []string{`{"status":`, `{"other":"detail"}`, "Plain error"} {
		block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion, Content: `{"wait_for":["q-1"]}`, ResultPayload: result, ResultContent: result}
		view := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
		if !strings.Contains(view, result) {
			t.Fatalf("unrecognized result lost: %s", view)
		}
	}
	block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion, Content: `{"wait_for":["q-1"]}`, ResultContent: "Question failed", ResultStatus: message.ToolStatusError}
	if view := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n")); !strings.Contains(view, "Question failed") {
		t.Fatalf("error lost: %s", view)
	}
}

func TestDecodeQuestionResultRejectsCombinedOrUnrecognizedPayloads(t *testing.T) {
	for _, raw := range []string{
		`{"status":"resolved"}` + "\nDiagnostic note",
		`{"answers":[{"selected":["First"]}]}`,
		`{"status":"unknown","answers":[{"selected":["First"]}]}`,
		`{"status":"resolved"}{"status":"accepted"}`,
	} {
		if _, ok := decodeQuestionResult(raw); ok {
			t.Fatalf("invalid receipt accepted: %s", raw)
		}
	}
}

func TestQuestionCallMarksOnlySelectedIDForDuplicateLabels(t *testing.T) {
	payload := `{"status":"resolved","answers":[{"header":"Destination","selected":["Use this"],"selected_ids":["second"],"outcome":"answered"}]}`
	block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion,
		Content:       `{"questions":[{"header":"Destination","question":"Where should files go?","options":[{"id":"first","label":"Use this","description":"First destination"},{"id":"second","label":"Use this","description":"Second destination"}]}]}`,
		ResultPayload: payload, ResultContent: payload, ResultDone: true,
	}
	view := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
	if !strings.Contains(view, "✓ 2. Use this") || strings.Contains(view, "✓ 1. Use this") {
		t.Fatalf("wrong option marked:\n%s", view)
	}
	payload = `{"status":"resolved","answers":[{"header":"Destination","selected":["Use this"],"outcome":"answered"}]}`
	block.ResultPayload, block.ResultContent = payload, payload
	view = terminaltext.StripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
	if !strings.Contains(view, "Custom: Use this") || strings.Contains(view, "✓ 1. Use this") || strings.Contains(view, "✓ 2. Use this") {
		t.Fatalf("free text interpreted as selection:\n%s", view)
	}
}

func TestQuestionCallLabelsLifecycleKindFromDurableArgs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     string
		want     string // header role suffix that must appear
		bodyWant string // optional body line that must appear
		reject   []string
	}{
		{name: "sync ask", args: `{"questions":[{"header":"H","question":"Q"}]}`, want: "· ask", reject: []string{"async", "wait_for", "↳ Kind:"}},
		{name: "explicit sync ask", args: `{"questions":[{"header":"H","question":"Q"}],"wait":true}`, want: "· ask", reject: []string{"async", "wait_for", "↳ Kind:"}},
		{name: "async ask", args: `{"questions":[{"header":"H","question":"Q"}],"wait":false}`, want: "· ask (async)", reject: []string{"wait_for", "↳ Kind:"}},
		{name: "wait_for one", args: `{"wait_for":["q-1"]}`, want: "· wait_for (1 question)", bodyWant: "asked earlier", reject: []string{"q-1", "ask (async)", "↳ Kind:"}},
		{name: "wait_for many", args: `{"wait_for":["q-1","q-2","q-3"]}`, want: "· wait_for (3 questions)", reject: []string{"q-1", "q-2", "q-3", "↳ Kind:"}},
		{name: "single question object", args: `{"questions":{"header":"H","question":"Q"},"wait":false}`, want: "· ask (async)"},
		{name: "ask with blank wait IDs", args: `{"questions":[{"header":"Choice","question":"Pick one"}],"wait_for":[""," "]}`, want: "· ask", bodyWant: "Pick one", reject: []string{"wait_for", "Waiting for"}},
		{name: "wait skips blank IDs", args: `{"wait_for":[""," q-1 "," "]}`, want: "· wait_for (1 question)", bodyWant: "asked earlier", reject: []string{"2 questions", "3 questions"}},
		{name: "nested wait item", args: `{"questions":[{"wait_for":["q-1"]}]}`, want: "· wait_for (1 question)", bodyWant: "asked earlier"},
		{name: "nested wait object", args: `{"questions":{"wait_for":["q-1"]}}`, want: "· wait_for (1 question)", bodyWant: "asked earlier"},
		{name: "malformed args", args: `{"questions":`, reject: []string{"· ", "↳ Kind:"}},
		{name: "no arguments", args: `{}`, reject: []string{"· ", "↳ Kind:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion, Content: tc.args, ResultDone: true, ResultStatus: message.ToolStatusSuccess}
			view := terminaltext.StripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
			if tc.want != "" && !strings.Contains(view, tc.want) {
				t.Fatalf("view lacks role suffix %q:\n%s", tc.want, view)
			}
			if tc.bodyWant != "" && !strings.Contains(view, tc.bodyWant) {
				t.Fatalf("view lacks body line %q:\n%s", tc.bodyWant, view)
			}
			for _, reject := range tc.reject {
				if strings.Contains(view, reject) {
					t.Fatalf("view leaks %q:\n%s", reject, view)
				}
			}
		})
	}
}

func TestQuestionCallKindSurvivesTranscriptRebuild(t *testing.T) {
	args := `{"wait_for":["819a8c293c20c4dbded169ee2df0c978"]}`
	restored := newTranscriptToolCallBlock(0, message.ToolCall{
		ID:   "call-d7db42dc05334fde8f0ac056c0fcf7bc",
		Name: tools.NameQuestion,
		Args: json.RawMessage(args),
	})
	view := terminaltext.StripANSI(strings.Join(restored.renderQuestionCall(100, ""), "\n"))
	if !strings.Contains(view, "· wait_for (1 question)") {
		t.Fatalf("restored card lost its role suffix:\n%s", view)
	}
	if strings.Contains(view, "819a8c293c20c4dbded169ee2df0c978") {
		t.Fatalf("protocol id leaked into the card:\n%s", view)
	}
}
