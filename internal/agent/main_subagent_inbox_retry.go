package agent

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/golog/log"
)

// takeMainInboxProgressSnapshots claims the main inbox's progress FIFO for
// delivery. Complete in-memory messages are claimed without disk access;
// only records that spilled to the durable fallback need to be reloaded.
//
// The per-agent progress map is only a latest-status view, never an owner of an
// undelivered update: replaceProgressMailboxWithinBudget and the requeue path
// publish the map entry in the same critical section that files the row in
// progressQueue or progressPending, so every map entry is also resident in one
// of those two. Claiming them here is what drains the FIFO, and the map is only
// cleaned up alongside. Reading it as a second source would re-emit an update
// that the pending reload below already delivers.
func (a *MainAgent) takeMainInboxProgressSnapshots() []SubAgentMailboxMessage {
	a.subAgentMailboxIDsMu.Lock()
	out := append([]SubAgentMailboxMessage(nil), a.subAgentInbox.progressQueue...)
	a.subAgentInbox.progressQueue = nil
	for _, msg := range out {
		if current, ok := a.subAgentInbox.progress[msg.AgentID]; ok &&
			current.MessageID == msg.MessageID {
			delete(a.subAgentInbox.progress, msg.AgentID)
		}
		a.releaseMailboxMemory(msg)
	}
	ids := append([]string(nil), a.subAgentInbox.progressPending...)
	a.subAgentInbox.progressPending = nil
	pendingAgents := make(map[string]string, len(ids))
	pendingTasks := make(map[string]string, len(ids))
	pendingAttempts := make(map[string]int, len(ids))
	for _, messageID := range ids {
		pendingAgents[messageID] = a.subAgentInbox.progressPendingAgent[messageID]
		delete(a.subAgentInbox.progressPendingAgent, messageID)
		pendingTasks[messageID] = a.subAgentInbox.progressPendingTask[messageID]
		delete(a.subAgentInbox.progressPendingTask, messageID)
		pendingAttempts[messageID] = a.subAgentInbox.progressPendingAttempts[messageID]
		delete(a.subAgentInbox.progressPendingAttempts, messageID)
		for agentID, msg := range a.subAgentInbox.progress {
			if msg.MessageID == messageID {
				delete(a.subAgentInbox.progress, agentID)
			}
		}
	}
	a.subAgentMailboxIDsMu.Unlock()
	if len(ids) == 0 {
		return out
	}
	if out == nil {
		out = make([]SubAgentMailboxMessage, 0, len(ids))
	}
	for i, messageID := range ids {
		msg, found, err := a.loadDurableMailboxMessage(messageID)
		if err != nil {
			// The failure stopped the loop at ids[i]: only that id was actually
			// attempted, so only it may consume a retry attempt. The untouched
			// remainder is requeued with its previous count.
			a.retryProgressPending(ids[i:], 1, pendingAgents, pendingTasks, pendingAttempts)
			break
		}
		if found {
			out = append(out, *msg)
			continue
		}
		if a.isSubAgentMailboxConsumed(messageID) {
			continue
		}
		// A row that is not yet visible in the durable log is still treated as
		// a persistence race during the immediate retry stage: keep its id
		// rather than dropping the progress update. The delayed retry stage is
		// what decides that the row is permanently missing (see
		// retryDeferredMailboxDeliveries).
		a.retryProgressPending([]string{messageID}, 1, pendingAgents, pendingTasks, pendingAttempts)
	}
	return out
}

// mainInboxProgressReloadMaxAttempts bounds how many immediate reload attempts
// a progress snapshot id is kept for. A durable row that is not visible yet is
// normally a short-lived persistence race the next dispatch resolves, so a
// couple of retries preserve that delivery guarantee; once they are exhausted
// the id moves into the deferred retry set, which is cooldown-gated so
// hasRunnableMailboxWork does not keep reporting pending progress and the main
// can reach global idle without rescanning the mailbox log on every dispatch.
const mainInboxProgressReloadMaxAttempts = 3

// mainInboxDeferredRetryCooldown spaces out the deferred reload attempts for a
// mailbox message whose immediate retries were exhausted, so a burst of idle
// dispatches cannot rescan the mailbox log over and over.
const mainInboxDeferredRetryCooldown = 10 * time.Second

