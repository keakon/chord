package agent

import (
	"fmt"
	"testing"
)

// TestIdleMailboxDrainWakesWithoutUserInput pins that every mailbox message
// reaching an idle main opens its own turn: a background job result just as
// much as a worker's completion notice, no matter how many idle wakes preceded
// it. A message that waits for the user to write again strands the work it
// reports, because nothing else in the process can deliver it.
func TestIdleMailboxDrainWakesWithoutUserInput(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	kinds := []SubAgentMailboxKind{SubAgentMailboxKindBackgroundResult, SubAgentMailboxKindCompleted}
	const wakes = 6
	for i := range wakes {
		messageID := fmt.Sprintf("idle-wake-%d", i)
		a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
			MessageID:   messageID,
			AgentID:     "worker-1",
			TaskID:      "task-1",
			Kind:        kinds[i%len(kinds)],
			Priority:    SubAgentMailboxPriorityNotify,
			MessageType: AgentMessageTypeNotice,
			Summary:     "background work update " + messageID,
		})

		a.drainSubAgentInbox()

		if a.currentTurn() == nil {
			t.Fatalf("idle drain left the main asleep on wake %d", i+1)
		}
		staged := a.pendingSubAgentMailboxes
		if len(staged) != i+1 {
			t.Fatalf("staged mailbox batch after wake %d has %d messages, want %d", i+1, len(staged), i+1)
		}
		if last := staged[len(staged)-1]; last.MessageID != messageID {
			t.Fatalf("last staged message after wake %d = %q, want %q", i+1, last.MessageID, messageID)
		}

		// End the turn without any user input, so the next arrival has to wake
		// the main on its own.
		a.turnMu.Lock()
		a.turn = nil
		a.turnMu.Unlock()
		drainAgentEvents(a.outputCh)
	}
}
