package message

import "encoding/json"

// IsQuestionDecision identifies a visible decision from its durable fact. The
// kind controls local-only state visibility; a result without a user-text
// source is a recorded decision regardless of the message's role or prose.
// Withdrawals and replacements carry source_id and remain user-authored input.
func IsQuestionDecision(msg Message) bool {
	if msg.Kind == KindQuestionState || len(msg.Question) == 0 {
		return false
	}
	var fact struct {
		Result   *struct{} `json:"result"`
		SourceID string    `json:"source_id"`
	}
	return json.Unmarshal(msg.Question, &fact) == nil && fact.Result != nil && fact.SourceID == ""
}
