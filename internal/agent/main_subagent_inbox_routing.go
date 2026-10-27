package agent

import (
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// ownedMailboxRoute is where an owned-queue message can go right now.
type ownedMailboxRoute uint8

const (
	// ownedMailboxRouteNone covers the only-temporarily-unroutable case: a
	// message spooled under a parked owner that this mailbox may not wake. It
	// must never be reported as runnable mailbox work, or a stranded message
	// would suppress global idle forever.
	ownedMailboxRouteNone ownedMailboxRoute = iota
	ownedMailboxRouteLiveOwner
	ownedMailboxRouteWakeParkedOwner
	ownedMailboxRouteForwardToMain
)

// resolveOwnedMailboxRoute decides where an owned-queue message goes, with no
// delivery side effects. It is the single answer to that question:
// routeOwnedSubAgentMailbox performs the route it returns and
// ownedMailboxMessageRoutable only asks whether a route exists, so the
// "is this deliverable" predicate cannot drift from the delivery itself.
func (a *MainAgent) resolveOwnedMailboxRoute(msg SubAgentMailboxMessage) (ownedMailboxRoute, *SubAgent, *DurableTaskRecord) {
	ownerAgentID := strings.TrimSpace(msg.OwnerAgentID)
	if ownerAgentID == "" {
		return ownedMailboxRouteNone, nil, nil
	}
	if owner := unsettledMailboxOwner(a.subAgentByID(ownerAgentID)); owner != nil {
		return ownedMailboxRouteLiveOwner, owner, nil
	}
	rec := a.taskRecordByInstanceID(ownerAgentID)
	if rec == nil {
		return ownedMailboxRouteNone, nil, nil
	}
	if !rec.RuntimeParked {
		// The owner may have been rehydrated under a new instance ID.
		if owner := unsettledMailboxOwner(a.subAgentByTaskID(rec.TaskID)); owner != nil {
			return ownedMailboxRouteLiveOwner, owner, rec
		}
	}
	// A parked owner may be woken by a descendant mailbox (child completion or
	// child decision request); only genuine descendant messages wake it, so
	// unrelated messages spooled under its queue stay queued instead.
	if rec.RuntimeParked && msg.Kind != SubAgentMailboxKindProgress &&
		rec.allowsRehydrate(taskResumeByDescendantMailbox) && a.ownedMailboxIsFromDescendant(msg, rec) {
		return ownedMailboxRouteWakeParkedOwner, nil, rec
	}
	if !isNonTerminalTaskState(rec.State) {
		if a.handoffDeliveryHeld() {
			// Forwarding to the main inbox is automatic main delivery, so it is
			// held while a handoff wait is open: the message stays queued under
			// the terminal owner and the post-decision drain re-routes it.
			return ownedMailboxRouteNone, nil, rec
		}
		return ownedMailboxRouteForwardToMain, nil, rec
	}
	return ownedMailboxRouteNone, nil, rec
}

// unsettledMailboxOwner drops a runtime whose task already settled: a child
// mailbox must not resurrect a finished owner just because its runtime has not
// been parked yet. Such a message is forwarded to the main inbox, exactly as it
// is once only the owner's terminal record remains.
func unsettledMailboxOwner(sub *SubAgent) *SubAgent {
	if sub == nil || isTerminalSubAgentState(sub.State()) {
		return nil
	}
	return sub
}

func (a *MainAgent) routeOwnedSubAgentMailbox(msg SubAgentMailboxMessage) bool {
	finish, err := a.beginSessionStructural(a.parentCtx, false)
	if err != nil {
		return false
	}
	defer finish()
	route, owner, rec := a.resolveOwnedMailboxRoute(msg)
	switch route {
	case ownedMailboxRouteLiveOwner:
	case ownedMailboxRouteWakeParkedOwner:
		var err error
		if owner, _, err = a.rehydrateTask(rec); err != nil {
			return false
		}
	case ownedMailboxRouteForwardToMain:
		// Forward to main as a main-owned message: clear both owner fields so
		// the mailbox metadata, injection text, and durable task-record sync
		// cannot re-associate it with the finished owner. The record is already
		// durable — it was persisted before it entered the owned queue — so it
		// is routed into the main inbox without being written again: routing it
		// back through enqueueSubAgentMailbox would append a second mailbox.jsonl
		// row for the same MessageID. Clearing the owner makes the re-resolve
		// inside deliverSubAgentMailbox return a non-owned route, so this cannot
		// recurse.
		msg.OwnerAgentID = ""
		msg.OwnerTaskID = ""
		a.deliverSubAgentMailbox(msg)
		return true
	default:
		return false
	}
	if owner == nil {
		return false
	}
	// The owner receives the same <system-reminder>-wrapped injection text the
	// main transcript uses (systemReminderOverlayText via
	// appendMailboxMessageToContext): the content-trust prompt only recognizes
	// that wrapper as the harness-message marker, and wrapping here keeps the
	// pending user message and the durable context row on one identical text.
	text := systemReminderOverlayText(formatSubAgentMailboxInjectionText(&msg))
	reactivateLive := func(live *SubAgent, messageText string, statusMsg string, allowWakeBypass bool) bool {
		input := pendingUserMessage{Content: messageText, MailboxAckID: strings.TrimSpace(msg.MessageID), Mailbox: mailboxMetadata(&msg)}
		reservation := live.reserveUserMessage(input)
		if reservation == nil {
			return false
		}
		defer reservation.Cancel()
		held, _ := live.slotState()
		if !held {
			var err error
			if allowWakeBypass {
				err = a.acquireWakeReactivationSlot(live)
			} else {
				err = a.acquireSubAgentSlot(live)
			}
			if err != nil {
				return false
			}
		}
		if !reservation.Commit() {
			if !held {
				a.releaseSubAgentSlot(live)
			}
			return false
		}
		if !live.setState(SubAgentStateRunning, statusMsg) {
			if !held {
				a.releaseSubAgentSlot(live)
			}
			return false
		}
		a.noteSubAgentStateTransition(live, SubAgentStateRunning)
		a.emitActivity(live.instanceID, ActivityExecuting, "child event")
		a.emitToTUI(AgentStatusEvent{AgentID: live.instanceID, Status: "running", Message: statusMsg})
		a.orchestrationMetrics.recordMailboxDelivery(msg.MessageID, msg.CreatedAt)
		live.armStartupWatchdog()
		a.syncSubAgentPersists(live, "")
		a.saveRecoverySnapshot()
		return true
	}
	reactivateOwner := func(messageText string, statusMsg string, allowWakeBypass bool) bool {
		return a.withRegisteredSubAgent(owner, func(live *SubAgent) bool {
			return reactivateLive(live, messageText, statusMsg, allowWakeBypass)
		})
	}
	enqueueContext := func(messageText string) bool {
		return a.withRegisteredSubAgent(owner, func(live *SubAgent) bool {
			contextMessage := subAgentMailboxConversationMessage(&msg, messageText)
			contextMessage.MailboxAckID = msg.MessageID
			if !live.TryEnqueueContextAppend(contextMessage) {
				return false
			}
			a.orchestrationMetrics.recordMailboxDelivery(msg.MessageID, msg.CreatedAt)
			return true
		})
	}
	// A background result reaches its owner as the KindBackgroundResult message
	// the main transcript uses — still not a mailbox notice, and it continues
	// the owner's context without a user turn — wrapped in the same
	// <system-reminder> harness marker as the main-side durable row.
	enqueueBackgroundContext := func() bool {
		return a.withRegisteredSubAgent(owner, func(live *SubAgent) bool {
			content := strings.TrimSpace(msg.Summary)
			if content == "" {
				return false
			}
			contextMessage := message.Message{
				Role:         message.RoleUser,
				Kind:         message.KindBackgroundResult,
				Content:      systemReminderOverlayText(content),
				MailboxAckID: strings.TrimSpace(msg.MessageID),
				Mailbox:      mailboxMetadata(&msg),
			}
			if !live.TryEnqueueContextAppend(contextMessage) {
				return false
			}
			a.orchestrationMetrics.recordMailboxDelivery(msg.MessageID, msg.CreatedAt)
			live.ContinueFromContext()
			return true
		})
	}
	enqueueForProcessing := func(messageText, statusMsg string) bool {
		return a.withRegisteredSubAgent(owner, func(live *SubAgent) bool {
			if live.State() != SubAgentStateRunning {
				return reactivateLive(live, messageText, statusMsg, false)
			}
			if !live.InjectUserMessageWithMailboxAck(messageText, msg.MessageID, mailboxMetadata(&msg)) {
				return false
			}
			a.orchestrationMetrics.recordMailboxDelivery(msg.MessageID, msg.CreatedAt)
			live.armStartupWatchdog()
			return true
		})
	}
	switch msg.Kind {
	case SubAgentMailboxKindBackgroundResult:
		return enqueueBackgroundContext()
	case SubAgentMailboxKindProgress:
		return enqueueContext(text)
	case SubAgentMailboxKindCompleted:
		remaining := a.outstandingJoinChildTaskIDs(owner.taskID)
		if owner.State() == SubAgentStateWaitingDescendant && len(remaining) == 0 && msg.AgentID != "" {
			var transferredFrom *SubAgent
			if child := a.subAgentByID(msg.AgentID); child != nil {
				childHeld, _ := child.slotState()
				ownerHeld, _ := owner.slotState()
				if childHeld && !ownerHeld && a.transferSubAgentSlot(child, owner) {
					transferredFrom = child
				}
			}
			pendingComplete := owner.PendingCompleteIntent()
			if pendingComplete != nil && strings.TrimSpace(pendingComplete.Summary) != "" {
				pendingText := "Parent pending completion intent:\n- summary: " + pendingComplete.Summary
				if env := normalizeCompletionEnvelope(pendingComplete.Envelope); env != nil {
					if len(env.FilesChanged) > 0 {
						pendingText += "\n- files_changed: " + strings.Join(env.FilesChanged, ", ")
					}
					if len(env.Artifacts) > 0 {
						refs := make([]string, 0, len(env.Artifacts))
						for _, ref := range env.Artifacts {
							ref = tools.NormalizeArtifactRef(ref)
							if ref.RelPath != "" {
								refs = append(refs, ref.RelPath)
							}
						}
						if len(refs) > 0 {
							pendingText += "\n- artifact_refs: " + strings.Join(refs, ", ")
						}
					}
				}
				text = pendingText + "\n\n" + text
				owner.clearPendingCompleteIntent()
			}
			if reactivateOwner(text, "Child task completed; resuming", true) {
				return true
			}
			if transferredFrom != nil {
				a.transferSubAgentSlot(owner, transferredFrom)
			}
			return false
		}
		return enqueueForProcessing(text, "Child task completed; resuming")
	case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired, SubAgentMailboxKindRiskAlert, SubAgentMailboxKindDirectionChange:
		// A decision request or risk alert that lands after its task already
		// settled is stale: the owner cannot act on a worker that is no
		// longer running, and delivering it would surface a false "worker
		// needs you" after the task completed (or failed). Drop the late
		// delivery instead of replaying it.
		if rec := a.taskRecordByTaskID(strings.TrimSpace(msg.TaskID)); rec != nil && !isNonTerminalTaskState(rec.State) {
			log.Infof("dropping late %s mailbox for settled task task_id=%v agent_id=%v", msg.Kind, msg.TaskID, msg.AgentID)
			return false
		}
		if owner.State() == SubAgentStateWaitingDescendant {
			return reactivateOwner(text, "Child task requires parent decision", true)
		}
		return enqueueForProcessing(text, "Child task requires parent decision")
	default:
		return enqueueForProcessing(text, "Child task sent an update")
	}
}

// ownedMailboxIsFromDescendant reports whether an owned mailbox message is a
// genuine descendant mailbox of a parked owner record: each worker addresses
// its completion and decision requests to its direct parent, so the sender's
// durable task record and the message itself must both name this owner's task,
// and the owner instance the message was addressed to must be the instance the
// sender records as its owner. Only such messages may wake a parked owner; an
// unrelated message that happens to be spooled under the owner's queue must
// stay queued instead of rehydrating the owner.
func (a *MainAgent) ownedMailboxIsFromDescendant(msg SubAgentMailboxMessage, owner *DurableTaskRecord) bool {
	if owner == nil || strings.TrimSpace(owner.TaskID) == "" {
		return false
	}
	sender := a.taskRecordByTaskID(strings.TrimSpace(msg.TaskID))
	if sender == nil {
		return false
	}
	return strings.TrimSpace(sender.OwnerAgentID) == strings.TrimSpace(msg.OwnerAgentID) &&
		strings.TrimSpace(sender.OwnerTaskID) == strings.TrimSpace(owner.TaskID) &&
		strings.TrimSpace(msg.OwnerTaskID) == strings.TrimSpace(owner.TaskID)
}

// ownedMailboxMessageRoutable reports whether an owned queue message could be
// delivered right now, without delivering it.
func (a *MainAgent) ownedMailboxMessageRoutable(msg SubAgentMailboxMessage) bool {
	route, _, _ := a.resolveOwnedMailboxRoute(msg)
	return route != ownedMailboxRouteNone
}

func (a *MainAgent) enqueueOwnedSubAgentMailbox(msg SubAgentMailboxMessage) {
	ownerAgentID := strings.TrimSpace(msg.OwnerAgentID)
	if ownerAgentID == "" {
		return
	}
	// The per-owner queues are also read by the TUI-facing diagnostics entry
	// (see OrchestrationTaskDiagnostics), which runs on a different goroutine
	// from the delivery paths, so every mutation happens under
	// subAgentMailboxIDsMu. Helper reads of these maps (mailboxMemoryCount and
	// friends) are only safe when called with this lock held.
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if a.ownedSubAgentMailboxes == nil {
		a.ownedSubAgentMailboxes = make(map[string][]SubAgentMailboxMessage)
	}
	messageLimit, byteLimit := a.mailboxMemoryLimits()
	size := mailboxMessageBytes(msg)
	// FIFO per owner: once anything is spooled for this owner, later messages
	// must join the spool behind it rather than jump ahead through memory.
	if len(a.ownedMailboxSpool[ownerAgentID]) == 0 && a.mailboxMemoryCount() < messageLimit && a.subAgentInbox.memoryBytes+size <= byteLimit {
		a.ownedSubAgentMailboxes[ownerAgentID] = append(a.ownedSubAgentMailboxes[ownerAgentID], msg)
		a.subAgentInbox.memoryBytes += size
		return
	}
	if a.ownedMailboxSpool == nil {
		a.ownedMailboxSpool = make(map[string][]string)
	}
	a.ownedMailboxSpool[ownerAgentID] = append(a.ownedMailboxSpool[ownerAgentID], msg.MessageID)
	a.orchestrationMetrics.mailboxSpoolQueued.Add(1)
}

func (a *MainAgent) migrateSubAgentOwnerIdentity(previousAgentID, nextAgentID string) {
	previousAgentID = strings.TrimSpace(previousAgentID)
	nextAgentID = strings.TrimSpace(nextAgentID)
	if previousAgentID == "" || nextAgentID == "" || previousAgentID == nextAgentID {
		return
	}
	a.subAgentMailboxIDsMu.Lock()
	if queued := a.ownedSubAgentMailboxes[previousAgentID]; len(queued) > 0 {
		for i := range queued {
			queued[i].OwnerAgentID = nextAgentID
		}
		a.ownedSubAgentMailboxes[nextAgentID] = append(a.ownedSubAgentMailboxes[nextAgentID], queued...)
		delete(a.ownedSubAgentMailboxes, previousAgentID)
	}
	if queued := a.ownedMailboxSpool[previousAgentID]; len(queued) > 0 {
		a.ownedMailboxSpool[nextAgentID] = append(a.ownedMailboxSpool[nextAgentID], queued...)
		delete(a.ownedMailboxSpool, previousAgentID)
	}
	a.subAgentMailboxIDsMu.Unlock()
	a.subs.mu.Lock()
	for _, rec := range a.subs.taskRecords {
		if rec != nil && strings.TrimSpace(rec.OwnerAgentID) == previousAgentID {
			rec.OwnerAgentID = nextAgentID
		}
	}
	a.subs.mu.Unlock()
}

// drainOwnedSubAgentMailboxes routes every deliverable message queued under an
// owner and drops the ones routing accepted. The per-owner queues are also
// touched by TUI-facing APIs and the diagnostics goroutine, so the queue state
// is claimed and mutated only under subAgentMailboxIDsMu; routing happens
// outside that lock because it can re-enter delivery (for example forwarding a
// settled owner's mailbox to the main inbox).
func (a *MainAgent) drainOwnedSubAgentMailboxes(ownerAgentID string) bool {
	if a.mailboxDeliveryPaused.Load() || a.sessionExecutionPaused() {
		return false
	}
	ownerAgentID = strings.TrimSpace(ownerAgentID)
	if ownerAgentID == "" {
		return false
	}
	a.subAgentMailboxIDsMu.Lock()
	queue := append([]SubAgentMailboxMessage(nil), a.ownedSubAgentMailboxes[ownerAgentID]...)
	delete(a.ownedSubAgentMailboxes, ownerAgentID)
	spooled := append([]string(nil), a.ownedMailboxSpool[ownerAgentID]...)
	delete(a.ownedMailboxSpool, ownerAgentID)
	a.subAgentMailboxIDsMu.Unlock()

	progressed := false
	remaining := queue[:0]
	for _, msg := range queue {
		if a.routeOwnedSubAgentMailbox(msg) {
			a.subAgentMailboxIDsMu.Lock()
			a.releaseMailboxMemory(msg)
			a.subAgentMailboxIDsMu.Unlock()
			progressed = true
			continue
		}
		remaining = append(remaining, msg)
	}
	spoolRemaining := spooled[:0]
	for i, messageID := range spooled {
		msg, found, err := a.loadSpooledMailbox(messageID)
		if err != nil {
			log.Warnf("failed to reload owned spooled SubAgent mailbox message owner_agent_id=%v message_id=%v error=%v", ownerAgentID, messageID, err)
			spoolRemaining = append(spoolRemaining, spooled[i:]...)
			break
		}
		if !found {
			// A missing spool row is benign only when the message was already
			// consumed (the delivery path removed its waiting row). Otherwise
			// the index is ready and the row really is gone: this process can
			// no longer deliver it, so the drop is reported instead of the
			// silent skip that left the waiting row stuck forever.
			if a.isSubAgentMailboxConsumed(messageID) {
				log.Debugf("skipping consumed owned spooled SubAgent mailbox message owner_agent_id=%v message_id=%v", ownerAgentID, messageID)
				continue
			}
			a.reportSubAgentMailboxDropped(messageID, ownerAgentID, "", "spooled owned mailbox row is missing and not consumed")
			continue
		}
		if a.routeOwnedSubAgentMailbox(*msg) {
			progressed = true
			continue
		}
		spoolRemaining = append(spoolRemaining, messageID)
	}
	if len(remaining) > 0 || len(spoolRemaining) > 0 {
		a.subAgentMailboxIDsMu.Lock()
		// Keep the un-routed remainder ahead of anything another delivery path
		// enqueued while this drain was routing, so per-owner FIFO order holds.
		if len(remaining) > 0 {
			current := a.ownedSubAgentMailboxes[ownerAgentID]
			merged := make([]SubAgentMailboxMessage, 0, len(remaining)+len(current))
			merged = append(merged, remaining...)
			merged = append(merged, current...)
			a.ownedSubAgentMailboxes[ownerAgentID] = merged
		}
		if len(spoolRemaining) > 0 {
			current := a.ownedMailboxSpool[ownerAgentID]
			merged := make([]string, 0, len(spoolRemaining)+len(current))
			merged = append(merged, spoolRemaining...)
			merged = append(merged, current...)
			a.ownedMailboxSpool[ownerAgentID] = merged
		}
		a.subAgentMailboxIDsMu.Unlock()
	}
	return progressed
}
