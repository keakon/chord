package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func submitMainDraftForTest(a *MainAgent, draftID string, parts []message.ContentPart) bool {
	return a.SubmitUserInput(SubmitInput{OperationID: makeRequestID(), DraftID: draftID, Target: a.InputDeliveryTarget(), DeliveryPolicy: recovery.InputDeliveryNextRequest, Parts: parts})
}
func TestInputDeliveryDraftDeletionUsesIdentityAndRevision(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	first := submitDeliveryForTest(t, a, "first", recovery.InputDeliveryNextRequest)
	second := submitDeliveryForTest(t, a, "second", recovery.InputDeliveryNextRequest)
	result := runDeliveryOperation(t, a, &deliveryOperation{delete: &DeleteInput{OperationID: "delete", Target: a.InputDeliveryTarget(), InputID: first, ExpectedRevision: 1}})
	if result.Result != InputQueueCommitted || len(a.delivery.committed.Inputs) != 1 || a.delivery.committed.Inputs[0].InputID != second {
		t.Fatalf("delete: %+v", result)
	}
	stale := runDeliveryOperation(t, a, &deliveryOperation{delete: &DeleteInput{OperationID: "again", Target: a.InputDeliveryTarget(), InputID: first, ExpectedRevision: 1}})
	if stale.Result != InputQueueConflict {
		t.Fatal("deleted identity could be reused")
	}
}
func TestInputDeliveryIdleDraftConsumedIdentity(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.handleDeliveryOperation(&deliveryOperation{submit: &SubmitInput{OperationID: "submit", DraftID: "draft", Target: a.InputDeliveryTarget(), Content: "sample"}})
	for range 20 {
		if event, ok := nextAgentEvent(t, a.Events()).(PendingDraftConsumedEvent); ok {
			if event.DraftID != "draft" || event.InputID == "" || event.InputID != a.ctxMgr.Snapshot()[0].InputID {
				t.Fatalf("consumed identity: %+v", event)
			}
			return
		}
	}
	t.Fatal("missing consumed event")
}

func nextAgentEvent(t *testing.T, events <-chan AgentEvent) AgentEvent {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent event")
		return nil
	}
}
