package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestQuestionDecisionCardResolvesDuplicateLabels(t *testing.T) {
	item := tools.QuestionItem{Header: "Destination", Question: "Where should files go?", Options: []tools.QuestionOption{
		{ID: "option-first", Label: "Use this", Description: "First destination"},
		{ID: "option-second", Label: "Use this", Description: "Second destination"},
	}}
	fact := agent.QuestionFact{Operation: agent.QuestionOpAnswer, Updates: []agent.QuestionSnapshot{{Item: item}}, Result: &tools.QuestionAnswer{QuestionID: "question-identifier", ResultID: "result-identifier", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Use this"}, SelectedIDs: []string{"option-second"}}}
	block := questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, fact), 1)
	if block == nil {
		t.Fatal("decision has no card")
	}
	for _, want := range []string{item.Question, "Selected: Use this", "Second destination"} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("missing %q in %q", want, block.Content)
		}
	}
	for _, unwanted := range []string{"First destination", "option-second", "question-identifier", "result-identifier"} {
		if strings.Contains(block.Content, unwanted) {
			t.Fatalf("unexpected %q in %q", unwanted, block.Content)
		}
	}
	fact.Result.SelectedIDs = nil
	fact.Result.Selected = []string{"Use this\n  another destination"}
	body := questionDecisionCardBody(fact)
	if !strings.Contains(body, "Free-text answer:\nUse this\n  another destination") || strings.Contains(body, "Second destination") {
		t.Fatalf("custom answer changed: %q", body)
	}
}
