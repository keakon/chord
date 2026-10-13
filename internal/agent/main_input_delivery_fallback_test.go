package agent

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestFallbackInputRequiresDurableExecutionPermit(t *testing.T) {
	for _, outcome := range []string{"success", "cancel", "transcript failure"} {
		t.Run(outcome, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			work := a.newDeliveryWork(recovery.DeliveryWorkUser)
			a.newTurn(work)
			a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial"})
			inputID := submitDeliveryForTest(t, a, "correction", recovery.InputDeliveryNextRequest)
			followupID := submitDeliveryForTest(t, a, "later", recovery.InputDeliveryAfterCompletion)
			payload := &llmFallbackBoundaryPayload{turnID: a.turn.ID, messages: a.ctxMgr.Snapshot(), reply: make(chan llmFallbackBoundaryResult, 1)}
			a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: a.turn.ID, Payload: payload})
			select {
			case <-payload.reply:
				t.Fatal("fallback resumed before durable claim and start")
			default:
			}
			switch outcome {
			case "cancel":
				a.turn.Cancel()
			case "transcript failure":
				a.persist.rememberFailure(persistEntry{recovery: a.recoveryManager(), agentID: identity.MainAgentID, msg: message.Message{Role: message.RoleUser, Content: "sample"}}, &recovery.PersistWriteError{RetrySafe: true, Cause: errors.New("storage unavailable")})
			}
			finishDeliveryWrites(t, a)
			if outcome != "success" {
				select {
				case <-payload.reply:
					t.Fatal("fallback crossed failed or cancelled barrier")
				default:
				}
				if outcome == "transcript failure" && a.delivery.persistenceError == nil {
					t.Fatal("missing persistence failure")
				}
				return
			}
			select {
			case result := <-payload.reply:
				if result.err != nil || !result.rebuild || !requestHasUserText(result.messages, "correction") || requestHasUserText(result.messages, "later") {
					t.Fatalf("incorrect fallback admission: %+v", result)
				}
				again := fallbackBoundaryMessages(t, a, result.messages, 0)
				count := 0
				for _, msg := range again {
					if msg.InputID == inputID {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("admitted input copies: %d", count)
				}
			default:
				t.Fatal("fallback did not resume after permit")
			}
			disk, err := a.recoveryManager().LoadInputDelivery()
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range disk.Inputs {
				if input.InputID == inputID && input.Stage != recovery.PendingInputStageStarted {
					t.Fatal("request resumed without durable start")
				}
				if input.InputID == followupID && input.Stage != recovery.PendingInputStageQueued {
					t.Fatal("fallback consumed after-completion input")
				}
			}
			persisted, err := a.recoveryManager().LoadMessages(identity.MainAgentID)
			if err != nil {
				t.Fatal(err)
			}
			if !requestHasUserText(persisted, "correction") {
				t.Fatal("request resumed before transcript was durable")
			}
		})
	}
}

func TestFallbackInputResumesAfterQueueStorageRecovery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	submitDeliveryForTest(t, a, "correction", recovery.InputDeliveryNextRequest)
	a.delivery.persistenceError = errors.New("storage unavailable")
	payload := &llmFallbackBoundaryPayload{turnID: a.turn.ID, reply: make(chan llmFallbackBoundaryResult, 1)}
	a.handleLLMFallbackBoundary(Event{Type: EventLLMFallbackBoundary, TurnID: a.turn.ID, Payload: payload})
	if a.delivery.request == nil || a.delivery.request.fallback != payload {
		t.Fatal("storage failure lost fallback continuation")
	}
	runDeliveryOperation(t, a, &deliveryOperation{control: &QueueControl{OperationID: "pause", Target: a.InputDeliveryTarget(), Action: InputQueuePause, ExpectedQueueRevision: a.delivery.committed.Revision}})
	finishDeliveryWrites(t, a)
	select {
	case result := <-payload.reply:
		// Reloaded, unclaimed ordinary inputs remain parked until fresh user
		// intent; recovery must resume the original fallback, not launch a new
		// primary request or silently replay the restored queue.
		if result.err != nil || requestHasUserText(result.messages, "correction") || !a.delivery.restoredNextInputsBlocked {
			t.Fatalf("recovered fallback violated restored queue policy: %+v", result)
		}
	default:
		t.Fatal("storage recovery did not resume the waiting fallback")
	}
}
