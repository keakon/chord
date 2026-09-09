package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// mainInboxMailboxMessages collects every mailbox message currently queued in
// the main agent inbox (urgent, normal and progress snapshots).
func mainInboxMailboxMessages(a *MainAgent) []SubAgentMailboxMessage {
	if a == nil {
		return nil
	}
	var out []SubAgentMailboxMessage
	out = append(out, a.subAgentInbox.urgent...)
	out = append(out, a.subAgentInbox.normal...)
	for _, msg := range a.subAgentInbox.progress {
		out = append(out, msg)
	}
	return out
}

func mailboxQueuedIDs(a *MainAgent) map[string]struct{} {
	out := make(map[string]struct{})
	for _, msg := range mainInboxMailboxMessages(a) {
		if id := strings.TrimSpace(msg.MessageID); id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func mailboxRow(messageID, kind string) message.Message {
	return message.Message{
		Role:    message.RoleUser,
		Kind:    message.KindSubAgentMailbox,
		Content: "<system-reminder>\nSubAgent mailbox update for " + messageID + "\n</system-reminder>",
		Mailbox: &message.MailboxMetadata{
			MessageID:   messageID,
			AgentID:     "agent-1",
			TaskID:      "restored",
			Kind:        kind,
			MessageType: string(AgentMessageTypeRequest),
		},
	}
}

// persistCompactionMailboxRestoreSession writes a restorable session whose main
// transcript carries the given rows (mailbox delivery rows included) and whose
// mailbox log holds the given unconsumed messages — the crash-state shape of a
// process that died between request dispatch and the teardown ack.
func persistCompactionMailboxRestoreSession(t *testing.T, sessionDir string, rows []message.Message, mailboxMsgs []SubAgentMailboxMessage) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	for _, msg := range rows {
		if err := rm.PersistMessage("main", msg); err != nil {
			t.Fatalf("PersistMessage(main): %v", err)
		}
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		LastInputTokens:        1,
		LastTotalContextTokens: 2,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
	if len(mailboxMsgs) == 0 {
		return
	}
	f, err := os.Create(filepath.Join(sessionDir, "subagents", "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("Create(mailbox.jsonl): %v", err)
	}
	enc := json.NewEncoder(f)
	for _, msg := range mailboxMsgs {
		if err := enc.Encode(msg); err != nil {
			_ = f.Close()
			t.Fatalf("Encode(mailbox): %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(mailbox): %v", err)
	}
}

func decisionMailboxMessage(messageID, summary string) SubAgentMailboxMessage {
	return SubAgentMailboxMessage{
		MessageID:   messageID,
		AgentID:     "agent-1",
		TaskID:      "restored",
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     summary,
		Payload:     summary,
		RequiresAck: true,
		CreatedAt:   time.Now(),
	}
}

// compactionTestDraft builds a legacy (source-ref-less) compaction draft that
// replaces the transcript prefix [0, headSplit) with one checkpoint message.
func compactionTestDraft(a *MainAgent, sessionDir string, headSplit int) *compactionDraft {
	return &compactionDraft{
		NewMessages:        []message.Message{{Role: message.RoleUser, Content: "[Context Summary]\ncheckpoint summary", IsCompactionSummary: true}},
		HeadSplit:          headSplit,
		Index:              headSplit,
		AbsHistoryPath:     filepath.Join(sessionDir, "history-1.md"),
		AbsHistoryMetaPath: filepath.Join(sessionDir, "history-1.md.status.json"),
		SummaryMode:        compactionSummaryModeModelDriven,
		PlanID:             1,
		Target:             compactionTarget{sessionEpoch: a.sessionEpoch},
	}
}

func mustApplyCompactionDraft(t *testing.T, a *MainAgent, draft *compactionDraft) {
	t.Helper()
	if err := a.applyCompactionDraft(draft); err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
}

// TestCompactionAcksPresentedMailboxRowBeforeDroppingIt pins the presented
// half of the compaction mailbox settlement: a mailbox delivery row whose
// message was already acted on (a completed assistant output follows it) is
// durably acked before the compaction replace destroys the row, so a later
// restore neither replays it nor re-synthesizes it.
func TestCompactionAcksPresentedMailboxRowBeforeDroppingIt(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "compaction-presented-mailbox")
	const messageID = "agent-1-1"
	persistCompactionMailboxRestoreSession(t, sessionDir,
		[]message.Message{
			{Role: message.RoleUser, Content: "hello"},
			{Role: message.RoleAssistant, Content: "Working on it"},
			mailboxRow(messageID, string(SubAgentMailboxKindDecisionRequired)),
			{Role: message.RoleAssistant, Content: "Handling the mailbox update"},
		},
		[]SubAgentMailboxMessage{decisionMailboxMessage(messageID, "already delivered, ack never persisted")},
	)

	first := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := first.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	// Restore skips the message: its durable transcript row is delivery
	// evidence, and the teardown ack was never written.
	if queued := mailboxQueuedIDs(first); len(queued) != 0 {
		t.Fatalf("first restore queued %v, want none (transcript row already delivered it)", queued)
	}

	mustApplyCompactionDraft(t, first, compactionTestDraft(first, sessionDir, 3))

	// The row is gone, the message is acked, and the ack is durable.
	for _, msg := range first.ctxMgr.Snapshot() {
		if msg.Kind == message.KindSubAgentMailbox && msg.Mailbox != nil && strings.TrimSpace(msg.Mailbox.MessageID) == messageID {
			t.Fatalf("mailbox row for %q survived compaction", messageID)
		}
	}
	if !first.isSubAgentMailboxConsumed(messageID) {
		t.Fatal("presented mailbox message was not marked consumed")
	}
	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if ack, ok := acks[messageID]; !ok || ack.Outcome != mailboxAckOutcomeConsumed {
		t.Fatalf("durable acks for %q = %+v, want a consumed ack", messageID, acks[messageID])
	}

	// Crash-2 restore: the message is consumed, so neither the replay path
	// nor the terminal synthesis path may deliver it again.
	second := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := second.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("second RestoreSessionAtStartup: %v", err)
	}
	if queued := mailboxQueuedIDs(second); len(queued) != 0 {
		t.Fatalf("crash-2 restore queued %v, want none (message was acked before its row was dropped)", queued)
	}
}

// TestCompactionRequeuesUndeliveredMailboxAfterDroppingItsRow pins the
// unpresented half of the compaction mailbox settlement: a mailbox row with no
// completed assistant output after it (a crash leftover the model never saw)
// must not be acked. After the replace commits and destroys the row, the
// message is re-enqueued from the mailbox log so a later dispatch delivers it;
// if the process dies before that dispatch, the next restore still replays it
// from the unconsumed log record.
func TestCompactionRequeuesUndeliveredMailboxAfterDroppingItsRow(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "compaction-undelivered-mailbox")
	const messageID = "agent-1-2"
	persistCompactionMailboxRestoreSession(t, sessionDir,
		[]message.Message{
			{Role: message.RoleUser, Content: "hello"},
			{Role: message.RoleAssistant, Content: "Working on it"},
			mailboxRow(messageID, string(SubAgentMailboxKindDecisionRequired)),
		},
		[]SubAgentMailboxMessage{decisionMailboxMessage(messageID, "delivered at dispatch, interrupted before any model output")},
	)

	first := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := first.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	if queued := mailboxQueuedIDs(first); len(queued) != 0 {
		t.Fatalf("first restore queued %v, want none (transcript row already delivered it)", queued)
	}

	mustApplyCompactionDraft(t, first, compactionTestDraft(first, sessionDir, 3))

	// No ack was written: the model never saw the message, so consuming it
	// would destroy the notification for good.
	if first.isSubAgentMailboxConsumed(messageID) {
		t.Fatal("undelivered mailbox message must not be marked consumed")
	}
	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if _, ok := acks[messageID]; ok {
		t.Fatalf("undelivered mailbox message %q was acked: %+v", messageID, acks[messageID])
	}
	// The message was re-enqueued from its full-fidelity log record.
	queued := mainInboxMailboxMessages(first)
	if len(queued) != 1 || queued[0].MessageID != messageID {
		t.Fatalf("post-compaction inbox = %+v, want exactly %q re-enqueued", mailboxQueuedIDs(first), messageID)
	}
	if queued[0].Payload != "delivered at dispatch, interrupted before any model output" {
		t.Fatalf("re-enqueued message lost payload fidelity: %+v", queued[0])
	}

	// Crash-2 before the re-enqueued copy reaches a dispatch: the message is
	// still unconsumed and its row is gone, so restore replays it — the
	// compaction never lost the notification.
	second := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := second.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("second RestoreSessionAtStartup: %v", err)
	}
	requeued := mainInboxMailboxMessages(second)
	if len(requeued) != 1 || requeued[0].MessageID != messageID {
		t.Fatalf("crash-2 inbox = %+v, want exactly %q replayed from the log", mailboxQueuedIDs(second), messageID)
	}
}

