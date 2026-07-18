package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if msg.Completion != nil {
		msg.Completion = normalizeCompletionEnvelope(msg.Completion)
	}
	if shouldPersistMailboxArtifact(*msg) {
		artifactType := artifactTypeForMailboxKind(msg.Kind)
		title := fmt.Sprintf("%s %s mailbox", msg.AgentID, msg.Kind)
		body := strings.TrimSpace(msg.Payload)
		if body == "" {
			body = strings.TrimSpace(msg.Summary)
		}
		artifactID, artifactRelPath, err := persistSubAgentArtifact(a.sessionDir, msg.AgentID, msg.MessageID, artifactType, title, body)
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

func (a *MainAgent) routeOwnedSubAgentMailbox(msg SubAgentMailboxMessage) bool {
	ownerAgentID := strings.TrimSpace(msg.OwnerAgentID)
	if ownerAgentID == "" {
		return false
	}
	owner := a.subAgentByID(ownerAgentID)
	if owner == nil {
		rec := a.taskRecordByInstanceID(ownerAgentID)
		if rec != nil && !rec.RuntimeParked {
			owner = a.subAgentByTaskID(rec.TaskID)
		}
		if owner == nil && msg.Kind != SubAgentMailboxKindProgress {
			if rec != nil && rec.RuntimeParked && rec.allowsRehydrate(taskResumeByDescendantMailbox) {
				var err error
				owner, _, err = a.rehydrateTask(rec)
				if err != nil {
					return false
				}
			}
		}
	}
	if owner == nil {
		if rec := a.taskRecordByInstanceID(ownerAgentID); rec != nil && !isNonTerminalTaskState(rec.State) {
			msg.OwnerAgentID = ""
			a.enqueueSubAgentMailbox(msg)
			return true
		}
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
		live.setState(SubAgentStateRunning, statusMsg)
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
					if len(env.VerificationRun) > 0 {
						pendingText += "\n- verification_run: " + strings.Join(env.VerificationRun, ", ")
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
		if owner.State() == SubAgentStateWaitingDescendant {
			return reactivateOwner(text, "Child task requires parent decision", true)
		}
		return enqueueForProcessing(text, "Child task requires parent decision")
	default:
		return enqueueForProcessing(text, "Child task sent an update")
	}
}

func (a *MainAgent) enqueueOwnedSubAgentMailbox(msg SubAgentMailboxMessage) {
	ownerAgentID := strings.TrimSpace(msg.OwnerAgentID)
	if ownerAgentID == "" {
		return
	}
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

func (a *MainAgent) drainOwnedSubAgentMailboxes(ownerAgentID string) bool {
	if a.mailboxDeliveryPaused.Load() {
		return false
	}
	ownerAgentID = strings.TrimSpace(ownerAgentID)
	if ownerAgentID == "" {
		return false
	}
	queue := a.ownedSubAgentMailboxes[ownerAgentID]
	remaining := queue[:0]
	progressed := false
	for _, msg := range queue {
		if a.routeOwnedSubAgentMailbox(msg) {
			a.releaseMailboxMemory(msg)
			progressed = true
			continue
		}
		remaining = append(remaining, msg)
	}
	if len(remaining) == 0 {
		delete(a.ownedSubAgentMailboxes, ownerAgentID)
	} else {
		a.ownedSubAgentMailboxes[ownerAgentID] = remaining
	}
	spooled := a.ownedMailboxSpool[ownerAgentID]
	spoolRemaining := spooled[:0]
	for i, messageID := range spooled {
		msg, found, err := a.loadSpooledMailbox(messageID)
		if err != nil {
			log.Warnf("failed to reload owned spooled SubAgent mailbox message owner_agent_id=%v message_id=%v error=%v", ownerAgentID, messageID, err)
			spoolRemaining = append(spoolRemaining, spooled[i:]...)
			break
		}
		if !found {
			continue
		}
		if a.routeOwnedSubAgentMailbox(*msg) {
			progressed = true
			continue
		}
		spoolRemaining = append(spoolRemaining, messageID)
	}
	if len(spoolRemaining) == 0 {
		delete(a.ownedMailboxSpool, ownerAgentID)
	} else {
		a.ownedMailboxSpool[ownerAgentID] = spoolRemaining
	}
	return progressed
}

func (a *MainAgent) enqueueSubAgentMailbox(msg SubAgentMailboxMessage) {
	if err := a.prepareSubAgentMailboxMessage(&msg); err != nil {
		log.Warnf("failed to persist SubAgent mailbox message message_id=%v task_id=%v kind=%v error=%v", msg.MessageID, msg.TaskID, msg.Kind, err)
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("SubAgent mailbox durability degraded: %w", err)})
		if msg.Kind != SubAgentMailboxKindProgress {
			msg.persistPending = true
			a.requeueSubAgentMailboxInMemory(msg)
			return
		}
	}
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
	if a.subAgentInbox.progress == nil {
		a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
	}
	switch msg.Kind {
	case SubAgentMailboxKindProgress:
		a.replaceProgressMailboxWithinBudget(msg)
	default:
		if !a.storeMailboxInMemory(msg, false) {
			a.spoolMailboxMessage(msg, false)
			a.orchestrationMetrics.mailboxSpoolQueued.Add(1)
		}
	}
	a.refreshSubAgentInboxSummary()
}

// replaceProgressMailboxWithinBudget swaps the per-agent progress snapshot only
// when the replacement fits the memory budget. When it does not, the previous
// snapshot is kept so an overloaded inbox still reports last-known status
// instead of dropping both the old and the new update.
func (a *MainAgent) replaceProgressMailboxWithinBudget(msg SubAgentMailboxMessage) {
	if a.subAgentInbox.progress == nil {
		a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
	}
	previous, hadPrevious := a.subAgentInbox.progress[msg.AgentID]
	freedBytes, freedCount := 0, 0
	if hadPrevious {
		freedBytes = mailboxMessageBytes(previous)
		freedCount = 1
	}
	messageLimit, byteLimit := a.mailboxMemoryLimits()
	size := mailboxMessageBytes(msg)
	if a.mailboxMemoryCount()-freedCount >= messageLimit || a.subAgentInbox.memoryBytes-freedBytes+size > byteLimit {
		return
	}
	a.subAgentInbox.progress[msg.AgentID] = msg
	a.subAgentInbox.memoryBytes += size - freedBytes
	if a.subAgentInbox.memoryBytes < 0 {
		a.subAgentInbox.memoryBytes = 0
	}
}

func (a *MainAgent) requeueSubAgentMailboxInMemory(msg SubAgentMailboxMessage) {
	if msg.Kind == SubAgentMailboxKindProgress {
		if a.subAgentInbox.progress == nil {
			a.subAgentInbox.progress = make(map[string]SubAgentMailboxMessage)
		}
		if previous, ok := a.subAgentInbox.progress[msg.AgentID]; ok {
			a.subAgentInbox.memoryBytes -= mailboxMessageBytes(previous)
		}
		a.subAgentInbox.progress[msg.AgentID] = msg
		a.subAgentInbox.memoryBytes += mailboxMessageBytes(msg)
	} else if msg.persistPending {
		if msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent {
			a.subAgentInbox.urgent = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.urgent...)
		} else {
			a.subAgentInbox.normal = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.normal...)
		}
		a.subAgentInbox.memoryBytes += mailboxMessageBytes(msg)
	} else if !a.storeMailboxInMemory(msg, true) {
		a.spoolMailboxMessage(msg, true)
		a.orchestrationMetrics.mailboxSpoolQueued.Add(1)
	}
	a.refreshSubAgentInboxSummary()
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
	location, ok := a.subAgentInbox.spoolIndex[messageID]
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

