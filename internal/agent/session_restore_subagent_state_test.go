package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func mustMarshalCompletionEnvelopeForTest(t *testing.T, env *CompletionEnvelope) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadSessionUsesCompactedMailboxStateAndPreservesSequence(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "compacted-mailbox-state")
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	subagentsDir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	mailboxFile, err := os.Create(filepath.Join(subagentsDir, "mailbox.jsonl"))
	if err != nil {
		t.Fatalf("create mailbox log: %v", err)
	}
	ackFile, err := os.Create(filepath.Join(subagentsDir, "mailbox-acks.jsonl"))
	if err != nil {
		_ = mailboxFile.Close()
		t.Fatalf("create ack log: %v", err)
	}
	mailboxEncoder := json.NewEncoder(mailboxFile)
	ackEncoder := json.NewEncoder(ackFile)
	for i := range mailboxCompactionThreshold {
		id := fmt.Sprintf("worker-%d", i+1)
		if i == 0 {
			id = "worker-9999"
		}
		msg := SubAgentMailboxMessage{MessageID: id, AgentID: "worker", TaskID: "task", Kind: SubAgentMailboxKindCompleted}
		if err := mailboxEncoder.Encode(msg); err != nil {
			t.Fatalf("encode mailbox: %v", err)
		}
		if err := ackEncoder.Encode(SubAgentMailboxAckRecord{MessageID: id, Outcome: "consumed", AckedAt: time.Now()}); err != nil {
			t.Fatalf("encode ack: %v", err)
		}
	}
	if err := mailboxFile.Close(); err != nil {
		t.Fatalf("close mailbox log: %v", err)
	}
	if err := ackFile.Close(); err != nil {
		t.Fatalf("close ack log: %v", err)
	}

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	loaded, err := a.loadSessionState(sessionDir)
	if err != nil {
		t.Fatalf("loadSessionState: %v", err)
	}
	if len(loaded.MailboxMessages) != mailboxConsumedHistoryKeep {
		t.Fatalf("loaded mailbox messages = %d, want compacted %d", len(loaded.MailboxMessages), mailboxConsumedHistoryKeep)
	}
	for _, msg := range loaded.MailboxMessages {
		if msg.MessageID == "worker-9999" {
			t.Fatal("loaded mailbox state retained an entry removed by compaction")
		}
	}
	if loaded.MailboxSeqMax != 9999 {
		t.Fatalf("MailboxSeqMax = %d, want pre-compaction maximum 9999", loaded.MailboxSeqMax)
	}
}

func TestLoadSessionQuarantinesCorruptTaskSettlementJournal(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "corrupt-task-settlements")
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	record := &DurableTaskRecord{
		TaskID:            "task-complete",
		Attempt:           1,
		State:             string(SubAgentStateCompleted),
		LifecycleRevision: 2,
		LastSummary:       "done",
		UpdatedAt:         time.Now(),
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: record})
	if err := a.persistTaskRegistry(); err != nil {
		t.Fatalf("persistTaskRegistry: %v", err)
	}
	journalPath := taskSettlementJournalPath(sessionDir)
	if err := os.WriteFile(journalPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("write corrupt settlement journal: %v", err)
	}

	loaded, err := a.loadSessionState(sessionDir)
	if err != nil {
		t.Fatalf("loadSessionState: %v", err)
	}
	key := taskAttemptKey{TaskID: record.TaskID, Attempt: record.Attempt}
	if loaded.TaskSettlements[key] == nil {
		t.Fatalf("missing reconstructed settlement: %v", loaded.TaskSettlements)
	}
	if _, err := loadTaskSettlements(sessionDir); err != nil {
		t.Fatalf("replacement settlement journal remains corrupt: %v", err)
	}
	quarantined, err := filepath.Glob(journalPath + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob quarantined journals: %v", err)
	}
	if len(quarantined) != 1 {
		t.Fatalf("quarantined journals = %v, want one", quarantined)
	}
}

