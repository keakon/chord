package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tools"
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

func (a *MainAgent) normalizeSubAgentMailboxMessage(msg *SubAgentMailboxMessage) {
	if msg == nil {
		return
	}
	if strings.TrimSpace(msg.MessageID) == "" {
		msg.MessageID = a.nextSubAgentMailboxMessageID(msg.AgentID)
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	normalizeAgentMessageContract(msg)
	if msg.Completion != nil {
		msg.Completion = normalizeCompletionEnvelope(msg.Completion)
	}
	if msg.Attempt == 0 && strings.TrimSpace(msg.TaskID) != "" {
		a.subs.mu.RLock()
		if rec := a.subs.taskRecords[strings.TrimSpace(msg.TaskID)]; rec != nil {
			msg.Attempt = rec.Attempt
		}
		a.subs.mu.RUnlock()
	}
	if msg.SourceAttempt == 0 {
		msg.SourceAttempt = msg.Attempt
	}
	if msg.TargetAttempt == 0 && msg.TargetTaskID != "" {
		a.subs.mu.RLock()
		if rec := a.subs.taskRecords[msg.TargetTaskID]; rec != nil {
			msg.TargetAttempt = rec.Attempt
		}
		a.subs.mu.RUnlock()
	}
	msg.ArtifactRefs = tools.NormalizeArtifactRefs(msg.ArtifactRefs)
	if msg.Completion != nil {
		msg.ArtifactRefs = mergeArtifactRefs(msg.ArtifactRefs, msg.Completion.Artifacts)
	}
	if len(msg.MessagePayload) > mailboxArtifactPayloadThreshold {
		artifactType := "agent_message_payload"
		title := fmt.Sprintf("%s %s payload", msg.AgentID, msg.MessageType)
		artifactID, artifactRelPath, sizeBytes, digest, err := persistSubAgentArtifact(a.sessionDir, msg.AgentID, msg.MessageID, artifactType, title, string(msg.MessagePayload))
		if err == nil && artifactRelPath != "" {
			ref := tools.ArtifactRef{ID: artifactID, RelPath: artifactRelPath, Type: artifactType, SizeBytes: sizeBytes, SHA256: digest}
			msg.ArtifactRefs = mergeArtifactRefs(msg.ArtifactRefs, []tools.ArtifactRef{ref})
			msg.MessagePayload = nil
		}
	}
	if shouldPersistMailboxArtifact(*msg) {
		artifactType := artifactTypeForMailboxKind(msg.Kind)
		title := fmt.Sprintf("%s %s mailbox", msg.AgentID, msg.Kind)
		body := strings.TrimSpace(msg.Payload)
		if body == "" {
			body = strings.TrimSpace(msg.Summary)
		}
		artifactID, artifactRelPath, _, _, err := persistSubAgentArtifact(a.sessionDir, msg.AgentID, msg.MessageID, artifactType, title, body)
		if err == nil && artifactRelPath != "" {
			ref := tools.ArtifactRef{ID: artifactID, RelPath: artifactRelPath, Path: artifactRelPath, Type: artifactType}
			if msg.Completion == nil {
				msg.Completion = &CompletionEnvelope{}
			}
			msg.Completion.Artifacts = mergeArtifactRefs(msg.Completion.Artifacts, []tools.ArtifactRef{ref})
			msg.Payload = compactMailboxArtifactPayload(msg.Summary, artifactRelPath)
		}
	}
}

func normalizeAgentMessageContract(msg *SubAgentMailboxMessage) {
	if msg == nil {
		return
	}
	msg.SourceTaskID = strings.TrimSpace(msg.SourceTaskID)
	if msg.SourceTaskID == "" {
		msg.SourceTaskID = strings.TrimSpace(msg.TaskID)
	}
	msg.SourceAttempt = max(msg.SourceAttempt, msg.Attempt)
	msg.TargetTaskID = strings.TrimSpace(msg.TargetTaskID)
	if msg.TargetTaskID == "" {
		msg.TargetTaskID = strings.TrimSpace(msg.OwnerTaskID)
	}
	msg.Subtype = strings.TrimSpace(msg.Subtype)
	msg.CorrelationID = strings.TrimSpace(msg.CorrelationID)
	msg.InReplyTo = strings.TrimSpace(msg.InReplyTo)
	if msg.MessageType == "" {
		msg.MessageType = messageTypeForMailboxKind(msg.Kind)
	}
	if msg.CorrelationID == "" && msg.MessageType == AgentMessageTypeRequest {
		msg.CorrelationID = strings.TrimSpace(msg.MessageID)
	}
	if msg.Durability == "" {
		if msg.Kind == SubAgentMailboxKindProgress {
			msg.Durability = AgentMessageDurabilityBestEffort
		} else {
			msg.Durability = AgentMessageDurabilityRequired
		}
	}
}

func messageTypeForMailboxKind(kind SubAgentMailboxKind) AgentMessageType {
	switch kind {
	case SubAgentMailboxKindProgress:
		return AgentMessageTypeProgress
	case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired, SubAgentMailboxKindDirectionChange:
		return AgentMessageTypeRequest
	default:
		return AgentMessageTypeNotice
	}
}

func validateAgentMessageContract(msg *SubAgentMailboxMessage) error {
	if msg == nil {
		return nil
	}
	switch msg.MessageType {
	case AgentMessageTypeProgress, AgentMessageTypeNotice, AgentMessageTypeRequest, AgentMessageTypeResponse:
	default:
		return fmt.Errorf("invalid message_type %q", msg.MessageType)
	}
	if msg.Durability != AgentMessageDurabilityRequired && msg.Durability != AgentMessageDurabilityBestEffort {
		return fmt.Errorf("invalid message durability %q", msg.Durability)
	}
	if len(msg.MessagePayload) > maxAgentMessagePayloadBytes {
		return fmt.Errorf("message_payload exceeds maximum size %d bytes", maxAgentMessagePayloadBytes)
	}
	if len(msg.MessagePayload) > 0 {
		trimmed := bytes.TrimSpace(msg.MessagePayload)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return fmt.Errorf("message_payload must be a JSON object")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
			return fmt.Errorf("message_payload must be a JSON object")
		}
		msg.MessagePayload = append(json.RawMessage(nil), trimmed...)
	}
	if msg.MessageType == AgentMessageTypeRequest && msg.CorrelationID == "" {
		return fmt.Errorf("request message requires correlation_id")
	}
	if msg.MessageType == AgentMessageTypeResponse && (msg.CorrelationID == "" || msg.InReplyTo == "") {
		return fmt.Errorf("response message requires correlation_id and in_reply_to")
	}
	return nil
}

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
	text := formatSubAgentMailboxInjectionText(&msg)
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
		a.persistSubAgentMeta(live)
		a.syncTaskRecordFromSub(live, "")
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
	// A background result reaches its owner as the raw KindBackgroundResult
	// message the main transcript uses, not as a system-reminder mailbox
	// notice, and it continues the owner's context without a user turn.
	enqueueBackgroundContext := func() bool {
		return a.withRegisteredSubAgent(owner, func(live *SubAgent) bool {
			contextMessage := message.Message{
				Role:         message.RoleUser,
				Kind:         message.KindBackgroundResult,
				Content:      strings.TrimSpace(msg.Summary),
				MailboxAckID: strings.TrimSpace(msg.MessageID),
				Mailbox:      mailboxMetadata(&msg),
			}
			if contextMessage.Content == "" {
				return false
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

// drainOwnedSubAgentMailboxes routes every deliverable message queued under an
// owner and drops the ones routing accepted. The per-owner queues are also
// touched by TUI-facing APIs and the diagnostics goroutine, so the queue state
// is claimed and mutated only under subAgentMailboxIDsMu; routing happens
// outside that lock because it can re-enter delivery (for example forwarding a
// settled owner's mailbox to the main inbox).
func (a *MainAgent) drainOwnedSubAgentMailboxes(ownerAgentID string) bool {
	if a.mailboxDeliveryPaused.Load() {
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

func (a *MainAgent) loadSpooledMailbox(messageID string) (*SubAgentMailboxMessage, bool, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || a.isSubAgentMailboxConsumed(messageID) {
		return nil, false, nil
	}
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	if err := a.indexSpooledMailbox(path); err != nil {
		return nil, false, err
	}
	return a.reloadSpooledMailbox(path, messageID)
}

// reloadSpooledMailbox resolves messageID against the current spool index and,
// on a trusted hit, decodes the indexed byte span. Callers must refresh the
// index with indexSpooledMailbox first. The lookup and its ready/hit/miss
// decision are split out of loadSpooledMailbox so the stale-hit case — a
// rebuild that a concurrent append or rollback invalidated while it read the
// log, leaving an entry that can point at a span covering another record — is
// testable without reproducing the timing window.
func (a *MainAgent) reloadSpooledMailbox(path, messageID string) (*SubAgentMailboxMessage, bool, error) {
	a.subAgentMailboxIDsMu.Lock()
	location, ok := a.subAgentInbox.spoolIndex[messageID]
	ready := a.subAgentInbox.spoolIndexReady
	a.subAgentMailboxIDsMu.Unlock()
	if !ready {
		// The rebuild above could not republish — a persist or rollback landed
		// while it read the log — so the index may be missing this id or still
		// hold an entry that points at another row. Report the reload as failed
		// so the queueing callers keep the id queued for a retry; the next load
		// rebuilds over the current log. A stale index is never authoritative,
		// even when the id already appears in it.
		return nil, false, fmt.Errorf("spool mailbox index stale for message_id=%v: a concurrent append invalidated the rebuild", messageID)
	}
	if !ok {
		return nil, false, nil
	}
	msg, err := readSpooledMailboxAt(path, location)
	if err != nil {
		return nil, false, err
	}
	a.orchestrationMetrics.mailboxSpoolRehydrated.Add(1)
	return &msg, true, nil
}

// indexSpooledMailbox rebuilds the spool index after a write left it stale
// (spoolIndexReady false). The index is shared with the append path that keeps
// it fresh under subAgentMailboxIDsMu, so the log is read outside the lock and
// the freshly built map is published under it. Writes bump spoolWriteGen
// under the same lock after they touch the log; the rebuild snapshots the
// counter before reading and publishes only when it is unchanged, so a
// persist or rollback that landed mid-read keeps the index stale and the next
// load rebuilds over the current log instead of publishing an index that
// drops the appended entry (see publishSpooledMailboxIndex).
func (a *MainAgent) indexSpooledMailbox(path string) error {
	a.subAgentMailboxIDsMu.Lock()
	ready := a.subAgentInbox.spoolIndexReady
	writeGen := a.subAgentInbox.spoolWriteGen
	a.subAgentMailboxIDsMu.Unlock()
	if ready {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open spooled mailbox: %w", err)
	}
	built := make(map[string]mailboxSpoolLocation)
	dec := json.NewDecoder(f)
	var offset int64
	for {
		var msg SubAgentMailboxMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			_ = f.Close()
			return fmt.Errorf("decode spooled mailbox: %w", err)
		}
		messageID := strings.TrimSpace(msg.MessageID)
		if messageID != "" {
			if _, exists := built[messageID]; !exists {
				built[messageID] = mailboxSpoolLocation{offset: offset, length: dec.InputOffset() - offset}
			}
		}
		offset = dec.InputOffset()
	}
	_ = f.Close()
	a.publishSpooledMailboxIndex(built, writeGen)
	return nil
}

// publishSpooledMailboxIndex publishes a freshly built spool index when no
// other rebuild won in the meantime and the write generation still matches
// the snapshot taken before the log was read. When either condition fails the
// current index is left as-is and spoolIndexReady stays false, so the next
// load rebuilds over the current log: publishing a map that missed an append
// (or kept a rolled-back row) would let loadSpooledMailbox treat a queued id
// as not-found and drop it from the spool forever.
func (a *MainAgent) publishSpooledMailboxIndex(built map[string]mailboxSpoolLocation, writeGen uint64) {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if a.subAgentInbox.spoolIndexReady {
		// Another rebuild finished first; its index is at least as fresh.
		return
	}
	if a.subAgentInbox.spoolWriteGen != writeGen {
		// A persist appended or a rollback truncated the log while this index
		// was being built. Stay stale so the next load rebuilds over the
		// current log.
		return
	}
	a.subAgentInbox.spoolIndex = built
	a.subAgentInbox.spoolIndexReady = true
}

func readSpooledMailboxAt(path string, location mailboxSpoolLocation) (SubAgentMailboxMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return SubAgentMailboxMessage{}, fmt.Errorf("open spooled mailbox message: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(location.offset, io.SeekStart); err != nil {
		return SubAgentMailboxMessage{}, fmt.Errorf("seek spooled mailbox message: %w", err)
	}
	var msg SubAgentMailboxMessage
	if err := json.NewDecoder(io.LimitReader(f, location.length)).Decode(&msg); err != nil {
		return SubAgentMailboxMessage{}, fmt.Errorf("decode spooled mailbox message: %w", err)
	}
	return msg, nil
}

func (a *MainAgent) dequeueSpooledSubAgentMailbox() *SubAgentMailboxMessage {
	return a.dequeueSpooledMailboxQueue(&a.subAgentInbox.spoolNormal)
}

// dequeueSpooledMailboxQueue claims one queued spool id at a time under
// subAgentMailboxIDsMu and reloads the message from the mailbox log outside
// the lock. A missing record that was already consumed is skipped; one whose
// index is ready but whose row really is gone is reported as a dropped
// delivery instead of being skipped silently. A failed reload puts the id back
// at the front so the next call retries it.
func (a *MainAgent) dequeueSpooledMailboxQueue(queue *[]string) *SubAgentMailboxMessage {
	for {
		a.subAgentMailboxIDsMu.Lock()
		if len(*queue) == 0 {
			a.subAgentMailboxIDsMu.Unlock()
			return nil
		}
		id := strings.TrimSpace((*queue)[0])
		*queue = (*queue)[1:]
		a.subAgentMailboxIDsMu.Unlock()
		msg, found, err := a.loadSpooledMailbox(id)
		if err != nil {
			log.Warnf("failed to reload spooled SubAgent mailbox message message_id=%v error=%v", id, err)
			a.subAgentMailboxIDsMu.Lock()
			*queue = append([]string{id}, (*queue)...)
			a.subAgentMailboxIDsMu.Unlock()
			return nil
		}
		if !found {
			if a.isSubAgentMailboxConsumed(id) {
				log.Debugf("skipping consumed spooled SubAgent mailbox message message_id=%v", id)
				continue
			}
			a.reportSubAgentMailboxDropped(id, "", "", "spooled main-inbox mailbox row is missing and not consumed")
			continue
		}
		return msg
	}
}

func shouldPersistMailboxArtifact(msg SubAgentMailboxMessage) bool {
	if msg.Completion != nil && len(msg.Completion.Artifacts) > 0 {
		return false
	}
	// A background result's body is the exact text the owner must read, not a
	// handoff summary an artifact can stand in for, so it never moves its body
	// out of the row.
	if msg.Kind == SubAgentMailboxKindBackgroundResult {
		return false
	}
	payload := strings.TrimSpace(msg.Payload)
	summary := strings.TrimSpace(msg.Summary)
	return len(payload) > mailboxArtifactPayloadThreshold || (payload == "" && len(summary) > mailboxArtifactPayloadThreshold)
}

func compactMailboxArtifactPayload(summary, artifactRelPath string) string {
	summary = strings.TrimSpace(summary)
	artifactRelPath = strings.TrimSpace(artifactRelPath)
	if artifactRelPath == "" {
		return summary
	}
	if summary == "" {
		return fmt.Sprintf("Detailed handoff saved to artifact: %s", artifactRelPath)
	}
	return fmt.Sprintf("%s\n\nDetailed handoff saved to artifact: %s", summary, artifactRelPath)
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

func (a *MainAgent) emitSubAgentMailboxUI(msg SubAgentMailboxMessage) {
	if strings.TrimSpace(msg.AgentID) == "" {
		return
	}
	switch msg.Kind {
	case SubAgentMailboxKindProgress:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "running", Message: msg.Summary})
	case SubAgentMailboxKindCompleted:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "done", Message: msg.Summary})
	case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "waiting_main", Message: msg.Summary})
		a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "warn", AgentID: msg.AgentID})
	case SubAgentMailboxKindRiskAlert:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "waiting_main", Message: msg.Summary})
		a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "error", AgentID: msg.AgentID})
	case SubAgentMailboxKindDirectionChange:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "waiting_main", Message: msg.Summary})
		a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "warn", AgentID: msg.AgentID})
	}
}

