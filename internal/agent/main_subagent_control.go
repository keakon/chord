package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) subAgentByTaskID(taskID string) *SubAgent {
	return a.subs.subAgentByTaskID(taskID)
}

func (a *MainAgent) acquireSubAgentSlot(sub *SubAgent) error {
	return a.acquireSubAgentSlotWithBypass(sub, false)
}

func (a *MainAgent) acquireWakeReactivationSlot(sub *SubAgent) error {
	return a.acquireSubAgentSlotWithBypass(sub, true)
}

func (a *MainAgent) acquireSubAgentSlotWithBypass(sub *SubAgent, allowBypass bool) error {
	if sub == nil {
		return nil
	}
	sub.semMu.Lock()
	defer sub.semMu.Unlock()
	if sub.semHeld {
		return nil
	}
	governor := a.governor
	if governor == nil {
		governor = newResourceGovernor(config.OrchestrationConfig{})
		a.governor = governor
		a.sem = governor.runtimeSlots
	}
	if governor.tryAcquireRuntime() {
		sub.semHeld, sub.semBorrowed, sub.semBypassed = true, false, false
		return nil
	}
	if !allowBypass {
		return fmt.Errorf("max concurrent agents reached (cap=%d), wait for a running agent to complete", cap(a.sem))
	}
	if governor.tryBorrowRuntime() {
		sub.semHeld, sub.semBorrowed, sub.semBypassed = true, true, false
		return nil
	}
	// Both pools are exhausted. Wake reactivations run on the main event loop,
	// and the releases that would free capacity are processed by that same
	// loop, so blocking here can deadlock the whole agent. Fall back to a
	// bounded pool of uncounted bypass grants and surface the overflow through
	// metrics.
	if governor.tryBypassRuntime() {
		sub.semHeld, sub.semBorrowed, sub.semBypassed = true, false, true
		a.orchestrationMetrics.acquireRuntimeBypass()
		log.Warnf("SubAgent wake reactivation exceeded runtime and borrow capacity; granting bounded bypass agent_id=%v task_id=%v bypass_limit=%v", sub.instanceID, sub.taskID, governor.maxBypassed)
		return nil
	}
	// Every pool is exhausted, including the safety valve. Refuse instead of
	// exceeding the configured ceiling: the caller leaves the durable message
	// queued (owned mailbox / spool keeps FIFO order) and the next release
	// drains it.
	a.orchestrationMetrics.rejectRuntimeBypass()
	log.Warnf("SubAgent wake reactivation refused; runtime, borrow, and bypass pools exhausted agent_id=%v task_id=%v", sub.instanceID, sub.taskID)
	return fmt.Errorf("max concurrent agents reached (cap=%d) and the wake bypass pool is exhausted (cap=%d), wait for a running agent to complete", cap(a.sem), governor.maxBypassed)
}

func (a *MainAgent) releaseSubAgentSlot(sub *SubAgent) {
	if sub == nil {
		return
	}
	sub.semMu.Lock()
	if !sub.semHeld {
		sub.semMu.Unlock()
		return
	}
	borrowed := sub.semBorrowed
	bypassed := sub.semBypassed
	sub.semHeld = false
	sub.semBorrowed = false
	sub.semBypassed = false
	sub.semMu.Unlock()
	if bypassed {
		a.orchestrationMetrics.releaseRuntimeBypass()
		a.governor.releaseBypassRuntime()
		return
	}
	if a.governor == nil {
		return
	}
	a.governor.releaseRuntime(borrowed)
}

func (a *MainAgent) markSubAgentReactivated(sub *SubAgent, summary string) {
	if !sub.setState(SubAgentStateRunning, summary) {
		return
	}
	a.noteSubAgentStateTransition(sub, SubAgentStateRunning)
	a.emitActivity(sub.instanceID, ActivityExecuting, "resumed")
	a.emitToTUI(AgentStatusEvent{
		AgentID: sub.instanceID,
		Status:  "running",
		Message: summary,
	})
}

func (a *MainAgent) transferSubAgentSlot(from, to *SubAgent) bool {
	if from == nil || to == nil || from == to {
		return false
	}
	first, second := from, to
	if first.instanceID > second.instanceID {
		first, second = second, first
	}
	first.semMu.Lock()
	second.semMu.Lock()
	defer second.semMu.Unlock()
	defer first.semMu.Unlock()
	if !from.semHeld || to.semHeld {
		return false
	}
	to.semHeld = true
	to.semBorrowed = from.semBorrowed
	to.semBypassed = from.semBypassed
	from.semHeld = false
	from.semBorrowed = false
	from.semBypassed = false
	return true
}

func normalizeSubAgentMessage(kind, message string) string {
	message = strings.TrimSpace(message)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return message
	}
	return fmt.Sprintf("[%s] %s", kind, message)
}

func respondSubAgentControl(reply chan subAgentControlResult, handle tools.TaskHandle, err error) {
	if reply == nil {
		return
	}
	reply <- subAgentControlResult{Handle: handle, Err: err}
}