func TestLoadSessionPreservesValidSettlementPrefixBeforeCorruption(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "corrupt-settlement-prefix")
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume"}); err != nil {
		t.Fatal(err)
	}
	rm.Close()
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := appendTaskSettlement(sessionDir, &TaskSettlement{
		TaskID: "task-prefix", Attempt: 1, TerminalRevision: 2,
		Outcome: string(SubAgentStateCompleted), Summary: "completed before corruption", SettledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(taskSettlementJournalPath(sessionDir), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not-json\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.loadSessionState(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.TaskSettlements[taskAttemptKey{TaskID: "task-prefix", Attempt: 1}]; got == nil || got.Summary != "completed before corruption" {
		t.Fatalf("valid settlement prefix = %#v", got)
	}
	// The recovered prefix must be re-seeded into the fresh journal: repair
	// marks these settlements durable, so nothing re-appends them later, and
	// without the reseed a subsequent registry loss would drop them entirely.
	reloaded, err := loadTaskSettlements(sessionDir)
	if err != nil {
		t.Fatalf("replacement settlement journal remains corrupt: %v", err)
	}
	if got := reloaded[taskAttemptKey{TaskID: "task-prefix", Attempt: 1}]; got == nil || got.Summary != "completed before corruption" {
		t.Fatalf("re-seeded journal settlement = %#v, want recovered prefix on disk", got)
	}
}

func TestLoadSessionDegradesCorruptAgentRequestFile(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "corrupt-agent-requests")
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	path := agentRequestsPath(sessionDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatalf("write corrupt %s: %v", path, err)
	}

	loaded, err := a.loadSessionState(sessionDir)
	if err != nil {
		t.Fatalf("a corrupt coordination file must degrade the slice, not fail restore: %v", err)
	}
	if !loaded.AgentRequestsDegraded {
		t.Fatal("AgentRequestsDegraded flag not set for a corrupt agent-requests file")
	}
	if len(loaded.AgentRequests) != 0 {
		t.Fatalf("degraded coordination state = %v, want empty", loaded.AgentRequests)
	}
}

func TestRaiseDegradedSeqFloorDoesNotOverwriteConcurrentProgress(t *testing.T) {
	var seq atomic.Uint64
	seq.Store(1)
	const concurrentValue = 2_000_000_000
	seq.Store(concurrentValue)
	raiseDegradedSeqFloor(&seq, true)
	if got := seq.Load(); got != concurrentValue {
		t.Fatalf("degraded sequence floor overwrote newer sequence, got %d want %d", got, concurrentValue)
	}
}

func TestGuardDegradedAgentRequestSeqAvoidsIDReuse(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.guardDegradedAgentRequestSeq(false)
	if got := a.agentRequestSeq.Load(); got != 0 {
		t.Fatalf("a healthy restore must keep the loaded sequence, got %d", got)
	}
	// The restored transcript and mailbox may still reference corr-N IDs from
	// the dropped file; the wall-clock floor keeps new IDs disjoint so replies
	// to old requests cannot resolve against unrelated new ones.
	a.guardDegradedAgentRequestSeq(true)
	if got := a.agentRequestSeq.Load(); got < 1_700_000_000 {
		t.Fatalf("a degraded restore must raise the sequence floor, got %d", got)
	}
}

func TestRestoreLoadedSubAgentsPreservesCompletedTaskState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if a.recoveryManager() == nil {
		t.Fatal("expected recovery manager")
	}

	count := a.restoreLoadedSubAgents([]loadedSubAgentState{{
		InstanceID:   "worker-1",
		TaskID:       "adhoc-1",
		AgentDefName: "restorer",
		TaskDesc:     "Investigate issue",
		State:        SubAgentStateCompleted,
		LastSummary:  "done summary",
	}})
	if count != 1 {
		t.Fatalf("restoreLoadedSubAgents() = %d, want 1", count)
	}
	got := a.GetSubAgents()
	if len(got) != 1 {
		t.Fatalf("len(GetSubAgents()) = %d, want 1", len(got))
	}
	if got[0].State != string(SubAgentStateCompleted) {
		t.Fatalf("state = %q, want %q", got[0].State, SubAgentStateCompleted)
	}
	if got[0].LastSummary != "done summary" {
		t.Fatalf("LastSummary = %q, want done summary", got[0].LastSummary)
	}
}

func TestRestoredCancelledSubAgentContinueReactivatesWithoutAppendingMessage(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	provider := &shutdownBlockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(func() { close(provider.release) })
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
			Type: config.ProviderTypeMessages,
			Models: map[string]config.ModelConfig{
				"test-model": {Limit: config.ModelLimit{Context: 8192, Output: 1024}},
			},
		}, []string{"test-key"})
		return llm.NewClient(providerCfg, provider, "test-model", 1024, systemPrompt)
	})

	count := a.restoreLoadedSubAgents([]loadedSubAgentState{{
		InstanceID:   "restorer-3",
		TaskID:       "adhoc-3",
		AgentDefName: "restorer",
		TaskDesc:     "Investigate issue",
		State:        SubAgentStateCancelled,
		LastSummary:  "Cancelled before shutdown",
		Messages:     []message.Message{{Role: "user", Content: "Investigate issue"}},
		// The explicit continue revives this record through rehydration,
		// which refuses scope-less records.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}})
	if count != 1 {
		t.Fatalf("restoreLoadedSubAgents() = %d, want 1", count)
	}
	if sub := a.subAgentByID("restorer-3"); sub != nil {
		t.Fatal("restored worker should remain parked before explicit continue")
	}
	a.SwitchFocus("restorer-3")
	before := a.GetMessages()

	a.ContinueFromContext()
	sub := a.subAgentByTaskID("adhoc-3")
	if sub == nil {
		t.Fatal("expected explicit continue to rehydrate worker")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		sub.turnMu.Lock()
		turnStarted := sub.turn != nil
		sub.turnMu.Unlock()
		if turnStarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for restored SubAgent turn")
		}
		time.Sleep(time.Millisecond)
	}
	if got := sub.State(); got != SubAgentStateRunning {
		t.Fatalf("State() = %q, want %q", got, SubAgentStateRunning)
	}
	if !sub.semHeld {
		t.Fatal("restored SubAgent did not acquire a concurrency slot")
	}
	after := sub.GetMessages()
	if len(after) != len(before) {
		t.Fatalf("message count after ContinueFromContext() = %d, want unchanged %d", len(after), len(before))
	}
	for i := range before {
		if after[i].Role != before[i].Role || after[i].Content != before[i].Content {
			t.Fatalf("message %d changed after ContinueFromContext(): before=%+v after=%+v", i, before[i], after[i])
		}
	}
	record := a.taskRecordByTaskID(sub.taskID)
	if record == nil || record.State != string(SubAgentStateRunning) {
		t.Fatalf("task record after ContinueFromContext() = %#v, want running", record)
	}
	meta, err := loadSubAgentMeta(a.sessionDir, sub.instanceID)
	if err != nil {
		t.Fatalf("loadSubAgentMeta: %v", err)
	}
	if meta.State != string(SubAgentStateRunning) {
		t.Fatalf("meta.State = %q, want %q", meta.State, SubAgentStateRunning)
	}
}

func TestRestoreLoadedSubAgentsRestoresOwnerDepthAndPendingComplete(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:       "restorer",
			Mode:       "subagent",
			Models:     map[string][]string{"default": {"test/test-model"}},
			Delegation: config.DelegationConfig{MaxChildren: 2, MaxDepth: 2},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})

	count := a.restoreLoadedSubAgents([]loadedSubAgentState{{
		InstanceID:             "worker-1",
		TaskID:                 "adhoc-1",
		AgentDefName:           "restorer",
		TaskDesc:               "Investigate issue",
		OwnerAgentID:           "worker-parent",
		OwnerTaskID:            "adhoc-parent",
		Depth:                  2,
		State:                  SubAgentStateWaitingDescendant,
		LastSummary:            "waiting for child",
		PendingCompleteIntent:  true,
		PendingCompleteSummary: "final summary",
		PendingCompleteEnvelope: mustMarshalCompletionEnvelopeForTest(t, &CompletionEnvelope{
			Summary: "final summary",
			VerificationRecords: []VerificationRecord{{
				ToolCallID: "verify-restore",
				Command:    "go test ./internal/agent",
				Status:     "failed",
				Summary:    "exit 1",
			}},
		}),
		JoinToOwner: true,
	}})
	if count != 1 {
		t.Fatalf("restoreLoadedSubAgents() = %d, want 1", count)
	}
	restored := a.taskRecordByTaskID("adhoc-1")
	if restored == nil || !restored.RuntimeParked {
		t.Fatalf("task record = %#v, want parked restored task", restored)
	}
	if restored.OwnerAgentID != "worker-parent" {
		t.Fatalf("OwnerAgentID = %q, want worker-parent", restored.OwnerAgentID)
	}
	if restored.OwnerTaskID != "adhoc-parent" {
		t.Fatalf("OwnerTaskID = %q, want adhoc-parent", restored.OwnerTaskID)
	}
	if restored.Depth != 2 {
		t.Fatalf("Depth = %d, want 2", restored.Depth)
	}
	if restored.PendingCompletion == nil || restored.PendingCompletion.Summary != "final summary" {
		t.Fatalf("PendingCompletion = %#v, want summary %q", restored.PendingCompletion, "final summary")
	}
	records := restored.PendingCompletion.VerificationRecords
	if len(records) != 1 || records[0].ToolCallID != "verify-restore" || records[0].Command != "go test ./internal/agent" || records[0].Status != "failed" || records[0].Summary != "exit 1" {
		t.Fatalf("restored verification records = %#v", records)
	}
}