func (a *MainAgent) indexSpooledMailbox(path string) error {
	if a.subAgentInbox.spoolIndexReady {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open spooled mailbox: %w", err)
	}
	defer f.Close()
	if a.subAgentInbox.spoolIndex == nil {
		a.subAgentInbox.spoolIndex = make(map[string]mailboxSpoolLocation)
	} else {
		clear(a.subAgentInbox.spoolIndex)
	}
	dec := json.NewDecoder(f)
	var offset int64
	for {
		var msg SubAgentMailboxMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				a.subAgentInbox.spoolIndexReady = true
				return nil
			}
			return fmt.Errorf("decode spooled mailbox: %w", err)
		}
		messageID := strings.TrimSpace(msg.MessageID)
		if messageID != "" {
			if _, exists := a.subAgentInbox.spoolIndex[messageID]; !exists {
				a.subAgentInbox.spoolIndex[messageID] = mailboxSpoolLocation{offset: offset, length: dec.InputOffset() - offset}
			}
		}
		offset = dec.InputOffset()
	}
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
	if msg := a.dequeueSpooledMailboxQueue(&a.subAgentInbox.spoolNormal); msg != nil {
		return msg
	}
	return nil
}

func (a *MainAgent) dequeueSpooledMailboxQueue(queue *[]string) *SubAgentMailboxMessage {
	for len(*queue) > 0 {
		id := strings.TrimSpace((*queue)[0])
		msg, found, err := a.loadSpooledMailbox(id)
		if err != nil {
			log.Warnf("failed to reload spooled SubAgent mailbox message message_id=%v error=%v", id, err)
			return nil
		}
		if !found {
			*queue = (*queue)[1:]
			continue
		}
		*queue = (*queue)[1:]
		return msg
	}
	return nil
}

