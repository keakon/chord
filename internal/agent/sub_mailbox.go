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
	// SubAgentMailboxKindBackgroundResult carries a finished background job's
	// result to its owner's transcript. It is not a SubAgent lifecycle
	// message: it has no sender agent or task (AgentID and TaskID stay empty),
	// so it moves no task record; its row exists purely so the result is
	// durable and delivered at a request boundary like any other mailbox
	// message, instead of living only in memory until then.
	SubAgentMailboxKindBackgroundResult SubAgentMailboxKind = "background_result"
)

type SubAgentMailboxPriority string

const (
	SubAgentMailboxPriorityNotify    SubAgentMailboxPriority = "notify"
	SubAgentMailboxPriorityUrgent    SubAgentMailboxPriority = "urgent"
	SubAgentMailboxPriorityInterrupt SubAgentMailboxPriority = "interrupt"
)

type AgentMessageType string

const (
	AgentMessageTypeProgress AgentMessageType = "progress"
	AgentMessageTypeNotice   AgentMessageType = "notice"
	AgentMessageTypeRequest  AgentMessageType = "request"
	AgentMessageTypeResponse AgentMessageType = "response"
)

type AgentMessageDurability string

const (
	AgentMessageDurabilityBestEffort AgentMessageDurability = "best_effort"
	AgentMessageDurabilityRequired   AgentMessageDurability = "required"
)

const maxAgentMessagePayloadBytes = 32 * 1024

// Mailbox ack outcomes written to the durable ack log, plus the reply kind
// the main agent records when it consumed a mailbox message in a main turn.
const (
	mailboxAckOutcomeConsumed  = "consumed"
	mailboxAckOutcomeRetryable = "retryable"
	mailboxReplyKindMainTurn   = "main_turn"
)

type ArtifactRef = tools.ArtifactRef

type CompletionEnvelope struct {
	Summary                   string              `json:"summary,omitempty"`
	FilesChanged              []string            `json:"files_changed,omitempty"`
	ReportedFilesChanged      []string            `json:"reported_files_changed,omitempty"`
	ActualFilesChanged        []string            `json:"actual_files_changed,omitempty"`
	FileAttributionIncomplete bool                `json:"file_attribution_incomplete,omitempty"`
	RemainingLimitations      []string            `json:"remaining_limitations,omitempty"`
	KnownRisks                []string            `json:"known_risks,omitempty"`
	FollowUpRecommended       []string            `json:"follow_up_recommended,omitempty"`
	Artifacts                 []tools.ArtifactRef `json:"artifacts,omitempty"`
	ResultType                string              `json:"result_type,omitempty"`
	Result                    json.RawMessage     `json:"result,omitempty"`
	ResultRef                 *tools.ResultRef    `json:"result_ref,omitempty"`
}

type SubAgentMailboxMessage struct {
	MessageID      string                  `json:"message_id"`
	AgentID        string                  `json:"agent_id"`
	TaskID         string                  `json:"task_id"`
	Attempt        uint64                  `json:"attempt,omitempty"`
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
	MessageType    AgentMessageType        `json:"message_type,omitempty"`
	Subtype        string                  `json:"subtype,omitempty"`
	SourceTaskID   string                  `json:"source_task_id,omitempty"`
	SourceAttempt  uint64                  `json:"source_attempt,omitempty"`
	TargetTaskID   string                  `json:"target_task_id,omitempty"`
	TargetAttempt  uint64                  `json:"target_attempt,omitempty"`
	CorrelationID  string                  `json:"correlation_id,omitempty"`
	MessagePayload json.RawMessage         `json:"message_payload,omitempty"`
	ArtifactRefs   []tools.ArtifactRef     `json:"artifact_refs,omitempty"`
	Durability     AgentMessageDurability  `json:"durability,omitempty"`
	// ReportOnly marks a mailbox row whose kind/subtype are user-supplied
	// display text (a worker's notify), not a trusted lifecycle signal. The
	// kind is preserved for card badges, retention, and restore replay, but it
	// must never drive task-record state transitions (see
	// syncTaskRecordFromMailbox): only rows produced by genuine lifecycle
	// events (completion, failure, escalation) may flip a task to terminal or
	// waiting state. Persisted so a replay keeps the distinction.
	ReportOnly     bool `json:"report_only,omitempty"`
	persistPending bool `json:"-"`
	// rollbackAppendOffset records where the last persist of this message
	// appended its mailbox.jsonl line (-1 when none or unknown), so a guarded
	// settlement that decides the message must not survive can roll the line
	// back (see rollbackSubAgentMailboxMessage). Transient bookkeeping only:
	// it is never serialized and never copied onto another message.
	rollbackAppendOffset int64
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
	// progressQueue is the in-memory FIFO for progress messages waiting for the
	// next main-agent request. progressPending is the durable-log fallback for
	// records that do not fit the memory budget.
	progressQueue        []SubAgentMailboxMessage
	progressPending      []string
	progressPendingAgent map[string]string
	spoolUrgent          []string
	spoolNormal          []string
	spoolIndex           map[string]mailboxSpoolLocation
	spoolIndexReady      bool
	// spoolWriteGen counts every completed mailbox.jsonl mutation (persist
	// appends and rollback truncations). It is bumped under
	// subAgentMailboxIDsMu right after the file write, so an index rebuild
	// that snapshots it before reading the log can detect at publish time
	// whether a write landed while it was reading — without stat'ing the
	// file under the lock (see indexSpooledMailbox).
	spoolWriteGen uint64
	memoryBytes   int
}