func TestRestoreLoadedSubAgentsKeepsOwnedMailboxQueuedUntilManualContinue(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})

	msg := SubAgentMailboxMessage{
		MessageID:    "worker-child-restore-1",
		AgentID:      "worker-child",
		TaskID:       "adhoc-child",
		OwnerAgentID: "worker-parent",
		OwnerTaskID:  "adhoc-parent",
		Kind:         SubAgentMailboxKindProgress,
		Priority:     SubAgentMailboxPriorityNotify,
		Summary:      "child progress during restore",
		Payload:      "child progress during restore",
	}
	a.enqueueOwnedSubAgentMailbox(msg)
	if got := len(a.ownedSubAgentMailboxes["worker-parent"]); got != 1 {
		t.Fatalf("len(ownedSubAgentMailboxes[worker-parent]) = %d, want 1", got)
	}

	count := a.restoreLoadedSubAgents([]loadedSubAgentState{{
		InstanceID:   "worker-parent",
		TaskID:       "adhoc-parent",
		AgentDefName: "restorer",
		TaskDesc:     "Investigate issue",
		State:        SubAgentStateRunning,
		LastSummary:  "restored parent",
		// Manual continue wakes the parked parent through rehydration, which
		// refuses scope-less records.
		ExpectedWriteScope: tools.WriteScope{PathPrefix: []string{"internal/agent"}},
	}})
	if count != 1 {
		t.Fatalf("restoreLoadedSubAgents() = %d, want 1", count)
	}

	if got := len(a.ownedSubAgentMailboxes["worker-parent"]); got != 1 {
		t.Fatalf("len(ownedSubAgentMailboxes[worker-parent]) after restore = %d, want 1", got)
	}
	if restored := a.subAgentByID("worker-parent"); restored != nil {
		t.Fatal("restored parent should remain parked before manual continue")
	}
	a.SwitchFocus("worker-parent")
	a.ContinueFromContext()

	deadline := time.Now().Add(2 * time.Second)

	var ackErr error
	var acks map[string]SubAgentMailboxAckRecord
	for {
		acks, ackErr = loadSubAgentMailboxAcks(a.sessionDir)
		if ackErr != nil {
			t.Fatalf("loadSubAgentMailboxAcks: %v", ackErr)
		}
		if ack, ok := acks[msg.MessageID]; ok && ack.Outcome == "consumed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ack for %s not recorded: %#v", msg.MessageID, acks[msg.MessageID])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRestoredMailboxEventWaitsForManualSubAgentContinue(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-parent")
	sub.instanceID = "worker-parent"
	sub.setState(SubAgentStateIdle, "restored parent")
	a.subs.mu.Lock()
	delete(a.subs.subAgents, "worker-1")
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()
	a.mailboxDeliveryPaused.Store(true)

	a.handleSubAgentMailboxEvent(Event{
		Type: EventSubAgentMailbox,
		Payload: &SubAgentMailboxMessage{
			MessageID:    "worker-child-restore-1",
			AgentID:      "worker-child",
			TaskID:       "adhoc-child",
			OwnerAgentID: sub.instanceID,
			OwnerTaskID:  sub.taskID,
			Kind:         SubAgentMailboxKindCompleted,
			Priority:     SubAgentMailboxPriorityUrgent,
			Summary:      "child completed after restore",
		},
	})

	if got := sub.State(); got != SubAgentStateIdle {
		t.Fatalf("mailbox event resumed restored parent, state = %q", got)
	}
	if got := len(a.ownedSubAgentMailboxes[sub.instanceID]); got != 1 {
		t.Fatalf("owned mailbox count before manual continue = %d, want 1", got)
	}
	a.SwitchFocus(sub.instanceID)
	a.ContinueFromContext()
	if a.mailboxDeliveryPaused.Load() {
		t.Fatal("manual SubAgent continue did not release mailbox delivery barrier")
	}
	if got := len(a.ownedSubAgentMailboxes[sub.instanceID]); got != 0 {
		t.Fatalf("owned mailbox count after manual continue = %d, want 0", got)
	}
}

func TestRestoreSessionAtStartupUsesSnapshotSubAgentState(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "snapshot-sub-state")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sessionDir): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	if err := rm.PersistMessage("agent-1", message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(agent-1): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		CreatedAt: time.Now(),
		ActiveAgents: []recovery.AgentSnapshot{{
			InstanceID:   "agent-1",
			TaskID:       "adhoc-9",
			AgentDefName: "restorer",
			TaskDesc:     "Investigate issue",
			State:        string(SubAgentStateCancelled),
			LastSummary:  "Cancelled by MainAgent",
		}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}

	subagents := a.GetSubAgents()
	if len(subagents) != 1 {
		t.Fatalf("len(GetSubAgents()) = %d, want restored worker visible", len(subagents))
	}
	if subagents[0].InstanceID != "agent-1" || subagents[0].State != string(SubAgentStateCancelled) {
		t.Fatalf("GetSubAgents()[0] = %+v, want agent-1 restored cancelled", subagents[0])
	}
	record := a.taskRecordByTaskID("adhoc-9")
	if record == nil {
		t.Fatal("expected durable task record for cancelled worker")
	}
	if record.State != string(SubAgentStateCancelled) {
		t.Fatalf("record.State = %q, want %q", record.State, SubAgentStateCancelled)
	}
	if record.LastSummary != "Cancelled by MainAgent" {
		t.Fatalf("record.LastSummary = %q, want %q", record.LastSummary, "Cancelled by MainAgent")
	}
}