// TestCompactionAbortsWhenMailboxAckWriteFails pins the abort contract of the
// presented branch: the ack is written before the replace commits, so an ack
// failure returns an error and leaves the transcript (and the mailbox row)
// untouched. Neither the notification nor the delivery evidence is lost.
func TestCompactionAbortsWhenMailboxAckWriteFails(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.requestBatches.reserve(a.sessionEpoch, 0)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "first request"})
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "first reply"})
	a.ctxMgr.Append(mailboxRow("agent-1-3", string(SubAgentMailboxKindDecisionRequired)))
	a.ctxMgr.Append(message.Message{Role: message.RoleAssistant, Content: "Handling the mailbox update"})

	// Occupy the ack log path with a directory so the durable ack cannot land.
	acksPath := filepath.Join(a.sessionDir, "subagents", "mailbox-acks.jsonl")
	if err := os.MkdirAll(acksPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(ack path as dir): %v", err)
	}

	err := a.applyCompactionDraft(compactionTestDraft(a, a.sessionDir, 3))
	if err == nil || !strings.Contains(err.Error(), "ack delivered mailbox agent-1-3") {
		t.Fatalf("applyCompactionDraft error = %v, want the mailbox ack failure to abort the apply", err)
	}

	// The transcript is untouched: no checkpoint at the head, the mailbox row
	// still present. A later apply retries the settlement from the same rows.
	snapshot := a.ctxMgr.Snapshot()
	if len(snapshot) != 4 || snapshot[0].Content != "first request" {
		t.Fatalf("transcript after failed apply = %d messages starting %q, want the original 4 messages", len(snapshot), snapshot[0].Content)
	}
	found := false
	for _, msg := range snapshot {
		if msg.Kind == message.KindSubAgentMailbox && msg.Mailbox != nil && strings.TrimSpace(msg.Mailbox.MessageID) == "agent-1-3" {
			found = true
		}
	}
	if !found {
		t.Fatal("mailbox row disappeared although the apply aborted")
	}
}

