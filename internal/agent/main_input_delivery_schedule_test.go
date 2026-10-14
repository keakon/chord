package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// Drive only storage completion events. Advancing a followup is explicit in
// these tests, so every assertion observes a precise durable boundary.
func finishDeliveryWrites(t *testing.T, a *MainAgent) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for a.delivery.inflight != nil || len(a.delivery.pending) != 0 {
		select {
		case evt := <-a.eventCh:
			if evt.Type == eventInputDeliveryCommitted {
				a.dispatch(evt)
			}
		case <-a.outputCh:
		case <-timeout.C:
			t.Fatal("delivery write did not finish")
		}
	}
}
func submitDeliveryForTest(t *testing.T, a *MainAgent, id string, policy recovery.InputDeliveryPolicy) string {
	t.Helper()
	result := runDeliveryOperation(t, a, &deliveryOperation{submit: &SubmitInput{OperationID: id, Target: a.InputDeliveryTarget(), Content: id, DeliveryPolicy: policy}})
	if result.Result != InputQueueCommitted {
		t.Fatalf("submit %s: %+v", id, result)
	}
	return result.InputID
}
func TestInputDeliveryOrdinaryRequestExcludesFollowup(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	followup := submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	ordinary := submitDeliveryForTest(t, a, "correction", recovery.InputDeliveryNextRequest)
	ids := selectDeliveryInputs(a.delivery.committed, false)
	if len(ids) != 1 || ids[0] != ordinary {
		t.Fatalf("request selection: %v", ids)
	}
	// Suppress network dispatch after the permit without bypassing either write.
	a.delivery.request = &deliveryRequest{turnID: a.turn.ID + 1}
	a.claimDeliveryInputs(ids, false)
	finishDeliveryWrites(t, a)
	messages := a.ctxMgr.Snapshot()
	if len(messages) != 1 || messages[0].InputID != ordinary {
		t.Fatalf("request transcript: %+v", messages)
	}
	disk, err := a.recoveryManager().LoadInputDelivery()
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range disk.Inputs {
		if input.InputID == followup && (input.Stage != recovery.PendingInputStageQueued || input.WorkID != "") {
			t.Fatalf("followup consumed: %+v", input)
		}
		if input.InputID == ordinary && (input.Stage != recovery.PendingInputStageStarted || input.WorkID != work) {
			t.Fatalf("missing request permit: %+v", input)
		}
	}
}
func TestInputDeliveryFollowupsRequireDurableCompletion(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial task"})
	first := submitDeliveryForTest(t, a, "first", recovery.InputDeliveryAfterCompletion)
	second := submitDeliveryForTest(t, a, "second", recovery.InputDeliveryAfterCompletion)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("released active work: %v", ids)
	}
	a.settleDeliveryWork(work, recovery.DeliveryWorkCompleted)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("released before commit: %v", ids)
	}
	finishDeliveryWrites(t, a)
	ids := selectDeliveryInputs(a.delivery.committed, true)
	if len(ids) != 1 || ids[0] != first {
		t.Fatalf("first selection: %v state=%+v error=%v", ids, a.delivery.committed, a.delivery.persistenceError)
	}
	a.delivery.request = &deliveryRequest{turnID: a.turn.ID + 100}
	a.claimDeliveryInputs(ids, true)
	finishDeliveryWrites(t, a)
	nextWork := a.turn.DeliveryWorkID
	if nextWork == work || nextWork == "" {
		t.Fatal("followup reused predecessor work")
	}
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("second overtook first: %v", ids)
	}
	a.settleDeliveryWork(nextWork, recovery.DeliveryWorkCompleted)
	finishDeliveryWrites(t, a)
	ids = selectDeliveryInputs(a.delivery.committed, true)
	if len(ids) != 1 || ids[0] != second {
		t.Fatalf("second selection: %v", ids)
	}
}
func TestInputDeliveryCancelledClaimRetainsUnappendedInput(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	id := submitDeliveryForTest(t, a, "correction", recovery.InputDeliveryNextRequest)
	a.claimDeliveryInputs([]string{id}, false)
	a.delivery.cancelGeneration.Add(1)
	finishDeliveryWrites(t, a)
	if len(a.ctxMgr.Snapshot()) != 0 {
		t.Fatal("cancelled claim appended input")
	}
	state := a.InputQueueSnapshot().State
	if len(state.Inputs) != 1 || state.Inputs[0].Stage != recovery.PendingInputStageQueued || state.Gate.Mode != recovery.FollowupGatePaused {
		t.Fatalf("cancel lost reserved input: %+v", state)
	}
}
func TestInputDeliveryFailurePauseSurvivesOrdinaryCompletion(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	a.settleDeliveryWork(work, recovery.DeliveryWorkFailed)
	finishDeliveryWrites(t, a)
	ordinaryWork := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(ordinaryWork)
	submitDeliveryForTest(t, a, "ordinary", recovery.InputDeliveryNextRequest)
	a.delivery.request = &deliveryRequest{turnID: a.turn.ID + 1}
	a.claimDeliveryInputs(selectDeliveryInputs(a.delivery.committed, false), false)
	finishDeliveryWrites(t, a)
	a.settleDeliveryWork(ordinaryWork, recovery.DeliveryWorkCompleted)
	finishDeliveryWrites(t, a)
	if a.delivery.committed.Gate.Reason != recovery.FollowupPredecessorFailed || len(selectDeliveryInputs(a.delivery.committed, true)) != 0 {
		t.Fatal("ordinary completion released paused followup")
	}
}

