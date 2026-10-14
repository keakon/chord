package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func TestParkedSubAgentFocusedStatsAndPoolDoNotFallBackToMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.modelPoolPolicy = NewRuntimeModelPoolPolicy()
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain, Models: map[string][]string{"main": {"test/main"}}},
		"worker":  {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"base": {"test/worker"}, "fast": {"test/worker-fast"}}},
	})
	a.activeConfig = a.agentConfigs["builder"]
	a.usageTracker.RecordForAgent(identity.MainAgentID, "test/main", nil, message.TokenUsage{InputTokens: 100})

	sub := newControllableTestSubAgent(t, a, "adhoc-park-focus")
	sub.agentDefName = "worker"
	a.usageTracker.RecordForAgent(sub.instanceID, "test/worker", nil, message.TokenUsage{InputTokens: 7, OutputTokens: 3})
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")
	a.SwitchFocus(sub.instanceID)
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}

	stats := a.GetSidebarUsageStats()
	if stats.InputTokens != 7 || stats.OutputTokens != 3 {
		t.Fatalf("parked usage = %#v, want worker-only stats", stats)
	}
	if current, limit := a.GetContextStats(); current != 0 || limit != 0 {
		t.Fatalf("parked context stats = (%d, %d), want unavailable zeros", current, limit)
	}
	if got := a.CurrentPoolName(); got != "base" {
		t.Fatalf("CurrentPoolName() = %q, want worker base pool", got)
	}
	if err := a.SetCurrentModelPool("fast"); err != nil {
		t.Fatalf("SetCurrentModelPool(fast): %v", err)
	}
	a.dispatch(<-a.eventCh)
	if got, ok := a.AgentOverridePoolName("worker"); !ok || got != "fast" {
		t.Fatalf("worker pool override = %q, %v; want fast, true", got, ok)
	}
	if got := a.modelPoolPolicy.CurrentModelPool(); got != "" {
		t.Fatalf("main current pool = %q, want unchanged", got)
	}
}

// A settled terminal task focus (runtime gone without ever parking) must show
// the task's own ledger/transcript/model data in the sidebar facades and refuse
// every write path instead of silently falling back to the main role — the same
// guarantee TestParkedSubAgentFocusedStatsAndPoolDoNotFallBackToMain gives
// parked tasks.
func TestSettledTaskFocusedStatsAndPoolDoNotFallBackToMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.modelPoolPolicy = NewRuntimeModelPoolPolicy()
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain, Models: map[string][]string{"main": {"test/main"}}},
		"worker":  {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"base": {"test/worker"}, "fast": {"test/worker-fast"}}},
	})
	a.activeConfig = a.agentConfigs["builder"]
	a.usageTracker.RecordForAgent(identity.MainAgentID, "test/main", nil, message.TokenUsage{InputTokens: 100})
	a.ctxMgr.Append(message.Message{Role: "user", Content: "main-msg"})

	const taskID = "settled-facade-task"
	const instanceID = "settled-facade-worker-1"
	a.setTaskRecords(map[string]*DurableTaskRecord{
		taskID: {
			TaskID:            taskID,
			AgentDefName:      "worker",
			LatestInstanceID:  instanceID,
			InstanceHistory:   []string{instanceID},
			State:             string(SubAgentStateCompleted),
			ResumePolicy:      taskResumePolicyNotify,
			SettlementDurable: true,
			SelectedModelRef:  "test/worker",
			UpdatedAt:         time.Now(),
		},
	})
	a.usageTracker.RecordForAgent(instanceID, "test/worker", nil, message.TokenUsage{InputTokens: 7, OutputTokens: 3})
	manager := a.recoveryManager()
	for _, content := range []string{"settled ask", "settled reply"} {
		if err := manager.PersistMessage(instanceID, message.Message{Role: "user", Content: content}); err != nil {
			t.Fatalf("persist settled row: %v", err)
		}
	}
	a.SwitchFocus(instanceID)

	// Usage/cost reads come from the settled task's own ledger sums.
	stats := a.GetSidebarUsageStats()
	if stats.InputTokens != 7 || stats.OutputTokens != 3 {
		t.Fatalf("settled usage = %#v, want the settled task's 7/3 tokens, not the main role's numbers", stats)
	}

	// Context frame: no live context manager, so unavailable zeros.
	if current, limit := a.GetContextStats(); current != 0 || limit != 0 {
		t.Fatalf("settled context stats = (%d, %d), want unavailable zeros", current, limit)
	}

	// Message count/bytes come from the settled transcript rows, not the main
	// session's one message.
	if got := a.GetContextMessageCount(); got != 2 {
		t.Fatalf("settled context message count = %d, want the 2 transcript rows", got)
	}
	if got := a.GetContextBytes(); got <= 0 {
		t.Fatalf("settled context bytes = %d, want > 0", got)
	}

	// Model reads resolve the settled task's own agent definition and record.
	state := a.FocusedModelState()
	if state.SelectedRef != "test/worker" {
		t.Fatalf("FocusedModelState.SelectedRef = %q, want the settled task's test/worker", state.SelectedRef)
	}
	if got := a.CurrentPoolName(); got != "base" {
		t.Fatalf("CurrentPoolName() = %q, want worker base pool", got)
	}
	if confirmed, total := a.KeyStats(); confirmed != 0 || total != 0 {
		t.Fatalf("KeyStats = %d/%d, want 0/0 (no runtime client, never the main role's keys)", confirmed, total)
	}

	// Pool switching refuses on a settled view and leaves the main pool alone.
	if err := a.SetCurrentModelPool("fast"); err == nil {
		t.Fatal("SetCurrentModelPool(fast) on a settled view: error = nil, want read-only refusal")
	}
	if got, ok := a.AgentOverridePoolName("worker"); ok {
		t.Fatalf("worker pool override = %q after refused switch, want none", got)
	}
	if got := a.modelPoolPolicy.CurrentModelPool(); got != "" {
		t.Fatalf("main current pool = %q after refused switch, want unchanged", got)
	}

	// Context append refuses instead of writing into the main session.
	a.AppendContextMessage(message.Message{Role: "user", Content: "shell output while settled"})
	if rows := a.ctxMgr.Snapshot(); len(rows) != 1 || rows[0].Content != "main-msg" {
		t.Fatalf("main context = %#v after settled append, want the unchanged main-msg only", rows)
	}
	if msgs := a.GetMessages(); len(msgs) != 2 {
		t.Fatalf("settled transcript = %d rows after settled append, want 2", len(msgs))
	}
}

