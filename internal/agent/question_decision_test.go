package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestQuestionDecisionTextKeepsSelectionBoundariesAndDescriptions(t *testing.T) {
	item := tools.QuestionItem{Header: "Destinations", Question: "Where should the files go?", Multiple: true, Options: []tools.QuestionOption{
		{ID: "first", Label: "Copy, then open", Description: "First destination"},
		{ID: "second", Label: "Copy, then open", Description: "Second destination"},
	}}
	fact := QuestionFact{Operation: QuestionOpRevise, Updates: []QuestionSnapshot{{Item: item}}, Result: &tools.QuestionAnswer{QuestionID: "q-1", ResultID: "r-1", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Copy, then open", "Copy, then open"}, SelectedIDs: []string{"second", "first"}}}
	text := questionDecisionText(fact)
	for _, want := range []string{"Question answer revised: Destinations", "Question: Where should the files go?", "Selected: Copy, then open\nDescription: Second destination\nSelected: Copy, then open\nDescription: First destination", `Selected option IDs: ["second","first"]`, "Question ID: q-1", "Result ID: r-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestQuestionDecisionTextPreservesFreeText(t *testing.T) {
	answer := "first\n  second, third\nlast"
	text := questionDecisionText(QuestionFact{Result: &tools.QuestionAnswer{Outcome: tools.QuestionOutcomeAnswered, Selected: []string{answer}}, Updates: []QuestionSnapshot{{Item: tools.QuestionItem{Question: "Which destination?", Options: []tools.QuestionOption{{ID: "first", Label: "First option"}}}}}})
	if !strings.Contains(text, "Free-text answer:\n"+answer) || strings.Contains(text, "Selected:") || strings.Contains(text, "Selected option IDs:") {
		t.Fatalf("free text changed: %q", text)
	}
}

func TestQuestionProjectionPreservesSelectedMeaningAfterCompaction(t *testing.T) {
	t.Parallel()
	a := newQuestionTestAgent(t)
	item := tools.QuestionItem{Header: "Destination", Question: "Where should the files go?", Options: []tools.QuestionOption{
		{ID: "first", Label: "Use this", Description: "First destination"},
		{ID: "second", Label: "Use this", Description: "Second destination"},
	}}
	id := createTestQuestions(t, a, []tools.QuestionItem{item}, false).Result.QuestionIDs[0]
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"second"}}, time.Time{})
	applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpRevise, QuestionID: id, Answers: []string{"first"}}, time.Time{})
	projected := questionRequestContext(a.snapshotQuestionRequest(), nil)
	if len(projected) != 2 {
		t.Fatalf("projections = %d", len(projected))
	}
	for i, want := range []string{"Selected: Use this\nDescription: Second destination", "Question answer revised: Destination"} {
		if !strings.Contains(projected[i].Content, want) || !strings.Contains(projected[i].Content, item.Question) {
			t.Fatalf("decision %d lost its meaning: %q", i, projected[i].Content)
		}
	}
	if !strings.Contains(projected[1].Content, "Description: First destination") {
		t.Fatalf("revision lost selection: %q", projected[1].Content)
	}
}
