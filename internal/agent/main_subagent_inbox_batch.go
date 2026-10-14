package agent

import (
	"slices"
)

func (a *MainAgent) stageNextSubAgentMailboxBatch() bool {
	if a.mailboxDeliveryPaused.Load() || nativeMessagesTurnPending(a.ctxMgr) {
		return false
	}
	// One staging cycle drains the whole routable main-inbox backlog — every
	// urgent/normal queue message (FIFO within its priority class, urgent
	// first) plus every current progress snapshot — so the next request carries
	// all pending mailbox messages together instead of one message per request.
	// Progress/notice is claimed at every request boundary, mid-turn included:
	// like a queued user message it rides the next LLM request without
	// interrupting the one in flight or starting a request of its own.
	var progress []SubAgentMailboxMessage
	// A deferred delivery retry only runs between turns: each due entry
	// reloads the mailbox log and requeues, so re-running it on every mid-turn
	// continuation would churn the queues the active turn never drains. The
	// next between-turns drain keeps the retry alive.
	if a.turn == nil {
		a.retryDeferredMailboxDeliveries()
	}
	progress = a.takeMainInboxProgressSnapshots()
	var queued []*SubAgentMailboxMessage
	for {
		msg := a.dequeueNextSubAgentMailbox()
		if msg == nil {
			break
		}
		if msg.Kind == SubAgentMailboxKindProgress {
			// Progress does not ride the urgent/normal queues as an actionable
			// head; whether the main is busy or idle it folds into the snapshot
			// set staged below so it is delivered with the same request.
			progress = append(progress, *msg)
			continue
		}
		if !a.ensureSubAgentMailboxPersisted(msg) {
			// Only the message whose persist failed is unclaimed; the loop
			// stops here so the rest of the queue keeps its order behind it.
			a.requeueSubAgentMailboxInMemory(*msg)
			break
		}
		queued = append(queued, msg)
	}
	if len(queued) == 0 && len(progress) == 0 {
		return false
	}
	var pending []*SubAgentMailboxMessage
	pending = append(pending, queued...)
	// Progress snapshots join after the actionable queue heads so batch order
	// stays stable for the overlay and ack consumers.
	for i := range progress {
		if !a.ensureSubAgentMailboxPersisted(&progress[i]) {
			// requeueSubAgentMailboxInMemory prepends to its queue, so the
			// rollback walks the claimed batch in reverse: the last-claimed
			// message must go back first for the queue order to match this
			// batch's order, which is every queued head followed by the
			// progress snapshots.
			for j := len(progress) - 1; j >= i; j-- {
				a.requeueSubAgentMailboxInMemory(progress[j])
			}
			for _, p := range slices.Backward(queued) {
				a.requeueSubAgentMailboxInMemory(*p)
			}
			return false
		}
		pending = append(pending, &progress[i])
	}
	// The staged batch is shared with the TUI-facing manual delivery path
	// (takeOutstandingMailboxForSub claims it during a manual message), so it
	// is published under subAgentMailboxIDsMu. A turn can deliver several
	// batches — each carried by the next request — so the new messages append
	// after what is already staged (preserving time order) and the active set
	// accumulates every batch until the turn closeout acks or requeues it. The
	// active head pointer is only filled when it was empty: it marks the first
	// not-yet-acked message, not the newest arrival.
	var batchHead *SubAgentMailboxMessage
	if len(queued) > 0 {
		batchHead = queued[0]
	}
	a.subAgentMailboxIDsMu.Lock()
	a.pendingSubAgentMailboxes = append(a.pendingSubAgentMailboxes, pending...)
	a.activeSubAgentMailboxes = append(a.activeSubAgentMailboxes, pending...)
	if a.activeSubAgentMailbox == nil {
		a.activeSubAgentMailbox = batchHead
	}
	a.activeSubAgentMailboxAck = true
	a.subAgentMailboxIDsMu.Unlock()
	for _, delivered := range pending {
		if delivered != nil {
			a.orchestrationMetrics.recordMailboxDelivery(delivered.MessageID, delivered.CreatedAt)
		}
	}
	a.refreshSubAgentInboxSummary()
	return true
}

