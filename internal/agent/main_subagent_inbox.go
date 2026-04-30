package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) prepareSubAgentMailboxMessage(msg *SubAgentMailboxMessage) {
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
	if len(msg.ArtifactRelPaths) > 0 {
		legacyRefs := artifactRefsFromLegacy(msg.ArtifactIDs, msg.ArtifactRelPaths, msg.ArtifactType)
		if msg.Completion == nil {
			msg.Completion = &CompletionEnvelope{}
		}
		msg.Completion.Artifacts = mergeArtifactRefs(msg.Completion.Artifacts, legacyRefs)
	}
	if shouldPersistMailboxArtifact(*msg) {
		artifactType := msg.ArtifactType
		if strings.TrimSpace(artifactType) == "" {
			artifactType = artifactTypeForMailboxKind(msg.Kind)
		}
		title := fmt.Sprintf("%s %s mailbox", msg.AgentID, msg.Kind)
		body := strings.TrimSpace(msg.Payload)
		if body == "" {
			body = strings.TrimSpace(msg.Summary)
		}
		artifactID, artifactRelPath, err := persistSubAgentArtifact(a.sessionDir, msg.AgentID, msg.MessageID, artifactType, title, body)
		if err == nil && artifactRelPath != "" {
			msg.ArtifactType = artifactType
			msg.ArtifactIDs = appendUniqueString(msg.ArtifactIDs, artifactID)
			msg.ArtifactRelPaths = appendUniqueString(msg.ArtifactRelPaths, artifactRelPath)
			if msg.Completion == nil {
				msg.Completion = &CompletionEnvelope{}
			}
			msg.Completion.Artifacts = mergeArtifactRefs(msg.Completion.Artifacts, artifactRefsFromLegacy([]string{artifactID}, []string{artifactRelPath}, artifactType))
			msg.Payload = compactMailboxArtifactPayload(msg.Summary, artifactRelPath)
		}
	}
	if sub := a.subAgentByID(msg.AgentID); sub != nil {
		sub.setLastMailboxID(msg.MessageID)
		artifactRefs := tools.NormalizeArtifactRefs(nil)
		if msg.Completion != nil {
			artifactRefs = mergeArtifactRefs(artifactRefs, msg.Completion.Artifacts)
		}
		artifactRefs = mergeArtifactRefs(artifactRefs, artifactRefsFromLegacy(msg.ArtifactIDs, msg.ArtifactRelPaths, msg.ArtifactType))
		if len(artifactRefs) > 0 {
			first := artifactRefs[0]
			sub.setLastArtifact(first.ID, first.RelPath, first.Type)
		}
		a.persistSubAgentMeta(sub)
	}
	a.persistSubAgentMailboxMessage(*msg)
	a.syncTaskRecordFromMailbox(*msg)
	a.emitSubAgentMailboxUI(*msg)
}

func (a *MainAgent) routeOwnedSubAgentMailbox(msg SubAgentMailboxMessage) bool {
	ownerAgentID := strings.TrimSpace(msg.OwnerAgentID)
	if ownerAgentID == "" {
		return false
	}
	owner := a.subAgentByID(ownerAgentID)
	if owner == nil {
		return false
	}
	text := formatSubAgentMailboxInjectionText(&msg)
	reactivateOwner := func(messageText string, statusMsg string, allowWakeBypass bool) bool {
		if !owner.semHeld {
			var err error
			if allowWakeBypass {
				err = a.acquireWakeReactivationSlot(owner)
			} else {
				err = a.acquireSubAgentSlot(owner)
			}
			if err != nil {
				return false
			}
		}
		owner.setState(SubAgentStateRunning, statusMsg)
		a.noteSubAgentStateTransition(owner, SubAgentStateRunning)
		a.emitActivity(owner.instanceID, ActivityExecuting, "child event")
		a.emitToTUI(AgentStatusEvent{AgentID: owner.instanceID, Status: "running", Message: statusMsg})
		owner.InjectUserMessageWithMailboxAck(messageText, msg.MessageID)
		a.persistSubAgentMeta(owner)
		a.syncTaskRecordFromSub(owner, "")
		a.saveRecoverySnapshot()
		return true
	}
	switch msg.Kind {
	case SubAgentMailboxKindProgress:
		if owner.TryEnqueueContextAppend(message.Message{Role: "user", Content: text, MailboxAckID: msg.MessageID}) {
			return true
		}
		return false
	case SubAgentMailboxKindCompleted:
		remaining := a.outstandingJoinChildTaskIDs(owner.taskID)
		if owner.State() == SubAgentStateWaitingDescendant && len(remaining) == 0 && msg.AgentID != "" {
			if child := a.subAgentByID(msg.AgentID); child != nil && child.semHeld && !owner.semHeld {
				a.transferSubAgentSlot(child, owner)
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
			return false
		}
		if owner.TryEnqueueContextAppend(message.Message{Role: "user", Content: text, MailboxAckID: msg.MessageID}) {
			return true
		}
		return false
	case SubAgentMailboxKindBlocked, SubAgentMailboxKindDecisionRequired, SubAgentMailboxKindRiskAlert, SubAgentMailboxKindDirectionChange:
		if owner.State() == SubAgentStateWaitingDescendant {
			return reactivateOwner(text, "Child task requires parent decision", true)
		}
		owner.InjectUserMessageWithMailboxAck(text, msg.MessageID)
		return true
	default:
		if owner.TryEnqueueContextAppend(message.Message{Role: "user", Content: text, MailboxAckID: msg.MessageID}) {
			return true
		}
		return false
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
	a.ownedSubAgentMailboxes[ownerAgentID] = append(a.ownedSubAgentMailboxes[ownerAgentID], msg)
}

func (a *MainAgent) drainOwnedSubAgentMailboxes(ownerAgentID string) bool {
	ownerAgentID = strings.TrimSpace(ownerAgentID)
	if ownerAgentID == "" || len(a.ownedSubAgentMailboxes) == 0 {
		return false
	}
	queue := a.ownedSubAgentMailboxes[ownerAgentID]
	if len(queue) == 0 {
		delete(a.ownedSubAgentMailboxes, ownerAgentID)
		return false
	}
	remaining := queue[:0]
	progressed := false
	for _, msg := range queue {
		if a.routeOwnedSubAgentMailbox(msg) {
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
	return progressed
}

func (a *MainAgent) enqueueSubAgentMailbox(msg SubAgentMailboxMessage) {
	a.prepareSubAgentMailboxMessage(&msg)
	if a.routeOwnedSubAgentMailbox(msg) {
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
		a.subAgentInbox.progress[msg.AgentID] = msg
	default:
		if msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent {
			a.subAgentInbox.urgent = append(a.subAgentInbox.urgent, msg)
		} else {
			a.subAgentInbox.normal = append(a.subAgentInbox.normal, msg)
		}
	}
	a.refreshSubAgentInboxSummary()
}

func shouldPersistMailboxArtifact(msg SubAgentMailboxMessage) bool {
	if len(msg.ArtifactRelPaths) > 0 {
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

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if strings.TrimSpace(item) == value {
			return items
		}
	}
	return append(items, value)
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
	a.enqueueSubAgentMailbox(*msg)
	if msg.Kind != SubAgentMailboxKindProgress {
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
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "waiting_primary", Message: msg.Summary})
		a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "warn", AgentID: msg.AgentID})
	case SubAgentMailboxKindRiskAlert:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "waiting_primary", Message: msg.Summary})
		a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "error", AgentID: msg.AgentID})
	case SubAgentMailboxKindDirectionChange:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "waiting_primary", Message: msg.Summary})
		a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "warn", AgentID: msg.AgentID})
	}
}