func (a *MainAgent) persistSubAgentMailboxMessage(msg SubAgentMailboxMessage) error {
	_, err := a.persistSubAgentMailboxMessageWithOffset(msg)
	return err
}

// persistSubAgentMailboxMessageWithOffset appends msg to the session's
// mailbox.jsonl and returns the byte offset the appended line starts at (-1
// when nothing was appended or the offset could not be determined), so a
// caller that decides the message must not survive can roll the line back
// (see rollbackSubAgentMailboxMessage) exactly like the guarded settlement
// journal rollback (see task_terminal.go). mailbox.jsonl is written only on
// the event-loop goroutine (or, at startup, before the loop runs), so the
// append and a later rollback are synchronous; a rollback must still run
// before any other append extends the file.
func (a *MainAgent) persistSubAgentMailboxMessageWithOffset(msg SubAgentMailboxMessage) (int64, error) {
	sessionDir := strings.TrimSpace(a.sessionDir)
	if sessionDir == "" {
		return -1, nil
	}
	path := filepath.Join(sessionDir, "subagents", "mailbox.jsonl")
	startOffset, endOffset, err := a.appendSubAgentMailboxLog(sessionDir, path, msg)
	if err != nil {
		a.subAgentMailboxIDsMu.Lock()
		// The failed write may still have extended the file; treat the log as
		// touched so an in-flight index rebuild does not publish over it.
		a.subAgentInbox.spoolWriteGen++
		a.subAgentInbox.spoolIndexReady = false
		a.subAgentMailboxIDsMu.Unlock()
		return -1, err
	}
	messageID := strings.TrimSpace(msg.MessageID)
	a.subAgentMailboxIDsMu.Lock()
	// The append is durable now; any index rebuild that started before it
	// must not publish a map built from the shorter log.
	a.subAgentInbox.spoolWriteGen++
	if a.subAgentInbox.spoolIndexReady && startOffset >= 0 && endOffset > startOffset && messageID != "" {
		if _, exists := a.subAgentInbox.spoolIndex[messageID]; !exists {
			a.subAgentInbox.spoolIndex[messageID] = mailboxSpoolLocation{offset: startOffset, length: endOffset - startOffset}
		}
	} else {
		a.subAgentInbox.spoolIndexReady = false
	}
	a.subAgentMailboxIDsMu.Unlock()
	return startOffset, nil
}

