package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func TestLoadRestoredSubAgentStatesCharacterizationMetaMailboxTaskPriority(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "restore-priority")
	rm := recovery.NewRecoveryManager(sessionDir)
	if err := rm.PersistMessage("main", message.Message{Role: "user", Content: "resume"}); err != nil {
		t.Fatalf("PersistMessage(main): %v", err)
	}
	if err := rm.PersistMessage("worker-1", message.Message{Role: "user", Content: "from transcript"}); err != nil {
		t.Fatalf("PersistMessage(worker-1): %v", err)
	}
	if err := rm.SaveSnapshot(&recovery.SessionSnapshot{CreatedAt: time.Now(), ActiveAgents: []recovery.AgentSnapshot{{
		InstanceID:             "worker-1",
		TaskID:                 "task-from-snapshot",
		AgentDefName:           "restorer",
		TaskDesc:               "from snapshot",
		State:                  string(SubAgentStateRunning),
		LastSummary:            "summary from snapshot",
		OwnerAgentID:           "owner-from-snapshot",
		OwnerTaskID:            "owner-task-from-snapshot",
		Depth:                  1,
		JoinToOwner:            false,
		PendingCompleteIntent:  false,
		PendingCompleteSummary: "pending from snapshot",
	}}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rm.Close()

	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	a.SetAgentConfigs(map[string]*config.AgentConfig{"restorer": {Name: "restorer", Mode: "subagent", Models: []string{"test/test-model"}}})
	a.SetLLMFactory(func(systemPrompt string, agentModels []string, variant string) *llm.Client { return newTestLLMClient() })

	metaSub := &SubAgent{instanceID: "worker-1", taskID: "task-from-meta", agentDefName: "restorer", taskDesc: "from meta", ownerAgentID: "owner-from-meta", ownerTaskID: "owner-task-from-meta", depth: 3}
	metaSub.setLastMailboxID("mailbox-123")
	metaSub.setReplyThread("reply-msg", "reply-mailbox", "progress", "reply summary")
	metaSub.setLastArtifact(tools.ArtifactRef{ID: "artifact-1", RelPath: "artifacts/out.txt", Path: "artifacts/out.txt", Type: "text/plain"})
	metaSub.setPendingCompleteIntent(&AgentResult{Summary: "pending from meta"})
	metaSub.setState(SubAgentStateWaitingPrimary, "summary from meta")
	a.persistSubAgentMeta(metaSub)

	mailbox := []SubAgentMailboxMessage{{AgentID: "worker-1", Kind: SubAgentMailboxKindCompleted, Summary: "summary from mailbox"}}
	taskRecords := map[string]*DurableTaskRecord{"task-from-snapshot": {TaskID: "task-from-snapshot", JoinToOwner: true}}
	rm2 := recovery.NewRecoveryManager(sessionDir)
	defer rm2.Close()
	snap, err := rm2.Recover()
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	states := a.loadRestoredSubAgentStates(sessionDir, rm2, snap, mailbox, taskRecords)
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want 1", len(states))
	}
	got := states[0]
	if got.TaskID != "task-from-snapshot" {
		t.Fatalf("TaskID = %q, want snapshot priority", got.TaskID)
	}
	if got.TaskDesc != "from snapshot" {
		t.Fatalf("TaskDesc = %q, want snapshot priority", got.TaskDesc)
	}
	if got.State != SubAgentStateIdle {
		t.Fatalf("State = %q, want running normalized to idle from snapshot priority", got.State)
	}
	if got.LastSummary != "summary from snapshot" {
		t.Fatalf("LastSummary = %q, want snapshot priority", got.LastSummary)
	}
	if got.OwnerAgentID != "owner-from-meta" || got.OwnerTaskID != "owner-task-from-meta" {
		t.Fatalf("owner fields = (%q,%q), want meta priority", got.OwnerAgentID, got.OwnerTaskID)
	}
	if got.Depth != 3 {
		t.Fatalf("Depth = %d, want meta priority", got.Depth)
	}
	if !got.JoinToOwner {
		t.Fatal("JoinToOwner = false, want task record priority")
	}
	if !got.PendingCompleteIntent || got.PendingCompleteSummary != "pending from meta" {
		t.Fatalf("pending complete = (%v,%q), want meta priority", got.PendingCompleteIntent, got.PendingCompleteSummary)
	}
	if got.LastMailboxID != "mailbox-123" || got.LastReplyMessageID != "reply-msg" || got.LastArtifact.ID != "artifact-1" {
		t.Fatalf("meta continuity fields not restored: %#v", got)
	}
}
