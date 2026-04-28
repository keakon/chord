package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SubAgentMailboxKind string

const (
	SubAgentMailboxKindProgress         SubAgentMailboxKind = "progress"
	SubAgentMailboxKindCompleted        SubAgentMailboxKind = "completed"
	SubAgentMailboxKindBlocked          SubAgentMailboxKind = "blocked"
	SubAgentMailboxKindDecisionRequired SubAgentMailboxKind = "decision_required"
	SubAgentMailboxKindRiskAlert        SubAgentMailboxKind = "risk_alert"
	SubAgentMailboxKindDirectionChange  SubAgentMailboxKind = "direction_change_request"
)

type SubAgentMailboxPriority string

const (
	SubAgentMailboxPriorityNotify    SubAgentMailboxPriority = "notify"
	SubAgentMailboxPriorityUrgent    SubAgentMailboxPriority = "urgent"
	SubAgentMailboxPriorityInterrupt SubAgentMailboxPriority = "interrupt"
)

type CompletionEnvelope struct {
	Summary             string   `json:"summary,omitempty"`
	FilesChanged        []string `json:"files_changed,omitempty"`
	VerificationRun     []string `json:"verification_run,omitempty"`
	BlockersRemaining   []string `json:"blockers_remaining,omitempty"`
	FollowUpRecommended []string `json:"follow_up_recommended,omitempty"`
}

type SubAgentMailboxMessage struct {
	MessageID        string                  `json:"message_id"`
	AgentID          string                  `json:"agent_id"`
	TaskID           string                  `json:"task_id"`
	OwnerAgentID     string                  `json:"owner_agent_id,omitempty"`
	OwnerTaskID      string                  `json:"owner_task_id,omitempty"`
	InReplyTo        string                  `json:"in_reply_to,omitempty"`
	Kind             SubAgentMailboxKind     `json:"kind"`
	Priority         SubAgentMailboxPriority `json:"priority"`
	Summary          string                  `json:"summary"`
	Payload          string                  `json:"payload,omitempty"`
	Completion       *CompletionEnvelope     `json:"completion,omitempty"`
	ArtifactIDs      []string                `json:"artifact_ids,omitempty"`
	ArtifactRelPaths []string                `json:"artifact_rel_paths,omitempty"`
	ArtifactType     string                  `json:"artifact_type,omitempty"`
	RequiresAck      bool                    `json:"requires_ack,omitempty"`
	Consumed         bool                    `json:"consumed,omitempty"`
	CreatedAt        time.Time               `json:"created_at"`
}

func firstNonNilCompletion(values ...*CompletionEnvelope) *CompletionEnvelope {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

type SubAgentMailboxAckRecord struct {
	MessageID        string    `json:"message_id"`
	Outcome          string    `json:"outcome"`
	TurnID           uint64    `json:"turn_id,omitempty"`
	InReplyTo        string    `json:"in_reply_to,omitempty"`
	ReplyMessageID   string    `json:"reply_message_id,omitempty"`
	ReplyToMailboxID string    `json:"reply_to_mailbox_id,omitempty"`
	ReplySummary     string    `json:"reply_summary,omitempty"`
	ReplyKind        string    `json:"reply_kind,omitempty"`
	ArtifactID       string    `json:"artifact_id,omitempty"`
	ArtifactRelPath  string    `json:"artifact_rel_path,omitempty"`
	ArtifactType     string    `json:"artifact_type,omitempty"`
	AckedAt          time.Time `json:"acked_at"`
}

type subAgentInbox struct {
	urgent   []SubAgentMailboxMessage
	normal   []SubAgentMailboxMessage
	progress map[string]SubAgentMailboxMessage
}

func newSubAgentInbox() subAgentInbox {
	return subAgentInbox{progress: make(map[string]SubAgentMailboxMessage)}
}

func (a *MainAgent) nextSubAgentMailboxMessageID(agentID string) string {
	n := a.subAgentMailboxSeq.Add(1)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "subagent"
	}
	return fmt.Sprintf("%s-%d", agentID, n)
}

