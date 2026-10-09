package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func stageCancelTestMailbox(a *MainAgent) *SubAgentMailboxMessage {
	msg := &SubAgentMailboxMessage{
		MessageID: "worker-result-1",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Summary:   "Review complete",
	}
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{msg}
	a.activeSubAgentMailbox = msg
	a.activeSubAgentMailboxAck = true
	return msg
}

func TestCancelParksMailboxBeforeSubAgentInterruptCompletes(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	stageCancelTestMailbox(a)
	sub := newPersistenceTestSubAgent(a, "worker-2")
	entered := make(chan struct{})
	release := make(chan struct{})
	sub.turn.activeToolBatchCancel = func() {
		close(entered)
		<-release
	}
	a.subs.subAgents[sub.instanceID] = sub
	done := make(chan bool, 1)
	go func() { done <- a.CancelCurrentTurn() }()
	defer func() {
		close(release)
		select {
		case cancelled := <-done:
			if !cancelled {
				t.Error("cancel returned false")
			}
		case <-time.After(3 * time.Second):
			t.Error("cancel did not finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("subagent cancellation did not start")
	}
	evt := <-a.eventCh
	if evt.Type != EventTurnCancelled {
		t.Fatalf("event = %s", evt.Type)
	}
	a.handleTurnCancelled(evt)
	a.drainRunnableMailboxWork()
	if a.turn != nil || !a.mailboxDeliveryPaused.Load() {
		t.Fatalf("cancel closeout did not park mailbox: turn=%d, paused=%v",
			a.currentTurnID(), a.mailboxDeliveryPaused.Load())
	}
}

func TestCancelledMailboxResumesWithDraft(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	stageCancelTestMailbox(a)
	if !a.CancelCurrentTurn() {
		t.Fatal("cancel returned false")
	}
	a.handleTurnCancelled(<-a.eventCh)
	a.handlePendingDraftUpsert(Event{Payload: pendingUserMessage{
		DraftID: "draft-1", Content: "Continue the review", FromUser: true,
	}})
	if a.turn == nil {
		t.Fatal("draft did not start a turn")
	}
	if a.mailboxDeliveryPaused.Load() || len(a.activeSubAgentMailboxes) == 0 {
		t.Fatalf("draft did not resume mailbox: paused=%v, active=%d",
			a.mailboxDeliveryPaused.Load(), len(a.activeSubAgentMailboxes))
	}
}

func TestCancelAcceptedInputParksMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	stageCancelTestMailbox(a)
	a.activeSubAgentMailboxAck = false
	a.requeueActiveSubAgentMailbox()
	a.SendUserMessage("Review the result")
	if !a.CancelCurrentTurn() {
		t.Fatal("cancel returned false")
	}
	evt := <-a.eventCh
	if evt.Type != EventUserMessage {
		t.Fatalf("first event = %s", evt.Type)
	}
	a.handleUserMessage(evt)
	evt = <-a.eventCh
	if evt.Type != EventTurnCancelRequested {
		t.Fatalf("second event = %s", evt.Type)
	}
	a.handleTurnCancelRequested(evt)
	a.emitGlobalIdleIfReady()
	if a.turn != nil || !a.mailboxDeliveryPaused.Load() {
		t.Fatalf("cancelled input restarted mailbox: turn=%d, paused=%v",
			a.currentTurnID(), a.mailboxDeliveryPaused.Load())
	}
}

func TestCancelledMailboxConsumedWithNextUserAction(t *testing.T) {
	for _, action := range []string{"input", "draft", "continue"} {
		t.Run(action, func(t *testing.T) {
			provider := &keyRecordingProvider{calls: []scriptedStreamCall{completedReply("Result received")}}
			a := newCancelWindowTestAgent(t, provider)
			a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "Review the result"})
			a.newTurn()
			mailbox := stageCancelTestMailbox(a)
			if !a.CancelCurrentTurn() {
				t.Fatal("cancel returned false")
			}
			// Queue continuation before closeout to cover an action arriving
			// while the loop still has the preceding cancellation to process.
			switch action {
			case "input":
				a.SendUserMessage("Continue the review")
			case "draft":
				if !a.QueuePendingUserDraft("draft-1", []message.ContentPart{{Type: message.ContentPartText, Text: "Continue the review"}}) {
					t.Fatal("draft was not accepted")
				}
			case "continue":
				a.ContinueFromContext()
			}
			a.handleTurnCancelled(<-a.eventCh)
			if !a.mailboxDeliveryPaused.Load() || a.hasRunnableMailboxWork() {
				t.Fatal("mailbox was not parked before the next action")
			}
			drainAgentEvents(a.outputCh)
			startMainAgentLoopForTest(t, a)
			collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
				started := false
				for _, evt := range events {
					switch evt.(type) {
					case RequestCycleStartedEvent:
						started = true
					case GlobalIdleEvent:
						if started {
							return true
						}
					}
				}
				return false
			})
			requests := provider.recorded()
			if len(requests) != 1 {
				t.Fatalf("requests = %d, want one resumed request", len(requests))
			}
			if got := countSubAgentMailboxMessages(requests[0].messages, mailbox.MessageID); got != 1 {
				t.Fatalf("mailbox copies in resumed request = %d, want 1", got)
			}
			acks, err := loadSubAgentMailboxAcks(a.SessionDir())
			if err != nil {
				t.Fatal(err)
			}
			ack := acks[mailbox.MessageID]
			if ack.Outcome != mailboxAckOutcomeConsumed || ack.ReplyToMailboxID != mailbox.MessageID || ack.ReplySummary != "Result received" {
				t.Fatalf("mailbox ack = %#v, want consumed with the resumed reply", ack)
			}
			if a.mailboxDeliveryPaused.Load() {
				t.Fatal("user action left mailbox paused")
			}
		})
	}
}