// mainInboxDeferredRetryWindow bounds how long one deferred message is retried
// from its first failed reload before the delivery is abandoned and reported.
// It is long enough to cover a slow persistence flush or a transient disk
// stall, and short enough that a permanently missing row stops absorbing the
// retry cadence.
const mainInboxDeferredRetryWindow = 5 * time.Minute

// retryProgressPending re-appends progress ids whose durable row could not be
// reloaded this dispatch, keeping FIFO order behind anything already re-queued.
// Only the first `attempted` ids were actually tried before the failure, so
// only they consume a retry attempt; the untouched remainder is requeued with
// its previous count instead of being advanced toward the deferred set it was
// never given a chance to earn. An id that reaches
// mainInboxProgressReloadMaxAttempts failed attempts moves into the deferred
// retry set instead of being dropped silently: the delivery is retried on a
// cooldown and reported dropped only once it is confirmed permanently missing
// or the retry window expires (see retryDeferredMailboxDeliveries). Called
// without subAgentMailboxIDsMu held.
func (a *MainAgent) retryProgressPending(ids []string, attempted int, agents, tasks map[string]string, attempts map[string]int) {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	for i, messageID := range ids {
		if i >= attempted {
			a.subAgentInbox.progressPending = append(a.subAgentInbox.progressPending, messageID)
			a.subAgentInbox.progressPendingAgent[messageID] = agents[messageID]
			a.subAgentInbox.progressPendingTask[messageID] = tasks[messageID]
			a.subAgentInbox.progressPendingAttempts[messageID] = attempts[messageID]
			continue
		}
		tries := attempts[messageID] + 1
		if tries >= mainInboxProgressReloadMaxAttempts {
			a.deferProgressRetryLocked(messageID, agents[messageID], tasks[messageID])
			continue
		}
		a.subAgentInbox.progressPending = append(a.subAgentInbox.progressPending, messageID)
		a.subAgentInbox.progressPendingAgent[messageID] = agents[messageID]
		a.subAgentInbox.progressPendingTask[messageID] = tasks[messageID]
		a.subAgentInbox.progressPendingAttempts[messageID] = tries
	}
}

// deferProgressRetryLocked moves one progress snapshot id into the deferred
// retry set under subAgentMailboxIDsMu, extending an existing entry's cooldown
// instead of adding a duplicate id. Called with the lock held.
func (a *MainAgent) deferProgressRetryLocked(messageID, agentID, taskID string) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	now := time.Now()
	for i := range a.subAgentInbox.deferredProgress {
		if a.subAgentInbox.deferredProgress[i].MessageID != messageID {
			continue
		}
		a.subAgentInbox.deferredProgress[i].Attempts++
		a.subAgentInbox.deferredProgress[i].NextAttemptAt = now.Add(mainInboxDeferredRetryCooldown)
		return
	}
	a.subAgentInbox.deferredProgress = append(a.subAgentInbox.deferredProgress, deferredMailboxRetry{
		MessageID:     messageID,
		AgentID:       strings.TrimSpace(agentID),
		TaskID:        strings.TrimSpace(taskID),
		FirstFailedAt: now,
		NextAttemptAt: now.Add(mainInboxDeferredRetryCooldown),
	})
}

