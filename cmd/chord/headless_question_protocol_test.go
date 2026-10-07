package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func questionEventForTest(id, header, text string, labels, details []string, multiple bool, deadline time.Time, owner string) agent.QuestionStateEvent {
	item := tools.QuestionItem{Header: header, Question: text, Multiple: multiple}
	for i, label := range labels {
		opt := tools.QuestionOption{ID: label, Label: label}
		if i < len(details) {
			opt.Description = details[i]
		}
		item.Options = append(item.Options, opt)
	}
	return agent.QuestionStateEvent{Question: agent.QuestionSnapshot{ID: id, Item: item, Deadline: deadline, AgentID: owner, Visible: true, Version: 1}}
}

func testPendingQuestion(s *headlessState) *headlessQuestionPayload {
	for _, q := range s.questions {
		if q.Outcome == "" {
			return q
		}
	}
	return nil
}

func TestHeadlessHistoricalRevisionPreservesOutcomeAndNewPending(t *testing.T) {
	state := &headlessState{questions: map[string]*headlessQuestionPayload{
		"historical": {RequestID: "historical", Version: 2, Outcome: tools.QuestionOutcomeDefaulted},
		"pending":    {RequestID: "pending", Version: 1},
	}}
	q := agent.QuestionSnapshot{ID: "historical", Version: 3, Outcome: tools.QuestionOutcomeDefaulted, ResultID: "original", ScopeID: "original-scope"}
	revised := tools.QuestionAnswer{QuestionID: q.ID, ResultID: "revision", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Updated preference"}}
	fact, err := json.Marshal(agent.QuestionFact{Operation: agent.QuestionOpRevise, Updates: []agent.QuestionSnapshot{q}, Result: &revised})
	if err != nil {
		t.Fatal(err)
	}
	events := filterHeadlessEvent(agent.QuestionTranscriptEvent{Message: message.Message{Question: fact}}, state)
	if len(events) != 1 || events[0].Type != "question_updated" {
		t.Fatalf("revision events = %#v", events)
	}
	payload := events[0].Payload.(*headlessQuestionPayload)
	if payload.Outcome != tools.QuestionOutcomeDefaulted || payload.ResultID != "original" || payload.ScopeID != "original-scope" || payload.RevisionResult == nil || payload.RevisionResult.ResultID != "revision" {
		t.Fatalf("historical identity or revision lost: %#v", payload)
	}
	if duplicate := filterHeadlessEvent(agent.QuestionStateEvent{Question: q}, state); len(duplicate) != 0 {
		t.Fatalf("duplicate revision notification: %#v", duplicate)
	}
	if pending := state.pendingQuestionSnapshots(); len(pending) != 1 || pending[0].RequestID != "pending" {
		t.Fatalf("unrelated pending question changed: %#v", pending)
	}
}

type questionSnapshotBackend struct {
	mockBackend
	questions   []agent.QuestionSnapshot
	query       agent.QuestionSnapshotQuery
	nextAfterID string
}

func (b *questionSnapshotBackend) QuestionSnapshots(_ context.Context, query agent.QuestionSnapshotQuery) (agent.QuestionReceipt, error) {
	b.query = query
	return agent.QuestionReceipt{SessionID: "current-session", BindingID: "binding", NextAfterID: b.nextAfterID, Questions: b.questions}, nil
}

func TestHeadlessStatusUsesCommittedQuestionsBeforePushArrives(t *testing.T) {
	backend := &questionSnapshotBackend{questions: []agent.QuestionSnapshot{
		{ID: "resolved", Visible: true, Version: 2, Outcome: tools.QuestionOutcomeAnswered},
		{ID: "next", Visible: true, Version: 1},
		{ID: "not-open", Version: 1},
	}}
	state := &headlessState{questions: map[string]*headlessQuestionPayload{
		"resolved": {RequestID: "resolved", Version: 1},
	}}
	out := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, out.writer())
	env := findHeadlessEnvelopeValue(out.drain(), "status_response")
	if env == nil {
		t.Fatal("missing status response")
	}
	payload := headlessPayloadMap(t, env.Payload)
	pending := payload["pending_questions"].([]any)
	if payload["session_id"] != "current-session" || len(pending) != 1 || pending[0].(map[string]any)["request_id"] != "next" || len(payload["questions"].([]any)) != 2 {
		t.Fatalf("status did not use committed projection: %#v", payload)
	}
}