// appendSubAgentMailboxLog appends msg to the session mailbox log and returns
// the byte span the appended record occupies. The write runs under
// spoolAppendMu so the log size before and after it bounds exactly this record
// even when another goroutine appends concurrently; otherwise the recorded
// start would be a stale size and the recorded length would cover the
// interleaved rows, and a reload of this id would decode a different message.
func (a *MainAgent) appendSubAgentMailboxLog(sessionDir, path string, msg SubAgentMailboxMessage) (int64, int64, error) {
	a.spoolAppendMu.Lock()
	defer a.spoolAppendMu.Unlock()
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return -1, -1, fmt.Errorf("open mailbox log: %w", err)
	}
	startOffset := int64(-1)
	if info, statErr := f.Stat(); statErr == nil {
		startOffset = info.Size()
	}
	if err := json.NewEncoder(f).Encode(msg); err != nil {
		_ = f.Close()
		return startOffset, -1, fmt.Errorf("append mailbox message: %w", err)
	}
	endOffset := int64(-1)
	if info, statErr := f.Stat(); statErr == nil {
		endOffset = info.Size()
	}
	if err := f.Close(); err != nil {
		return startOffset, endOffset, fmt.Errorf("close mailbox log: %w", err)
	}
	return startOffset, endOffset, nil
}