func (a *MainAgent) takeOutstandingMailboxForSub(sub *SubAgent) *SubAgentMailboxMessage {
	if a == nil || sub == nil {
		return nil
	}
	match := func(msg *SubAgentMailboxMessage) bool {
		if msg == nil {
			return false
		}
		return msg.AgentID == sub.instanceID || (strings.TrimSpace(msg.TaskID) != "" && msg.TaskID == sub.taskID)
	}
	removeFirstMatch := func(batch []*SubAgentMailboxMessage) ([]*SubAgentMailboxMessage, *SubAgentMailboxMessage) {
		for i, msg := range batch {
			if !match(msg) {
				continue
			}
			found := msg
			batch = append(batch[:i], batch[i+1:]...)
			return batch, found
		}
		return batch, nil
	}
	// The staged batch and the main-inbox queues are shared with the mailbox
	// delivery paths running on the event loop, so the whole claim runs under
	// subAgentMailboxIDsMu and the summary refresh happens after it.
	var msg *SubAgentMailboxMessage
	a.subAgentMailboxIDsMu.Lock()
	if len(a.activeSubAgentMailboxes) > 0 || match(a.activeSubAgentMailbox) {
		var taken *SubAgentMailboxMessage
		a.activeSubAgentMailboxes, taken = removeFirstMatch(a.activeSubAgentMailboxes)
		if taken == nil && match(a.activeSubAgentMailbox) {
			taken = a.activeSubAgentMailbox
		}
		if taken != nil {
			a.pendingSubAgentMailboxes, _ = removeFirstMatch(a.pendingSubAgentMailboxes)
			if len(a.activeSubAgentMailboxes) > 0 {
				a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
			} else {
				a.activeSubAgentMailbox = nil
				a.activeSubAgentMailboxAck = false
			}
			msg = taken
		}
	}
	if msg == nil && len(a.pendingSubAgentMailboxes) > 0 {
		a.pendingSubAgentMailboxes, msg = removeFirstMatch(a.pendingSubAgentMailboxes)
	}
	if msg == nil {
		for i := len(a.subAgentInbox.urgent) - 1; i >= 0; i-- {
			if !match(&a.subAgentInbox.urgent[i]) {
				continue
			}
			taken := a.subAgentInbox.urgent[i]
			a.subAgentInbox.urgent = append(a.subAgentInbox.urgent[:i], a.subAgentInbox.urgent[i+1:]...)
			a.releaseMailboxMemory(taken)
			msg = &taken
			break
		}
	}
	if msg == nil {
		for i := len(a.subAgentInbox.normal) - 1; i >= 0; i-- {
			if !match(&a.subAgentInbox.normal[i]) {
				continue
			}
			taken := a.subAgentInbox.normal[i]
			a.subAgentInbox.normal = append(a.subAgentInbox.normal[:i], a.subAgentInbox.normal[i+1:]...)
			a.releaseMailboxMemory(taken)
			msg = &taken
			break
		}
	}
	a.subAgentMailboxIDsMu.Unlock()
	if msg != nil {
		a.refreshSubAgentInboxSummary()
	}
	return msg
}

func (a *MainAgent) canCallerControlTask(callerAgentID, callerTaskID, taskID string) (*DurableTaskRecord, error) {
	taskID = strings.TrimSpace(taskID)
	callerAgentID = strings.TrimSpace(callerAgentID)
	callerTaskID = strings.TrimSpace(callerTaskID)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	rec := a.taskRecordByTaskID(taskID)
	if rec == nil {
		return nil, fmt.Errorf("unknown task_id %q", taskID)
	}
	ownerAgentID := strings.TrimSpace(rec.OwnerAgentID)
	ownerTaskID := strings.TrimSpace(rec.OwnerTaskID)
	if ownerTaskID != "" {
		if ownerTaskID != callerTaskID {
			return nil, fmt.Errorf("task %s is owned by task %s; caller task %s is not allowed", taskID, ownerTaskID, blankToDefault(callerTaskID, "main"))
		}
		return rec, nil
	}
	if callerAgentID == "" {
		if ownerAgentID != "" {
			return nil, fmt.Errorf("task %s is owned by %s; only the direct owner may control it", taskID, ownerAgentID)
		}
		return rec, nil
	}
	if ownerAgentID != callerAgentID {
		return nil, fmt.Errorf("task %s is not owned by caller %s", taskID, callerAgentID)
	}
	return rec, nil
}

func (a *MainAgent) sendMessageToSubAgentNow(callerAgentID, callerTaskID, taskID, message, kind string) (tools.TaskHandle, error) {
	return a.sendMessageToSubAgentWithTrigger(callerAgentID, callerTaskID, taskID, message, kind, taskResumeByTargetedNotify)
}

func (a *MainAgent) sendMessageToSubAgentWithTrigger(callerAgentID, callerTaskID, taskID, messageText, kind string, trigger taskResumeTrigger) (tools.TaskHandle, error) {
	taskID = strings.TrimSpace(taskID)
	messageText = strings.TrimSpace(messageText)
	kind = strings.TrimSpace(kind)
	if taskID == "" {
		return tools.TaskHandle{}, fmt.Errorf("task_id is required")
	}
	if messageText == "" {
		return tools.TaskHandle{}, fmt.Errorf("message is required")
	}
	record, err := a.canCallerControlTask(callerAgentID, callerTaskID, taskID)
	if err != nil {
		return tools.TaskHandle{}, err
	}
	if isTerminalSubAgentState(SubAgentState(strings.TrimSpace(record.State))) && !record.SettlementDurable {
		record, err = a.retryTaskSettlementDurability(taskID)
		if err != nil || !record.SettlementDurable {
			return tools.TaskHandle{}, fmt.Errorf("task %s cannot start a new attempt until its terminal settlement is durable: %w", taskID, err)
		}
	}
	sub := a.subAgentByTaskID(taskID)
	rehydrated := false
	previousAgentID := ""
	// Admission is decided from the task's settlement state, not from whether a
	// runtime happens to still be live. A settled task whose worker has not been
	// parked yet must answer the same two questions a parked one does — may this
	// trigger start a new attempt at all, and has the attempt moved past the
	// immutable settlement of the previous one. Skipping them for a live runtime
	// let a message undo a cancel, and let the second attempt's completion be
	// discarded as a settlement conflict while the owner was handed the first
	// attempt's result.
	if sub == nil || taskAlreadySettled(record, sub) {
		if !record.allowsRehydrate(trigger) {
			if SubAgentState(strings.TrimSpace(record.State)) == SubAgentStateCancelled {
				return tools.TaskHandle{}, fmt.Errorf("task %s was cancelled; a message must not undo that decision — delegate the work again if it should still be done", taskID)
			}
			return tools.TaskHandle{}, fmt.Errorf("task %s is %s; follow-up is not allowed without a live worker", taskID, strings.TrimSpace(record.State))
		}
	}
	if sub == nil {
		sub, previousAgentID, rehydrated, err = a.getOrRehydrateTask(record)
		if err != nil {
			return tools.TaskHandle{}, err
		}
	} else if err := a.beginNextTaskAttemptForLiveSub(sub); err != nil {
		return tools.TaskHandle{}, err
	}

	status, statusMessage, err := a.deliverMessageToSubAgentWithMetadata(sub, messageText, kind, false, &message.MailboxMetadata{
		AgentID: controlPlaneAgentID(callerAgentID),
		TaskID:  callerTaskID,
		Kind:    kind,
	})
	if err != nil {
		if rehydrated {
			a.closeSubAgent(sub.instanceID)
		}
		return tools.TaskHandle{}, err
	}

	handle := tools.TaskHandle{
		Status:  status,
		TaskID:  sub.taskID,
		AgentID: sub.instanceID,
		Message: statusMessage,
	}
	if rehydrated {
		handle.Status = "rehydrated"
		handle.PreviousAgentID = previousAgentID
		handle.Rehydrated = true
		handle.Message = "message delivered and task rehydrated"
	}
	sourceAgentID := controlPlaneAgentID(callerAgentID)
	sourceAgentType := ""
	sourceParentAgentID := ""
	sourceParentTaskID := ""
	if callerAgentID != "" {
		if caller := a.subAgentByID(callerAgentID); caller != nil {
			sourceAgentType = caller.agentDefName
			ownerAgentID, ownerTaskID, _, _ := caller.ownerSnapshot()
			sourceParentAgentID = controlPlaneAgentID(ownerAgentID)
			sourceParentTaskID = ownerTaskID
		}
	}
	a.emitToTUI(AgentNotifyEvent{
		AgentID:       sourceAgentID,
		TaskID:        callerTaskID,
		AgentType:     sourceAgentType,
		ParentAgentID: sourceParentAgentID,
		ParentTaskID:  sourceParentTaskID,
		TargetAgentID: sub.instanceID,
		TargetTaskID:  sub.taskID,
		Kind:          kind,
		Message:       messageText,
	})
	return handle, nil
}

