package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func questionDecisionMessage(t *testing.T, kind string, fact agent.QuestionFact) message.Message {
	t.Helper()
	payload, err := json.Marshal(fact)
	if err != nil {
		t.Fatalf("marshal question fact: %v", err)
	}
	return message.Message{Role: message.RoleUser, Kind: kind, Question: payload}
}

// Recorded decisions belong on status cards rather than user prose cards.
func TestQuestionTranscriptBlockRendersDecisionsAsCards(t *testing.T) {
	item := tools.QuestionItem{Header: "Publish tag", Question: "Publish the sample tag?", Options: []tools.QuestionOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}}}
	answered := agent.QuestionFact{
		Operation: agent.QuestionOpAnswer,
		Updates:   []agent.QuestionSnapshot{{Item: item}},
		Result:    &tools.QuestionAnswer{QuestionID: "q-1", Header: item.Header, Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Yes"}, SelectedIDs: []string{"yes"}},
	}
	block := questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, answered), 7)
	if block == nil || block.Type != BlockStatus {
		t.Fatalf("answered decision block = %#v, want a status card", block)
	}
	if block.StatusTitle != "QUESTION ANSWERED" {
		t.Fatalf("title = %q", block.StatusTitle)
	}
	for _, want := range []string{"Publish tag", "Publish the sample tag?", "Yes"} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("card body = %q, want it to name %q", block.Content, want)
		}
	}

	// A system default is a decision too, and the title has to say so: it is
	// not user consent, and a card reading "answered" would imply it was.
	defaulted := answered
	defaulted.Result = &tools.QuestionAnswer{QuestionID: "q-1", Header: item.Header, Outcome: tools.QuestionOutcomeDefaulted, Selected: []string{"No"}, SelectedIDs: []string{"no"}}
	block = questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, defaulted), 8)
	if block == nil || block.Type != BlockStatus || block.StatusTitle != "QUESTION DEFAULTED BY SYSTEM" {
		t.Fatalf("defaulted decision block = %#v", block)
	}

	// A revision replaces an earlier answer; it is still a recorded decision.
	revised := answered
	revised.Operation = agent.QuestionOpRevise
	block = questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, revised), 9)
	if block == nil || block.StatusTitle != "QUESTION REVISED" {
		t.Fatalf("revised decision block = %#v", block)
	}
}

func TestQuestionNoResponseCardExplainsSystemClosure(t *testing.T) {
	fact := agent.QuestionFact{
		Operation: agent.QuestionOpAnswer,
		Updates:   []agent.QuestionSnapshot{{Item: tools.QuestionItem{Header: "Publish release", Question: "Publish version 2.3?"}}},
		Result:    &tools.QuestionAnswer{QuestionID: "q-1", Header: "Publish release", Outcome: tools.QuestionOutcomeNoResponse},
	}
	block := questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, fact), 10)
	if block == nil || block.Type != BlockStatus || block.StatusTitle != "QUESTION NO RESPONSE" {
		t.Fatalf("no-response decision block = %#v", block)
	}
	for _, want := range []string{"No answer was recorded", "system closed this question", "not user consent or authorization"} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("no-response card body = %q, want it to contain %q", block.Content, want)
		}
	}

	fact.Result = &tools.QuestionAnswer{QuestionID: "q-1", Header: "Publish release", Outcome: tools.QuestionOutcomeDefaulted, Selected: []string{"No"}}
	block = questionTranscriptBlock(questionDecisionMessage(t, message.KindQuestionResult, fact), 11)
	if block == nil || block.Type != BlockStatus || block.StatusTitle != "QUESTION DEFAULTED BY SYSTEM" {
		t.Fatalf("defaulted decision block = %#v", block)
	}
	for _, want := range []string{"The system adopted this default", "not user consent or authorization"} {
		if !strings.Contains(block.Content, want) {
			t.Fatalf("defaulted card body = %q, want it to contain %q", block.Content, want)
		}
	}
}