// TestCompactionAckedTerminalMailboxIsNeitherReplayedNorSynthesized pins the
// interaction with restore's terminal synthesis: a completed-task mailbox
// whose row compaction destroyed after a durable ack must not come back on the
// next restore — not through the replay path (it is consumed) and not through
// restoreSynthesizeUndeliveredTerminalMailboxes (the log record still covers
// the settlement).
func TestCompactionAckedTerminalMailboxIsNeitherReplayedNorSynthesized(t *testing.T) {
	const (
		taskID     = "adhoc-settled-completion"
		instanceID = "agent-settled-completion"
		messageID  = "agent-settled-completion-1"
	)
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "compaction-terminal-mailbox")
	now := time.Now()
	completion := &CompletionEnvelope{Summary: "investigation complete", FilesChanged: []string{"internal/agent/main_subagent.go"}}
	settlement := &TaskSettlement{
		TaskID:           taskID,
		Attempt:          1,
		TerminalRevision: 2,
		Outcome:          string(SubAgentStateCompleted),
		Summary:          "investigation complete",
		Completion:       completion,
		SettledAt:        now,
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	if err := appendTaskSettlement(sessionDir, settlement); err != nil {
		t.Fatalf("appendTaskSettlement: %v", err)
	}
	if err := persistDurableTaskRecords(sessionDir, map[string]*DurableTaskRecord{
		taskID: {
			TaskID:            taskID,
			AgentDefName:      "restorer",
			TaskDesc:          "Investigate issue",
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      durableTaskResumePolicy(SubAgentStateCompleted),
			LatestInstanceID:  instanceID,
			InstanceHistory:   []string{instanceID},
			LastSummary:       settlement.Summary,
			Attempt:           1,
			LifecycleRevision: settlement.TerminalRevision,
			LatestSettlement:  cloneTaskSettlement(settlement),
			LastCompletion:    normalizeCompletionEnvelope(completion),
			SettlementDurable: true,
			CreatedAt:         now,
			UpdatedAt:         now,
			RuntimeParked:     true,
		},
	}); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}
	mailboxMsgs := []SubAgentMailboxMessage{{
		MessageID:  messageID,
		AgentID:    instanceID,
		TaskID:     taskID,
		Attempt:    1,
		Kind:       SubAgentMailboxKindCompleted,
		Priority:   SubAgentMailboxPriorityUrgent,
		Summary:    "investigation complete",
		Payload:    "investigation complete",
		Completion: completion,
		CreatedAt:  now,
	}}
	persistCompactionMailboxRestoreSession(t, sessionDir,
		[]message.Message{
			{Role: message.RoleUser, Content: "hello"},
			{Role: message.RoleAssistant, Content: "Working on it"},
			mailboxRow(messageID, string(SubAgentMailboxKindCompleted)),
			{Role: message.RoleAssistant, Content: "Handling the completion"},
		},
		mailboxMsgs,
	)

	restoreAgent := func() *MainAgent {
		a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
		a.SetAgentConfigs(map[string]*config.AgentConfig{
			"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
		})
		a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
		if _, err := a.restoreSessionState(sessionDir); err != nil {
			t.Fatalf("restoreSessionState: %v", err)
		}
		return a
	}
	first := restoreAgent()
	// The log record covers the settlement and the row is durable, so the
	// first restore neither replays nor synthesizes the completion.
	if queued := mailboxQueuedIDs(first); len(queued) != 0 {
		t.Fatalf("first restore queued %v, want none (log coverage + durable row)", queued)
	}

	mustApplyCompactionDraft(t, first, compactionTestDraft(first, sessionDir, 3))

	acks, err := loadSubAgentMailboxAcks(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxAcks: %v", err)
	}
	if ack, ok := acks[messageID]; !ok || ack.Outcome != mailboxAckOutcomeConsumed {
		t.Fatalf("durable acks for %q = %+v, want a consumed ack", messageID, acks[messageID])
	}

	// Crash-2 restore: the consumed terminal message must not be delivered
	// again by either path.
	second := restoreAgent()
	if queued := mailboxQueuedIDs(second); len(queued) != 0 {
		t.Fatalf("crash-2 restore queued %v, want none (no replay, no synthesis)", queued)
	}
	onDisk, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	terminalOnDisk := 0
	for _, msg := range onDisk {
		if msg.TaskID == taskID && msg.Kind == SubAgentMailboxKindCompleted {
			terminalOnDisk++
		}
	}
	if terminalOnDisk != 1 {
		t.Fatalf("completed mailbox records on disk for %q = %d, want exactly the original one (no synthesized duplicate)", taskID, terminalOnDisk)
	}
}

