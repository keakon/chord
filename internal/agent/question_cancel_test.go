package agent

import (
	"context"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

func TestQuestionCancellationDispatchIgnoresUnrelatedEvents(t *testing.T) {
	for _, name := range []string{"stale_turn", "missing_payload", "newer_message", "invalid_request"} {
		t.Run(name, func(t *testing.T) {
			a := newQuestionTestAgent(t)
			a.globalConfig.QuestionTimeout = 3600
			items := questionItems("Preference")
			items[0].ResponsePolicy = tools.QuestionPolicyOptional
			id := createTestQuestions(t, a, items, false).Result.QuestionIDs[0]
			applyTestQuestion(t, a, QuestionOperation{Operation: QuestionOpPresented, QuestionID: id, SupportsInteraction: true}, time.Now())
			wait := &questionCommand{operation: QuestionOperation{Operation: questionOpWait, OperationID: "wait"}, reply: make(chan QuestionReceipt, 1), ctx: context.Background()}
			a.questions.waits["wait"] = &questionWait{command: wait, ids: []string{id}}
			a.turn.originAcceptedOrder = 2
			before := a.questions.records[id]
			messages, transactions := a.ctxMgr.MessageCount(), len(a.questions.transactions)
			turn := a.turn
			event := Event{Type: EventTurnCancelled, TurnID: turn.ID, Payload: &TurnCancelledPayload{TurnID: turn.ID}}
			switch name {
			case "stale_turn":
				event.TurnID = turn.ID + 1
			case "missing_payload":
				event.Payload = (*TurnCancelledPayload)(nil)
			case "newer_message":
				event = Event{Type: EventTurnCancelRequested, Payload: &turnCancelRequestPayload{AcceptedUpTo: 1}}
			case "invalid_request":
				event = Event{Type: EventTurnCancelRequested}
			}
			a.dispatch(event)
			if a.turn != turn || a.questions.paused || a.questions.active != nil || len(a.questions.queue) != 0 || len(a.questions.waits) != 1 || !reflect.DeepEqual(a.questions.records[id], before) || a.ctxMgr.MessageCount() != messages || len(a.questions.transactions) != transactions {
				t.Fatal("unrelated cancellation changed the current turn or question state")
			}
			select {
			case receipt := <-wait.reply:
				t.Fatalf("unrelated cancellation interrupted the wait: %+v", receipt)
			default:
			}
		})
	}
}

func TestQuestionCancellationDispatchInterruptsCurrentWait(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	wait := &questionCommand{reply: make(chan QuestionReceipt, 1)}
	a.questions.waits["wait"] = &questionWait{command: wait, ids: []string{id}}
	a.dispatch(Event{Type: EventTurnCancelled, TurnID: a.turn.ID, Payload: &TurnCancelledPayload{TurnID: a.turn.ID}})
	select {
	case receipt := <-wait.reply:
		if receipt.Result.Status != tools.QuestionStatusWaitInterrupted {
			t.Fatalf("cancelled wait = %+v", receipt)
		}
	default:
		t.Fatal("current cancellation did not interrupt the wait")
	}
}

func TestQuestionOperationCancellationWhileEventQueueIsFull(t *testing.T) {
	a := &MainAgent{eventCh: make(chan Event, 1), stoppingCh: make(chan struct{})}
	a.eventCh <- Event{Type: EventUserMessage}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.ApplyQuestionOperation(ctx, QuestionOperation{Operation: QuestionOpAnswer, OperationID: "answer", QuestionID: "choice"})
		done <- err
	}()
	deadline := time.After(3 * time.Second)
	for a.eventBackpressure.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("operation did not reach event admission backpressure")
		default:
			runtime.Gosched()
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancelled operation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled operation remained blocked on event admission")
	}
}

func TestQuestionCancelTaskStopsRunningTurn(t *testing.T) {
	a := newQuestionTestAgent(t)
	createTestQuestions(t, a, questionItems("Choice"), false)
	turnCtx := a.turn.Ctx
	receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{
		Operation: QuestionOpCancelTask, OperationID: "cancel-task", UserText: "Stop this task",
	}})
	if !receipt.Accepted {
		t.Fatalf("cancel rejected: %+v", receipt)
	}
	if turnCtx.Err() == nil {
		t.Fatal("cancel_task accepted but the running turn was not cancelled")
	}
}

func TestQuestionWithdrawRequirementKeepsRunningTurn(t *testing.T) {
	a := newQuestionTestAgent(t)
	id := createTestQuestions(t, a, questionItems("Choice"), false).Result.QuestionIDs[0]
	turnCtx := a.turn.Ctx
	receipt := runQuestionCommand(t, a, &questionCommand{operation: QuestionOperation{
		Operation: QuestionOpWithdraw, OperationID: "withdraw", QuestionID: id, UserText: "No longer needed",
	}})
	if !receipt.Accepted {
		t.Fatalf("withdraw rejected: %+v", receipt)
	}
	if turnCtx.Err() != nil {
		t.Fatal("withdraw_requirement must not cancel the running turn")
	}
}
