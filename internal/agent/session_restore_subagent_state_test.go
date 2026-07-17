package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

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
	for i := 0; i < mailboxCompactionThreshold; i++ {
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
	if a.recovery == nil {
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
		JoinToOwner:            true,
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
	if err := a.recovery.PersistMessage("main", message.Message{Role: "user", Content: "main work"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	if err := a.recovery.PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "worker task"}); err != nil {
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
	a.recovery = recovery.NewRecoveryManager(sessionDir)
	if err := a.recovery.PersistMessage("main", message.Message{Role: "user", Content: "resume worker conversation"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	a.ctxMgr.Append(message.Message{Role: "user", Content: "resume worker conversation"})
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	sub.agentDefName = "restorer"
	sub.setState(SubAgentStateWaitingMain, "need decision")
	if err := a.recovery.PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
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
	a.recovery = recovery.NewRecoveryManager(sessionDir)
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
	if err := a.recovery.PersistMessage("main", message.Message{Role: "user", Content: "resume this session"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	a.ctxMgr.Append(message.Message{Role: "user", Content: "resume this session"})
	sub := newControllableTestSubAgent(t, a, "adhoc-21")
	sub.agentDefName = "restorer"
	sub.taskDesc = "Investigate issue"
	sub.ctxMgr.Append(message.Message{Role: "user", Content: "Investigate issue"})
	if err := a.recovery.PersistMessage(sub.instanceID, message.Message{Role: "user", Content: "Investigate issue"}); err != nil {
		t.Fatalf("PersistMessage(sub): %v", err)
	}
	a.handleAgentDone(Event{
		SourceID: sub.instanceID,
		Payload:  &AgentResult{Summary: "done"},
	})

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