// seedSettledSnapshotConflictSession lays out a session that crashed in the
// window where the terminal outcome is already durable (settlement journal
// settled, tasks.json record terminal) but the recovery snapshot still lists
// instanceID as a running SubAgent under taskID. The snapshot predates the
// terminal transition, so it must not be allowed to downgrade the settled
// record during restore. tasks.json is left for the caller so each test can
// choose whether the registry record mirrors the settlement (the durable shape
// commitTerminalTask persists) or lost that mirror.
func seedSettledSnapshotConflictSession(t *testing.T, taskID, instanceID string) (projectRoot, sessionDir string, settlement *TaskSettlement) {
	t.Helper()
	projectRoot = t.TempDir()
	sessionDir = testProjectSessionDir(t, projectRoot, "settled-snapshot-conflict")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	if err := rm.PersistMessage(instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(%s): %v", instanceID, err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		CreatedAt: time.Now(),
		ActiveAgents: []recovery.AgentSnapshot{{
			InstanceID:   instanceID,
			TaskID:       taskID,
			AgentDefName: "restorer",
			TaskDesc:     "Investigate issue",
			State:        string(SubAgentStateRunning),
			LastSummary:  "running the investigation",
		}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()
	settlement = &TaskSettlement{
		TaskID:           taskID,
		Attempt:          1,
		TerminalRevision: 2,
		Outcome:          string(SubAgentStateCompleted),
		Summary:          "investigation done",
		SettledAt:        time.Now(),
	}
	if err := appendTaskSettlement(sessionDir, settlement); err != nil {
		t.Fatalf("appendTaskSettlement: %v", err)
	}
	return projectRoot, sessionDir, settlement
}

// assertRestoredTaskStillCompleted checks the settled-completed invariant after
// a full restoreSessionState pass: the task record State, the registry on disk
// (restore repersists it), the mirrored settlement, and the restored agent's
// visible state must all stay completed even though the recovery snapshot said
// the instance was running.
func assertRestoredTaskStillCompleted(t *testing.T, a *MainAgent, sessionDir, taskID, instanceID string) {
	t.Helper()
	rec := a.taskRecordByTaskID(taskID)
	if rec == nil {
		t.Fatalf("task record for %q missing after restore", taskID)
	}
	if rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record State = %q after restore, want %q: a stale snapshot-derived state downgraded the settled completed record", rec.State, SubAgentStateCompleted)
	}
	if rec.LatestSettlement == nil || rec.LatestSettlement.Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("task record lost its completed settlement after restore: %#v", rec.LatestSettlement)
	}
	onDisk, err := loadDurableTaskRecords(sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := onDisk[taskID]; got == nil || got.State != string(SubAgentStateCompleted) {
		t.Fatalf("persisted task record after restore = %#v, want State completed", got)
	}
	found := false
	for _, info := range a.GetSubAgents() {
		if info.InstanceID != instanceID {
			continue
		}
		found = true
		if info.State != string(SubAgentStateCompleted) {
			t.Fatalf("restored agent %q visible state = %q, want %q", instanceID, info.State, SubAgentStateCompleted)
		}
	}
	if !found {
		t.Fatalf("restored agent %q not visible after restore", instanceID)
	}
}

// TestRestoreSessionDurableSettledRecordSurvivesStaleRunningSnapshot is the
// conflict case where tasks.json and the settlement journal both say completed
// while the recovery snapshot still lists the instance as running, and the
// tasks.json record already mirrors the durable settlement (the shape
// commitTerminalTask persists). Restore must keep the record completed; the
// snapshot only records which instances were once live and must not flip the
// terminal state back to the non-terminal state it derives from the stale
// snapshot.
func TestRestoreSessionDurableSettledRecordSurvivesStaleRunningSnapshot(t *testing.T) {
	const (
		taskID     = "adhoc-settled-mirror"
		instanceID = "agent-settled-mirror"
	)
	projectRoot, sessionDir, settlement := seedSettledSnapshotConflictSession(t, taskID, instanceID)
	record := &DurableTaskRecord{
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
		SettlementDurable: true,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := persistDurableTaskRecords(sessionDir, map[string]*DurableTaskRecord{taskID: record}); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	assertRestoredTaskStillCompleted(t, a, sessionDir, taskID, instanceID)
}

// TestRestoreSessionSettlementRepairSurvivesStaleRunningSnapshotRestore covers
// the conflict case where the registry write lost the settlement mirror (the
// crash landed between the journal append and the registry persist), so
// repairTaskRecordsFromSettlements is the step that re-establishes completed
// during restore. That repair result must survive the rest of the restore
// instead of being flipped back to the snapshot-derived state.
func TestRestoreSessionSettlementRepairSurvivesStaleRunningSnapshotRestore(t *testing.T) {
	const (
		taskID     = "adhoc-settled-repair"
		instanceID = "agent-settled-repair"
	)
	projectRoot, sessionDir, settlement := seedSettledSnapshotConflictSession(t, taskID, instanceID)
	record := &DurableTaskRecord{
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
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := persistDurableTaskRecords(sessionDir, map[string]*DurableTaskRecord{taskID: record}); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	assertRestoredTaskStillCompleted(t, a, sessionDir, taskID, instanceID)
}

func TestRestoreSessionRebuildsTaskIdentityFromSnapshot(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "snapshot-task-identity")
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	if err := rm.PersistMessage("agent-identity", message.Message{Role: "user", Content: "inspect identity"}); err != nil {
		t.Fatalf("PersistMessage(agent): %v", err)
	}
	wantScope := tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		CreatedAt: time.Now(),
		ActiveAgents: []recovery.AgentSnapshot{{
			InstanceID:         "agent-identity",
			TaskID:             "adhoc-identity",
			AgentDefName:       "restorer",
			TaskDesc:           "inspect identity",
			PlanTaskRef:        "plan-item-identity",
			SemanticTaskKey:    "inspect-identity",
			ExpectedWriteScope: wantScope,
			State:              string(SubAgentStateCompleted),
		}},
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {Name: "restorer", Mode: "subagent", Models: map[string][]string{"default": {"test/test-model"}}},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	record := a.taskRecordByTaskID("adhoc-identity")
	if record == nil {
		t.Fatal("expected durable task record rebuilt from snapshot")
	}
	if record.PlanTaskRef != "plan-item-identity" || record.SemanticTaskKey != "inspect-identity" {
		t.Fatalf("restored identity = (%q, %q), want snapshot values", record.PlanTaskRef, record.SemanticTaskKey)
	}
	if len(record.ExpectedWriteScope.PathPrefix) != 1 || record.ExpectedWriteScope.PathPrefix[0] != "internal/agent" {
		t.Fatalf("restored write scope = %#v, want %#v", record.ExpectedWriteScope, wantScope)
	}
}

func TestSubAgentMetaPersistsTaskIdentity(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-meta-identity")
	sub.planTaskRef = "plan-item-meta"
	sub.semanticTaskKey = "meta-identity"
	sub.writeScope = tools.WriteScope{Files: []string{"internal/agent/subagent_meta.go"}}
	a.persistSubAgentMeta(sub)

	meta, err := loadSubAgentMeta(a.sessionDir, sub.instanceID)
	if err != nil {
		t.Fatalf("loadSubAgentMeta: %v", err)
	}
	if meta == nil || meta.PlanTaskRef != sub.planTaskRef || meta.SemanticTaskKey != sub.semanticTaskKey {
		t.Fatalf("persisted meta = %#v, want task identity", meta)
	}
	if len(meta.ExpectedWriteScope.Files) != 1 || meta.ExpectedWriteScope.Files[0] != "internal/agent/subagent_meta.go" {
		t.Fatalf("persisted meta write scope = %#v, want original file scope", meta.ExpectedWriteScope)
	}
}

func TestCancelledSubAgentSnapshotRestoresParkedCancelledTask(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "cancelled-sub-state")
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "user", Content: "main work"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "worker task"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	a.saveRecoverySnapshot()

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	restored := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	restored.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {
			Name:   "worker",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	restored.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := restored.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState(): %v", err)
	}
	if restoredSub := restored.subAgentByTaskID("adhoc-7"); restoredSub != nil {
		t.Fatal("cancelled SubAgent should remain parked after restore")
	}
	rec := restored.taskRecordByTaskID("adhoc-7")
	if rec == nil || !rec.RuntimeParked || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("restored task record = %#v, want parked cancelled task", rec)
	}
}

func TestRestoreSessionAtStartupDoesNotReviveClosedWorkerFromTranscriptOnly(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "closed-sub-transcript")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(sessionDir): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	if err := rm.PersistMessage("agent-1", message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(agent-1): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{
		CreatedAt:    time.Now(),
		ActiveAgents: nil,
	}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	if got := a.GetSubAgents(); len(got) != 0 {
		t.Fatalf("len(GetSubAgents()) = %d, want 0 when snapshot has no active agents", len(got))
	}
}