// The status bar reads one number for the focused agent's prompt side, and it
// has to mean the same thing before and after that agent parks. Reading a live
// agent from its context manager broke that: a context manager belongs to one
// runtime instance, so a task resumed for a second attempt reported only the
// current attempt while live and every attempt once parked — the number jumped
// on parking without any work happening. Both sides now read the ledger, which
// accounts per task.
func TestFocusedTokenUsageCoversEveryAttemptWhileLive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.SetAgentConfigs(map[string]*config.AgentConfig{
		"worker": {Name: "worker", Mode: config.AgentModeSubAgent, Models: map[string][]string{"default": {"test/worker"}}},
	})
	a.usageTracker.RecordForAgent(identity.MainAgentID, "test/main", nil, message.TokenUsage{InputTokens: 900, OutputTokens: 90})

	sub := newControllableTestSubAgent(t, a, "adhoc-usage-frame")
	sub.agentDefName = "worker"
	// A first attempt that already ended, and the live attempt that replaced it.
	a.usageTracker.RecordForAgent("worker-earlier", "test/worker", nil, message.TokenUsage{InputTokens: 300, OutputTokens: 30})
	current := message.TokenUsage{InputTokens: 1000, OutputTokens: 40, CacheReadTokens: 800, CacheWriteTokens: 50}
	a.usageTracker.RecordForAgent(sub.instanceID, "test/worker", nil, current)
	sub.ctxMgr.UpdateFromUsage(current)
	sub.setState(SubAgentStateCompleted, "done")
	a.syncTaskRecordFromSub(sub, "")
	record := a.taskRecordByTaskID("adhoc-usage-frame")
	record.InstanceHistory = []string{"worker-earlier", sub.instanceID}
	a.setTaskRecords(map[string]*DurableTaskRecord{"adhoc-usage-frame": record})

	a.SwitchFocus(sub.instanceID)
	live := a.GetTokenUsage()
	if live.OutputTokens != 70 {
		t.Fatalf("live focused OutputTokens = %d, want 70 (both attempts of this task, not just the live instance)", live.OutputTokens)
	}
	if live.InputTokens != 1300 {
		t.Fatalf("live focused InputTokens = %d, want 1300 (both attempts, prompt side including the cached prefix)", live.InputTokens)
	}

	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false")
	}
	if parked := a.GetTokenUsage(); parked != live {
		t.Fatalf("parked focused usage = %#v, want the live reading %#v unchanged by parking", parked, live)
	}
}