// retryDeferredMailboxDeliveries runs the delayed second-stage retries for
// mailbox messages whose immediate reload attempts were exhausted. It only
// runs between turns and while delivery is not paused: a mid-turn request must
// never pick up a progress snapshot, and a queue-resident progress message must
// not ping-pong in and out of a turn. Each message is retried at most once per
// cooldown, so a burst of dispatches does not rescan the mailbox log, and the
// retry window bounds how long a row that never becomes deliverable keeps the
// set alive. A message that becomes visible returns to the normal delivery
// pipeline; one that is confirmed permanently missing, or whose reload keeps
// failing past the window, is reported dropped (see reportSubAgentMailboxDropped).
func (a *MainAgent) retryDeferredMailboxDeliveries() {
	if a == nil || a.mailboxDeliveryPaused.Load() || a.turn != nil {
		return
	}
	a.subAgentMailboxIDsMu.Lock()
	if len(a.subAgentInbox.deferredProgress) == 0 {
		a.subAgentMailboxIDsMu.Unlock()
		return
	}
	now := time.Now()
	due := make([]deferredMailboxRetry, 0, len(a.subAgentInbox.deferredProgress))
	remaining := a.subAgentInbox.deferredProgress[:0]
	for _, entry := range a.subAgentInbox.deferredProgress {
		if now.Before(entry.NextAttemptAt) {
			remaining = append(remaining, entry)
			continue
		}
		due = append(due, entry)
	}
	a.subAgentInbox.deferredProgress = remaining
	a.subAgentMailboxIDsMu.Unlock()
	// Requeueing prepends to the progress FIFO, so the due entries are consumed
	// from the newest to the oldest: the oldest deferred message must end up in
	// front of the newer ones it was deferred behind.
	for _, entry := range slices.Backward(due) {
		msg, found, err := a.loadDurableMailboxMessage(entry.MessageID)
		switch {
		case err != nil:
			if now.Sub(entry.FirstFailedAt) >= mainInboxDeferredRetryWindow {
				a.reportSubAgentMailboxDropped(entry.MessageID, entry.AgentID, entry.TaskID, fmt.Sprintf("durable row could not be reloaded within %s: %v", mainInboxDeferredRetryWindow, err))
				continue
			}
			a.redeferMailboxDelivery(entry, err)
		case found:
			a.requeueSubAgentMailboxInMemory(*msg)
		case a.isSubAgentMailboxConsumed(entry.MessageID):
			// Benign: the delivery path already removed the waiting row.
			log.Debugf("deferred mailbox message already consumed message_id=%v", entry.MessageID)
		default:
			a.reportSubAgentMailboxDropped(entry.MessageID, entry.AgentID, entry.TaskID, "durable row is permanently missing after the immediate reload attempts")
		}
	}
}

// redeferMailboxDelivery puts one deferred entry back into the set after a
// failed reload, bumping its attempt count and cooling it down. Called without
// subAgentMailboxIDsMu held; the retry runs on the event-loop goroutine, which
// is the only writer of the deferred set.
func (a *MainAgent) redeferMailboxDelivery(entry deferredMailboxRetry, loadErr error) {
	entry.Attempts++
	entry.NextAttemptAt = time.Now().Add(mainInboxDeferredRetryCooldown)
	a.subAgentMailboxIDsMu.Lock()
	a.subAgentInbox.deferredProgress = append(a.subAgentInbox.deferredProgress, entry)
	a.subAgentMailboxIDsMu.Unlock()
	log.Warnf("mailbox message still not reloadable message_id=%v agent_id=%v attempts=%v error=%v", entry.MessageID, entry.AgentID, entry.Attempts, loadErr)
}

// mailboxDeliveryDroppedToastCategory groups the drop toasts so a burst of
// abandoned deliveries collapses into one queued toast.
const mailboxDeliveryDroppedToastCategory = "mailbox_delivery_dropped"

// mailboxDeliveryDroppedToastSuffix is the honest user-facing explanation that
// closes every delivery drop. It never promises a replay: when the durable row
// is confirmed missing, this session has no copy left, and only a later
// restore or a fresh delegation can make the work happen again.
const mailboxDeliveryDroppedToastSuffix = "could not be delivered in this session; delegate the work again if it still matters"

// reportSubAgentMailboxDropped is the single exit for a mailbox message this
// process can no longer deliver: the in-memory reference was abandoned, so the
// UI waiting row must be removed and the operator told. It never deletes or
// acks the durable row — an unconsumed message is still replayed by a later
// restore, and only a missing or expired row is reported. The task id appears
// in the toast only when it is known.
func (a *MainAgent) reportSubAgentMailboxDropped(messageID, agentID, taskID, reason string) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	agentID = strings.TrimSpace(agentID)
	taskID = strings.TrimSpace(taskID)
	a.emitToTUI(MailboxDeliveryDroppedEvent{MessageID: messageID})
	text := "An agent message " + mailboxDeliveryDroppedToastSuffix
	if taskID != "" {
		text = fmt.Sprintf("Message for task %s %s", taskID, mailboxDeliveryDroppedToastSuffix)
	}
	a.emitToTUI(ToastEvent{Message: text, Level: "warn", Category: mailboxDeliveryDroppedToastCategory, AgentID: agentID})
	log.Warnf("dropping mailbox message delivery message_id=%v agent_id=%v task_id=%v reason=%v", messageID, agentID, taskID, reason)
}