func (a *MainAgent) deliverManualMessageToSubAgent(sub *SubAgent, message, kind string) (string, string, error) {
	return a.deliverMessageToSubAgentWithMode(sub, message, kind, true)
}

// subAgentDeliveryPrep carries the state prepared under sub.lifecycleMu that
// the unlocked delivery tail (owned-mailbox drain + commit + bookkeeping)
// still needs.
type subAgentDeliveryPrep struct {
	reservation      *subAgentInputReservation
	alreadyDelivered bool
	replyRecord      SubAgentMailboxAckRecord
	replyArtifact    tools.ArtifactRef
	replyMessageID   string
	replySummary     string
	replyKind        string
	needsResume      bool
	previousState    SubAgentState
	previousSummary  string
	drainOwned       bool
	status           string
	statusMessage    string
	mailboxMetadata  *message.MailboxMetadata
	trackReply       bool
}

func (a *MainAgent) deliverMessageToSubAgentWithMode(sub *SubAgent, message, kind string, manual bool) (string, string, error) {
	return a.deliverMessageToSubAgentWithMetadata(sub, message, kind, manual, nil)
}

func (a *MainAgent) deliverMessageToSubAgentWithMetadata(sub *SubAgent, message, kind string, manual bool, metadata *message.MailboxMetadata) (string, string, error) {
	return a.deliverMessageToSubAgentWithMetadataMode(sub, message, kind, manual, metadata, true)
}

func (a *MainAgent) deliverMessageToSubAgentWithMetadataMode(sub *SubAgent, message, kind string, manual bool, metadata *message.MailboxMetadata, trackReply bool) (string, string, error) {
	if sub == nil {
		return "", "", fmt.Errorf("missing worker")
	}
	prep, err := a.prepareSubAgentDelivery(sub, message, kind, manual, metadata, trackReply)
	if err != nil {
		return "", "", err
	}
	if prep.alreadyDelivered {
		return prep.status, prep.statusMessage, nil
	}
	defer prep.reservation.Cancel()

	// Drain owned child mailboxes before the manual message becomes visible so
	// the worker sees child reports first and its turn-start context drain
	// cannot race ahead of this routing. This must run outside sub.lifecycleMu
	// because routing re-enters the owner's lifecycle lock; parking stays
	// excluded because the outstanding reservation blocks canPark.
	if prep.drainOwned {
		a.drainOwnedSubAgentMailboxes(sub.instanceID)
	}
	if !prep.reservation.Commit() {
		if prep.needsResume {
			if !sub.rollbackState(SubAgentStateRunning, prep.previousState, prep.previousSummary) {
				a.releaseSubAgentSlot(sub)
				return "", "", fmt.Errorf("SubAgent %s cannot restore state %q", sub.instanceID, prep.previousState)
			}
			a.noteSubAgentStateTransition(sub, prep.previousState)
			a.releaseSubAgentSlot(sub)
		}
		return "", "", fmt.Errorf("SubAgent %s is shutting down; message not delivered", sub.instanceID)
	}
	if prep.replyRecord.MessageID != "" {
		a.applySubAgentMailboxReply(sub.instanceID, prep.replyRecord, prep.replyArtifact)
	} else if prep.trackReply {
		sub.setReplyThread(prep.replyMessageID, "", prep.replyKind, prep.replySummary)
		if prep.replyArtifact.RelPath != "" {
			sub.setLastArtifact(prep.replyArtifact)
		}
	}
	if prep.needsResume {
		sub.armStartupWatchdog()
	}

	a.saveRecoverySnapshot()
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	return prep.status, prep.statusMessage, nil
}

