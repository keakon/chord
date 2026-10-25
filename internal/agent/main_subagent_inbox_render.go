package agent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

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
	case SubAgentMailboxKindNotice:
		a.emitToTUI(AgentStatusEvent{AgentID: msg.AgentID, Status: "running", Message: msg.Summary})
		if msg.Priority == SubAgentMailboxPriorityInterrupt {
			a.emitToTUI(ToastEvent{Message: msg.Summary, Level: "warn", AgentID: msg.AgentID})
		}
	}
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
			if label := artifactRefLabel(ref); label != "" {
				b.WriteString("\n  - ")
				b.WriteString(label)
			}
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
			// Normalization merges completion artifacts into ArtifactRefs, which
			// the section above already rendered. Keep only refs this mailbox has
			// not shown yet so the owner reads each artifact once.
			listed := make(map[string]struct{}, len(msg.ArtifactRefs))
			for _, ref := range msg.ArtifactRefs {
				if label := artifactRefLabel(ref); label != "" {
					listed[label] = struct{}{}
				}
			}
			refs := make([]string, 0, len(msg.Completion.Artifacts))
			for _, ref := range msg.Completion.Artifacts {
				label := artifactRefLabel(ref)
				if label == "" {
					continue
				}
				if _, ok := listed[label]; ok {
					continue
				}
				listed[label] = struct{}{}
				refs = append(refs, label)
			}
			if len(refs) > 0 {
				b.WriteString("\n- artifact_refs: ")
				b.WriteString(strings.Join(refs, ", "))
			}
		}
		if wt := msg.Completion.Worktree; wt != nil {
			// The owner must be able to act on the result: say which checkout
			// holds it, what it branched from, and how much changed.
			if label := strings.TrimSpace(wt.Name); label != "" {
				b.WriteString("\n- worktree: ")
				b.WriteString(label)
				if branch := strings.TrimSpace(wt.Branch); branch != "" {
					b.WriteString(" (branch ")
					b.WriteString(branch)
					b.WriteString(")")
				}
			}
			if path := strings.TrimSpace(wt.Path); path != "" {
				b.WriteString("\n- worktree_path: ")
				b.WriteString(path)
			}
			if base := strings.TrimSpace(wt.Base); base != "" {
				b.WriteString("\n- worktree_base: ")
				b.WriteString(base)
			}
			if wt.Generation > 0 {
				b.WriteString("\n- worktree_generation: ")
				b.WriteString(strconv.FormatUint(wt.Generation, 10))
			}
			if stat := strings.TrimSpace(wt.DiffStat); stat != "" {
				b.WriteString("\n- worktree_diff_stat:\n  ")
				b.WriteString(strings.ReplaceAll(stat, "\n", "\n  "))
			} else if derr := strings.TrimSpace(wt.DiffError); derr != "" {
				b.WriteString("\n- worktree_diff_stat: unknown (")
				b.WriteString(derr)
				b.WriteString(")")
			}
		}
	}
	if msg.RequiresAck {
		b.WriteString("\n- requires_ack: true")
	}
	return b.String()
}

func artifactRefLabel(ref tools.ArtifactRef) string {
	ref = tools.NormalizeArtifactRef(ref)
	if ref.RelPath != "" {
		return ref.RelPath
	}
	return ref.ID
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