// rollbackSubAgentMailboxMessage truncates the mailbox.jsonl entry that
// persistSubAgentMailboxMessageWithOffset appended at offset, mirroring the
// guarded-settlement journal rollback (truncateTaskSettlementJournal in
// task_settlement.go). It withdraws a mailbox message (a WaitingMain expiry
// alert) whose guarded terminal settle backed off, so neither this process
// nor a restore after a crash can deliver an expiry that never happened. Both
// the append and the rollback run synchronously on the event-loop goroutine —
// the sole writer of the log — so the file still ends at the appended line.
// The message's idempotency mark is dropped with the line and its spool-index
// entry (the append was at the tail, so nothing indexed after it can exist)
// is removed.
func (a *MainAgent) rollbackSubAgentMailboxMessage(msg *SubAgentMailboxMessage, offset int64) {
	if msg == nil || offset < 0 {
		return
	}
	sessionDir := strings.TrimSpace(a.sessionDir)
	messageID := strings.TrimSpace(msg.MessageID)
	if sessionDir == "" || messageID == "" {
		return
	}
	a.spoolAppendMu.Lock()
	err := truncateSubAgentMailboxLog(sessionDir, offset)
	a.spoolAppendMu.Unlock()
	if err != nil {
		log.Errorf("failed to roll back mailbox message message_id=%v offset=%v error=%v (restore may replay it)", messageID, offset, err)
		return
	}
	a.subAgentMailboxIDsMu.Lock()
	// The truncation is durable now; any index rebuild that read the longer
	// log must not publish a map that resurrects the withdrawn row.
	a.subAgentInbox.spoolWriteGen++
	delete(a.subAgentMailboxIDs, messageID)
	if location, ok := a.subAgentInbox.spoolIndex[messageID]; ok && location.offset >= offset {
		delete(a.subAgentInbox.spoolIndex, messageID)
	}
	a.subAgentMailboxIDsMu.Unlock()
}

