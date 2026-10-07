package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
)

type headlessQuestionsBackend interface {
	ApplyQuestionOperation(context.Context, agent.QuestionOperation) (agent.QuestionReceipt, error)
}

func headlessQuestionSnapshot(q agent.QuestionSnapshot) *headlessQuestionPayload {
	payload := &headlessQuestionPayload{BindingID: q.BindingID, ToolName: "question", Header: q.Item.Header, Question: q.Item.Question, Multiple: q.Item.Multiple, RequestID: q.ID, AgentID: q.AgentID, TaskID: q.TaskID, ScopeID: q.ScopeID, Version: q.Version, ResponsePolicy: q.Item.ResponsePolicy, DefaultOptionID: q.Item.DefaultOptionID, Timer: q.Timer, Outcome: q.Outcome, ResultID: q.ResultID, Selected: q.Selected, SelectedIDs: q.SelectedIDs, DurationSeconds: int64(q.Duration / time.Second)}
	for _, o := range q.Item.Options {
		payload.Options = append(payload.Options, o.Label)
		payload.OptionDetails = append(payload.OptionDetails, o.Description)
		payload.OptionIDs = append(payload.OptionIDs, o.ID)
	}
	if !q.Deadline.IsZero() {
		payload.Deadline = new(q.Deadline)
	}
	if q.Revision != nil {
		payload.RevisionResult = q.Revision
	}
	return payload
}
func (s *headlessState) pendingQuestionSnapshots() []*headlessQuestionPayload {
	result := make([]*headlessQuestionPayload, 0, len(s.questions))
	for _, q := range s.questions {
		if q.Outcome == "" {
			result = append(result, q)
		}
	}
	slices.SortFunc(result, func(x, y *headlessQuestionPayload) int { return strings.Compare(x.RequestID, y.RequestID) })
	return result
}
func handleHeadlessQuestionCommand(cmd headlessCommand, backend headlessBackend, out *stdoutWriter) {
	questions, ok := backend.(headlessQuestionsBackend)
	if !ok {
		out.emit(headlessEnvelope{Type: "error", Payload: map[string]string{"message": "questions unavailable"}})
		return
	}
	receipt, err := questions.ApplyQuestionOperation(out.sendContext(), agent.QuestionOperation{Operation: cmd.Action, OperationID: cmd.OperationID, QuestionID: cmd.RequestID, Version: cmd.Version, BindingID: cmd.BindingID, Answers: cmd.Answers, Custom: cmd.Custom, ReplacementID: cmd.ReplacementID, UserText: cmd.UserText, SupportsInteraction: cmd.SupportsInteraction})
	if err != nil {
		receipt.Error = err.Error()
	}
	out.emit(headlessEnvelope{Type: "question_receipt", Payload: map[string]any{"operation_id": cmd.OperationID, "request_id": cmd.RequestID, "accepted": receipt.Accepted, "status": receipt.Status, "version": receipt.Version, "error": receipt.Error}})
}

type headlessQuestionSnapshotBackend interface {
	QuestionSnapshots(context.Context, agent.QuestionSnapshotQuery) (agent.QuestionReceipt, error)
}

func headlessCurrentQuestions(backend headlessBackend, query agent.QuestionSnapshotQuery) (agent.QuestionReceipt, []*headlessQuestionPayload, bool) {
	source, ok := backend.(headlessQuestionSnapshotBackend)
	if !ok {
		return agent.QuestionReceipt{}, nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	page, err := source.QuestionSnapshots(ctx, query)
	if err != nil {
		page.Error = err.Error()
	}
	if page.Error != "" {
		return page, nil, true
	}
	result := make([]*headlessQuestionPayload, 0, len(page.Questions))
	for _, q := range page.Questions {
		if q.Visible || q.Outcome != "" {
			result = append(result, headlessQuestionSnapshot(q))
		}
	}
	return page, result, true
}

// headlessCachedQuestions keeps live pending state and a bounded recent cache
// projection for backends without the durable QuestionSnapshots query.
func headlessCachedQuestions(s *headlessState) []*headlessQuestionPayload {
	pending := s.pendingQuestionSnapshots()
	result := slices.Clone(pending)
	var closed []*headlessQuestionPayload
	for _, q := range s.questions {
		if q.Outcome != "" {
			closed = append(closed, q)
		}
	}
	slices.SortFunc(closed, func(x, y *headlessQuestionPayload) int { return strings.Compare(y.RequestID, x.RequestID) })
	bytes := 0
	for i, q := range closed {
		data, err := json.Marshal(q)
		if err != nil || i == 32 || bytes+len(data) > 2<<20 {
			break
		}
		bytes += len(data)
		result = append(result, q)
	}
	return result
}