func (a *MainAgent) nextSubAgentReplyMessageID(agentID string) string {
	n := a.subAgentMailboxSeq.Add(1)
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "subagent"
	}
	return fmt.Sprintf("%s-reply-%d", agentID, n)
}

func normalizeReplyKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "main_turn"
	}
	return kind
}

func (a *MainAgent) markSubAgentMailboxConsumedWithReply(agentID, messageID string, turnID uint64, replySummary, replyKind string) (replyMessageID, artifactRelPath, artifactType string) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return "", "", ""
	}
	replyKind = normalizeReplyKind(replyKind)
	replySummary = truncateMailboxReplySummary(replySummary)
	replyMessageID = a.nextSubAgentReplyMessageID(agentID)
	artifactID := ""
	artifactRelPath = ""
	artifactType = ""
	if len(strings.TrimSpace(replySummary)) > replyArtifactPayloadThreshold {
		artifactType = "execution_spec"
		artifactID, artifactRelPath, _ = persistSubAgentArtifact(a.sessionDir, agentID, replyMessageID, artifactType, "MainAgent follow-up", replySummary)
	}
	record := SubAgentMailboxAckRecord{
		MessageID:        messageID,
		Outcome:          "consumed",
		TurnID:           turnID,
		InReplyTo:        messageID,
		ReplyMessageID:   replyMessageID,
		ReplyToMailboxID: messageID,
		ReplySummary:     replySummary,
		ReplyKind:        replyKind,
		ArtifactID:       artifactID,
		ArtifactRelPath:  artifactRelPath,
		ArtifactType:     artifactType,
		AckedAt:          time.Now(),
	}
	a.appendSubAgentMailboxAck(record)
	if sub := a.subAgentByID(agentID); sub != nil {
		sub.setReplyThread(replyMessageID, messageID, replyKind, replySummary)
		if artifactRelPath != "" {
			sub.setLastArtifact(artifactID, artifactRelPath, artifactType)
		}
		a.persistSubAgentMeta(sub)
	}
	return replyMessageID, artifactRelPath, artifactType
}

func (a *MainAgent) markSubAgentMailboxRetryable(messageID string, turnID uint64) {
	a.appendSubAgentMailboxAck(SubAgentMailboxAckRecord{
		MessageID: messageID,
		Outcome:   "retryable",
		TurnID:    turnID,
		AckedAt:   time.Now(),
	})
}

func (a *MainAgent) markSubAgentMailboxConsumed(messageID string) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return
	}
	a.appendSubAgentMailboxAck(SubAgentMailboxAckRecord{
		MessageID: messageID,
		Outcome:   "consumed",
		AckedAt:   time.Now(),
	})
}

func (a *MainAgent) appendSubAgentMailboxAck(record SubAgentMailboxAckRecord) {
	sessionDir := strings.TrimSpace(a.sessionDir)
	record.MessageID = strings.TrimSpace(record.MessageID)
	if sessionDir == "" || record.MessageID == "" {
		return
	}
	dir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, "mailbox-acks.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(record)
}

func loadSubAgentMailboxAcks(sessionPath string) (map[string]SubAgentMailboxAckRecord, error) {
	path := filepath.Join(sessionPath, "subagents", "mailbox-acks.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	out := make(map[string]SubAgentMailboxAckRecord)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ack SubAgentMailboxAckRecord
		if err := json.Unmarshal([]byte(line), &ack); err != nil {
			return nil, err
		}
		if strings.TrimSpace(ack.MessageID) == "" {
			continue
		}
		out[ack.MessageID] = ack
	}
	return out, nil
}

func applyMailboxAcks(msgs []SubAgentMailboxMessage, acks map[string]SubAgentMailboxAckRecord) []SubAgentMailboxMessage {
	if len(msgs) == 0 || len(acks) == 0 {
		return msgs
	}
	out := append([]SubAgentMailboxMessage(nil), msgs...)
	for i := range out {
		if ack, ok := acks[out[i].MessageID]; ok && ack.Outcome == "consumed" {
			out[i].Consumed = true
		}
	}
	return out
}

func truncateMailboxReplySummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	const maxLen = 240
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}