type mailboxSpoolLocation struct {
	offset int64
	length int64
}

func newSubAgentInbox() subAgentInbox {
	return subAgentInbox{
		progress:             make(map[string]SubAgentMailboxMessage),
		progressPendingAgent: make(map[string]string),
		spoolIndex:           make(map[string]mailboxSpoolLocation),
	}
}

// resetSubAgentMailboxRuntime drops the entire in-memory mailbox pipeline at a
// session boundary (a /new, a fork, a plan-execution switch, or a restore):
// the main-inbox queues (urgent/normal/progress), the durable-spool id
// queues, the per-owner queues, the staged/active batch, the idempotency and
// consumed sets, and the mailbox memory budget. Without it the replaced
// session's leftover mailbox messages would be staged into the next session's
// first idle drain and delivered to the new session's model — and then
// replayed again from the replaced session's own mailbox log when that
// session is resumed, so both sessions would see the same message once each.
// Durable mailbox rows and acks are never touched: the replaced session
// replays its unconsumed log when it is resumed, and the new session starts
// with its own empty log.
func (a *MainAgent) resetSubAgentMailboxRuntime() {
	a.subAgentMailboxIDsMu.Lock()
	a.subAgentInbox = newSubAgentInbox()
	a.ownedSubAgentMailboxes = nil
	a.ownedMailboxSpool = nil
	a.pendingSubAgentMailboxes = nil
	a.activeSubAgentMailboxes = nil
	a.activeSubAgentMailbox = nil
	a.activeSubAgentMailboxAck = false
	a.subAgentMailboxIDs = make(map[string]struct{})
	a.subAgentMailboxConsumed = make(map[string]struct{})
	a.subAgentMailboxIDsMu.Unlock()
	a.refreshSubAgentInboxSummary()
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

// mailboxMemoryCount reads the owned owner-queue maps, so it must be called
// with subAgentMailboxIDsMu held (its callers in the store/replace/enqueue
// paths all do).
func (a *MainAgent) mailboxMemoryCount() int {
	count := len(a.subAgentInbox.urgent) + len(a.subAgentInbox.normal) + len(a.subAgentInbox.progressQueue)
	for _, queued := range a.ownedSubAgentMailboxes {
		count += len(queued)
	}
	return count
}

// releaseMailboxMemory decrements the mailbox memory accounting for a message
// that left an in-memory queue. It mutates the same counters the queue
// mutations guard, so it must be called with subAgentMailboxIDsMu held.
func (a *MainAgent) releaseMailboxMemory(msg SubAgentMailboxMessage) {
	a.subAgentInbox.memoryBytes -= mailboxMessageBytes(msg)
	if a.subAgentInbox.memoryBytes < 0 {
		a.subAgentInbox.memoryBytes = 0
	}
}

func (a *MainAgent) storeMailboxInMemory(msg SubAgentMailboxMessage, front bool) bool {
	// mailboxMemoryCount reads the owner-queue maps shared with TUI-facing
	// goroutines; see that helper's locking note.
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	return a.storeMailboxInMemoryLocked(msg, front)
}

// storeMailboxInMemoryLocked is the shared in-memory store body; callers must
// hold subAgentMailboxIDsMu (storeMailboxInMemory and the requeue path do).
func (a *MainAgent) storeMailboxInMemoryLocked(msg SubAgentMailboxMessage, front bool) bool {
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

// spoolMailboxMessage queues a message id in the durable spool for its
// priority class. It mutates the subAgentInbox spool queues shared with other
// goroutines, so it must be called with subAgentMailboxIDsMu held.
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
		return mailboxReplyKindMainTurn
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
		artifactID, artifactRelPath, _, _, _ := persistSubAgentArtifact(a.sessionDir, agentID, replyMessageID, artifactType, "MainAgent follow-up", replyBody)
		if artifactRelPath != "" {
			artifact = tools.ArtifactRef{ID: artifactID, RelPath: artifactRelPath, Path: artifactRelPath, Type: artifactType}
		}
	}
	return SubAgentMailboxAckRecord{
		MessageID:        messageID,
		Outcome:          mailboxAckOutcomeConsumed,
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
		Outcome:   mailboxAckOutcomeRetryable,
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
		Outcome:   mailboxAckOutcomeConsumed,
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
	if record.Outcome == mailboxAckOutcomeConsumed {
		a.subAgentMailboxConsumed[record.MessageID] = struct{}{}
	} else {
		delete(a.subAgentMailboxConsumed, record.MessageID)
	}
	a.subAgentMailboxIDsMu.Unlock()
	if record.Outcome == mailboxAckOutcomeConsumed {
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
		if ack, ok := acks[out[i].MessageID]; ok && ack.Outcome == mailboxAckOutcomeConsumed {
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