func TestMailboxReplyChainPersistsAcrossResume(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "mailbox-reply-chain")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	a := newTestMainAgent(t, projectRoot)
	a.sessionDir = sessionDir
	a.installRecoveryManager(recovery.NewRecoveryManager(sessionDir))
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "user", Content: "resume worker conversation"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	a.ctxMgr.Append(message.Message{Role: "user", Content: "resume worker conversation"})
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	sub.agentDefName = "restorer"
	// The reply chain is replayed through a rehydrated worker after restore;
	// its durable record must carry a write boundary to be rehydratable.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.setState(SubAgentStateWaitingMain, "need decision")
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Investigate issue"})
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID:   "worker-1-1",
		AgentID:     sub.instanceID,
		TaskID:      sub.taskID,
		Kind:        SubAgentMailboxKindDecisionRequired,
		Priority:    SubAgentMailboxPriorityInterrupt,
		Summary:     "need decision",
		RequiresAck: true,
		CreatedAt:   time.Now(),
	})
	if _, err := a.NotifySubAgent(context.Background(), "adhoc-7", "approve approach", "reply"); err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	a.saveRecoverySnapshot()

	meta, err := loadSubAgentMeta(sessionDir, sub.instanceID)
	if err != nil {
		t.Fatalf("loadSubAgentMeta(before restore): %v", err)
	}
	if meta == nil || meta.LastReplyMessageID == "" {
		t.Fatal("expected subagent meta to persist last reply message id")
	}

	a2 := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a2.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a2.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a2.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}

	rec := a2.taskRecordByTaskID("adhoc-7")
	if rec == nil || !rec.RuntimeParked {
		t.Fatalf("restored task record = %#v, want parked task", rec)
	}
	restored, _, err := a2.rehydrateTask(rec)
	if err != nil {
		t.Fatalf("rehydrateTask: %v", err)
	}
	if restored == nil {
		t.Fatal("expected restored worker")
	}
	replyMessageID, replyToMailboxID, replyKind, replySummary := restored.LastReplyThread()
	if replyMessageID == "" {
		t.Fatal("expected restored worker to carry reply-chain head")
	}
	if replyToMailboxID != "worker-1-1" {
		t.Fatalf("replyToMailboxID = %q, want worker-1-1", replyToMailboxID)
	}
	if replyKind != "reply" {
		t.Fatalf("replyKind = %q, want reply", replyKind)
	}
	if replySummary != "approve approach" {
		t.Fatalf("replySummary = %q, want %q", replySummary, "approve approach")
	}

	a2.handleAgentNotify(Event{SourceID: restored.instanceID, Payload: tools.AgentNotifyPayload{Message: "continuing with approved plan", Kind: "progress"}})
	foundNotify := false
	for len(a2.outputCh) > 0 {
		if notify, ok := (<-a2.outputCh).(AgentNotifyEvent); ok {
			if notify.AgentID != restored.instanceID || notify.TaskID != restored.taskID || notify.ParentAgentID != "main" || notify.TargetAgentID != "main" || notify.Kind != "progress" || notify.Message != "continuing with approved plan" {
				t.Fatalf("notify event = %#v", notify)
			}
			foundNotify = true
		}
	}
	if !foundNotify {
		t.Fatal("expected AgentNotifyEvent")
	}
	var evt Event
	for {
		evt = <-a2.eventCh
		if evt.Type == EventSubAgentMailbox {
			break
		}
	}
	a2.handleSubAgentMailboxEvent(evt)
	mailboxMsgs, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	last := mailboxMsgs[len(mailboxMsgs)-1]
	if last.InReplyTo != replyMessageID {
		t.Fatalf("last.InReplyTo = %q, want %q", last.InReplyTo, replyMessageID)
	}
}

func TestRestoreSessionCompletedTaskCanRehydrateFollowUp(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "completed-task-rehydrate")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}

	a := newTestMainAgent(t, projectRoot)
	a.sessionDir = sessionDir
	a.installRecoveryManager(recovery.NewRecoveryManager(sessionDir))
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if err := a.recoveryManager().PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	a.ctxMgr.Append(message.Message{Role: "user", Content: "resume this session"})
	sub := newControllableTestSubAgent(t, a, "adhoc-21")
	sub.agentDefName = "restorer"
	sub.taskDesc = "Investigate issue"
	// handleAgentDone below persists this worker's durable record, which the
	// second agent revives by rehydrating after restore — a scope-less record
	// is refused outright, so carry the boundary a real task was admitted with.
	sub.writeScope = tools.WriteScope{PathPrefix: []string{"internal/agent"}}
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Investigate issue"})
	if err := a.recoveryManager().PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})
	records, err := loadDurableTaskRecords(sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if rec := records[sub.taskID]; rec == nil || rec.AgentDefName != "restorer" {
		t.Fatalf("completed durable task = %#v, want restorer agent definition", rec)
	}

	a2 := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a2.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a2.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a2.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	if got := a2.GetSubAgents(); len(got) != 1 || got[0].InstanceID != sub.instanceID || got[0].State != string(SubAgentStateCompleted) {
		t.Fatalf("GetSubAgents() = %#v, want one visible parked task before rehydrate", got)
	}
	if rec := a2.taskRecordByTaskID(sub.taskID); rec == nil || rec.AgentDefName != "restorer" {
		t.Fatalf("restored durable task = %#v, want restorer agent definition", rec)
	}

	handle, err := a2.NotifySubAgent(context.Background(), "adhoc-21", "follow up on edge cases", "follow_up")
	if err != nil {
		t.Fatalf("NotifySubAgent: %v", err)
	}
	if !handle.Rehydrated {
		t.Fatal("expected rehydrated handle after restore")
	}
	restored := a2.subAgentByTaskID("adhoc-21")
	if restored == nil {
		t.Fatal("expected rehydrated live worker after restore")
	}
	if restored.instanceID != handle.AgentID {
		t.Fatalf("restored.instanceID = %q, want %q", restored.instanceID, handle.AgentID)
	}
}

func TestSubAgentMetaJSONWritten(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newControllableTestSubAgent(t, a, "adhoc-11")
	sub.setState(SubAgentStateCompleted, "done")
	sub.setLastMailboxID("worker-1-5")
	sub.setReplyThread("worker-1-reply-6", "worker-1-5", "follow_up", "check follow-up")
	a.persistSubAgentMeta(sub)

	path := subAgentMetaPath(a.sessionDir, sub.instanceID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(meta): %v", err)
	}
	var meta subAgentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("Unmarshal(meta): %v", err)
	}
	if meta.TaskID != "adhoc-11" {
		t.Fatalf("meta.TaskID = %q, want adhoc-11", meta.TaskID)
	}
	if meta.LastReplyToMailboxID != "worker-1-5" {
		t.Fatalf("meta.LastReplyToMailboxID = %q, want worker-1-5", meta.LastReplyToMailboxID)
	}
}