// TestQuestionDecisionCardTitlesFollowTheStatusCardVocabulary pins the badge
// every outcome renders. The card is a status card, so its badge has to read
// like every other one — upper case, subject then outcome — and the two
// outcomes whose wire token would say the wrong thing in a badge
// ("defaulted", "no_response") must never surface as tokens: one of them is a
// system decision that must not read as user consent.
func TestQuestionDecisionCardTitlesFollowTheStatusCardVocabulary(t *testing.T) {
	base := agent.QuestionFact{
		Operation: agent.QuestionOpAnswer,
		Updates:   []agent.QuestionSnapshot{{Item: tools.QuestionItem{Header: "Publish tag", Question: "Push?"}}},
	}
	cases := []struct {
		name      string
		operation string
		outcome   string
		want      string
	}{
		{name: "answered", outcome: tools.QuestionOutcomeAnswered, want: "QUESTION ANSWERED"},
		{name: "revised", operation: agent.QuestionOpRevise, outcome: tools.QuestionOutcomeAnswered, want: "QUESTION REVISED"},
		{name: "defaulted", outcome: tools.QuestionOutcomeDefaulted, want: "QUESTION DEFAULTED BY SYSTEM"},
		{name: "no response", outcome: tools.QuestionOutcomeNoResponse, want: "QUESTION NO RESPONSE"},
		{name: "superseded", outcome: tools.QuestionOutcomeSuperseded, want: "QUESTION SUPERSEDED"},
		{name: "cancelled", outcome: tools.QuestionOutcomeCancelled, want: "QUESTION CANCELLED"},
		{name: "error", outcome: tools.QuestionOutcomeError, want: "QUESTION ERROR"},
		{name: "empty outcome", want: questionDecisionCardBadge},
		// An outcome this build does not know still renders as a badge, never as
		// the lower-case token the wire carries.
		{name: "unknown outcome", outcome: "future_outcome", want: "QUESTION FUTURE_OUTCOME"},
	}
	for _, tc := range cases {
		fact := base
		if tc.operation != "" {
			fact.Operation = tc.operation
		}
		fact.Result = &tools.QuestionAnswer{QuestionID: "q-1", Header: "Publish tag", Outcome: tc.outcome}
		got := questionDecisionCardTitle(fact)
		if got != tc.want {
			t.Fatalf("%s: title = %q, want %q", tc.name, got, tc.want)
		}
		if strings.ToUpper(got) != got {
			t.Fatalf("%s: title %q is not the upper-case vocabulary the other status badges use", tc.name, got)
		}
		if strings.ContainsAny(got, "·|") {
			t.Fatalf("%s: title %q carries a separator no other status badge uses", tc.name, got)
		}
	}

	// No decision at all still needs a badge: an empty title falls back to the
	// generic info card and would mislabel the row.
	if got := questionDecisionCardTitle(agent.QuestionFact{}); got != questionDecisionCardBadge {
		t.Fatalf("decisionless title = %q, want %q", got, questionDecisionCardBadge)
	}
}

// TestQuestionTranscriptBlockLeavesUserWordsAlone pins what must NOT become a
// card. Withdraw and replace carry a durable user-text source, so they keep
// rendering as user prose. A recorded answer remains a decision even without
// a kind; its fact is the authority for that distinction.
func TestQuestionTranscriptBlockLeavesUserWordsAlone(t *testing.T) {
	withdrawn := agent.QuestionFact{
		Operation: agent.QuestionOpWithdraw,
		SourceID:  "input-1",
		Updates:   []agent.QuestionSnapshot{{Item: tools.QuestionItem{Header: "Publish tag", Question: "Push?"}}},
		Result:    &tools.QuestionAnswer{QuestionID: "q-1", Header: "Publish tag", Outcome: tools.QuestionOutcomeCancelled},
	}
	if block := questionTranscriptBlock(questionDecisionMessage(t, "", withdrawn), 3); block != nil {
		t.Fatalf("withdraw block = %#v, want the user's own text rendered as prose", block)
	}

	answered := agent.QuestionFact{
		Operation: agent.QuestionOpAnswer,
		Updates:   []agent.QuestionSnapshot{{Item: tools.QuestionItem{Header: "Publish tag", Question: "Push?"}}},
		Result:    &tools.QuestionAnswer{QuestionID: "q-1", Header: "Publish tag", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Yes"}},
	}
	block := questionTranscriptBlock(questionDecisionMessage(t, "", answered), 4)
	if block == nil || block.Type != BlockStatus || block.StatusTitle != "QUESTION ANSWERED" {
		t.Fatalf("decision block = %#v, want an answered status card", block)
	}
	withdrawn.Operation = agent.QuestionOpReplace
	if block := questionTranscriptBlock(questionDecisionMessage(t, "", withdrawn), 6); block != nil {
		t.Fatalf("replacement block = %#v, want user prose", block)
	}

	// The state facts carry no decision and are not part of the transcript.
	state := questionDecisionMessage(t, message.KindQuestionState, answered)
	if block := questionTranscriptBlock(state, 5); block != nil {
		t.Fatalf("state block = %#v, want none", block)
	}
}
