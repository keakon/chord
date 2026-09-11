package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func enqueueIdleWakeForTest(a *MainAgent, messageID string, kind SubAgentMailboxKind, priority SubAgentMailboxPriority) {
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   messageID,
		AgentID:     "worker-1",
		TaskID:      "task-1",
		Kind:        kind,
		Priority:    priority,
		MessageType: AgentMessageTypeNotice,
		Summary:     "background work update",
	})
}

// TestIdleMailboxWakeBudgetBlocksNormalWakeUntilUserInput pins the anti-self-
// incentive bound: once maxConsecutiveIdleWakes idle wakes have run with no user
// input, a further normal completion stays durable and queued instead of opening
// its own turn, and the next user message both resets the budget and delivers it.
func TestIdleMailboxWakeBudgetBlocksNormalWakeUntilUserInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.consecutiveIdleWakes.Store(maxConsecutiveIdleWakes)

	enqueueIdleWakeForTest(a, "wake-budget-1", SubAgentMailboxKindBackgroundResult, SubAgentMailboxPriorityNotify)
	a.drainSubAgentInbox()
	if a.turn != nil {
		t.Fatal("exhausted wake budget must not open a new turn")
	}
	if got := len(a.subAgentInbox.normal); got != 1 {
		t.Fatalf("normal queue depth = %d, want the wake held for the next user turn", got)
	}

	// Fresh user input resets the budget and releases the held result.
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "carry on"})
	if got := a.consecutiveIdleWakes.Load(); got != 0 {
		t.Fatalf("consecutiveIdleWakes = %d, want 0 after user input", got)
	}
	a.drainSubAgentInbox()
	if a.turn == nil {
		t.Fatal("user input must let the held background result open a turn")
	}
	if got := a.consecutiveIdleWakes.Load(); got != 1 {
		t.Fatalf("consecutiveIdleWakes = %d, want 1 after one idle wake", got)
	}
}

// TestIdleMailboxWakeBudgetLetsInterruptThrough pins the escape hatch: a parked
// worker's decision request must never be blocked by the wake budget, or the
// worker would deadlock waiting on an owner that will not wake.
func TestIdleMailboxWakeBudgetLetsInterruptThrough(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.consecutiveIdleWakes.Store(maxConsecutiveIdleWakes)

	enqueueIdleWakeForTest(a, "decision-1", SubAgentMailboxKindDecisionRequired, SubAgentMailboxPriorityInterrupt)
	a.drainSubAgentInbox()
	if a.turn == nil {
		t.Fatal("an interrupt must open a turn even with the wake budget exhausted")
	}
}

// TestIdleMailboxWakeBudgetStillReachesGlobalIdle pins the interaction with
// hasRunnableMailboxWork: a message the wake budget is holding is not runnable
// work — only a user turn clears the budget, and the periodic lifecycle sweep
// drains through the same gate — so it must not keep the main from reporting
// global idle. Without this, GlobalIdleEvent, the OnIdle hook, and
// parkQuiescentSubAgents would all wait on a turn only the user can start.
func TestIdleMailboxWakeBudgetStillReachesGlobalIdle(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.consecutiveIdleWakes.Store(maxConsecutiveIdleWakes)

	enqueueIdleWakeForTest(a, "wake-budget-idle", SubAgentMailboxKindBackgroundResult, SubAgentMailboxPriorityNotify)
	a.drainSubAgentInbox()
	if a.turn != nil {
		t.Fatal("exhausted wake budget must not open a new turn")
	}
	if got := len(a.subAgentInbox.normal); got != 1 {
		t.Fatalf("normal queue depth = %d, want the wake held for the next user turn", got)
	}
	// Drop the queue-announcement events the enqueue emitted so the next read
	// observes the idle event alone.
	for len(a.Events()) > 0 {
		<-a.Events()
	}

	if a.hasRunnableMailboxWork() {
		t.Fatal("a budget-held message must not count as runnable mailbox work")
	}
	if a.hasQueuedAutomaticWork() {
		t.Fatal("a budget-held message must not suppress global idle")
	}
	if !a.emitGlobalIdleIfReady() {
		t.Fatal("expected global idle while the only pending work is budget-held")
	}
	select {
	case evt := <-a.Events():
		if _, ok := evt.(GlobalIdleEvent); !ok {
			t.Fatalf("event = %T, want GlobalIdleEvent", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for GlobalIdleEvent")
	}

	// The held result is still durable and rides the next user turn.
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "carry on"})
	a.drainSubAgentInbox()
	if a.turn == nil {
		t.Fatal("user input must release the held message")
	}
}