func shouldPersistMailboxArtifact(msg SubAgentMailboxMessage) bool {
	if msg.Completion != nil && len(msg.Completion.Artifacts) > 0 {
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
	if messageID != "" && (!a.markSubAgentMailboxSeen(messageID) || a.isSubAgentMailboxConsumed(messageID)) {
		return
	}
	a.enqueueSubAgentMailbox(*msg)
	if msg.Kind != SubAgentMailboxKindProgress && !a.mailboxDeliveryPaused.Load() {
		a.drainSubAgentInbox()
	}
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
	sessionDir := strings.TrimSpace(a.sessionDir)
	if sessionDir == "" {
		return nil
	}
	dir := filepath.Join(sessionDir, "subagents")
	path := filepath.Join(dir, "mailbox.jsonl")
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("open mailbox log: %w", err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(msg); err != nil {
		_ = f.Close()
		return fmt.Errorf("append mailbox message: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close mailbox log: %w", err)
	}
	a.subAgentInbox.spoolIndexReady = false
	return nil
}

func (a *MainAgent) dequeueNextSubAgentMailbox() *SubAgentMailboxMessage {
	if len(a.subAgentInbox.urgent) > 0 {
		msg := a.subAgentInbox.urgent[0]
		a.subAgentInbox.urgent = a.subAgentInbox.urgent[1:]
		a.releaseMailboxMemory(msg)
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	if len(a.subAgentInbox.spoolUrgent) > 0 {
		if msg := a.dequeueSpooledMailboxQueue(&a.subAgentInbox.spoolUrgent); msg != nil {
			return msg
		}
	}
	if len(a.subAgentInbox.normal) > 0 {
		msg := a.subAgentInbox.normal[0]
		a.subAgentInbox.normal = a.subAgentInbox.normal[1:]
		a.releaseMailboxMemory(msg)
		a.refreshSubAgentInboxSummary()
		return &msg
	}
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

func (a *MainAgent) stageNextSubAgentMailboxBatch() bool {
	if a.mailboxDeliveryPaused.Load() {
		return false
	}
	msg := a.dequeueNextSubAgentMailbox()
	if msg == nil {
		return false
	}
	if !a.ensureSubAgentMailboxPersisted(msg) {
		a.requeueSubAgentMailboxInMemory(*msg)
		return false
	}
	if msg.Kind == SubAgentMailboxKindProgress {
		return false
	}
	pending := []*SubAgentMailboxMessage{msg}
	if msg.Kind == SubAgentMailboxKindCompleted {
		for {
			next := a.dequeueNextSubAgentMailbox()
			if next == nil {
				break
			}
			if next.Kind == SubAgentMailboxKindProgress {
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
	a.pendingSubAgentMailboxes = pending
	a.activeSubAgentMailboxes = append([]*SubAgentMailboxMessage(nil), pending...)
	a.activeSubAgentMailbox = msg
	a.activeSubAgentMailboxAck = true
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
	if len(a.pendingSubAgentMailboxes) > 0 || len(a.activeSubAgentMailboxes) > 0 || a.activeSubAgentMailbox != nil {
		return false
	}
	return a.stageNextSubAgentMailboxBatch()
}

func (a *MainAgent) drainSubAgentInbox() {
	if a.mailboxDeliveryPaused.Load() {
		return
	}
	if a.turn != nil {
		return
	}
	if !a.stageNextSubAgentMailboxBatch() {
		return
	}
	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "main")
}

func (a *MainAgent) markActiveSubAgentMailboxAck(ack bool) {
	if len(a.activeSubAgentMailboxes) == 0 && a.activeSubAgentMailbox == nil {
		return
	}
	a.activeSubAgentMailboxAck = ack
}

func (a *MainAgent) requeueActiveSubAgentMailbox() {
	if (len(a.activeSubAgentMailboxes) == 0 && a.activeSubAgentMailbox == nil) || a.activeSubAgentMailboxAck {
		return
	}
	batch := a.activeSubAgentMailboxes
	if len(batch) == 0 && a.activeSubAgentMailbox != nil {
		batch = []*SubAgentMailboxMessage{a.activeSubAgentMailbox}
	}
	for i := len(batch) - 1; i >= 0; i-- {
		msg := batch[i]
		if msg == nil {
			continue
		}
		switch msg.Kind {
		case SubAgentMailboxKindProgress:
			if previous, ok := a.subAgentInbox.progress[msg.AgentID]; ok {
				a.releaseMailboxMemory(previous)
			}
			a.subAgentInbox.progress[msg.AgentID] = *msg
			a.subAgentInbox.memoryBytes += mailboxMessageBytes(*msg)
		default:
			a.requeueSubAgentMailboxInMemory(*msg)
		}
	}
	a.refreshSubAgentInboxSummary()
}

func (a *MainAgent) takePendingSubAgentMailboxes() []*SubAgentMailboxMessage {
	msgs := a.pendingSubAgentMailboxes
	a.pendingSubAgentMailboxes = nil
	return msgs
}

func (a *MainAgent) refreshSubAgentInboxSummary() {
	counts := make(map[string]int)
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
	b.WriteString("\n- priority: ")
	b.WriteString(string(msg.Priority))
	b.WriteString("\n- summary: ")
	b.WriteString(msg.Summary)
	if strings.TrimSpace(msg.Payload) != "" {
		b.WriteString("\n- payload: ")
		b.WriteString(msg.Payload)
	}
	if msg.Completion != nil {
		if len(msg.Completion.FilesChanged) > 0 {
			b.WriteString("\n- files_changed: ")
			b.WriteString(strings.Join(msg.Completion.FilesChanged, ", "))
		}
		if len(msg.Completion.VerificationRun) > 0 {
			b.WriteString("\n- verification_run: ")
			b.WriteString(strings.Join(msg.Completion.VerificationRun, ", "))
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
		MessageID:    strings.TrimSpace(msg.MessageID),
		AgentID:      strings.TrimSpace(msg.AgentID),
		TaskID:       strings.TrimSpace(msg.TaskID),
		OwnerAgentID: strings.TrimSpace(msg.OwnerAgentID),
		OwnerTaskID:  strings.TrimSpace(msg.OwnerTaskID),
		Kind:         strings.TrimSpace(string(msg.Kind)),
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
