package agent

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/golog/log"
)

func (a *MainAgent) prepareSubAgentMailboxMessage(msg *SubAgentMailboxMessage) error {
	if msg == nil {
		return nil
	}
	a.normalizeSubAgentMailboxMessage(msg)
	if err := validateAgentMessageContract(msg); err != nil {
		return err
	}
	if err := a.persistSubAgentMailboxMessage(*msg); err != nil {
		return err
	}
	a.applyPersistedSubAgentMailboxMessage(msg)
	return nil
}

func (a *MainAgent) applyPersistedSubAgentMailboxMessage(msg *SubAgentMailboxMessage) {
	if msg == nil {
		return
	}
	a.orchestrationMetrics.recordMailboxCreated(msg.MessageID, msg.CreatedAt)
	if sub := a.subAgentByID(msg.AgentID); sub != nil {
		sub.setLastMailboxID(msg.MessageID)
		if msg.Completion != nil && len(msg.Completion.Artifacts) > 0 {
			sub.setLastArtifact(msg.Completion.Artifacts[0])
		}
		a.persistSubAgentMeta(sub)
	}
	a.syncTaskRecordFromMailbox(*msg)
	a.emitSubAgentMailboxUI(*msg)
	a.emitToTUI(MailboxQueuedEvent{Message: *msg})
}

func (a *MainAgent) markSubAgentMailboxSeen(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if a == nil || messageID == "" {
		return false
	}
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if a.subAgentMailboxIDs == nil {
		a.subAgentMailboxIDs = make(map[string]struct{})
	}
	if _, ok := a.subAgentMailboxIDs[messageID]; ok {
		return false
	}
	a.subAgentMailboxIDs[messageID] = struct{}{}
	return true
}

func (a *MainAgent) enqueueSubAgentMailbox(msg SubAgentMailboxMessage) {
	if err := a.prepareSubAgentMailboxMessage(&msg); err != nil {
		log.Warnf("failed to persist SubAgent mailbox message message_id=%v task_id=%v kind=%v error=%v", msg.MessageID, msg.TaskID, msg.Kind, err)
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("SubAgent mailbox durability degraded: %w", err)})
		msg.persistPending = true
		a.requeueSubAgentMailboxInMemory(msg)
		return
	}
	a.deliverSubAgentMailbox(msg)
}

