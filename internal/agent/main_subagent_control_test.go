package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/recovery"
)

func newControllableTestSubAgent(t *testing.T, parent *MainAgent, taskID string) *SubAgent {
	t.Helper()
	return newControllableTestSubAgentWithWorkDir(t, parent, taskID, WorkDirState{})
}

// newControllableTestSubAgentWithWorkDir is newControllableTestSubAgent for a
// worker that starts with a checkout binding, for example one placed in a
// managed worktree.
func newControllableTestSubAgentWithWorkDir(t *testing.T, parent *MainAgent, taskID string, state WorkDirState) *SubAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(parent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "worker-1",
		TaskID:       taskID,
		AgentDefName: "worker",
		TaskDesc:     "do work",
		LLMClient:    newTestLLMClient(),
		Recovery:     parent.recoveryManager(),
		Parent:       parent,
		ParentCtx:    ctx,
		Cancel:       cancel,
		BaseTools:    parent.tools,
		WorkDir:      parent.contentRoot,
		WorkDirState: state,
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	parent.subs.mu.Lock()
	parent.subs.subAgents[sub.instanceID] = sub
	parent.subs.mu.Unlock()
	parent.syncTaskRecordFromSub(sub, "")
	return sub
}

func configureNestedDelegationTestRuntime(a *MainAgent, maxDepth int) {
	a.llmFactory = func(systemPrompt string, agentModels []string, variant string) *llm.Client {
		return newTestLLMClient()
	}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {
			Name:        "worker",
			Mode:        "subagent",
			Models:      map[string][]string{"default": {"sample/test-model"}},
			Delegation:  config.DelegationConfig{MaxChildren: 10, MaxDepth: maxDepth},
			Description: "Nested worker",
		},
	}
	a.activeConfig = &config.AgentConfig{
		Name:       "builder",
		Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: maxDepth},
	}
}

func waitForEnvStatusUpdateEvent(t *testing.T, events <-chan AgentEvent) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if _, ok := event.(EnvStatusUpdateEvent); ok {
				return
			}
		case <-timer.C:
			t.Fatal("timed out waiting for environment refresh")
		}
	}
}

func startMainAgentLoopForTest(t *testing.T, a *MainAgent) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = a.Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !a.started.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for MainAgent event loop to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		<-a.done
	})
	return cancel
}

func newRevivalRaceTestMainAgent(t *testing.T) (*MainAgent, *DurableTaskRecord) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"restorer": {
			Name:   "restorer",
			Mode:   config.AgentModeSubAgent,
			Models: map[string][]string{"default": {"test/test-model"}},
		},
	})
	a.SetLLMFactory(func(string, []string, string) *llm.Client { return newTestLLMClient() })
	record := &DurableTaskRecord{
		TaskID:           "revival-race",
		AgentDefName:     "restorer",
		TaskDesc:         "reply after parking",
		State:            string(SubAgentStateWaitingMain),
		Attempt:          3,
		ResumePolicy:     taskResumePolicyNotify,
		LatestInstanceID: "restorer-9",
		InstanceHistory:  []string{"restorer-9"},
		RuntimeParked:    true,
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{record.TaskID: cloneDurableTaskRecord(record)})
	return a, record
}

// installSnapshotBlockingRecoveryManager installs a real recovery manager whose
// snapshot.json path is already a directory, so every SaveSnapshot fails with a
// filesystem error while message, meta, and task-registry writes keep working.
// It is the deterministic injection point for the recovery-snapshot failure
// branch of CreateSubAgent and rehydrateTaskAsActivationLeader.
func installSnapshotBlockingRecoveryManager(t *testing.T, a *MainAgent) {
	t.Helper()
	blocker := filepath.Join(a.sessionDir, "snapshot.json")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatalf("mkdir snapshot blocker: %v", err)
	}
	a.installRecoveryManager(recovery.NewRecoveryManager(a.sessionDir))
}

// ---------------------------------------------------------------------------
// WaitingMain expiry trigger, owner notification, and request-expired ledger
// (regression: expiry previously depended on user speech)
// ---------------------------------------------------------------------------

// pendingAgentRequestForSource returns the durable escalation request a source
// task owns in the sweep tests below, cloned under the registry lock.
func pendingAgentRequestForSource(t *testing.T, a *MainAgent, taskID string) *DurableAgentRequest {
	t.Helper()
	a.subs.mu.RLock()
	defer a.subs.mu.RUnlock()
	for _, request := range a.subs.agentRequests {
		if request != nil && strings.TrimSpace(request.SourceTaskID) == taskID {
			return cloneDurableAgentRequest(request)
		}
	}
	return nil
}

// dispatchQueuedEvents runs every already-queued event through dispatch until
// the queues are empty. It is the test-side stand-in for the Run loop; new
// events produced during dispatch are drained as well, so callers must keep
// mailbox delivery paused unless a real turn is wanted.
func dispatchQueuedEvents(t *testing.T, a *MainAgent) {
	t.Helper()
	for {
		evt, ok := a.popQueuedEvent()
		if !ok {
			return
		}
		a.dispatch(evt)
	}
}

// findRiskAlertForTask locates the expiry risk_alert mailbox in the main-agent
// inbox (memory or spool) for the given task.
func findRiskAlertForTask(a *MainAgent, taskID string) *SubAgentMailboxMessage {
	collect := func() []SubAgentMailboxMessage {
		out := append([]SubAgentMailboxMessage{}, a.subAgentInbox.urgent...)
		out = append(out, a.subAgentInbox.normal...)
		for _, id := range a.subAgentInbox.spoolUrgent {
			if msg, found, err := a.loadSpooledMailbox(id); err == nil && found {
				out = append(out, *msg)
			}
		}
		for _, id := range a.subAgentInbox.spoolNormal {
			if msg, found, err := a.loadSpooledMailbox(id); err == nil && found {
				out = append(out, *msg)
			}
		}
		return out
	}
	for _, msg := range collect() {
		if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID {
			duplicate := msg
			return &duplicate
		}
	}
	return nil
}

// sawRiskAlertNotify drains the TUI output channel looking for the
// control-plane AgentNotifyEvent that accompanies the expiry mailbox.
func sawRiskAlertNotify(a *MainAgent, taskID string) bool {
	for {
		select {
		case evt := <-a.outputCh:
			notify, ok := evt.(AgentNotifyEvent)
			if ok && strings.TrimSpace(notify.TaskID) == taskID && notify.Kind == string(SubAgentMailboxKindRiskAlert) {
				return true
			}
		default:
			return false
		}
	}
}

func completedMailboxMessage(msgs []SubAgentMailboxMessage, taskID string) *SubAgentMailboxMessage {
	for i := range msgs {
		msg := &msgs[i]
		if msg.Kind != SubAgentMailboxKindCompleted || strings.TrimSpace(msg.TaskID) != taskID {
			continue
		}
		return msg
	}
	return nil
}