// prepareSubAgentDelivery performs the lifecycleMu-guarded half of a delivery:
// liveness check, slot acquisition, mailbox targeting, reply preparation, input
// reservation, ack persistence, and reactivation. On success the caller owns
// the returned reservation and must Commit or Cancel it.
func (a *MainAgent) prepareSubAgentDelivery(sub *SubAgent, message, kind string, manual bool, metadata *message.MailboxMetadata, trackReply bool) (*subAgentDeliveryPrep, error) {
	sub.lifecycleMu.Lock()
	defer sub.lifecycleMu.Unlock()
	if !a.subs.withSubAgent(sub.instanceID, func(current *SubAgent) bool { return current == sub }) {
		return nil, fmt.Errorf("SubAgent %s is no longer live; retry through task %s", sub.instanceID, sub.taskID)
	}
	if metadata != nil && sub.hasAcceptedMailbox(metadata.MessageID) {
		return &subAgentDeliveryPrep{
			alreadyDelivered: true,
			status:           "delivered",
			statusMessage:    "message already delivered",
		}, nil
	}
	state := sub.State()
	// A terminal runtime must never be reactivated by a plain delivery. Only
	// the explicit new-attempt machinery (resetForAttempt plus the task-record
	// attempt bump, run before this point by sendMessageToSubAgentWithTrigger
	// and the resume paths) may prepare a settled runtime for reuse; any other
	// late or manual message reaching a settled worker is a resurrection of
	// the finished attempt, so it is refused here at the shared delivery gate.
	if isTerminalSubAgentState(state) {
		return nil, fmt.Errorf("task %s has already finished as %s; a follow-up must delegate the work again or start a new attempt", strings.TrimSpace(sub.taskID), state)
	}

	a.subAgentMailboxIDsMu.Lock()
	drainOwned := manual && (len(a.ownedSubAgentMailboxes[sub.instanceID]) > 0 || len(a.ownedMailboxSpool[sub.instanceID]) > 0)
	a.subAgentMailboxIDsMu.Unlock()

	prep := &subAgentDeliveryPrep{
		replyKind:       normalizeReplyKind(kind),
		needsResume:     state != SubAgentStateRunning,
		previousState:   state,
		previousSummary: sub.LastSummary(),
		drainOwned:      drainOwned,
		status:          "queued",
		statusMessage:   "message delivered to running worker",
		mailboxMetadata: metadata,
		trackReply:      trackReply,
	}
	payload := normalizeSubAgentMessage(kind, message)

	if prep.needsResume {
		if err := a.acquireSubAgentSlot(sub); err != nil {
			return nil, err
		}
	}
	if manual {
		a.mailboxDeliveryPaused.Store(false)
	}

	var targetMailboxID string
	var targetMailbox *SubAgentMailboxMessage
	if trackReply {
		targetMailboxID = strings.TrimSpace(sub.LastMailboxID())
		targetMailbox = a.takeOutstandingMailboxForSub(sub)
	}
	if targetMailbox != nil {
		if !a.ensureSubAgentMailboxPersisted(targetMailbox) {
			a.requeueSubAgentMailboxInMemory(*targetMailbox)
			if prep.needsResume {
				a.releaseSubAgentSlot(sub)
			}
			return nil, fmt.Errorf("persist target mailbox before reply acknowledgement")
		}
		targetMailboxID = strings.TrimSpace(targetMailbox.MessageID)
	}

	if targetMailboxID != "" {
		prep.replyRecord, prep.replyArtifact = a.prepareSubAgentMailboxReply(sub.instanceID, targetMailboxID, 0, message, prep.replyKind)
		if prep.replyArtifact.RelPath != "" {
			payload = fmt.Sprintf("[%s] Summary: %s\nDetailed instruction artifact: %s", prep.replyKind, truncateMailboxReplySummary(message), prep.replyArtifact.RelPath)
		}
	} else if trackReply {
		prep.replyMessageID = a.nextSubAgentReplyMessageID(sub.instanceID)
		prep.replySummary = truncateMailboxReplySummary(message)
		if len(strings.TrimSpace(message)) > replyArtifactPayloadThreshold {
			artifactType := "execution_spec"
			artifactID, artifactRelPath, _, _, err := persistSubAgentArtifact(a.sessionDir, sub.instanceID, prep.replyMessageID, artifactType, "MainAgent follow-up", message)
			if err == nil && artifactRelPath != "" {
				prep.replyArtifact = tools.ArtifactRef{ID: artifactID, RelPath: artifactRelPath, Path: artifactRelPath, Type: artifactType}
				payload = fmt.Sprintf("[%s] Summary: %s\nDetailed instruction artifact: %s", prep.replyKind, truncateMailboxReplySummary(message), artifactRelPath)
			}
		}
	}
	prep.reservation = sub.reserveUserMessage(pendingUserMessage{Content: payload, FromUser: manual, DrainContextAppends: prep.drainOwned, Mailbox: prep.mailboxMetadata})
	if prep.reservation == nil {
		if targetMailbox != nil {
			a.requeueSubAgentMailboxInMemory(*targetMailbox)
		}
		if prep.needsResume {
			a.releaseSubAgentSlot(sub)
		}
		return nil, fmt.Errorf("SubAgent %s rejected the message during resume", sub.instanceID)
	}

	if prep.replyRecord.MessageID != "" {
		if err := a.appendSubAgentMailboxAck(prep.replyRecord); err != nil {
			prep.reservation.Cancel()
			if targetMailbox != nil {
				a.requeueSubAgentMailboxInMemory(*targetMailbox)
			}
			if prep.needsResume {
				a.releaseSubAgentSlot(sub)
			}
			return nil, fmt.Errorf("persist mailbox reply acknowledgement: %w", err)
		}
	}

	if prep.needsResume {
		a.markSubAgentReactivated(sub, message)
		prep.status = "resumed"
		prep.statusMessage = "message delivered and worker resumed"
	}
	return prep, nil
}

// taskAlreadySettled reports whether the task's current attempt has reached a
// terminal outcome, whether that outcome is recorded on the durable record, on
// the still-live runtime, or on both. Parking is what usually removes such a
// runtime, so any window before it — and any park that was refused — leaves a
// settled task with a live worker attached.
func taskAlreadySettled(record *DurableTaskRecord, sub *SubAgent) bool {
	if record != nil && isTerminalSubAgentState(SubAgentState(strings.TrimSpace(record.State))) {
		return true
	}
	return sub != nil && isTerminalSubAgentState(sub.State())
}