// deliverSubAgentMailbox routes an already-persisted mailbox message to its
// destination (owner runtime reactivation, the main-agent inbox, or the
// per-owner queue). It never writes the message again — callers must have
// persisted it first, either through prepareSubAgentMailboxMessage or because
// the message was loaded from the durable mailbox log.
func (a *MainAgent) deliverSubAgentMailbox(msg SubAgentMailboxMessage) {
	if !a.mailboxDeliveryPaused.Load() && a.routeOwnedSubAgentMailbox(msg) {
		return
	}
	if strings.TrimSpace(msg.OwnerAgentID) != "" {
		a.enqueueOwnedSubAgentMailbox(msg)
		a.refreshSubAgentInboxSummary()
		return
	}
	if strings.TrimSpace(msg.MessageID) == "" {
		msg.MessageID = a.nextSubAgentMailboxMessageID(msg.AgentID)
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	switch msg.Kind {
	case SubAgentMailboxKindProgress:
		a.replaceProgressMailboxWithinBudget(msg)
	default:
		if !a.storeMailboxInMemory(msg, false) {
			a.subAgentMailboxIDsMu.Lock()
			a.spoolMailboxMessage(msg, false)
			a.subAgentMailboxIDsMu.Unlock()
			a.orchestrationMetrics.mailboxSpoolQueued.Add(1)
		}
	}
	a.refreshSubAgentInboxSummary()
}

// replaceProgressMailboxWithinBudget retains the historical name for the
// progress enqueue path. The map is only a latest-status view; every durable
// progress message enters the FIFO or its durable-log fallback.
func (a *MainAgent) replaceProgressMailboxWithinBudget(msg SubAgentMailboxMessage) {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	messageID := strings.TrimSpace(msg.MessageID)
	if messageID == "" {
		return
	}
	if a.subAgentInbox.progress == nil {
		a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
	}
	a.subAgentInbox.progress[msg.AgentID] = msg
	if a.subAgentInbox.progressPendingAgent == nil {
		a.subAgentInbox.progressPendingAgent = make(map[string]string)
	}
	if slices.Contains(a.subAgentInbox.progressPending, messageID) {
		return
	}
	for _, queued := range a.subAgentInbox.progressQueue {
		if queued.MessageID == messageID {
			return
		}
	}
	messageLimit, byteLimit := a.mailboxMemoryLimits()
	size := mailboxMessageBytes(msg)
	// Once an older progress row has spilled, later rows must follow it in the
	// durable fallback rather than jumping ahead through memory.
	if len(a.subAgentInbox.progressPending) > 0 ||
		a.mailboxMemoryCount() >= messageLimit ||
		a.subAgentInbox.memoryBytes+size > byteLimit {
		a.subAgentInbox.progressPending = append(a.subAgentInbox.progressPending, messageID)
		a.subAgentInbox.progressPendingAgent[messageID] = msg.AgentID
		a.subAgentInbox.progressPendingTask[messageID] = msg.TaskID
		return
	}
	a.subAgentInbox.progressQueue = append(a.subAgentInbox.progressQueue, msg)
	a.subAgentInbox.memoryBytes += size
}

func (a *MainAgent) requeueSubAgentMailboxInMemory(msg SubAgentMailboxMessage) {
	spooled := false
	a.subAgentMailboxIDsMu.Lock()
	spooled = a.requeueSubAgentMailboxInMemoryLocked(msg)
	a.subAgentMailboxIDsMu.Unlock()
	if spooled {
		a.orchestrationMetrics.mailboxSpoolQueued.Add(1)
	}
	a.refreshSubAgentInboxSummary()
}

// requeueSubAgentMailboxInMemoryLocked requeues one mailbox message into its
// in-memory queue (or the durable spool when the memory budget is exhausted)
// under subAgentMailboxIDsMu, which the caller must hold. It reports whether
// the message landed in the durable spool.
func (a *MainAgent) requeueSubAgentMailboxInMemoryLocked(msg SubAgentMailboxMessage) bool {
	if msg.Kind == SubAgentMailboxKindProgress {
		if msg.MessageID == "" {
			return false
		}
		remaining := a.subAgentInbox.progressPending[:0]
		for _, messageID := range a.subAgentInbox.progressPending {
			if messageID != msg.MessageID {
				remaining = append(remaining, messageID)
			}
		}
		a.subAgentInbox.progressPending = remaining
		if a.subAgentInbox.progressPendingAgent != nil {
			delete(a.subAgentInbox.progressPendingAgent, msg.MessageID)
		}
		delete(a.subAgentInbox.progressPendingTask, msg.MessageID)
		if a.subAgentInbox.progressPendingAttempts != nil {
			delete(a.subAgentInbox.progressPendingAttempts, msg.MessageID)
		}
		if a.subAgentInbox.progress == nil {
			a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
		}
		a.subAgentInbox.progress[msg.AgentID] = msg
		a.subAgentInbox.progressQueue = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.progressQueue...)
		a.subAgentInbox.memoryBytes += mailboxMessageBytes(msg)
		return false
	}
	if msg.persistPending {
		if msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent {
			a.subAgentInbox.urgent = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.urgent...)
		} else {
			a.subAgentInbox.normal = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.normal...)
		}
		a.subAgentInbox.memoryBytes += mailboxMessageBytes(msg)
		return false
	}
	if a.storeMailboxInMemoryLocked(msg, true) {
		return false
	}
	a.spoolMailboxMessage(msg, true)
	return true
}