// TestCompactionClosesRestoreSkipReplayCascade drives the original cascade
// scenario end to end: a crash leaves a presented and an unpresented mailbox
// message delivered-but-unacked; restore skips both (durable rows); compaction
// then destroys the rows — acking the presented message and re-enqueueing the
// unpresented one; a second crash and restore must not repeat the presented
// message while the unpresented one is still delivered exactly once.
func TestCompactionClosesRestoreSkipReplayCascade(t *testing.T) {
	const (
		presentedID   = "agent-1-4"
		undeliveredID = "agent-1-5"
	)
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "compaction-cascade-mailbox")
	persistCompactionMailboxRestoreSession(t, sessionDir,
		[]message.Message{
			{Role: message.RoleUser, Content: "hello"},
			{Role: message.RoleAssistant, Content: "Working on it"},
			mailboxRow(presentedID, string(SubAgentMailboxKindDecisionRequired)),
			{Role: message.RoleAssistant, Content: "Handling the mailbox update"},
			mailboxRow(undeliveredID, string(SubAgentMailboxKindDecisionRequired)),
		},
		[]SubAgentMailboxMessage{
			decisionMailboxMessage(presentedID, "already delivered and acted on"),
			decisionMailboxMessage(undeliveredID, "delivered but never shown"),
		},
	)

	// Crash-1 restore: both messages carry durable rows, so neither replays.
	first := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := first.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("first RestoreSessionAtStartup: %v", err)
	}
	if queued := mailboxQueuedIDs(first); len(queued) != 0 {
		t.Fatalf("crash-1 restore queued %v, want none", queued)
	}

	// Compaction drops every row: the presented message is acked, the
	// unpresented one is re-enqueued for a later dispatch.
	mustApplyCompactionDraft(t, first, compactionTestDraft(first, sessionDir, 5))
	if !first.isSubAgentMailboxConsumed(presentedID) {
		t.Fatal("presented mailbox message was not acked by the compaction")
	}
	queued := mailboxQueuedIDs(first)
	if len(queued) != 1 {
		t.Fatalf("post-compaction inbox = %v, want exactly %q", queued, undeliveredID)
	}
	if _, ok := queued[undeliveredID]; !ok {
		t.Fatalf("post-compaction inbox = %v, want the undelivered message re-enqueued", queued)
	}

	// Crash-2 restore: only the unpresented message may come back — the
	// presented one must never be delivered a second time.
	second := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := second.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("second RestoreSessionAtStartup: %v", err)
	}
	requeued := mainInboxMailboxMessages(second)
	if len(requeued) != 1 || requeued[0].MessageID != undeliveredID {
		t.Fatalf("crash-2 inbox = %+v, want exactly %q (no repeat of the presented %q)", mailboxQueuedIDs(second), undeliveredID, presentedID)
	}
}