// beginNextTaskAttemptForLiveSub opens a new attempt on a task whose runtime is
// still live but whose current attempt already settled. A settlement is
// immutable per attempt: without the bump the worker's second completion is
// rejected as a conflicting settlement, terminalStatusAfterCommit pins the
// runtime back to the first outcome, and the owner receives the first attempt's
// summary and artifacts for work it asked to have redone. Parked tasks get the
// same treatment from rehydrateTaskAsActivationLeader; this is the live half of
// that rule.
func (a *MainAgent) beginNextTaskAttemptForLiveSub(sub *SubAgent) error {
	if a == nil || sub == nil {
		return nil
	}
	taskID := strings.TrimSpace(sub.taskID)
	if taskID == "" {
		return nil
	}
	a.settlementJournalMu.Lock()
	defer a.settlementJournalMu.Unlock()
	a.subs.mu.RLock()
	previous := cloneDurableTaskRecord(a.subs.taskRecords[taskID])
	a.subs.mu.RUnlock()
	if !taskAlreadySettled(previous, sub) {
		return nil
	}
	if previous != nil {
		next := cloneDurableTaskRecord(previous)
		next.Attempt = previous.Attempt + 1
		next.LatestSettlement = nil
		next.SettlementDurable = false
		next.LastCompletion = nil
		next.ClosedReason = ""
		next.State = string(SubAgentStateIdle)
		next.ResumePolicy = durableTaskResumePolicy(SubAgentStateIdle)
		next.LifecycleRevision = previous.LifecycleRevision + 1
		next.LastUpdatedTurn = a.explicitUserTurnCount.Load()
		next.UpdatedAt = time.Now()
		// Persist before publishing: a failed write must not leave memory
		// claiming an attempt the journal never saw.
		if err := a.persistTaskRegistryRecord(a.sessionDir, taskID, next); err != nil {
			return fmt.Errorf("persist new attempt for task %s: %w", taskID, err)
		}
		a.subs.mu.Lock()
		if current := a.subs.taskRecords[taskID]; current != nil && current.Attempt != previous.Attempt {
			a.subs.mu.Unlock()
			return fmt.Errorf("task %s attempt changed while starting a new one", taskID)
		}
		a.subs.taskRecords[taskID] = next
		a.subs.notifyTaskChangeLocked()
		a.subs.mu.Unlock()
	}
	// A runtime left in a terminal state can never accept a turn again
	// (canStartUserTurn requires Running), so the fresh attempt starts from the
	// same idle state a rehydrated one does.
	if isTerminalSubAgentState(sub.State()) {
		if !sub.resetForAttempt(sub.LastSummary()) {
			return fmt.Errorf("SubAgent %s cannot prepare terminal runtime for reuse", sub.instanceID)
		}
		a.noteSubAgentStateTransition(sub, SubAgentStateIdle)
	}
	return nil
}

func (a *MainAgent) getOrRehydrateTask(record *DurableTaskRecord) (*SubAgent, string, bool, error) {
	if record == nil {
		return nil, "", false, fmt.Errorf("missing task record")
	}
	if a.shuttingDown.Load() || a.admissionPaused.Load() {
		return nil, "", false, fmt.Errorf("cannot reactivate task during shutdown or session transition")
	}
	admissionEpoch := a.admissionEpoch.Load()
	taskID := strings.TrimSpace(record.TaskID)
	if taskID == "" {
		return nil, "", false, fmt.Errorf("missing task ID")
	}
	if sub, activation, leader := a.subs.beginTaskActivation(taskID); sub != nil {
		return sub, "", false, nil
	} else if !leader {
		select {
		case <-activation.done:
			return activation.sub, activation.previousAgentID, false, activation.err
		case <-a.parentCtx.Done():
			return nil, "", false, a.parentCtx.Err()
		}
	} else {
		return a.rehydrateTaskAsActivationLeader(record, activation, admissionEpoch)
	}
}

func (a *MainAgent) rehydrateTask(record *DurableTaskRecord) (*SubAgent, string, error) {
	sub, previousAgentID, _, err := a.getOrRehydrateTask(record)
	return sub, previousAgentID, err
}