func durableTaskRecordIncludesInstance(rec *DurableTaskRecord, instanceID string) bool {
	if rec == nil {
		return false
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return false
	}
	if strings.TrimSpace(rec.LatestInstanceID) == instanceID {
		return true
	}
	for _, seen := range rec.InstanceHistory {
		if strings.TrimSpace(seen) == instanceID {
			return true
		}
	}
	return false
}

func (a *MainAgent) shouldAcceptSubAgentMailbox(sourceID string, msg *SubAgentMailboxMessage) bool {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return true
	}
	if a.subAgentByID(sourceID) != nil {
		return true
	}
	if msg != nil {
		if rec := a.taskRecordByTaskID(msg.TaskID); rec != nil && rec.RuntimeParked && durableTaskRecordIncludesInstance(rec, sourceID) {
			if agentID := strings.TrimSpace(msg.AgentID); agentID != "" && agentID != sourceID {
				return false
			}
			return strings.TrimSpace(rec.OwnerAgentID) == strings.TrimSpace(msg.OwnerAgentID) &&
				strings.TrimSpace(rec.OwnerTaskID) == strings.TrimSpace(msg.OwnerTaskID)
		}
	}
	if msg == nil || msg.Kind != SubAgentMailboxKindCompleted {
		return false
	}
	if agentID := strings.TrimSpace(msg.AgentID); agentID != "" && agentID != sourceID {
		return false
	}
	rec := a.taskRecordByTaskID(msg.TaskID)
	if rec == nil || strings.TrimSpace(rec.State) != string(SubAgentStateCompleted) {
		return false
	}
	if strings.TrimSpace(rec.OwnerAgentID) != strings.TrimSpace(msg.OwnerAgentID) {
		return false
	}
	if strings.TrimSpace(rec.OwnerTaskID) != strings.TrimSpace(msg.OwnerTaskID) {
		return false
	}
	return durableTaskRecordIncludesInstance(rec, sourceID)
}

func (a *MainAgent) handleSubAgentMailboxEvent(evt Event) {
	msg, ok := evt.Payload.(*SubAgentMailboxMessage)
	if !ok || msg == nil {
		return
	}
	if !a.shouldAcceptSubAgentMailbox(evt.SourceID, msg) {
		return
	}
	messageID := strings.TrimSpace(msg.MessageID)
	if messageID != "" && a.isSubAgentMailboxConsumed(messageID) {
		return
	}
	// Drop events whose message is already queued for delivery in this
	// session. Restore re-delivers every unconsumed mailbox message from the
	// durable log into the delivery pipeline at startup, so a later event
	// carrying the same MessageID would otherwise queue a second copy for the
	// owner. The completion event below is the one legitimate in-process
	// arrival of an already-persisted message, and only because its message
	// has not been queued yet — the check here keeps replay of an
	// already-restored message (or a repeated dispatch of the same event)
	// idempotent.
	if messageID != "" && a.hasQueuedMailboxMessage(messageID) {
		return
	}
	// A message that is already durably recorded and applied in this process
	// (the completion path persists and applies before its terminal commit,
	// then queues this event for delivery only) must not be persisted or
	// applied again — writing it twice would duplicate the entry in the
	// mailbox log and applying it twice would double-count delivery
	// bookkeeping. The event is that message's delivery, so it is routed below
	// through the same path as a fresh message.
	alreadyApplied := messageID != "" && !a.markSubAgentMailboxSeen(messageID)
	if alreadyApplied {
		a.deliverSubAgentMailbox(*msg)
	} else {
		a.enqueueSubAgentMailbox(*msg)
	}
	if msg.Kind != SubAgentMailboxKindProgress && !a.mailboxDeliveryPaused.Load() {
		a.drainSubAgentInbox()
	}
}

