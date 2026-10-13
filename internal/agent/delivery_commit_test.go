package agent

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestDeliveryCommitRequiresAckAndOwnsMessage(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			pump := newPersistencePump(2)
			a := &MainAgent{persist: pump}
			manager := recovery.NewRecoveryManager(t.TempDir())
			defer manager.Close()
			received := make(chan persistEntry, 1)
			release := make(chan struct{})
			acks := make(chan *deliveryCommitAck, 1)
			pump.start(func(entry persistEntry) {
				received <- entry
				<-release
				var err error
				if failure {
					err = errors.New("disk sync failed")
				}
				entry.after(err)
			})
			defer func() { pump.closeUntil(nil); <-pump.done }()
			msg := message.Message{Role: message.RoleTool, DeliveredFiles: []message.DeliveredFile{{ID: "saved", Locator: message.FileLocator{Kind: message.FileLocatorWorkspace, Workspace: &message.WorkspaceFileLocator{Path: "/original", Root: "/"}}}}}
			if err := a.enqueueDeliveryCommit(manager, "owner", msg, func(ack *deliveryCommitAck) { acks <- ack }); err != nil {
				t.Fatal(err)
			}
			entry := <-received
			if !entry.durable || entry.agentID != "owner" {
				t.Fatal("missing durable owner binding")
			}
			msg.DeliveredFiles[0].Locator.Workspace.Path = "/mutated"
			select {
			case <-acks:
				t.Fatal("published before durable ack")
			default:
			}
			close(release)
			ack := <-acks
			if (ack.Err != nil) != failure {
				t.Fatalf("wrong ack: %v", ack.Err)
			}
			if ack.Message.DeliveredFiles[0].Locator.Workspace.Path != "/original" {
				t.Fatal("queued canonical message aliased caller")
			}
		})
	}
}
func TestDeliveryCommitRejectsNonpersistentSession(t *testing.T) {
	a := &MainAgent{}
	called := false
	if err := a.enqueueDeliveryCommit(nil, "owner", message.Message{}, func(*deliveryCommitAck) { called = true }); err == nil {
		t.Fatal("nonpersistent delivery accepted")
	}
	if called {
		t.Fatal("synchronous rejection invoked loop-blocking callback")
	}
}

func TestDeliveryCancellationWaitsForQueuedAck(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.persist.closeUntil(nil)
	<-a.persist.done
	a.persist = newPersistencePump(2)
	received := make(chan persistEntry, 1)
	release := make(chan struct{})
	a.persist.start(func(entry persistEntry) { received <- entry; <-release; entry.after(nil) })
	a.installRecoveryManager(recovery.NewRecoveryManager(t.TempDir()))
	a.newTurn("")
	payload := &ToolResultPayload{CallID: "delivery-call", Name: "present", TurnID: a.turn.ID, Result: "declared", DeliveredFiles: []message.DeliveredFile{{ID: "delivery"}}, composedTexts: &composedToolResultTexts{Display: "hook rewritten text", Context: "hook rewritten text"}}
	if !a.beginDeliveryCommit(payload) {
		t.Fatal("delivery did not wait for ack")
	}
	entry := <-received
	if len(entry.msg.DeliveredFiles) != 1 || entry.msg.Content != "hook rewritten text" {
		t.Fatal("canonical message did not preserve structured files through hook text")
	}
	if len(message.CollectDeliveredFiles(a.ctxMgr.Snapshot())) != 0 {
		t.Fatal("unacknowledged delivery entered context")
	}
	turn := a.turn
	a.handleTurnCancelled(Event{Type: EventTurnCancelled, TurnID: turn.ID, Payload: &TurnCancelledPayload{}})
	if a.turn != turn || turn.deliveryCancel == nil {
		t.Fatal("queued delivery cancelled before ack")
	}
	close(release)
	evt := <-a.eventCh
	for evt.Type != EventToolResult {
		evt = <-a.eventCh
	}
	ackPayload := evt.Payload.(*ToolResultPayload)
	if ackPayload.deliveryCommit == nil || ackPayload.deliveryCommit.Err != nil {
		t.Fatal("durable ack missing")
	}
	if a.beginDeliveryCommit(ackPayload) {
		t.Fatal("ack was enqueued a second time")
	}
}

func TestDeliveryPendingDefersSubAgentRestart(t *testing.T) {
	sub := &SubAgent{turn: &Turn{ID: 7}}
	sub.turn.deliveryPending.Store(true)
	sub.handleContinueSignal(continueMsg{restartStoppedTurn: true, drainContextAppends: true})
	if sub.pendingContinue == nil || !sub.pendingContinue.restartStoppedTurn {
		t.Fatal("restart was not preserved while delivery awaited acknowledgment")
	}
	if sub.tryHandlePendingContinue() || sub.turn.ID != 7 {
		t.Fatal("pending delivery allowed a new turn")
	}
}

func TestDeliveryQueuedCommitOwnsShutdownSettlement(t *testing.T) {
	turn := &Turn{deliveryCommitCallID: "queued"}
	calls := turn.filterCompletedToolCalls([]PendingToolCall{{CallID: "queued"}, {CallID: "other"}})
	if len(calls) != 1 || calls[0].CallID != "other" {
		t.Fatal("shutdown would overwrite the queue-owned canonical delivery")
	}
}

func TestDeliveryAckRejectsReplacedTurnWithSameID(t *testing.T) {
	old := &Turn{ID: 7}
	current := &Turn{ID: 7}
	payload := &ToolResultPayload{TurnID: 7, deliveryCommit: &deliveryCommitAck{turn: old}}
	a := &MainAgent{turn: current}
	a.handleToolResult(Event{TurnID: 7, Payload: payload})
	sub := &SubAgent{turn: current}
	sub.handleToolResult(&toolResult{TurnID: 7, deliveryCommit: &deliveryCommitAck{turn: old}})
	if current.PendingToolCalls.Load() != 0 {
		t.Fatal("stale acknowledgment changed the replacement turn")
	}
}