func (a *MainAgent) rehydrateTaskAsActivationLeader(record *DurableTaskRecord, activation *subAgentActivation, admissionEpoch uint64) (sub *SubAgent, previousAgentID string, rehydrated bool, err error) {
	taskID := strings.TrimSpace(record.TaskID)
	defer func() {
		a.subs.completeTaskActivation(taskID, activation, sub, previousAgentID, err)
	}()
	// The revived worker runs under the record's normalized declared scope.
	// Delegate admits a task with an empty expected_write_scope only when its
	// role's permission rules register no file-modifying tools; under that
	// role the revived worker's tool surface is itself the boundary — no
	// write, edit, delete, or apply_patch tool is registered, so an empty
	// scope means "nothing to declare" rather than "unrestricted". A
	// write-capable role cannot produce an empty-scope record through
	// Delegate, so the record needs no further check: the role, re-derived
	// from the record's AgentDefName at rehydration, governs.
	writeScope := record.ExpectedWriteScope.Normalized()
	agentDef, err := a.resolveAgentDef(record.AgentDefName)
	if err != nil {
		return nil, "", false, err
	}
	if a.llmFactory == nil {
		return nil, "", false, fmt.Errorf("LLM client factory not configured; call SetLLMFactory before rehydrating SubAgents")
	}
	manager := a.recoveryManager()
	msgs, err := loadTaskHistoryMessages(manager, record, loadToolActivityStarted(manager))
	if err != nil {
		return nil, "", false, fmt.Errorf("load task history for %s: %w", record.TaskID, err)
	}

	subLLMClient := a.llmFactory("", a.effectiveSubAgentModels(agentDef), agentDef.Variant)
	a.applyServiceTierToClient(subLLMClient)
	var extraMCPTools []tools.Tool
	if len(agentDef.MCP) > 0 {
		extraMCPTools, err = a.getOrCreateAgentMCP(agentDef.Name, agentDef.MCP)
		if err != nil {
			return nil, "", false, err
		}
	}
	instanceID := NextInstanceID(agentDef.Name)
	ctx, cancel := context.WithCancel(a.parentCtx)
	subCfg := a.baseSubAgentConfig(agentDef, instanceID, subLLMClient, ctx, cancel, extraMCPTools)
	subCfg.TaskID = record.TaskID
	subCfg.TaskDesc = record.TaskDesc
	subCfg.PlanTaskRef = record.PlanTaskRef
	subCfg.SemanticKey = record.SemanticTaskKey
	subCfg.WriteScope = writeScope
	if len(record.ResultSchema) > 0 {
		// The contract is recompiled from its canonical bytes so a restored
		// worker validates exactly what it was admitted with. Uncompilable
		// bytes mean the record was damaged outside the tool chain; dropping
		// the contract is honest (nothing can enforce it) but must be loud,
		// because the task then keeps running without its declared contract.
		schema, canonical, err := tools.CompileResultSchema(record.ResultSchema)
		if err != nil {
			log.Warnf("dropping unreadable result contract task_id=%v error=%v", record.TaskID, err)
		} else {
			subCfg.ResultSchema = schema
			subCfg.ResultSchemaJSON = canonical
		}
	}
	subCfg.OwnerAgentID = record.OwnerAgentID
	subCfg.OwnerTaskID = record.OwnerTaskID
	subCfg.Depth = record.Depth
	subCfg.JoinToOwner = record.JoinToOwner
	// A resumed task continues in the checkout its last instance was working
	// in, when that checkout is still a chord-managed worktree of this
	// repository. Otherwise the new instance keeps the inherited directory
	// rather than resolving the restored transcript against a stale path.
	if meta, metaErr := loadSubAgentMeta(a.sessionDir, record.LatestInstanceID); metaErr != nil {
		log.Warnf("load subagent meta for workdir failed instance=%v error=%v", record.LatestInstanceID, metaErr)
	} else if meta != nil {
		if info := a.resolveRestoredWorktree(ctx, meta.WorkDir); info != nil {
			subCfg.WorkDir = info.Path
			// Resume in the recorded checkout with its identity intact: the
			// worker's environment block, completion report, and worktree
			// current-marking all read this binding.
			subCfg.WorkDirState = WorkDirState{
				Path:       info.Path,
				WorktreeID: info.Name,
				Branch:     info.Branch,
				BaseSHA:    info.BaseSHA,
			}
		}
	}
	sub = NewSubAgent(subCfg)
	sub.RestoreMessages(msgs)
	state := SubAgentState(strings.TrimSpace(record.State))
	if state == "" || state == SubAgentStateRunning || isTerminalSubAgentState(state) {
		// A terminal record only reaches rehydration through a resume trigger
		// that already authorized a new attempt (see allowsRehydrate), and the
		// attempt bookkeeping below clears the previous settlement. Carrying the
		// terminal state into the fresh runtime would leave a worker that can
		// never accept work.
		state = SubAgentStateIdle
	}
	sub.restoreState(state, strings.TrimSpace(record.LastSummary))
	if record.PendingCompletion != nil {
		sub.setPendingCompleteIntent(&AgentResult{Summary: record.PendingCompletion.Summary, Envelope: record.PendingCompletion})
	}
	if record.LastMailboxID != "" {
		sub.setLastMailboxID(record.LastMailboxID)
	}
	if record.LastReplyMessageID != "" || record.LastReplySummary != "" {
		sub.setReplyThread(record.LastReplyMessageID, record.LastReplyToMailboxID, record.LastReplyKind, record.LastReplySummary)
	}
	if len(record.LastArtifactRefs) > 0 {
		sub.setLastArtifact(record.LastArtifactRefs[0])
	}
	sub.persistenceHealth.restore(record.Persistence)
	admissionStartedAt := time.Now()
	a.admissionMu.Lock()
	a.orchestrationMetrics.recordAdmissionWait(time.Since(admissionStartedAt))
	if a.shuttingDown.Load() || a.admissionPaused.Load() || a.admissionEpoch.Load() != admissionEpoch {
		a.admissionMu.Unlock()
		cancel()
		return nil, "", false, fmt.Errorf("task reactivation invalidated by session or lifecycle change")
	}
	if err := a.acquireSubAgentSlot(sub); err != nil {
		a.admissionMu.Unlock()
		cancel()
		return nil, "", false, err
	}
	clientCommitted := false
	defer func() {
		if !clientCommitted && subLLMClient != nil {
			subLLMClient.Close()
		}
	}()
	previousAgentID = strings.TrimSpace(record.LatestInstanceID)
	a.subs.mu.Lock()
	if a.subs.activations[taskID] != activation || activation.cancelled {
		live := a.subs.subAgentByTaskIDLocked(taskID)
		a.subs.mu.Unlock()
		a.releaseSubAgentSlot(sub)
		a.admissionMu.Unlock()
		cancel()
		if live == nil {
			return nil, "", false, fmt.Errorf("task %s activation was superseded", taskID)
		}
		return live, "", false, nil
	}
	registrationSessionDir := a.sessionDir
	// The caller's record may be a stale snapshot: the WaitingMain expiry sweep
	// or a cascade cancel can settle this task between that snapshot and this
	// point, and deciding the attempt from the snapshot would resurrect an
	// attempt that already has a terminal settlement — its real completion
	// could then never be recorded. Decide from the current record instead.
	baseRecord := a.subs.taskRecords[taskID]
	if baseRecord == nil {
		baseRecord = record
	}
	// The record may have been settled, or its advisory scope replaced, while
	// this activation was preparing its runtime outside admissionMu. Publish
	// the current durable scope so the live snapshot matches the record.
	sub.publishWriteScope(baseRecord.ExpectedWriteScope)
	baseWasTerminal := isTerminalSubAgentState(SubAgentState(strings.TrimSpace(baseRecord.State)))
	rehydratedRecord := buildTaskRecordFromSub(sub, a.subs.taskRecords[taskID], "", a.explicitUserTurnCount.Load(), time.Now())
	if baseWasTerminal {
		rehydratedRecord.Attempt = baseRecord.Attempt + 1
		rehydratedRecord.LatestSettlement = nil
		rehydratedRecord.SettlementDurable = false
		rehydratedRecord.LastCompletion = nil
	}
	a.subs.mu.Unlock()
	if a.rehydrateCommitHook != nil {
		a.rehydrateCommitHook()
	}
	persistErr := a.persistSubAgentRegistration(registrationSessionDir, sub, rehydratedRecord)
	if persistErr != nil {
		_ = os.Remove(subAgentMetaPath(registrationSessionDir, sub.instanceID))
		a.releaseSubAgentSlot(sub)
		a.admissionMu.Unlock()
		cancel()
		return nil, "", false, fmt.Errorf("persist rehydrated durable task registration: %w", persistErr)
	}
	a.subs.mu.Lock()
	// Re-check the record at the commit point: the sweep or a cascade cancel
	// may have settled this attempt while the registration was being persisted.
	// Publishing the runtime then would overwrite a terminal record whose
	// settlement already exists, so back off and let the caller retry — the
	// retry sees the terminal record and starts a fresh attempt.
	settledDuringRehydrate := false
	if current := a.subs.taskRecords[taskID]; !baseWasTerminal && current != nil &&
		isTerminalSubAgentState(SubAgentState(strings.TrimSpace(current.State))) {
		settledDuringRehydrate = true
	}
	if a.subs.activations[taskID] != activation || activation.cancelled || a.subs.subAgentByTaskIDLocked(taskID) != nil || settledDuringRehydrate {
		live := a.subs.subAgentByTaskIDLocked(taskID)
		restoreRecord := record
		if settledDuringRehydrate {
			restoreRecord = cloneDurableTaskRecord(a.subs.taskRecords[taskID])
		}
		a.subs.mu.Unlock()
		_ = os.Remove(subAgentMetaPath(registrationSessionDir, sub.instanceID))
		_ = a.persistTaskRegistryRecord(registrationSessionDir, taskID, restoreRecord)
		a.releaseSubAgentSlot(sub)
		a.admissionMu.Unlock()
		cancel()
		if live != nil {
			return live, "", false, nil
		}
		if settledDuringRehydrate {
			return nil, "", false, fmt.Errorf("task %s settled as %s while reactivating; retry to start a new attempt", taskID, restoreRecord.State)
		}
		return nil, "", false, fmt.Errorf("task %s activation was superseded after persistence", taskID)
	}
	a.publishSubAgentLocked(sub, taskID, rehydratedRecord)
	a.subs.mu.Unlock()
	a.persistSubAgentRecoverySnapshot(sub, taskID)
	clientCommitted = true
	a.admissionMu.Unlock()
	a.migrateSubAgentOwnerIdentity(previousAgentID, sub.instanceID)
	a.focusedTaskMu.RLock()
	focusedTaskID := a.focusedTaskID
	a.focusedTaskMu.RUnlock()
	if focusedTaskID == sub.taskID {
		a.focusedAgent.Store(sub)
	}
	sub.startRunLoop()
	a.emitToTUI(AgentStartedEvent{
		AgentID:         sub.instanceID,
		PreviousAgentID: previousAgentID,
		TaskID:          sub.taskID,
		AgentType:       sub.agentDefName,
		Description:     sub.taskDesc,
		ParentAgentID:   controlPlaneAgentID(sub.OwnerAgentID()),
		ParentTaskID:    sub.OwnerTaskID(),
	})
	a.orchestrationMetrics.recordRehydrate(taskID, time.Now())
	return sub, previousAgentID, true, nil
}