func TestMailboxLongPayloadPersistsArtifact(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newControllableTestSubAgent(t, a, "adhoc-12")
	longPayload := "detail " + strings.Repeat("payload ", 80)
	a.enqueueSubAgentMailbox(SubAgentMailboxMessage{
		MessageID: "worker-1-20",
		AgentID:   sub.instanceID,
		TaskID:    sub.taskID,
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
		Summary:   "final report ready",
		Payload:   longPayload,
	})

	msgs, err := loadSubAgentMailboxMessages(a.sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	last := msgs[len(msgs)-1]
	if last.Completion == nil || len(last.Completion.Artifacts) == 0 {
		t.Fatal("expected mailbox artifact path to be persisted")
	}
	if _, err := os.Stat(filepath.Join(a.sessionDir, filepath.FromSlash(last.Completion.Artifacts[0].RelPath))); err != nil {
		t.Fatalf("artifact file missing: %v", err)
	}
}

func restoredCompletedMailboxesForTask(a *MainAgent, taskID string) []SubAgentMailboxMessage {
	var out []SubAgentMailboxMessage
	collect := func(msgs []SubAgentMailboxMessage) {
		for _, msg := range msgs {
			if msg.Kind == SubAgentMailboxKindCompleted && strings.TrimSpace(msg.TaskID) == taskID {
				out = append(out, msg)
			}
		}
	}
	collect(a.subAgentInbox.urgent)
	collect(a.subAgentInbox.normal)
	return out
}

// TestRestoreSessionSynthesizesCompletionForSettledTaskWithLostMailbox covers
// the restore-side closure of the "settled but never notified" crash window: a
// task whose terminal settlement and registry record are durable completed but
// whose completion mailbox never reached the mailbox log (crash landed between
// the terminal commit and the mailbox delivery; no LastMailboxID, no mailbox
// log entry). After restart the owner inbox must still receive exactly one
// completion. A sibling completed task whose completion mailbox IS in the log
// (the durable-write crash shape: message persisted, apply never ran) must not
// be synthesized a second time on top of the log replay, and a second restore
// of the same session must not notify again — both restores must converge on
// the same physical message ids.
func TestRestoreSessionSynthesizesCompletionForSettledTaskWithLostMailbox(t *testing.T) {
	const (
		lostTaskID     = "adhoc-lost-completion"
		lostInstanceID = "agent-lost-completion"
		notifiedTaskID = "adhoc-notified-completion"
		notifiedMsgID  = "agent-notified-completion-1"
	)
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "lost-completion-mailbox")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	now := time.Now()
	lostCompletion := &CompletionEnvelope{
		Summary:      "investigation complete",
		FilesChanged: []string{"internal/agent/main_subagent.go"},
	}
	lostSettlement := &TaskSettlement{
		TaskID:           lostTaskID,
		Attempt:          1,
		TerminalRevision: 2,
		Outcome:          string(SubAgentStateCompleted),
		Summary:          "investigation complete",
		Completion:       lostCompletion,
		SettledAt:        now,
	}
	if err := appendTaskSettlement(sessionDir, lostSettlement); err != nil {
		t.Fatalf("appendTaskSettlement: %v", err)
	}
	records := map[string]*DurableTaskRecord{
		lostTaskID: {
			TaskID:            lostTaskID,
			AgentDefName:      "restorer",
			TaskDesc:          "Investigate issue",
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      durableTaskResumePolicy(SubAgentStateCompleted),
			LatestInstanceID:  lostInstanceID,
			InstanceHistory:   []string{lostInstanceID},
			LastSummary:       lostSettlement.Summary,
			Attempt:           1,
			LifecycleRevision: lostSettlement.TerminalRevision,
			LatestSettlement:  cloneTaskSettlement(lostSettlement),
			LastCompletion:    normalizeCompletionEnvelope(lostCompletion),
			SettlementDurable: true,
			CreatedAt:         now,
			UpdatedAt:         now,
			RuntimeParked:     true,
		},
		notifiedTaskID: {
			TaskID:            notifiedTaskID,
			AgentDefName:      "restorer",
			TaskDesc:          "Investigate issue",
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      durableTaskResumePolicy(SubAgentStateCompleted),
			LatestInstanceID:  "agent-notified-completion",
			InstanceHistory:   []string{"agent-notified-completion"},
			LastSummary:       "reported complete",
			Attempt:           1,
			LifecycleRevision: 2,
			LatestSettlement: &TaskSettlement{
				TaskID: notifiedTaskID, Attempt: 1, TerminalRevision: 2,
				Outcome: string(SubAgentStateCompleted), Summary: "reported complete",
				Completion: &CompletionEnvelope{Summary: "reported complete"}, SettledAt: now,
			},
			LastCompletion:    &CompletionEnvelope{Summary: "reported complete"},
			SettlementDurable: true,
			CreatedAt:         now,
			UpdatedAt:         now,
			RuntimeParked:     true,
		},
	}
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}
	// The notified task's completion IS in the mailbox log (persisted, never
	// applied): restore replays it and must not synthesize a second one.
	mailboxFile, err := os.OpenFile(filepath.Join(sessionDir, "subagents", "mailbox.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open mailbox log: %v", err)
	}
	if err := json.NewEncoder(mailboxFile).Encode(SubAgentMailboxMessage{
		MessageID:  notifiedMsgID,
		AgentID:    "agent-notified-completion",
		TaskID:     notifiedTaskID,
		Attempt:    1,
		Kind:       SubAgentMailboxKindCompleted,
		Priority:   SubAgentMailboxPriorityUrgent,
		Summary:    "reported complete",
		Payload:    "reported complete",
		Completion: &CompletionEnvelope{Summary: "reported complete"},
		CreatedAt:  now,
	}); err != nil {
		_ = mailboxFile.Close()
		t.Fatalf("encode mailbox message: %v", err)
	}
	if err := mailboxFile.Close(); err != nil {
		t.Fatalf("close mailbox log: %v", err)
	}

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
	second := restoreAgent()

	var firstLostID string
	for _, a := range []*MainAgent{first, second} {
		lost := restoredCompletedMailboxesForTask(a, lostTaskID)
		if len(lost) != 1 {
			t.Fatalf("restored owner inbox completed mailboxes for %q = %d, want exactly one synthesized completion", lostTaskID, len(lost))
		}
		if lost[0].Summary != "investigation complete" || lost[0].Completion == nil || lost[0].Completion.Summary != "investigation complete" {
			t.Fatalf("synthesized completion = %#v, want investigation complete", lost[0])
		}
		if strings.TrimSpace(lost[0].OwnerAgentID) != "" || lost[0].AgentID != lostInstanceID {
			t.Fatalf("synthesized completion owner/source = (%q, %q), want main-owned from %q", lost[0].OwnerAgentID, lost[0].AgentID, lostInstanceID)
		}
		if firstLostID == "" {
			firstLostID = lost[0].MessageID
		} else if lost[0].MessageID != firstLostID {
			t.Fatalf("second restore re-synthesized a different completion message: first=%q second=%q", firstLostID, lost[0].MessageID)
		}
		notified := restoredCompletedMailboxesForTask(a, notifiedTaskID)
		if len(notified) != 1 || notified[0].MessageID != notifiedMsgID {
			t.Fatalf("restored owner inbox completed mailboxes for %q = %#v, want only the original log message %q (no re-synthesis)", notifiedTaskID, notified, notifiedMsgID)
		}
	}
	// The synthesized completion is itself persisted, which is what keeps the
	// second restore from notifying again.
	onDisk, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var synthesizedOnDisk *SubAgentMailboxMessage
	for i := range onDisk {
		if onDisk[i].TaskID == lostTaskID && onDisk[i].Kind == SubAgentMailboxKindCompleted {
			synthesizedOnDisk = &onDisk[i]
		}
	}
	if synthesizedOnDisk == nil || synthesizedOnDisk.MessageID != firstLostID {
		t.Fatalf("synthesized completion not persisted for future restores: on-disk=%#v firstLostID=%q", onDisk, firstLostID)
	}
}

