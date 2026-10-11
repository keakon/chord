package tui

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// questionTranscriptBlock displays durable decisions as status cards.
func questionTranscriptBlock(msg message.Message, id int) *Block {
	if !message.IsQuestionDecision(msg) {
		return nil
	}
	var fact agent.QuestionFact
	if json.Unmarshal(msg.Question, &fact) != nil || fact.Result == nil {
		return nil
	}
	body := questionDecisionCardBody(fact)
	return &Block{ID: id, Type: BlockStatus, StatusTitle: questionDecisionCardTitle(fact), Content: body}
}

const questionDecisionCardBadge = "QUESTION"

func questionDecisionCardTitle(fact agent.QuestionFact) string {
	if fact.Result == nil {
		return questionDecisionCardBadge
	}
	outcome := strings.TrimSpace(fact.Result.Outcome)
	switch {
	case outcome == tools.QuestionOutcomeAnswered && fact.Operation == agent.QuestionOpRevise:
		// A revision replaces an earlier answer: same outcome, later decision.
		return questionDecisionCardBadge + " REVISED"
	case outcome == tools.QuestionOutcomeAnswered:
		return questionDecisionCardBadge + " ANSWERED"
	case outcome == tools.QuestionOutcomeDeclined:
		return questionDecisionCardBadge + " DECLINED"
	case outcome == tools.QuestionOutcomeDefaulted:
		return questionDecisionCardBadge + " DEFAULTED BY SYSTEM"
	case outcome == tools.QuestionOutcomeNoResponse:
		return questionDecisionCardBadge + " NO RESPONSE"
	case outcome == tools.QuestionOutcomeSuperseded:
		return questionDecisionCardBadge + " SUPERSEDED"
	case outcome == tools.QuestionOutcomeCancelled:
		return questionDecisionCardBadge + " CANCELLED"
	case outcome == tools.QuestionOutcomeError:
		return questionDecisionCardBadge + " ERROR"
	case outcome == "":
		return questionDecisionCardBadge
	default:
		return questionDecisionCardBadge + " " + strings.ToUpper(outcome)
	}
}

// questionDecisionCardBody makes the recorded decision readable without the
// original tool card. Protocol IDs remain in model-facing text only.
func questionDecisionCardBody(fact agent.QuestionFact) string {
	result := fact.Result
	if result == nil {
		return ""
	}
	var item tools.QuestionItem
	if len(fact.Updates) > 0 {
		item = fact.Updates[0].Item
	}
	header := strings.TrimSpace(item.Header)
	if header == "" {
		header = strings.TrimSpace(result.Header)
	}
	lines := make([]string, 0, 4)
	if header != "" {
		lines = append(lines, header)
	}
	if question := strings.TrimSpace(item.Question); question != "" && question != header {
		lines = append(lines, question)
	}
	for _, option := range result.SelectedOptions(item) {
		lines = append(lines, "Selected: "+option.Label)
		if option.Description != "" {
			lines = append(lines, option.Description)
		}
	}
	if len(result.SelectedIDs) == 0 && len(result.Selected) > 0 {
		lines = append(lines, "Free-text answer:", strings.Join(result.Selected, "\n"))
	}
	if notice := agent.QuestionDecisionNotice(result.Outcome); notice != "" {
		lines = append(lines, notice)
	}
	return strings.Join(lines, "\n")
}
