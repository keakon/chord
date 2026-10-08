package agent

import (
	"testing"
	"time"
)

// TestMainLLMGateKeepsTurnRunningWhenCompactionDraftFailsToApply locks the
// recovery for a pre-request barrier whose ready draft cannot be applied: the
// gate owes the active turn its next request, so it must continue on the live
// context instead of parking the turn behind an event that never comes.
func TestMainLLMGateKeepsTurnRunningWhenCompactionDraftFailsToApply(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.newTurn()
	turnID := a.turn.ID

	// A draft whose source refs cannot match the live transcript (the observed
	// provenance failure) is rejected; the gate must still move on.
	a.compactionState.readyDraft = &compactionDraft{
		PlanID:    9001,
		HeadSplit: 1,
		SourceRefs: []checkpointSourceRef{{
			SessionID:            "stale-session",
			TranscriptGeneration: "generation",
			SegmentKind:          "archived_prefix",
			SegmentID:            "segment",
			Ordinal:              0,
			CanonicalPayloadHash: "stale-payload",
			Role:                 "user",
		}},
		Target: compactionTarget{
			sessionEpoch: a.sessionEpoch,
			turnID:       turnID,
			turnEpoch:    a.turn.Epoch,
		},
	}

	a.beginMainLLMAfterPreparation(a.turn.Ctx, turnID, "")

	if a.compactionState.readyDraft != nil {
		t.Fatal("failed draft is still parked at the barrier")
	}
	if !a.compactionContinuationStalled {
		t.Fatal("gate did not record the barrier stall")
	}
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatalf("gate parked the turn instead of continuing it: %+v", a.turn)
	}
	if !a.mainLLMRequestInFlight.Load() {
		t.Fatal("gate returned without dispatching the owed request")
	}

	// Follow-up input must queue behind the live request, not settle the turn
	// the failed gate just revived.
	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "keep going after the gate"})
	if a.turn == nil || a.turn.ID != turnID {
		t.Fatal("follow-up message settled the running turn")
	}
	if got := len(a.pendingUserMessages); got != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1 queued behind the running request", got)
	}
	if !a.mainLLMRequestInFlight.Load() {
		t.Fatal("follow-up message cleared the running request")
	}
}

// TestStalledTurnSettlesForContinueAndUserInput locks the revive path for a
// turn parked by the pre-request gate: user-driven actions settle it and start
// real work instead of hitting the active-turn guards, while a turn that is
// merely busy keeps queueing input.
func TestStalledTurnSettlesForContinueAndUserInput(t *testing.T) {
	t.Run("continue", func(t *testing.T) {
		a := newReadyTestMainAgent(t)
		a.newTurn()
		stalledID := a.turn.ID
		a.compactionContinuationStalled = true

		a.handleContinueFromContext()

		if a.compactionContinuationStalled {
			t.Fatal("continue left the stall marker set")
		}
		if a.turn == nil || a.turn.ID == stalledID {
			t.Fatalf("continue was swallowed by the stalled turn: %+v", a.turn)
		}
	})

	t.Run("user message", func(t *testing.T) {
		a := newReadyTestMainAgent(t)
		a.newTurn()
		stalledID := a.turn.ID
		a.compactionContinuationStalled = true

		a.handleUserMessage(Event{Type: EventUserMessage, Payload: "resume the work"})

		if a.compactionContinuationStalled {
			t.Fatal("user message left the stall marker set")
		}
		if a.turn == nil || a.turn.ID == stalledID {
			t.Fatalf("user message queued behind the stalled turn: %+v", a.turn)
		}
		if got := len(a.pendingUserMessages); got != 0 {
			t.Fatalf("len(pendingUserMessages) = %d, want the message to start a new turn", got)
		}
	})

	t.Run("busy turn still queues", func(t *testing.T) {
		a := newReadyTestMainAgent(t)
		a.newTurn()
		busyID := a.turn.ID

		a.handleUserMessage(Event{Type: EventUserMessage, Payload: "queued while busy"})

		if a.turn == nil || a.turn.ID != busyID {
			t.Fatal("busy turn was settled by a queued message")
		}
		if got := len(a.pendingUserMessages); got != 1 {
			t.Fatalf("len(pendingUserMessages) = %d, want 1 queued while busy", got)
		}
	})
}

// waitForQuestionCommitForTest settles the pending question transaction the
// way the event loop would, so a directly invoked resume does not leave
// r.active pointing at an unhandled commit.
func waitForQuestionCommitForTest(t *testing.T, a *MainAgent) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for a.questions.active != nil {
		select {
		case evt := <-a.eventCh:
			switch evt.Type {
			case EventQuestionCommitted:
				a.handleQuestionCommitted(evt.Payload.(*questionCommit))
			case EventQuestionCommand:
				a.handleQuestionCommand(evt.Payload.(*questionCommand))
			}
		case <-deadline:
			t.Fatal("question commit did not settle")
		}
	}
}

// TestResumeQuestionWorkOnlyClaimsContinuationWithPendingQuestion locks the
// resume path against swallowing a continue: with the runtime paused but no
// pending question to re-present, resume is bookkeeping and the caller keeps
// its continuation.
func TestResumeQuestionWorkOnlyClaimsContinuationWithPendingQuestion(t *testing.T) {
	for _, foreign := range []string{"scope", "task"} {
		t.Run("pending in another "+foreign, func(t *testing.T) {
			a := newQuestionTestAgent(t)
			createTestQuestions(t, a, questionItems("Choice"), false)
			for id, question := range a.questions.records {
				if foreign == "scope" {
					question.ScopeID = "another-scope"
				} else {
					question.TaskID = "another-task"
				}
				a.questions.records[id] = question
			}
			if r := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpPause, OperationID: makeRequestID()}}); !r.Accepted {
				t.Fatalf("pause rejected: %+v", r)
			}
			if a.resumeQuestionWork() {
				t.Fatal("a pending question belonging to another scope or task claimed the continuation")
			}
			waitForQuestionCommitForTest(t, a)
		})
	}
	t.Run("no pending question", func(t *testing.T) {
		a := newQuestionTestAgent(t)
		if r := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpPause, OperationID: makeRequestID()}}); !r.Accepted {
			t.Fatalf("pause rejected: %+v", r)
		}
		if !a.questions.paused {
			t.Fatal("pause did not take effect")
		}

		if a.resumeQuestionWork() {
			t.Fatal("resume without a pending question claimed the continuation")
		}
		waitForQuestionCommitForTest(t, a)
		if a.questions.paused {
			t.Fatal("resume did not clear the paused state")
		}
	})

	t.Run("pending question", func(t *testing.T) {
		a := newQuestionTestAgent(t)
		createTestQuestions(t, a, questionItems("Choice"), false)
		if r := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{Operation: questionOpPause, OperationID: makeRequestID()}}); !r.Accepted {
			t.Fatalf("pause rejected: %+v", r)
		}

		if !a.resumeQuestionWork() {
			t.Fatal("resume with a pending question must claim the continuation")
		}
		waitForQuestionCommitForTest(t, a)
	})
}