func (a *MainAgent) persistSubAgentMailboxMessage(msg SubAgentMailboxMessage) {
	sessionDir := strings.TrimSpace(a.sessionDir)
	if sessionDir == "" {
		return
	}
	dir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "mailbox.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(msg)
}

func (a *MainAgent) dequeueNextSubAgentMailbox() *SubAgentMailboxMessage {
	if len(a.subAgentInbox.urgent) > 0 {
		msg := a.subAgentInbox.urgent[0]
		a.subAgentInbox.urgent = a.subAgentInbox.urgent[1:]
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	if len(a.subAgentInbox.normal) > 0 {
		msg := a.subAgentInbox.normal[0]
		a.subAgentInbox.normal = a.subAgentInbox.normal[1:]
		a.refreshSubAgentInboxSummary()
		return &msg
	}
	return nil
}

func (a *MainAgent) stageNextSubAgentMailboxBatch() bool {
	msg := a.dequeueNextSubAgentMailbox()
	if msg == nil {
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
			if next.Kind != SubAgentMailboxKindCompleted {
				if next.Priority == SubAgentMailboxPriorityInterrupt || next.Priority == SubAgentMailboxPriorityUrgent {
					a.subAgentInbox.urgent = append([]SubAgentMailboxMessage{*next}, a.subAgentInbox.urgent...)
				} else {
					a.subAgentInbox.normal = append([]SubAgentMailboxMessage{*next}, a.subAgentInbox.normal...)
				}
				a.refreshSubAgentInboxSummary()
				break
			}
			pending = append(pending, next)
		}
	}
	a.pendingSubAgentMailboxes = pending
	a.activeSubAgentMailboxes = append([]*SubAgentMailboxMessage(nil), pending...)
	a.activeSubAgentMailbox = msg
	a.activeSubAgentMailboxAck = true
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
			a.subAgentInbox.progress[msg.AgentID] = *msg
		default:
			if msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent {
				a.subAgentInbox.urgent = append([]SubAgentMailboxMessage{*msg}, a.subAgentInbox.urgent...)
			} else {
				a.subAgentInbox.normal = append([]SubAgentMailboxMessage{*msg}, a.subAgentInbox.normal...)
			}
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
		if len(msg.Completion.BlockersRemaining) > 0 {
			b.WriteString("\n- blockers_remaining_deprecated: ")
			b.WriteString(strings.Join(msg.Completion.BlockersRemaining, ", "))
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
	if len(msg.ArtifactIDs) > 0 {
		b.WriteString("\n- artifact_ids: ")
		b.WriteString(strings.Join(msg.ArtifactIDs, ", "))
	}
	if len(msg.ArtifactRelPaths) > 0 {
		b.WriteString("\n- artifact_rel_paths: ")
		b.WriteString(strings.Join(msg.ArtifactRelPaths, ", "))
	}
	if strings.TrimSpace(msg.ArtifactType) != "" {
		b.WriteString("\n- artifact_type: ")
		b.WriteString(msg.ArtifactType)
	}
	if msg.RequiresAck {
		b.WriteString("\n- requires_ack: true")
	}
	return b.String()
}
