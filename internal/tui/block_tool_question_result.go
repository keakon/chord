package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// decodeQuestionResult accepts a clean current-protocol payload.
func decodeQuestionResult(payload string) (result tools.QuestionResult, ok bool) {
	if json.Unmarshal([]byte(payload), &result) != nil {
		return result, false
	}
	switch result.Status {
	case tools.QuestionStatusAccepted, tools.QuestionStatusResolved, tools.QuestionStatusWaitInterrupted:
		return result, true
	default:
		return result, false
	}
}

func questionResultDisplayLines(result tools.QuestionResult, answersShown bool) []string {
	var lines []string
	switch result.Status {
	case tools.QuestionStatusAccepted:
		lines = append(lines, "Questions sent; awaiting answers.")
	case tools.QuestionStatusWaitInterrupted:
		lines = append(lines, "Waiting interrupted.")
	case tools.QuestionStatusResolved:
		if len(result.Answers) == 0 {
			lines = append(lines, "Decisions received.")
		}
	}
	if n := len(result.PendingIDs); n > 0 {
		if n == 1 {
			lines = append(lines, "1 question still awaiting an answer.")
		} else {
			lines = append(lines, fmt.Sprintf("%d questions still awaiting answers.", n))
		}
	}
	if !answersShown {
		for _, answer := range result.Answers {
			if header := strings.TrimSpace(answer.Header); header != "" {
				lines = append(lines, header)
			}
			outcome := questionOutcomeDisplay(answer.Outcome)
			if answer.Outcome == tools.QuestionOutcomeAnswered {
				outcome = "answered"
			}
			if outcome != "" {
				lines = append(lines, "Outcome: "+outcome)
			}
			for _, selected := range answer.Selected {
				lines = append(lines, "Selected: "+selected)
			}
		}
	}
	return lines
}
