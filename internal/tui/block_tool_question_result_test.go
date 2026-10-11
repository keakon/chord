package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
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
			block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion, Content: tc.args, ResultPayload: string(payload), ResultNotes: []string{"Diagnostic note"}, ResultContent: string(payload) + "\nDiagnostic note", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess}
			view := stripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
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
		view := stripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
		if !strings.Contains(view, result) {
			t.Fatalf("unrecognized result lost: %s", view)
		}
	}
	block := &Block{Type: BlockToolCall, ToolName: tools.NameQuestion, Content: `{"wait_for":["q-1"]}`, ResultContent: "Question failed", ResultStatus: agent.ToolResultStatusError}
	if view := stripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n")); !strings.Contains(view, "Question failed") {
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
	view := stripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
	if !strings.Contains(view, "✓ 2. Use this") || strings.Contains(view, "✓ 1. Use this") {
		t.Fatalf("wrong option marked:\n%s", view)
	}
	payload = `{"status":"resolved","answers":[{"header":"Destination","selected":["Use this"],"outcome":"answered"}]}`
	block.ResultPayload, block.ResultContent = payload, payload
	view = stripANSI(strings.Join(block.renderQuestionCall(100, ""), "\n"))
	if !strings.Contains(view, "Custom: Use this") || strings.Contains(view, "✓ 1. Use this") || strings.Contains(view, "✓ 2. Use this") {
		t.Fatalf("free text interpreted as selection:\n%s", view)
	}
}