func restoredRiskAlertsForTask(a *MainAgent, taskID string) []SubAgentMailboxMessage {
	var out []SubAgentMailboxMessage
	collect := func(msgs []SubAgentMailboxMessage) {
		for _, msg := range msgs {
			if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID {
				out = append(out, msg)
			}
		}
	}
	collect(a.subAgentInbox.urgent)
	collect(a.subAgentInbox.normal)
	return out
}

// restoreFailedRiskAlertFixture writes a durable terminal-Failed task record
// and its settlement with no risk_alert in the mailbox log — the crash shape
// this extension fixes for handleAgentError (the failure mailbox persist is
// deferred past the terminal commit in older builds, so the notification can
// be lost between the commit and the delivery).
func restoreFailedRiskAlertFixture(t *testing.T, projectRoot, sessionDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	now := time.Now()
	failureSummary := "SubAgent failed (exit): worker panicked while patching"
	failedSettlement := &TaskSettlement{
		TaskID:           "adhoc-lost-failure",
		Attempt:          1,
		TerminalRevision: 2,
		Outcome:          string(SubAgentStateFailed),
		Summary:          failureSummary,
		SettledAt:        now,
	}
	if err := appendTaskSettlement(sessionDir, failedSettlement); err != nil {
		t.Fatalf("appendTaskSettlement: %v", err)
	}
	records := map[string]*DurableTaskRecord{
		"adhoc-lost-failure": {
			TaskID:            "adhoc-lost-failure",
			AgentDefName:      "restorer",
			TaskDesc:          "Investigate issue",
			State:             string(SubAgentStateFailed),
			ResumePolicy:      durableTaskResumePolicy(SubAgentStateFailed),
			LatestInstanceID:  "agent-lost-failure",
			InstanceHistory:   []string{"agent-lost-failure"},
			LastSummary:       failureSummary,
			Attempt:           1,
			LifecycleRevision: failedSettlement.TerminalRevision,
			LatestSettlement:  cloneTaskSettlement(failedSettlement),
			SettlementDurable: true,
			CreatedAt:         now,
			UpdatedAt:         now,
			RuntimeParked:     true,
		},
	}
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}
}

// TestRestoreSessionSynthesizesRiskAlertForFailedTaskWithLostMailbox covers the
// restore-side closure of the failure/expiry crash window: a task whose
// durable record is terminal Failed but whose risk_alert mailbox never reached
// the mailbox log (handleAgentError committed the failure after only queueing
// the mailbox). After restart the owner inbox must still receive exactly one
// risk_alert, and a second restore of the same session must not notify again —
// the synthesized message is itself persisted, so both restores converge on
// the same physical message id.
func TestRestoreSessionSynthesizesRiskAlertForFailedTaskWithLostMailbox(t *testing.T) {
	const taskID = "adhoc-lost-failure"
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "lost-failure-mailbox")
	restoreFailedRiskAlertFixture(t, projectRoot, sessionDir)

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
	second := restoreAgent()

	var firstAlertID string
	for _, a := range []*MainAgent{first, second} {
		alerts := restoredRiskAlertsForTask(a, taskID)
		if len(alerts) != 1 {
			t.Fatalf("restored owner inbox risk_alert mailboxes for %q = %d, want exactly one synthesized alert", taskID, len(alerts))
		}
		alert := alerts[0]
		if alert.Summary != "SubAgent failed (exit): worker panicked while patching" {
			t.Fatalf("synthesized risk_alert summary = %q, want the failure summary", alert.Summary)
		}
		if !strings.Contains(alert.Payload, "terminated without completion") {
			t.Fatalf("synthesized risk_alert payload = %q, want the failure guidance", alert.Payload)
		}
		if alert.AgentID != "agent-lost-failure" || alert.Attempt != 1 {
			t.Fatalf("synthesized risk_alert source = (%q, attempt %d), want (agent-lost-failure, attempt 1)", alert.AgentID, alert.Attempt)
		}
		if firstAlertID == "" {
			firstAlertID = alert.MessageID
		} else if alert.MessageID != firstAlertID {
			t.Fatalf("second restore re-synthesized a different alert message: first=%q second=%q", firstAlertID, alert.MessageID)
		}
	}
	onDisk, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var synthesizedOnDisk *SubAgentMailboxMessage
	for i := range onDisk {
		if onDisk[i].TaskID == taskID && onDisk[i].Kind == SubAgentMailboxKindRiskAlert {
			synthesizedOnDisk = &onDisk[i]
		}
	}
	if synthesizedOnDisk == nil || synthesizedOnDisk.MessageID != firstAlertID {
		t.Fatalf("synthesized risk_alert not persisted for future restores: on-disk=%#v firstAlertID=%q", onDisk, firstAlertID)
	}
}

// TestRestoreSessionSynthesizesExpiryRiskAlertButNotForUserStop covers the
// expiry branch of the crash-window synthesis and its discriminator: a task
// cancelled by the WaitingMain expiry sweep loses its risk_alert to the window
// between the terminal Cancelled commit and the mailbox delivery and must be
// re-notified after restart, while a task cancelled by a user stop (which never
// queues a risk_alert before its terminal commit) must not gain a notification
// from restore.
func TestRestoreSessionSynthesizesExpiryRiskAlertButNotForUserStop(t *testing.T) {
	const (
		expiredTaskID = "adhoc-expired"
		stoppedTaskID = "adhoc-stopped"
	)
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "lost-expiry-mailbox")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	now := time.Now()
	expiredReason := "expired waiting for main reply (no reply within 1h0m0s)"
	stoppedReason := "stopped by user"
	fixtures := []struct {
		taskID     string
		instanceID string
		reason     string
		closed     string
	}{
		{taskID: expiredTaskID, instanceID: "agent-expired", reason: expiredReason, closed: expiredReason},
		{taskID: stoppedTaskID, instanceID: "agent-stopped", reason: stoppedReason, closed: stoppedReason},
	}
	records := make(map[string]*DurableTaskRecord, len(fixtures))
	for _, fx := range fixtures {
		settlement := &TaskSettlement{
			TaskID:           fx.taskID,
			Attempt:          1,
			TerminalRevision: 2,
			Outcome:          string(SubAgentStateCancelled),
			Summary:          fx.reason,
			SettledAt:        now,
		}
		if err := appendTaskSettlement(sessionDir, settlement); err != nil {
			t.Fatalf("appendTaskSettlement(%s): %v", fx.taskID, err)
		}
		records[fx.taskID] = &DurableTaskRecord{
			TaskID:            fx.taskID,
			AgentDefName:      "restorer",
			TaskDesc:          "Investigate issue",
			State:             string(SubAgentStateCancelled),
			ResumePolicy:      durableTaskResumePolicy(SubAgentStateCancelled),
			LatestInstanceID:  fx.instanceID,
			InstanceHistory:   []string{fx.instanceID},
			LastSummary:       fx.reason,
			Attempt:           1,
			LifecycleRevision: settlement.TerminalRevision,
			LatestSettlement:  cloneTaskSettlement(settlement),
			SettlementDurable: true,
			ClosedReason:      fx.closed,
			CreatedAt:         now,
			UpdatedAt:         now,
			RuntimeParked:     true,
		}
	}
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}

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
	second := restoreAgent()

	for _, a := range []*MainAgent{first, second} {
		expired := restoredRiskAlertsForTask(a, expiredTaskID)
		if len(expired) != 1 {
			t.Fatalf("restored owner inbox risk_alert mailboxes for expired task %q = %d, want exactly one", expiredTaskID, len(expired))
		}
		if alert := expired[0]; !strings.Contains(alert.Summary, "expired waiting for main reply") || alert.AgentID != "agent-expired" {
			t.Fatalf("synthesized expiry risk_alert = %#v, want the expiry reason from agent-expired", alert)
		}
		if stopped := restoredRiskAlertsForTask(a, stoppedTaskID); len(stopped) != 0 {
			t.Fatalf("restored owner inbox risk_alert mailboxes for user-stopped task %q = %d, want none (user stops never notify)", stoppedTaskID, len(stopped))
		}
	}
}

