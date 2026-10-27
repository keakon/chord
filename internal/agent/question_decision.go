package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// questionDecisionText includes the question and its answer independently of
// the original tool call, which may already have been compacted out of history.
func questionDecisionText(fact QuestionFact) string {
	if fact.Result == nil {
		return ""
	}
	result := fact.Result
	var item tools.QuestionItem
	if len(fact.Updates) > 0 {
		item = fact.Updates[0].Item
	}
	header := strings.TrimSpace(item.Header)
	if header == "" {
		header = strings.TrimSpace(result.Header)
	}
	if header == "" {
		header = result.QuestionID
	}
	var b strings.Builder
	switch result.Outcome {
	case tools.QuestionOutcomeAnswered:
		if fact.Operation == QuestionOpRevise {
			fmt.Fprintf(&b, "Question answer revised: %s", header)
		} else {
			fmt.Fprintf(&b, "Question answered: %s", header)
		}
	case tools.QuestionOutcomeDefaulted:
		fmt.Fprintf(&b, "Question defaulted by system: %s", header)
	case tools.QuestionOutcomeNoResponse:
		fmt.Fprintf(&b, "Question closed without an answer: %s", header)
	case tools.QuestionOutcomeCancelled:
		fmt.Fprintf(&b, "Question cancelled: %s", header)
	case tools.QuestionOutcomeSuperseded:
		fmt.Fprintf(&b, "Question superseded: %s", header)
	default:
		fmt.Fprintf(&b, "Question %s: %s", result.Outcome, header)
	}
	if question := strings.TrimSpace(item.Question); question != "" {
		fmt.Fprintf(&b, "\nQuestion: %s", question)
	}
	for _, option := range result.SelectedOptions(item) {
		// The id travels on the line itself: labels may repeat, so pairing
		// these lines with the trailing Selected option IDs array by order
		// would not identify which option was chosen.
		fmt.Fprintf(&b, "\nSelected: %s (id=%s)", option.Label, option.ID)
		if option.Description != "" {
			fmt.Fprintf(&b, "\nDescription: %s", option.Description)
		}
	}
	if len(result.SelectedIDs) == 0 && len(result.Selected) > 0 {
		b.WriteString("\nFree-text answer:\n")
		b.WriteString(strings.Join(result.Selected, "\n"))
	}
	if notice := QuestionDecisionNotice(result.Outcome); notice != "" {
		b.WriteString("\n")
		b.WriteString(notice)
	}
	if result.QuestionID != "" {
		fmt.Fprintf(&b, "\nQuestion ID: %s", result.QuestionID)
	}
	if result.ResultID != "" {
		fmt.Fprintf(&b, "\nResult ID: %s", result.ResultID)
	}
	if len(result.SelectedIDs) > 0 {
		ids, _ := json.Marshal(result.SelectedIDs)
		fmt.Fprintf(&b, "\nSelected option IDs: %s", ids)
	}
	return b.String()
}

// QuestionDecisionNotice identifies runtime decisions that are not user consent.
// Model-facing text and TUI cards share the same provenance statement.
func QuestionDecisionNotice(outcome string) string {
	switch outcome {
	case tools.QuestionOutcomeDefaulted:
		return "The system adopted this default; it is not user consent or authorization."
	case tools.QuestionOutcomeNoResponse:
		return "No answer was recorded; the system closed this question. It is not user consent or authorization."
	default:
		return ""
	}
}
