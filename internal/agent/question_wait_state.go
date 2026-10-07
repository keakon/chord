package agent

import "slices"

// QuestionWaitStateEvent projects live execution dependencies. Waiting is not
// part of a question's durable answer state and does not create a transcript card.
type QuestionWaitStateEvent struct {
	BindingID   string
	QuestionIDs []string
}

func (QuestionWaitStateEvent) agentEvent() {}

func (a *MainAgent) publishQuestionWaits() {
	if len(a.questions.records) == 0 {
		return
	}
	ids := make([]string, 0)
	for _, wait := range a.questions.waits {
		for _, id := range wait.ids {
			if q, ok := a.questions.records[id]; ok && q.pending() && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	a.emitToTUI(QuestionWaitStateEvent{BindingID: a.questions.binding, QuestionIDs: ids})
}