func (a *MainAgent) prepareSubAgentMailboxBatchForTurnContinuation() bool {
	if a.turn == nil {
		return false
	}
	// Only an unconsumed pending batch blocks a new stage: it is exactly what
	// the next request will take, and staging again on top of it would just
	// re-take the same queues. The active set is the already-delivered
	// bookkeeping that waits for the turn closeout to ack or requeue it, so it
	// must not stop later arrivals from being staged for a subsequent request.
	a.subAgentMailboxIDsMu.Lock()
	hasPending := len(a.pendingSubAgentMailboxes) > 0
	a.subAgentMailboxIDsMu.Unlock()
	if hasPending {
		return false
	}
	return a.stageNextSubAgentMailboxBatch()
}

func (a *MainAgent) drainSubAgentInbox() {
	if a.mailboxDeliveryPaused.Load() || a.sessionExecutionPaused() {
		return
	}
	if a.handoffDeliveryHeld() {
		// A handoff user wait owns the foreground: starting a mailbox turn here
		// would abandon the wait (newTurn -> abandonPendingHandoff) and settle
		// its deferred result as a user cancellation. Every queue stays intact;
		// the post-decision drain delivers the backlog.
		return
	}
	if a.turn != nil {
		return
	}
	if !a.stageNextSubAgentMailboxBatch() {
		return
	}
	a.newTurn("")
	// A dispatching request: any parked user queue rides it too, in FIFO order.
	a.flushParkedPendingUserMessages()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "main")
}

func (a *MainAgent) markActiveSubAgentMailboxAck(ack bool) {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if len(a.activeSubAgentMailboxes) == 0 && a.activeSubAgentMailbox == nil {
		return
	}
	a.activeSubAgentMailboxAck = ack
}

// stagedActiveMailbox reports whether msg is still part of the staged active
// batch, by pointer identity with the live fields that the manual-delivery
// path (takeOutstandingMailboxForSub) and the lifecycle close path
// (removeSubAgentMailboxState) mutate. Callers that snapshotted the batch re-
// check each message before acting on it so a message already claimed or
// removed is not acked or requeued by its former owner.
func (a *MainAgent) stagedActiveMailbox(msg *SubAgentMailboxMessage) bool {
	if msg == nil {
		return false
	}
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if a.activeSubAgentMailbox == msg {
		return true
	}
	return slices.Contains(a.activeSubAgentMailboxes, msg)
}

func (a *MainAgent) requeueActiveSubAgentMailbox() {
	// The active batch is shared with the TUI-facing manual-delivery path
	// (takeOutstandingMailboxForSub claims messages out of it) and the
	// lifecycle close path (removeSubAgentMailboxState drops messages of a
	// closing agent), so the whole requeue runs in one subAgentMailboxIDsMu
	// critical section over the live batch. Requeueing from a snapshot taken
	// earlier would reinsert a message another path already claimed or
	// removed, delivering it twice or resurrecting it after its agent closed.
	// The batch is claimed (cleared) in the same section, so nothing staged
	// can be claimed again after its message was requeued.
	a.subAgentMailboxIDsMu.Lock()
	if (len(a.activeSubAgentMailboxes) == 0 && a.activeSubAgentMailbox == nil) || a.activeSubAgentMailboxAck {
		a.subAgentMailboxIDsMu.Unlock()
		return
	}
	spooled := false
	// Backwards iteration keeps the original batch order when requeueing
	// pushes messages to the front of the in-memory queues.
	for _, msg := range slices.Backward(a.activeSubAgentMailboxes) {
		if msg != nil && a.requeueSubAgentMailboxInMemoryLocked(*msg) {
			spooled = true
		}
	}
	if len(a.activeSubAgentMailboxes) == 0 && a.activeSubAgentMailbox != nil && a.requeueSubAgentMailboxInMemoryLocked(*a.activeSubAgentMailbox) {
		spooled = true
	}
	a.activeSubAgentMailboxes = nil
	a.activeSubAgentMailbox = nil
	a.activeSubAgentMailboxAck = false
	a.pendingSubAgentMailboxes = nil
	if spooled {
		a.orchestrationMetrics.mailboxSpoolQueued.Add(1)
	}
	a.subAgentMailboxIDsMu.Unlock()
	a.refreshSubAgentInboxSummary()
}

func (a *MainAgent) takePendingSubAgentMailboxes() []*SubAgentMailboxMessage {
	if nativeMessagesTurnPending(a.ctxMgr) {
		return nil
	}
	a.subAgentMailboxIDsMu.Lock()
	msgs := a.pendingSubAgentMailboxes
	a.pendingSubAgentMailboxes = nil
	a.subAgentMailboxIDsMu.Unlock()
	return msgs
}
