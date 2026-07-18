package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keakon/chord/internal/privatefs"
	"github.com/keakon/chord/internal/tools"
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

type ArtifactRef = tools.ArtifactRef

type CompletionEnvelope struct {
	Summary                   string              `json:"summary,omitempty"`
	FilesChanged              []string            `json:"files_changed,omitempty"`
	ReportedFilesChanged      []string            `json:"reported_files_changed,omitempty"`
	ActualFilesChanged        []string            `json:"actual_files_changed,omitempty"`
	FileAttributionIncomplete bool                `json:"file_attribution_incomplete,omitempty"`
	VerificationRun           []string            `json:"verification_run,omitempty"`
	RemainingLimitations      []string            `json:"remaining_limitations,omitempty"`
	KnownRisks                []string            `json:"known_risks,omitempty"`
	FollowUpRecommended       []string            `json:"follow_up_recommended,omitempty"`
	Artifacts                 []tools.ArtifactRef `json:"artifacts,omitempty"`
}

type SubAgentMailboxMessage struct {
	MessageID      string                  `json:"message_id"`
	AgentID        string                  `json:"agent_id"`
	TaskID         string                  `json:"task_id"`
	OwnerAgentID   string                  `json:"owner_agent_id,omitempty"`
	OwnerTaskID    string                  `json:"owner_task_id,omitempty"`
	InReplyTo      string                  `json:"in_reply_to,omitempty"`
	Kind           SubAgentMailboxKind     `json:"kind"`
	Priority       SubAgentMailboxPriority `json:"priority"`
	Summary        string                  `json:"summary"`
	Payload        string                  `json:"payload,omitempty"`
	Completion     *CompletionEnvelope     `json:"completion,omitempty"`
	RequiresAck    bool                    `json:"requires_ack,omitempty"`
	Consumed       bool                    `json:"consumed,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
	persistPending bool                    `json:"-"`
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
	urgent          []SubAgentMailboxMessage
	normal          []SubAgentMailboxMessage
	progress        map[string]SubAgentMailboxMessage
	spoolUrgent     []string
	spoolNormal     []string
	spoolIndex      map[string]mailboxSpoolLocation
	spoolIndexReady bool
	memoryBytes     int
}

type mailboxSpoolLocation struct {
	offset int64
	length int64
}

func newSubAgentInbox() subAgentInbox {
	return subAgentInbox{
		progress:   make(map[string]SubAgentMailboxMessage),
		spoolIndex: make(map[string]mailboxSpoolLocation),
	}
}

func mailboxMessageBytes(msg SubAgentMailboxMessage) int {
	data, err := json.Marshal(msg)
	if err == nil {
		return len(data)
	}
	return len(msg.Summary) + len(msg.Payload)
}

func (a *MainAgent) mailboxMemoryLimits() (int, int) {
	cfg := effectiveOrchestrationConfig(a.globalConfig, a.projectConfig)
	return cfg.EffectiveMailboxMemoryMessages(), cfg.EffectiveMailboxMemoryBytes()
}

func (a *MainAgent) mailboxMemoryCount() int {
	count := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal) + len(a.subAgentInbox.progress)
	for _, queued := range a.ownedSubAgentMailboxes {
		count += len(queued)
	}
	return count
}

func (a *MainAgent) releaseMailboxMemory(msg SubAgentMailboxMessage) {
	a.subAgentInbox.memoryBytes -= mailboxMessageBytes(msg)
	if a.subAgentInbox.memoryBytes < 0 {
		a.subAgentInbox.memoryBytes = 0
	}
}

func (a *MainAgent) storeMailboxInMemory(msg SubAgentMailboxMessage, front bool) bool {
	urgent := msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent
	if !front {
		// Preserve FIFO within each priority class: while older messages sit in
		// the durable spool, new arrivals must queue behind them there instead
		// of jumping ahead through the in-memory queue.
		spool := a.subAgentInbox.spoolNormal
		if urgent {
			spool = a.subAgentInbox.spoolUrgent
		}
		if len(spool) > 0 {
			return false
		}
	}
	messageLimit, byteLimit := a.mailboxMemoryLimits()
	size := mailboxMessageBytes(msg)
	if a.mailboxMemoryCount() >= messageLimit || a.subAgentInbox.memoryBytes+size > byteLimit {
		return false
	}
	if urgent {
		if front {
			a.subAgentInbox.urgent = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.urgent...)
		} else {
			a.subAgentInbox.urgent = append(a.subAgentInbox.urgent, msg)
		}
	} else if front {
		a.subAgentInbox.normal = append([]SubAgentMailboxMessage{msg}, a.subAgentInbox.normal...)
	} else {
		a.subAgentInbox.normal = append(a.subAgentInbox.normal, msg)
	}
	a.subAgentInbox.memoryBytes += size
	return true
}

func (a *MainAgent) spoolMailboxMessage(msg SubAgentMailboxMessage, front bool) {
	id := strings.TrimSpace(msg.MessageID)
	if id == "" {
		return
	}
	queue := &a.subAgentInbox.spoolNormal
	if msg.Priority == SubAgentMailboxPriorityInterrupt || msg.Priority == SubAgentMailboxPriorityUrgent {
		queue = &a.subAgentInbox.spoolUrgent
	}
	if front {
		*queue = append([]string{id}, (*queue)...)
	} else {
		*queue = append(*queue, id)
	}
	a.subAgentInbox.spoolIndexReady = false
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

func (a *MainAgent) prepareSubAgentMailboxReply(agentID, messageID string, turnID uint64, replyBody, replyKind string) (SubAgentMailboxAckRecord, tools.ArtifactRef) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return SubAgentMailboxAckRecord{}, tools.ArtifactRef{}
	}
	replyKind = normalizeReplyKind(replyKind)
	replySummary := truncateMailboxReplySummary(replyBody)
	replyMessageID := a.nextSubAgentReplyMessageID(agentID)
	artifact := tools.ArtifactRef{}
	if len(strings.TrimSpace(replyBody)) > replyArtifactPayloadThreshold {
		artifactType := "execution_spec"
		artifactID, artifactRelPath, _ := persistSubAgentArtifact(a.sessionDir, agentID, replyMessageID, artifactType, "MainAgent follow-up", replyBody)
		if artifactRelPath != "" {
			artifact = tools.ArtifactRef{ID: artifactID, RelPath: artifactRelPath, Path: artifactRelPath, Type: artifactType}
		}
	}
	return SubAgentMailboxAckRecord{
		MessageID:        messageID,
		Outcome:          "consumed",
		TurnID:           turnID,
		InReplyTo:        messageID,
		ReplyMessageID:   replyMessageID,
		ReplyToMailboxID: messageID,
		ReplySummary:     replySummary,
		ReplyKind:        replyKind,
		ArtifactID:       artifact.ID,
		ArtifactRelPath:  artifact.RelPath,
		ArtifactType:     artifact.Type,
		AckedAt:          time.Now(),
	}, artifact
}

func (a *MainAgent) applySubAgentMailboxReply(agentID string, record SubAgentMailboxAckRecord, artifact tools.ArtifactRef) {
	if sub := a.subAgentByID(agentID); sub != nil {
		sub.setReplyThread(record.ReplyMessageID, record.ReplyToMailboxID, record.ReplyKind, record.ReplySummary)
		if artifact.RelPath != "" {
			sub.setLastArtifact(artifact)
		}
		a.persistSubAgentMeta(sub)
	}
}

func (a *MainAgent) markSubAgentMailboxConsumedWithReply(agentID, messageID string, turnID uint64, replySummary, replyKind string) (replyMessageID, artifactRelPath, artifactType string, err error) {
	record, artifact := a.prepareSubAgentMailboxReply(agentID, messageID, turnID, replySummary, replyKind)
	if record.MessageID == "" {
		return "", "", "", nil
	}
	if err := a.appendSubAgentMailboxAck(record); err != nil {
		return "", "", "", err
	}
	a.applySubAgentMailboxReply(agentID, record, artifact)
	return record.ReplyMessageID, artifact.RelPath, artifact.Type, nil
}

func (a *MainAgent) markSubAgentMailboxRetryable(messageID string, turnID uint64) error {
	return a.appendSubAgentMailboxAck(SubAgentMailboxAckRecord{
		MessageID: messageID,
		Outcome:   "retryable",
		TurnID:    turnID,
		AckedAt:   time.Now(),
	})
}

func (a *MainAgent) markSubAgentMailboxConsumed(messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return nil
	}
	return a.appendSubAgentMailboxAck(SubAgentMailboxAckRecord{
		MessageID: messageID,
		Outcome:   "consumed",
		AckedAt:   time.Now(),
	})
}

func (a *MainAgent) appendSubAgentMailboxAck(record SubAgentMailboxAckRecord) error {
	sessionDir := strings.TrimSpace(a.sessionDir)
	record.MessageID = strings.TrimSpace(record.MessageID)
	if sessionDir == "" || record.MessageID == "" {
		return nil
	}
	dir := filepath.Join(sessionDir, "subagents")
	path := filepath.Join(dir, "mailbox-acks.jsonl")
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("open mailbox ack log: %w", err)
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(record); err != nil {
		_ = f.Close()
		return fmt.Errorf("append mailbox ack: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close mailbox ack log: %w", err)
	}
	a.subAgentMailboxIDsMu.Lock()
	if a.subAgentMailboxConsumed == nil {
		a.subAgentMailboxConsumed = make(map[string]struct{})
	}
	if record.Outcome == "consumed" {
		a.subAgentMailboxConsumed[record.MessageID] = struct{}{}
	} else {
		delete(a.subAgentMailboxConsumed, record.MessageID)
	}
	a.subAgentMailboxIDsMu.Unlock()
	if record.Outcome == "consumed" {
		a.orchestrationMetrics.recordMailboxAck(record.MessageID)
	}
	return nil
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

func (a *MainAgent) isSubAgentMailboxConsumed(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if a == nil || messageID == "" {
		return false
	}
	a.subAgentMailboxIDsMu.Lock()
	_, ok := a.subAgentMailboxConsumed[messageID]
	a.subAgentMailboxIDsMu.Unlock()
	return ok
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