func TestHeadlessStatusCarriesSnapshotRevision(t *testing.T) {
	revision := tools.QuestionAnswer{QuestionID: "history", ResultID: "revision", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Updated preference"}}
	backend := &questionSnapshotBackend{questions: []agent.QuestionSnapshot{{
		ID: "history", Visible: true, Version: 3,
		Outcome: tools.QuestionOutcomeDefaulted, ResultID: "original",
		Revision: &revision,
	}}}
	state := &headlessState{questions: map[string]*headlessQuestionPayload{}}
	out := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status"}, backend, state, out.writer())
	env := findHeadlessEnvelopeValue(out.drain(), "status_response")
	if env == nil {
		t.Fatal("missing status response")
	}
	payload := headlessPayloadMap(t, env.Payload)
	questions := payload["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("status questions = %#v", questions)
	}
	entry := questions[0].(map[string]any)
	got := entry["revision_result"]
	if got == nil {
		t.Fatalf("status discarded a committed revision: %#v", entry)
	}
	revisionMap := got.(map[string]any)
	if revisionMap["result_id"] != "revision" || entry["result_id"] != "original" {
		t.Fatalf("revision or original identity lost: %#v", entry)
	}
}

func TestHeadlessQuestionUpdatesKeepProjectedRevision(t *testing.T) {
	revised := tools.QuestionAnswer{QuestionID: "history", ResultID: "revision", Outcome: tools.QuestionOutcomeAnswered, Selected: []string{"Updated preference"}}
	q := agent.QuestionSnapshot{ID: "history", Visible: true, Version: 4, Outcome: tools.QuestionOutcomeAnswered, ResultID: "original", Selected: []string{"Original preference"}, Dependency: agent.QuestionDependencyWithdrawn, Revision: &revised}
	state := &headlessState{questions: map[string]*headlessQuestionPayload{
		q.ID: {RequestID: q.ID, Version: 3, Outcome: q.Outcome, ResultID: q.ResultID, RevisionResult: &revised},
	}}
	events := filterHeadlessEvent(agent.QuestionStateEvent{Question: q}, state)
	if len(events) != 1 || events[0].Type != "question_updated" {
		t.Fatalf("question update events = %#v", events)
	}
	update := events[0].Payload.(*headlessQuestionPayload)
	if update.RevisionResult == nil || update.RevisionResult.ResultID != "revision" || update.ResultID != "original" || update.Selected[0] != "Original preference" {
		t.Fatalf("question update lost revision or original decision: %+v", update)
	}
	if state.questions[q.ID] != update {
		t.Fatal("cache and published event disagree")
	}
	// A client with no cached history receives the same authoritative answer.
	empty := &headlessState{}
	filterHeadlessEvent(agent.QuestionStateEvent{Question: q}, empty)
	if empty.questions[q.ID].RevisionResult == nil || empty.questions[q.ID].RevisionResult.ResultID != "revision" {
		t.Fatal("question update depended on the event cache")
	}
}

func TestHeadlessStatusPassesQuestionCursorAndReconciliationIDs(t *testing.T) {
	backend := &questionSnapshotBackend{nextAfterID: "next", questions: []agent.QuestionSnapshot{{ID: "closed", Outcome: tools.QuestionOutcomeAnswered}}}
	state := &headlessState{questions: map[string]*headlessQuestionPayload{}}
	out := newTestOut()
	handleHeadlessCommand(headlessCommand{Type: "status", BindingID: "binding", QuestionsAfterID: "previous", QuestionIDs: []string{"closed"}}, backend, state, out.writer())
	env := findHeadlessEnvelopeValue(out.drain(), "status_response")
	if env == nil {
		t.Fatal("missing status page")
	}
	payload := headlessPayloadMap(t, env.Payload)
	if payload["question_binding_id"] != "binding" || payload["questions_next_after_id"] != "next" || backend.query.AfterID != "previous" || backend.query.BindingID != "binding" || len(backend.query.RequestIDs) != 1 || backend.query.RequestIDs[0] != "closed" {
		t.Fatalf("lost paging contract: %+v %+v", payload, backend.query)
	}
}