// TestRestoreSessionSynthesisSurvivesStaleLastMailboxIDFromEarlierAttempt pins
// the gate refinement for re-synthesizing a lost completion on attempt>=2: a
// completed attempt-2 task whose attempt-2 completion mailbox never reached the
// log (persist failure followed by a crash) still carries the attempt-1
// completion's MessageID as LastMailboxID, because rehydrate retains the
// previous attempt's last mailbox. The coarse "LastMailboxID empty means never
// notified" gate would skip this task and lose the completion forever; the
// covering-proof gate must recognize that the retained attempt-1 message does
// not cover attempt-2 and synthesize the lost completion.
func TestRestoreSessionSynthesisSurvivesStaleLastMailboxIDFromEarlierAttempt(t *testing.T) {
	const (
		taskID          = "adhoc-stale-attempt"
		attempt1MsgID   = "agent-stale-completion-1"
		attempt2AgentID = "agent-stale-attempt2"
	)
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "stale-attempt-completion")
	if err := os.MkdirAll(filepath.Join(sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	rm.Close()

	now := time.Now()
	attempt2Completion := &CompletionEnvelope{Summary: "second attempt complete"}
	attempt2Settlement := &TaskSettlement{
		TaskID:           taskID,
		Attempt:          2,
		TerminalRevision: 4,
		Outcome:          string(SubAgentStateCompleted),
		Summary:          "second attempt complete",
		Completion:       attempt2Completion,
		SettledAt:        now,
	}
	if err := appendTaskSettlement(sessionDir, attempt2Settlement); err != nil {
		t.Fatalf("appendTaskSettlement: %v", err)
	}
	records := map[string]*DurableTaskRecord{
		taskID: {
			TaskID:            taskID,
			AgentDefName:      "restorer",
			TaskDesc:          "Investigate issue",
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      durableTaskResumePolicy(SubAgentStateCompleted),
			LatestInstanceID:  attempt2AgentID,
			InstanceHistory:   []string{"agent-stale-attempt1", attempt2AgentID},
			LastSummary:       "second attempt complete",
			LastMailboxID:     attempt1MsgID, // retained from attempt 1 by rehydrate
			Attempt:           2,
			LifecycleRevision: attempt2Settlement.TerminalRevision,
			LatestSettlement:  cloneTaskSettlement(attempt2Settlement),
			LastCompletion:    normalizeCompletionEnvelope(attempt2Completion),
			SettlementDurable: true,
			CreatedAt:         now,
			UpdatedAt:         now,
			RuntimeParked:     true,
		},
	}
	if err := persistDurableTaskRecords(sessionDir, records); err != nil {
		t.Fatalf("persistDurableTaskRecords: %v", err)
	}
	// The attempt-1 completion is durably delivered (consumed) but no longer
	// queued anywhere; it stays in the log with a consumed ack so restore does
	// not replay it. The attempt-2 completion is absent entirely — the crash
	// shape under test.
	mailboxFile, err := os.OpenFile(filepath.Join(sessionDir, "subagents", "mailbox.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open mailbox log: %v", err)
	}
	if err := json.NewEncoder(mailboxFile).Encode(SubAgentMailboxMessage{
		MessageID:  attempt1MsgID,
		AgentID:    "agent-stale-attempt1",
		TaskID:     taskID,
		Attempt:    1,
		Kind:       SubAgentMailboxKindCompleted,
		Priority:   SubAgentMailboxPriorityUrgent,
		Summary:    "first attempt complete",
		Payload:    "first attempt complete",
		Completion: &CompletionEnvelope{Summary: "first attempt complete"},
		CreatedAt:  now,
	}); err != nil {
		_ = mailboxFile.Close()
		t.Fatalf("encode attempt-1 completion: %v", err)
	}
	if err := mailboxFile.Close(); err != nil {
		t.Fatalf("close mailbox log: %v", err)
	}
	ackFile, err := os.OpenFile(filepath.Join(sessionDir, "subagents", "mailbox-acks.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open ack log: %v", err)
	}
	if err := json.NewEncoder(ackFile).Encode(SubAgentMailboxAckRecord{MessageID: attempt1MsgID, Outcome: "consumed", AckedAt: now}); err != nil {
		_ = ackFile.Close()
		t.Fatalf("encode consumed ack: %v", err)
	}
	if err := ackFile.Close(); err != nil {
		t.Fatalf("close ack log: %v", err)
	}

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
	second := restoreAgent()

	for _, a := range []*MainAgent{first, second} {
		completed := restoredCompletedMailboxesForTask(a, taskID)
		if len(completed) != 1 {
			t.Fatalf("restored owner inbox completed mailboxes for %q = %d, want exactly one synthesized attempt-2 completion (stale LastMailboxID must not block synthesis)", taskID, len(completed))
		}
		if msg := completed[0]; msg.Attempt != 2 || msg.Summary != "second attempt complete" || msg.MessageID == attempt1MsgID {
			t.Fatalf("synthesized completion = %#v, want the attempt-2 completion, not the retained attempt-1 message %q", msg, attempt1MsgID)
		}
	}
	onDisk, err := loadSubAgentMailboxMessages(sessionDir)
	if err != nil {
		t.Fatalf("loadSubAgentMailboxMessages: %v", err)
	}
	var synthesizedOnDisk *SubAgentMailboxMessage
	for i := range onDisk {
		if onDisk[i].TaskID == taskID && onDisk[i].Attempt == 2 && onDisk[i].Kind == SubAgentMailboxKindCompleted {
			synthesizedOnDisk = &onDisk[i]
		}
	}
	if synthesizedOnDisk == nil {
		t.Fatalf("attempt-2 completion not persisted for future restores: on-disk=%#v", onDisk)
	}
}
