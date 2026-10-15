package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
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

func TestQuestionDecisionCardsKeepFieldsVisible(t *testing.T) {
	item := tools.QuestionItem{Header: "Destination", Question: "Where should files go?", Options: []tools.QuestionOption{
		{ID: "local", Label: "Save locally", Description: "Review before publishing."},
		{ID: "shared", Label: "Save a shared copy", Description: "Keep a second copy."},
	}}
	for _, tc := range []struct {
		name      string
		operation string
		answer    tools.QuestionAnswer
		want      []string
	}{
		{name: "selected", answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeAnswered, SelectedIDs: []string{"local"}}, want: []string{"Selected: Save locally", "Description: Review before publishing."}},
		{name: "multiple", answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeAnswered, SelectedIDs: []string{"local", "shared"}}, want: []string{"Selected: Save locally", "Selected: Save a shared copy", "Description: Keep a second copy."}},
		{name: "custom", answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"First line\n  Indented line\n\nFinal line"}}, want: []string{"Free-text answer:", "First line", "Indented line", "Final line"}},
		{name: "revised", operation: agent.QuestionOpRevise, answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeAnswered, SelectedIDs: []string{"shared"}}, want: []string{"Selected: Save a shared copy"}},
		{name: "system default", answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeDefaulted, SelectedIDs: []string{"local"}}, want: []string{"Selected: Save locally"}},
		{name: "no answer", answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeNoResponse}},
		{name: "cancelled", answer: tools.QuestionAnswer{Outcome: tools.QuestionOutcomeCancelled}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation := tc.operation
			if operation == "" {
				operation = agent.QuestionOpAnswer
			}
			fact := agent.QuestionFact{Operation: operation, Updates: []agent.QuestionSnapshot{{Item: item}}, Result: &tc.answer}
			msg := questionDecisionMessage(t, message.KindQuestionResult, fact)
			nextID := 1
			blocks := messagesToBlocks([]message.Message{msg}, &nextID)
			if len(blocks) != 1 || !blocks[0].QuestionDecision {
				t.Fatalf("decision blocks=%+v", blocks)
			}
			block := blocks[0]
			for _, width := range []int{60, 199} {
				block.Collapsed = true
				block.InvalidateCache()
				if block.canToggleAtWidth(width) || block.ToggleAtWidth(width) {
					t.Fatalf("decision offers folding at width %d", width)
				}
				view := terminaltext.StripANSI(strings.Join(block.Render(width, ""), "\n"))
				if strings.ContainsAny(view, toolDisclosureCollapsed+toolDisclosureExpanded) {
					t.Fatalf("decision has disclosure marker:\n%s", view)
				}
				bodyLines := stripANSILines(block.statusCardBodyLines(width))
				for i := range bodyLines {
					bodyLines[i] = strings.TrimSpace(bodyLines[i])
				}
				for _, want := range append([]string{item.Header, "Question: " + item.Question}, tc.want...) {
					if !slices.Contains(bodyLines, want) || !strings.Contains(view, want) {
						t.Fatalf("width %d: missing separate field %q:\n%s", width, want, view)
					}
				}
				if notice := agent.QuestionDecisionNotice(tc.answer.Outcome); notice != "" {
					body := strings.Join(strings.Fields(strings.Join(bodyLines, " ")), " ")
					if !strings.Contains(body, strings.Join(strings.Fields(notice), " ")) {
						t.Fatalf("system decision notice lost:\n%s", view)
					}
				}
				if tc.name == "custom" && !strings.Contains(strings.Join(block.statusCardBodyLines(width), "\n"), "  Indented line\n\nFinal line") {
					t.Fatal("custom answer lost indentation or blank lines")
				}
			}
		})
	}
}

func TestQuestionDecisionCardSpillRetainsDisplaySemantics(t *testing.T) {
	fact := agent.QuestionFact{Operation: agent.QuestionOpAnswer, Updates: []agent.QuestionSnapshot{{Item: tools.QuestionItem{Header: "Destination", Question: "Where should files go?"}}}, Result: &tools.QuestionAnswer{Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"First line\nSecond line"}}}
	block := questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, fact), 1)
	store, err := newViewportSpillStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref, err := store.Append(block)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ref)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Collapsed = true
	if !loaded.QuestionDecision || loaded.ToggleAtWidth(80) {
		t.Fatal("spilled decision lost its display semantics")
	}
	if got, want := strings.Join(loaded.Render(80, ""), "\n"), strings.Join(block.Render(80, ""), "\n"); got != want {
		t.Fatalf("spilled decision render differs:\n%s\nwant:\n%s", got, want)
	}
}