// hasQueuedMailboxMessage reports whether a message with the given ID is
// already staged in this agent's mailbox delivery pipeline: queued for the
// main inbox (in memory or in the durable spool), waiting in the pending
// batch, deferred for a delayed retry, or parked in an owner's queue. A
// message found here has already been delivered to its destination in this
// session (restored from the durable log, or produced by an earlier dispatch
// of the same event), so handling its mailbox event again would double-queue
// the owner.
func (a *MainAgent) hasQueuedMailboxMessage(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if a == nil || messageID == "" {
		return false
	}
	// The owner-queue half of the pipeline is shared with the TUI-facing API
	// goroutines, so the whole staged-state scan runs under the same lock that
	// guards every mutation of those maps.
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	for _, msg := range a.subAgentInbox.urgent {
		if msg.MessageID == messageID {
			return true
		}
	}
	for _, msg := range a.subAgentInbox.normal {
		if msg.MessageID == messageID {
			return true
		}
	}
	for _, msg := range a.subAgentInbox.progress {
		if msg.MessageID == messageID {
			return true
		}
	}
	for _, msg := range a.subAgentInbox.progressQueue {
		if msg.MessageID == messageID {
			return true
		}
	}
	if slices.Contains(a.subAgentInbox.spoolUrgent, messageID) {
		return true
	}
	if slices.Contains(a.subAgentInbox.spoolNormal, messageID) {
		return true
	}
	if slices.Contains(a.subAgentInbox.progressPending, messageID) {
		return true
	}
	for _, deferred := range a.subAgentInbox.deferredProgress {
		if deferred.MessageID == messageID {
			return true
		}
	}
	for _, queued := range a.ownedSubAgentMailboxes {
		for _, msg := range queued {
			if msg.MessageID == messageID {
				return true
			}
		}
	}
	for _, spooled := range a.ownedMailboxSpool {
		if slices.Contains(spooled, messageID) {
			return true
		}
	}
	for _, msg := range a.pendingSubAgentMailboxes {
		if msg != nil && msg.MessageID == messageID {
			return true
		}
	}
	for _, msg := range a.activeSubAgentMailboxes {
		if msg != nil && msg.MessageID == messageID {
			return true
		}
	}
	return a.activeSubAgentMailbox != nil && a.activeSubAgentMailbox.MessageID == messageID
}

func (a *MainAgent) dequeueNextSubAgentMailbox() *SubAgentMailboxMessage {
	// The main-inbox queues are shared with the TUI-facing manual delivery
	// path, so in-memory claims happen under subAgentMailboxIDsMu and the
	// spooled message reloads (which read the mailbox log) run on the claimed
	// ids outside it.
	a.subAgentMailboxIDsMu.Lock()
	if len(a.subAgentInbox.urgent) > 0 {
		msg := a.subAgentInbox.urgent[0]
		a.subAgentInbox.urgent = a.subAgentInbox.urgent[1:]
		a.releaseMailboxMemory(msg)
		a.subAgentMailboxIDsMu.Unlock()
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	spoolUrgentPending := len(a.subAgentInbox.spoolUrgent) > 0
	a.subAgentMailboxIDsMu.Unlock()
	if spoolUrgentPending {
		if msg := a.dequeueSpooledMailboxQueue(&a.subAgentInbox.spoolUrgent); msg != nil {
			return msg
		}
	}
	a.subAgentMailboxIDsMu.Lock()
	if len(a.subAgentInbox.normal) > 0 {
		msg := a.subAgentInbox.normal[0]
		a.subAgentInbox.normal = a.subAgentInbox.normal[1:]
		a.releaseMailboxMemory(msg)
		a.subAgentMailboxIDsMu.Unlock()
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	a.subAgentMailboxIDsMu.Unlock()
	return a.dequeueSpooledSubAgentMailbox()
}

func (a *MainAgent) ensureSubAgentMailboxPersisted(msg *SubAgentMailboxMessage) bool {
	if msg == nil || !msg.persistPending {
		return true
	}
	if err := a.persistSubAgentMailboxMessage(*msg); err != nil {
		log.Warnf("retrying SubAgent mailbox persistence failed message_id=%v task_id=%v error=%v", msg.MessageID, msg.TaskID, err)
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("SubAgent mailbox durability still degraded: %w", err)})
		return false
	}
	msg.persistPending = false
	a.applyPersistedSubAgentMailboxMessage(msg)
	return true
}