func TestInputDeliveryEndToEndSerialRequests(t *testing.T) {
	t.Parallel()
	a, provider := scenarioAgent(t,
		scriptedStreamCall{holdAfterStreams: true, resp: &message.Response{Content: "initial complete", StopReason: "stop"}},
		scriptedStreamCall{resp: &message.Response{Content: "correction complete", StopReason: "stop"}},
		scriptedStreamCall{resp: &message.Response{Content: "first complete", StopReason: "stop"}},
		scriptedStreamCall{resp: &message.Response{Content: "second complete", StopReason: "stop"}},
	)
	provider.streamedCh, provider.releaseCh = make(chan struct{}), make(chan struct{})
	startMainAgentLoopForTest(t, a)
	a.SendUserMessageWithReceipt("initial", "initial-request")
	select {
	case <-provider.streamedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("initial request did not start")
	}
	for _, input := range []SubmitInput{
		{RequestID: "first-request", Content: "first-followup", DeliveryPolicy: recovery.InputDeliveryAfterCompletion},
		{RequestID: "second-request", Content: "second-followup", DeliveryPolicy: recovery.InputDeliveryAfterCompletion},
		{RequestID: "correction-request", Content: "ordinary-correction", DeliveryPolicy: recovery.InputDeliveryNextRequest},
	} {
		input.Target = a.InputDeliveryTarget()
		if !a.SubmitUserInput(input) {
			t.Fatal("input not admitted")
		}
	}
	receipts := map[string]int{}
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	queued := 0
	for queued < 3 {
		select {
		case event := <-a.Events():
			if result, ok := event.(InputResultEvent); ok {
				receipts[result.RequestID]++
				if result.Status == InputQueued {
					if result.Target == nil || *result.Target != a.InputDeliveryTarget() {
						t.Fatalf("queued receipt lost target: %+v", result)
					}
					queued++
				} else if result.Status == InputRejected || result.Status == InputUnknown {
					t.Fatalf("receipt: %+v", result)
				}
			}
		case <-timeout.C:
			t.Fatal("inputs were not saved")
		}
	}
	close(provider.releaseCh)
	works := map[string]bool{}
	finished := false
	for !finished {
		select {
		case event := <-a.Events():
			switch event := event.(type) {
			case GlobalIdleEvent:
				requests, _ := provider.snapshot()
				if len(requests) < 4 {
					t.Fatal("aggregate idle settled before queued requests ran")
				}
			case InputResultEvent:
				receipts[event.RequestID]++
			case PendingDraftConsumedEvent:
				if event.WorkID != "" {
					works[event.WorkID] = true
				}
			case InputQueueChangedEvent:
				if state := event.Snapshot.State; state != nil && len(state.Inputs) == 0 {
					for _, work := range state.Works {
						if work.WorkID == state.Gate.WaitingWorkID && work.Outcome == recovery.DeliveryWorkCompleted {
							finished = true
						}
					}
				}
			}
		case <-timeout.C:
			t.Fatalf("queue did not finish: %+v", a.InputQueueSnapshot())
		}
	}
	requests, _ := provider.snapshot()
	if len(requests) != 4 || len(works) != 3 {
		t.Fatalf("requests=%d independent works=%d", len(requests), len(works))
	}
	contains := func(messages []message.Message, text string) bool {
		for _, msg := range messages {
			if msg.Content == text {
				return true
			}
		}
		return false
	}
	if !contains(requests[1], "ordinary-correction") || contains(requests[1], "first-followup") || contains(requests[1], "second-followup") {
		t.Fatal("ordinary correction did not take priority")
	}
	if !contains(requests[2], "first-followup") || contains(requests[2], "second-followup") || !contains(requests[3], "second-followup") {
		t.Fatal("followups were not serial")
	}
	for _, id := range []string{"first-request", "second-request", "correction-request"} {
		if receipts[id] != 1 {
			t.Fatalf("%s receipts=%d", id, receipts[id])
		}
	}
}

func TestInputDeliveryCoveredBatchPreservesNewerWork(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	old := submitDeliveryForTest(t, a, "old", recovery.InputDeliveryNextRequest)
	newer := submitDeliveryForTest(t, a, "new", recovery.InputDeliveryNextRequest)
	a.delivery.acceptedOrders[old] = 1
	a.delivery.acceptedOrders[newer] = 2
	a.raiseCancelUpTo(1)
	a.delivery.request = &deliveryRequest{turnID: a.turn.ID + 1}
	a.claimDeliveryInputs(selectDeliveryInputs(a.delivery.committed, false), false)
	finishDeliveryWrites(t, a)
	if a.turn == nil || a.turn.DeliveryWorkID != work || a.turn.Ctx.Err() != nil {
		t.Fatal("covered input cancelled newer work")
	}
	if len(a.delivery.committed.Inputs) != 1 || a.delivery.committed.Inputs[0].InputID != newer || a.delivery.committed.Inputs[0].Stage != recovery.PendingInputStageQueued {
		t.Fatal("newer input was included in cancelled batch")
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 || msgs[0].InputID != old {
		t.Fatal("covered input lost cancelled transcript presentation")
	}
}

func TestInputDeliveryPendingWritePreventsGlobalIdle(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.delivery.inflight = &deliveryOperation{}
	if a.emitGlobalIdleIfReady() {
		t.Fatal("idle crossed a pending persistence barrier")
	}
	a.delivery.inflight = nil
	a.delivery.committed = &recovery.InputDeliveryState{Gate: recovery.FollowupGate{Mode: recovery.FollowupGatePaused, Reason: recovery.FollowupUserPaused}, Inputs: []recovery.PendingInput{{InputID: "input", Sequence: 1, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryAfterCompletion}}}
	if a.deliveryPreventsGlobalIdle() {
		t.Fatal("paused queue suppressed idle indefinitely")
	}
}
