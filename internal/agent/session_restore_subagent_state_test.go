package agent

import (
	"context"
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

func TestRestoreLoadedSubAgentsKeepsCompletedState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: []string{"test/test-model"},
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

func TestRestoreLoadedSubAgentsRestoresOwnerDepthAndPendingComplete(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:       "restorer",
			Mode:       "subagent",
			Models:     []string{"test/test-model"},
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
	restored := a.subAgentByID("worker-1")
	if restored == nil {
		t.Fatal("expected restored worker")
	}
	if restored.OwnerAgentID() != "worker-parent" {
		t.Fatalf("OwnerAgentID() = %q, want worker-parent", restored.OwnerAgentID())
	}
	if restored.OwnerTaskID() != "adhoc-parent" {
		t.Fatalf("OwnerTaskID() = %q, want adhoc-parent", restored.OwnerTaskID())
	}
	if restored.Depth() != 2 {
		t.Fatalf("Depth() = %d, want 2", restored.Depth())
	}
	pending := restored.PendingCompleteIntent()
	if pending == nil || pending.Summary != "final summary" {
		t.Fatalf("PendingCompleteIntent() = %#v, want summary %q", pending, "final summary")
	}
}

func TestRestoreLoadedSubAgentsDrainsOwnedMailboxAfterOwnerRestore(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   "subagent",
			Models: []string{"test/test-model"},
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

	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := len(a.ownedSubAgentMailboxes["worker-parent"]); got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ownedSubAgentMailboxes[worker-parent] still queued: %#v", a.ownedSubAgentMailboxes["worker-parent"])
		}
		time.Sleep(10 * time.Millisecond)
	}

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
			Models: []string{"test/test-model"},
		},
	})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}

	subagents := a.GetSubAgents()
	if len(subagents) != 0 {
		t.Fatalf("len(GetSubAgents()) = %d, want 0 for restored terminal workers", len(subagents))
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
			Models: []string{"test/test-model"},
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
	sub.setState(SubAgentStateWaitingPrimary, "need decision")
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
			Models: []string{"test/test-model"},
		},
	})
	a2.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a2.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}

	restored := a2.subAgentByID("worker-1")
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

	a2.handleAgentNotify(Event{SourceID: restored.instanceID, Payload: "continuing with approved plan"})
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
			Models: []string{"test/test-model"},
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
			Models: []string{"test/test-model"},
		},
	})
	a2.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	})
	if _, err := a2.restoreSessionState(sessionDir); err != nil {
		t.Fatalf("restoreSessionState: %v", err)
	}
	if got := a2.GetSubAgents(); len(got) != 0 {
		t.Fatalf("len(GetSubAgents()) = %d, want 0 before rehydrate", len(got))
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