func TestMailboxPauseIgnoresStaleCancel(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	stale := a.abortTurn(a.turn, true)
	a.newTurn()
	a.handleTurnCancelled(stale)
	if a.mailboxDeliveryPaused.Load() {
		t.Fatal("stale cancel paused the replacement turn")
	}
}

func TestMailboxPauseUnaffectedBySubAgentOnlyCancel(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newPersistenceTestSubAgent(a, "worker-1")
	a.subs.subAgents[sub.instanceID] = sub
	if !a.CancelCurrentTurn() {
		t.Fatal("subagent cancel returned false")
	}
	a.handleTurnCancelRequested(<-a.eventCh)
	if a.mailboxDeliveryPaused.Load() {
		t.Fatal("subagent-only cancel paused main mailbox delivery")
	}
}

func TestMailboxPauseUnaffectedByStalledTurnSettlement(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "paused"}[paused], func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.newTurn()
			stageCancelTestMailbox(a)
			a.mailboxDeliveryPaused.Store(paused)
			a.compactionContinuationStalled = true
			a.settleStalledTurn("test continuation")
			if a.turn != nil || a.mailboxDeliveryPaused.Load() != paused {
				t.Fatalf("stalled settlement changed mailbox pause: turn=%d, paused=%v",
					a.currentTurnID(), a.mailboxDeliveryPaused.Load())
			}
		})
	}
}

func TestMailboxResumesForInputAfterCoveredQueue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	stageCancelTestMailbox(a)
	a.activeSubAgentMailboxAck = false
	a.requeueActiveSubAgentMailbox()
	a.raiseCancelUpTo(1)
	a.pendingUserMessages = []pendingUserMessage{
		{AcceptedOrder: 1, Content: "First request", FromUser: true},
		{AcceptedOrder: 2, Content: "Follow-up request", FromUser: true},
	}
	a.drainPendingUserMessages()
	if a.turn == nil || a.mailboxDeliveryPaused.Load() || len(a.activeSubAgentMailboxes) != 1 {
		t.Fatalf("later input did not resume mailbox: turn=%d, paused=%v, active=%d",
			a.currentTurnID(), a.mailboxDeliveryPaused.Load(), len(a.activeSubAgentMailboxes))
	}
}