func (a *MainAgent) stopSubAgentNow(callerAgentID, callerTaskID, taskID, reason string) (tools.TaskHandle, error) {
	taskID = strings.TrimSpace(taskID)
	reason = strings.TrimSpace(reason)
	if taskID == "" {
		return tools.TaskHandle{}, fmt.Errorf("task_id is required")
	}
	if _, err := a.canCallerControlTask(callerAgentID, callerTaskID, taskID); err != nil {
		return tools.TaskHandle{}, err
	}
	sub := a.subAgentByTaskID(taskID)
	if sub == nil {
		record := a.taskRecordByTaskID(taskID)
		if record != nil && record.RuntimeParked {
			cascadeFailures := a.cancelDescendantTasks(record.LatestInstanceID, taskID)
			reasonText := blankToDefault(reason, "stopped by main agent")
			outcome := a.settleDetachedTerminalTask(taskID, SubAgentStateCancelled, reasonText, reasonText)
			if outcome == "" {
				// Settle bails for two distinct reasons: the record vanished, or
				// its attempt changed inside the settlement window (the task was
				// revived concurrently). Only the former means "disappeared".
				if current := a.taskRecordByTaskID(taskID); current != nil {
					return tools.TaskHandle{}, fmt.Errorf("task %s changed while stopping parked worker; retry the stop", taskID)
				}
				return tools.TaskHandle{}, fmt.Errorf("task %s disappeared while stopping parked worker", taskID)
			}
			handleMessage := "parked worker stopped"
			eventMessage := blankToDefault(reason, "Stopped by MainAgent")
			if outcome != SubAgentStateCancelled {
				handleMessage = fmt.Sprintf("parked worker already %s", outcome)
				if current := a.taskRecordByTaskID(taskID); current != nil {
					eventMessage = blankToDefault(current.LastSummary, handleMessage)
				} else {
					eventMessage = handleMessage
				}
			}
			a.emitToTUI(AgentStatusEvent{AgentID: record.LatestInstanceID, Status: string(outcome), Message: eventMessage})
			return tools.TaskHandle{Status: string(outcome), TaskID: taskID, AgentID: record.LatestInstanceID, Message: appendCascadeFailures(handleMessage, cascadeFailures)}, nil
		}
		return tools.TaskHandle{}, fmt.Errorf("unknown task_id %q; cannot stop a missing worker", taskID)
	}

	state := sub.State()
	if state == SubAgentStateCancelled {
		return tools.TaskHandle{
			Status:  "cancelled",
			TaskID:  sub.taskID,
			AgentID: sub.instanceID,
			Message: "worker already cancelled",
		}, nil
	}
	if reason == "" {
		reason = "Stopped by MainAgent"
	}

	cascadeFailures := a.cancelDescendantTasks(sub.instanceID, sub.taskID)

	// Cancel synchronously for deterministic shutdown and tests.
	sub.cancelCurrentTurnFromLoop()
	a.releaseSubAgentSlot(sub)
	a.emitActivity(sub.instanceID, ActivityIdle, "")
	a.emitToTUI(ToastEvent{
		Message: reason,
		Level:   "warn",
		AgentID: sub.instanceID,
	})
	a.handleSubAgentCloseRequestedEvent(Event{
		Type:     EventSubAgentCloseRequested,
		SourceID: sub.instanceID,
		Payload: &SubAgentCloseRequestedPayload{
			Reason:       reason,
			ClosedReason: "stopped by main agent",
			FinalState:   SubAgentStateCancelled,
		},
	})
	a.saveRecoverySnapshot()

	return tools.TaskHandle{
		Status:  "cancelled",
		TaskID:  sub.taskID,
		AgentID: sub.instanceID,
		Message: appendCascadeFailures("worker stopped", cascadeFailures),
	}, nil
}

