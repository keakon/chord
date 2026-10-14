package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/tools"
)

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