func truncateSubAgentMailboxLog(sessionDir string, size int64) error {
	if strings.TrimSpace(sessionDir) == "" || size < 0 {
		return nil
	}
	path := filepath.Join(sessionDir, "subagents", "mailbox.jsonl")
	f, err := privatefs.OpenFile(sessionDir, path, os.O_RDWR)
	if err != nil {
		return fmt.Errorf("open mailbox log for rollback: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return fmt.Errorf("truncate mailbox log: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync mailbox log: %w", err)
	}
	return f.Close()
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

func (a *MainAgent) loadDurableMailboxMessage(messageID string) (*SubAgentMailboxMessage, bool, error) {
	// Reuse the shared spool index instead of rescanning mailbox.jsonl from the
	// start on every call: loadSpooledMailbox skips consumed records, rebuilds
	// the index only when a write left it stale, and reads exactly the record's
	// own line. A missing mailbox log keeps the historical not-found result (it
	// holds no row to deliver), while a genuine read failure stays an error the
	// delivery retry keeps honoring.
	msg, found, err := a.loadSpooledMailbox(messageID)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return msg, found, err
}

func (a *MainAgent) stageNextSubAgentMailboxBatch() bool {
	if a.mailboxDeliveryPaused.Load() {
		return false
	}
	// One staging cycle merges every current main-inbox progress snapshot into
	// the staged batch, so the next request carries the whole routable backlog
	// in one batch. Progress/notice is claimed at every request boundary, mid-
	// turn included: like a queued user message it rides the next LLM request
	// without interrupting the one in flight or starting a request of its own.
	var progress []SubAgentMailboxMessage
	// A deferred delivery retry only runs between turns: each due entry
	// reloads the mailbox log and requeues, so re-running it on every mid-turn
	// continuation would churn the queues the active turn never drains. The
	// next between-turns drain keeps the retry alive.
	if a.turn == nil {
		a.retryDeferredMailboxDeliveries()
	}
	progress = a.takeMainInboxProgressSnapshots()
	msg := a.dequeueNextSubAgentMailbox()
	if msg == nil {
		if len(progress) == 0 {
			return false
		}
	} else if msg.Kind == SubAgentMailboxKindProgress {
		// Progress does not ride the urgent/normal queues as an actionable
		// head; whether the main is busy or idle it folds into the snapshot set
		// staged below and is delivered with the same next request.
		progress = append(progress, *msg)
		msg = nil
	}
	if msg == nil && len(progress) == 0 {
		return false
	}
	var pending []*SubAgentMailboxMessage
	if msg != nil {
		if !a.ensureSubAgentMailboxPersisted(msg) {
			a.requeueSubAgentMailboxInMemory(*msg)
			// requeueSubAgentMailboxInMemory prepends, so roll back in reverse
			// to keep the claimed order.
			for _, p := range slices.Backward(progress) {
				a.requeueSubAgentMailboxInMemory(p)
			}
			return false
		}
		pending = append(pending, msg)
		if msg.Kind == SubAgentMailboxKindCompleted {
			for {
				next := a.dequeueNextSubAgentMailbox()
				if next == nil {
					break
				}
				if next.Kind == SubAgentMailboxKindProgress {
					// A stray queue-resident progress update (progress normally
					// lives in the per-agent snapshot map, not the actionable
					// queues) folds into the same snapshot set as the head so it
					// is delivered with the same request instead of blocking the
					// completed-head batch.
					progress = append(progress, *next)
					continue
				}
				if !a.ensureSubAgentMailboxPersisted(next) {
					a.requeueSubAgentMailboxInMemory(*next)
					break
				}
				if next.Kind != SubAgentMailboxKindCompleted {
					a.requeueSubAgentMailboxInMemory(*next)
					break
				}
				pending = append(pending, next)
			}
		}
	}
	// Progress snapshots join after the actionable queue heads so batch order
	// stays stable for the overlay and ack consumers.
	for i := range progress {
		if !a.ensureSubAgentMailboxPersisted(&progress[i]) {
			// requeueSubAgentMailboxInMemory prepends to its queue, so the
			// rollback walks the claimed batch in reverse: the last-claimed
			// message must go back first for the queue order to match the
			// order this batch was taken in.
			for j := len(progress) - 1; j >= i; j-- {
				a.requeueSubAgentMailboxInMemory(progress[j])
			}
			for _, p := range slices.Backward(pending) {
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
	a.subAgentMailboxIDsMu.Lock()
	a.pendingSubAgentMailboxes = append(a.pendingSubAgentMailboxes, pending...)
	a.activeSubAgentMailboxes = append(a.activeSubAgentMailboxes, pending...)
	if a.activeSubAgentMailbox == nil {
		a.activeSubAgentMailbox = msg
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
	if a.mailboxDeliveryPaused.Load() {
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
	urgentHead, actionableHead := a.mailboxHeadState()
	if !idleMailboxWakeAllowed(urgentHead, actionableHead, a.consecutiveIdleWakes.Load()) {
		return
	}
	// The head state was captured before staging drains the queues: a
	// progress-only batch is informational and must not consume the
	// consecutive-wake budget.
	actionableWake := actionableHead
	if !a.stageNextSubAgentMailboxBatch() {
		return
	}
	// A mailbox turn carrying parked user input is a user turn, not a wake; a
	// turn with none is one link in the consecutive-wake chain. Either way the
	// parked queue is consumed immediately below.
	parkedUserTurn := a.pendingUserDrainSuspended
	a.newTurn()
	switch {
	case parkedUserTurn:
		a.consecutiveIdleWakes.Store(0)
	case actionableWake && !urgentHead:
		// An urgent/interrupt head always bypasses the bound so a parked worker
		// can never deadlock waiting on its owner; counting it here would spend
		// the budget a later normal completion wake needs, so only the ordinary
		// completion path advances the consecutive-wake counter.
		a.consecutiveIdleWakes.Add(1)
	}
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
	a.subAgentMailboxIDsMu.Lock()
	msgs := a.pendingSubAgentMailboxes
	a.pendingSubAgentMailboxes = nil
	a.subAgentMailboxIDsMu.Unlock()
	return msgs
}

func (a *MainAgent) refreshSubAgentInboxSummary() {
	// The urgent-count snapshot is derived from the same queue state the
	// mailbox delivery and manual-delivery goroutines mutate, so all queue
	// reads run under subAgentMailboxIDsMu; the summary map itself is
	// published under its own lock after the snapshot is taken.
	counts := make(map[string]int)
	a.subAgentMailboxIDsMu.Lock()
	for _, msg := range a.subAgentInbox.urgent {
		counts[msg.AgentID]++
	}
	for ownerID, queued := range a.ownedSubAgentMailboxes {
		for _, msg := range queued {
			if msg.Priority != SubAgentMailboxPriorityInterrupt && msg.Priority != SubAgentMailboxPriorityUrgent {
				continue
			}
			counts[ownerID]++
		}
	}
	if len(a.pendingSubAgentMailboxes) > 0 {
		for _, pending := range a.pendingSubAgentMailboxes {
			if pending != nil && (pending.Priority == SubAgentMailboxPriorityInterrupt || pending.Priority == SubAgentMailboxPriorityUrgent) {
				counts[pending.AgentID]++
			}
		}
	}
	if !a.activeSubAgentMailboxAck {
		for _, active := range a.activeSubAgentMailboxes {
			if active != nil && (active.Priority == SubAgentMailboxPriorityInterrupt || active.Priority == SubAgentMailboxPriorityUrgent) {
				counts[active.AgentID]++
			}
		}
		if len(a.activeSubAgentMailboxes) == 0 && a.activeSubAgentMailbox != nil &&
			(a.activeSubAgentMailbox.Priority == SubAgentMailboxPriorityInterrupt || a.activeSubAgentMailbox.Priority == SubAgentMailboxPriorityUrgent) {
			counts[a.activeSubAgentMailbox.AgentID]++
		}
	}
	a.subAgentMailboxIDsMu.Unlock()
	a.subAgentInboxSummaryMu.Lock()
	a.subAgentUrgentCounts = counts
	a.subAgentInboxSummaryMu.Unlock()
}

func formatSubAgentMailboxInjectionText(msg *SubAgentMailboxMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("SubAgent mailbox update:\n")
	b.WriteString("- agent_id: ")
	b.WriteString(msg.AgentID)
	b.WriteString("\n- task_id: ")
	b.WriteString(msg.TaskID)
	if strings.TrimSpace(msg.OwnerAgentID) != "" {
		b.WriteString("\n- owner_agent_id: ")
		b.WriteString(msg.OwnerAgentID)
	}
	if strings.TrimSpace(msg.OwnerTaskID) != "" {
		b.WriteString("\n- owner_task_id: ")
		b.WriteString(msg.OwnerTaskID)
	}
	if strings.TrimSpace(msg.InReplyTo) != "" {
		b.WriteString("\n- in_reply_to: ")
		b.WriteString(msg.InReplyTo)
	}
	b.WriteString("\n- kind: ")
	b.WriteString(string(msg.Kind))
	b.WriteString("\n- message_type: ")
	b.WriteString(string(msg.MessageType))
	if msg.Subtype != "" {
		b.WriteString("\n- subtype: ")
		b.WriteString(msg.Subtype)
	}
	if msg.SourceTaskID != "" {
		b.WriteString("\n- source: ")
		b.WriteString(fmt.Sprintf("%s#%d", msg.SourceTaskID, msg.SourceAttempt))
	}
	if msg.TargetTaskID != "" {
		b.WriteString("\n- target: ")
		b.WriteString(fmt.Sprintf("%s#%d", msg.TargetTaskID, msg.TargetAttempt))
	}
	if msg.CorrelationID != "" {
		b.WriteString("\n- correlation_id: ")
		b.WriteString(msg.CorrelationID)
	}
	b.WriteString("\n- priority: ")
	b.WriteString(string(msg.Priority))
	b.WriteString("\n- summary: ")
	b.WriteString(msg.Summary)
	// Summary and Payload are rendered together only when they differ: several
	// mailbox producers (completion, notify, escalate) fill both fields with
	// the same text, so writing both lines billed the identical content twice
	// inside one mailbox. Payload stays stored on the message for its other
	// consumers (mailbox memory, artifact-body fallback, spool text); only the
	// model-facing text elides the copy.
	summary := strings.TrimSpace(msg.Summary)
	if payload := strings.TrimSpace(msg.Payload); payload != "" && payload != summary {
		b.WriteString("\n- payload: ")
		b.WriteString(msg.Payload)
	}
	if len(msg.MessagePayload) > 0 {
		b.WriteString("\n- message_payload: ")
		b.Write(msg.MessagePayload)
	}
	if len(msg.ArtifactRefs) > 0 {
		b.WriteString("\n- artifact_refs:")
		for _, ref := range msg.ArtifactRefs {
			b.WriteString("\n  - ")
			b.WriteString(ref.RelPath)
		}
	}
	if msg.Completion != nil {
		// The completed mailbox text is the single in-request expression of the
		// completion when the coordination snapshot elides the task (see
		// completionAlreadyDeliveredByMailbox), so the typed-result handle the
		// snapshot would otherwise surface is carried here instead.
		if msg.Completion.ResultType != "" {
			b.WriteString("\n- result_type: ")
			b.WriteString(msg.Completion.ResultType)
		}
		if msg.Completion.ResultRef != nil {
			label := strings.TrimSpace(msg.Completion.ResultRef.RelPath)
			if label == "" {
				label = strings.TrimSpace(msg.Completion.ResultRef.ID)
			}
			if label != "" {
				b.WriteString("\n- result_ref: ")
				b.WriteString(label)
			}
		}
		if len(msg.Completion.FilesChanged) > 0 {
			b.WriteString("\n- files_changed: ")
			b.WriteString(strings.Join(msg.Completion.FilesChanged, ", "))
		}
		if len(msg.Completion.RemainingLimitations) > 0 {
			b.WriteString("\n- remaining_limitations: ")
			b.WriteString(strings.Join(msg.Completion.RemainingLimitations, ", "))
		}
		if len(msg.Completion.KnownRisks) > 0 {
			b.WriteString("\n- known_risks: ")
			b.WriteString(strings.Join(msg.Completion.KnownRisks, ", "))
		}
		if len(msg.Completion.FollowUpRecommended) > 0 {
			b.WriteString("\n- follow_up_recommended: ")
			b.WriteString(strings.Join(msg.Completion.FollowUpRecommended, ", "))
		}
		if len(msg.Completion.Artifacts) > 0 {
			refs := make([]string, 0, len(msg.Completion.Artifacts))
			for _, ref := range msg.Completion.Artifacts {
				ref = tools.NormalizeArtifactRef(ref)
				if ref.RelPath != "" {
					refs = append(refs, ref.RelPath)
				} else if ref.ID != "" {
					refs = append(refs, ref.ID)
				}
			}
			if len(refs) > 0 {
				b.WriteString("\n- artifact_refs: ")
				b.WriteString(strings.Join(refs, ", "))
			}
		}
	}
	if msg.RequiresAck {
		b.WriteString("\n- requires_ack: true")
	}
	return b.String()
}

func mailboxMetadata(msg *SubAgentMailboxMessage) *message.MailboxMetadata {
	if msg == nil {
		return nil
	}
	return &message.MailboxMetadata{
		MessageID:     strings.TrimSpace(msg.MessageID),
		AgentID:       strings.TrimSpace(msg.AgentID),
		TaskID:        strings.TrimSpace(msg.TaskID),
		OwnerAgentID:  strings.TrimSpace(msg.OwnerAgentID),
		OwnerTaskID:   strings.TrimSpace(msg.OwnerTaskID),
		Kind:          strings.TrimSpace(string(msg.Kind)),
		MessageType:   strings.TrimSpace(string(msg.MessageType)),
		Subtype:       strings.TrimSpace(msg.Subtype),
		SourceTaskID:  strings.TrimSpace(msg.SourceTaskID),
		SourceAttempt: msg.SourceAttempt,
		TargetTaskID:  strings.TrimSpace(msg.TargetTaskID),
		TargetAttempt: msg.TargetAttempt,
		CorrelationID: strings.TrimSpace(msg.CorrelationID),
		InReplyTo:     strings.TrimSpace(msg.InReplyTo),
	}
}

func subAgentMailboxConversationMessage(msg *SubAgentMailboxMessage, content string) message.Message {
	return message.Message{
		Role:    message.RoleUser,
		Content: content,
		Kind:    message.KindSubAgentMailbox,
		Mailbox: mailboxMetadata(msg),
	}
}