// cancelDescendantTasks cancels every direct child of ownerTaskID and returns a
// description of the ones that could not be cancelled.
//
// A failed child must not abort the ancestor's own cancellation: doing so left
// the subtree half-cancelled with no compensation and no retry, and reported
// only the first failure. The ancestor is stopped regardless and the residue is
// surfaced on the handle so the caller can retry those task IDs explicitly.
func (a *MainAgent) cancelDescendantTasks(ownerAgentID, ownerTaskID string) []string {
	ownerTaskID = strings.TrimSpace(ownerTaskID)
	if ownerTaskID == "" {
		return nil
	}
	var failures []string
	for _, childTaskID := range a.directChildTaskIDs(ownerTaskID) {
		if childTaskID == "" || childTaskID == ownerTaskID {
			continue
		}
		if _, err := a.stopSubAgentNow(ownerAgentID, ownerTaskID, childTaskID, fmt.Sprintf("cancelled because ancestor task %s was stopped", ownerTaskID)); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%v)", childTaskID, err))
			log.Warnf("cascade cancel failed for child task owner_task_id=%v child_task_id=%v error=%v", ownerTaskID, childTaskID, err)
		}
	}
	return failures
}

func appendCascadeFailures(message string, failures []string) string {
	if len(failures) == 0 {
		return message
	}
	return fmt.Sprintf("%s; %d child task(s) still need an explicit cancel: %s", message, len(failures), strings.Join(failures, ", "))
}

func (a *MainAgent) NotifySubAgent(ctx context.Context, taskID, message, kind string) (tools.TaskHandle, error) {
	if ctx == nil {
		ctx = a.parentCtx
		if ctx == nil {
			ctx = context.Background()
		}
	}
	callerAgentID := strings.TrimSpace(tools.AgentIDFromContext(ctx))
	callerTaskID := strings.TrimSpace(tools.TaskIDFromContext(ctx))
	if callerAgentID == strings.TrimSpace(a.instanceID) {
		callerAgentID = ""
	}
	if !a.started.Load() {
		return a.sendMessageToSubAgentNow(callerAgentID, callerTaskID, taskID, message, kind)
	}
	reply := make(chan subAgentControlResult, 1)
	a.sendEvent(Event{
		Type: EventSubAgentSendMessage,
		Payload: &SubAgentSendMessagePayload{
			Ctx:           ctx,
			CallerAgentID: callerAgentID,
			CallerTaskID:  callerTaskID,
			TaskID:        taskID,
			Message:       message,
			Kind:          kind,
			Reply:         reply,
		},
	})
	select {
	case result := <-reply:
		return result.Handle, result.Err
	case <-ctx.Done():
		return tools.TaskHandle{}, ctx.Err()
	case <-a.parentCtx.Done():
		return tools.TaskHandle{}, a.parentCtx.Err()
	}
}

func (a *MainAgent) CancelSubAgent(ctx context.Context, taskID, reason string) (tools.TaskHandle, error) {
	if ctx == nil {
		ctx = a.parentCtx
		if ctx == nil {
			ctx = context.Background()
		}
	}
	callerAgentID := strings.TrimSpace(tools.AgentIDFromContext(ctx))
	callerTaskID := strings.TrimSpace(tools.TaskIDFromContext(ctx))
	if callerAgentID == strings.TrimSpace(a.instanceID) {
		callerAgentID = ""
	}
	if !a.started.Load() {
		return a.stopSubAgentNow(callerAgentID, callerTaskID, taskID, reason)
	}
	reply := make(chan subAgentControlResult, 1)
	a.sendEvent(Event{
		Type: EventSubAgentStop,
		Payload: &SubAgentStopPayload{
			Ctx:           ctx,
			CallerAgentID: callerAgentID,
			CallerTaskID:  callerTaskID,
			TaskID:        taskID,
			Reason:        reason,
			Reply:         reply,
		},
	})
	select {
	case result := <-reply:
		return result.Handle, result.Err
	case <-ctx.Done():
		return tools.TaskHandle{}, ctx.Err()
	case <-a.parentCtx.Done():
		return tools.TaskHandle{}, a.parentCtx.Err()
	}
}

func (a *MainAgent) handleSubAgentSendMessageEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentSendMessagePayload)
	if !ok || payload == nil {
		return
	}
	if payload.Ctx != nil && payload.Ctx.Err() != nil {
		respondSubAgentControl(payload.Reply, tools.TaskHandle{}, payload.Ctx.Err())
		return
	}
	handle, err := a.sendMessageToSubAgentNow(payload.CallerAgentID, payload.CallerTaskID, payload.TaskID, payload.Message, payload.Kind)
	respondSubAgentControl(payload.Reply, handle, err)
}

func (a *MainAgent) handleSubAgentStopEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentStopPayload)
	if !ok || payload == nil {
		return
	}
	if payload.Ctx != nil && payload.Ctx.Err() != nil {
		respondSubAgentControl(payload.Reply, tools.TaskHandle{}, payload.Ctx.Err())
		return
	}
	handle, err := a.stopSubAgentNow(payload.CallerAgentID, payload.CallerTaskID, payload.TaskID, payload.Reason)
	respondSubAgentControl(payload.Reply, handle, err)
}
