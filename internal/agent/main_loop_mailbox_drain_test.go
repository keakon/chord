package agent

import "testing"

// TestDrainRunnableMailboxWorkDeliversProgressOnlyOwnedQueue pins that the
// idle owner-queue scan treats a progress-only queue like any other: routing
// decides deliverability, so once the parked owner settles the progress is
// forwarded to the main inbox instead of being stranded because the scan
// pre-filtered by message kind.
func TestDrainRunnableMailboxWorkDeliversProgressOnlyOwnedQueue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	const (
		ownerTaskID     = "adhoc-progress-owner"
		ownerInstanceID = "worker-progress-owner"
		childTaskID     = "adhoc-progress-child"
		messageID       = "owned-progress-1"
	)
	owner := &DurableTaskRecord{
		TaskID:           ownerTaskID,
		AgentDefName:     "worker",
		TaskDesc:         "owner parked while a progress update waits",
		State:            string(SubAgentStateWaitingMain),
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: ownerInstanceID,
		InstanceHistory:  []string{ownerInstanceID},
		RuntimeParked:    true,
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[ownerTaskID] = owner
	a.subs.mu.Unlock()

	msg := SubAgentMailboxMessage{
		MessageID:    messageID,
		AgentID:      "worker-unrelated-agent",
		TaskID:       childTaskID,
		OwnerAgentID: ownerInstanceID,
		OwnerTaskID:  ownerTaskID,
		Kind:         SubAgentMailboxKindProgress,
		Summary:      "still working",
	}
	a.enqueueOwnedSubAgentMailbox(msg)
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != messageID {
		t.Fatalf("owned queue = %#v, want the progress parked under the owner", queued)
	}

	// Invariant: while the owner stays parked its progress is unroutable, so a
	// drain must leave it queued and it must not suppress global idle.
	a.drainRunnableMailboxWork()
	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 1 || queued[0].MessageID != messageID {
		t.Fatalf("owned queue = %#v, want unroutable progress left in place", queued)
	}
	if a.hasRunnableMailboxWork() {
		t.Fatal("unroutable parked-owner progress must not count as runnable mailbox work")
	}

	// The owner settles: routing now forwards the progress to the main inbox, so
	// the next drain must actually deliver it rather than skip the queue.
	a.subs.mu.Lock()
	owner.State = string(SubAgentStateCompleted)
	a.subs.mu.Unlock()

	a.drainRunnableMailboxWork()

	if queued := a.ownedSubAgentMailboxes[ownerInstanceID]; len(queued) != 0 {
		t.Fatalf("owned queue = %#v, want the progress delivered on the next drain", queued)
	}
	if !a.hasQueuedMailboxMessage(messageID) {
		t.Fatal("settled owner's progress was not forwarded to the main inbox")
	}
}
