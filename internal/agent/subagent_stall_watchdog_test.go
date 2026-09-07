package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// silenceUntilCanceledProvider stands in for a transport that never streams
// and never returns on its own: it blocks until its request context is
// cancelled, then surfaces the cancellation so the request goroutine exits
// through the normal aborted-request path (never posting a stale result).
type silenceUntilCanceledProvider struct{}

func (silenceUntilCanceledProvider) CompleteStream(ctx context.Context, _, _, _ string, _ []message.Message, _ []message.ToolDefinition, _ int, _ llm.RequestTuning, _ llm.StreamCallback) (*message.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (silenceUntilCanceledProvider) Complete(ctx context.Context, _, _, _ string, _ []message.Message, _ []message.ToolDefinition, _ int, _ llm.RequestTuning) (*message.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func newSilentLLMClient() *llm.Client {
	providerCfg := llm.NewProviderConfig("test", config.ProviderConfig{
		Type: config.ProviderTypeMessages,
		Models: map[string]config.ModelConfig{
			"test-model": {
				Limit: config.ModelLimit{
					Context: 8192,
					Output:  1024,
				},
				SupportedServiceTiers: []config.ServiceTier{config.ServiceTierFast, config.ServiceTierSlow},
			},
		},
	}, []string{"test-key"})
	return llm.NewClient(providerCfg, silenceUntilCanceledProvider{}, "test-model", 1024, "")
}

// newSilentSubAgent is newControllableTestSubAgent with a client whose
// requests never return on their own, for the silence-watchdog test.
func newSilentSubAgent(t *testing.T, parent *MainAgent, taskID string) *SubAgent {
	t.Helper()
	ctx, cancel := context.WithCancel(parent.parentCtx)
	sub := NewSubAgent(SubAgentConfig{
		InstanceID:   "silent-worker-1",
		TaskID:       taskID,
		AgentDefName: "worker",
		TaskDesc:     "do work",
		LLMClient:    newSilentLLMClient(),
		Recovery:     parent.recoveryManager(),
		Parent:       parent,
		ParentCtx:    ctx,
		Cancel:       cancel,
		BaseTools:    parent.tools,
		WorkDir:      parent.projectRoot,
		SessionDir:   parent.sessionDir,
		ModelName:    "test-model",
	})
	parent.subs.mu.Lock()
	parent.subs.subAgents[sub.instanceID] = sub
	parent.subs.mu.Unlock()
	parent.syncTaskRecordFromSub(sub, "")
	t.Cleanup(func() { sub.cancel() })
	return sub
}

// countRiskAlertsForTask counts the risk_alert mailboxes a task currently has
// in the main-agent inbox, so a test can pin one-alert-per-stall-episode.
func countRiskAlertsForTask(a *MainAgent, taskID string) int {
	count := 0
	for _, msg := range a.subAgentInbox.urgent {
		if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID {
			count++
		}
	}
	for _, msg := range a.subAgentInbox.normal {
		if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID {
			count++
		}
	}
	return count
}

// TestRunningWorkerActivityHeartbeatClearsSuspectedStall pins the fix for the
// false-positive: a worker that entered Running long ago but keeps refreshing
// its activity heartbeat (LLM round trips, tool results) must never be marked
// as suspected_stall, while the same worker without recent activity is.
func TestRunningWorkerActivityHeartbeatClearsSuspectedStall(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-heartbeat")
	sub.agentDefName = "worker"
	sub.semHeld = true
	sub.setState(SubAgentStateRunning, "working")
	a.syncTaskRecordFromSub(sub, "")

	// The worker has been in Running since before the stall threshold with no
	// refresh: the coordination snapshot must suspect a stall.
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	block := a.buildCoordinationSnapshotOverlay()
	if !strings.Contains(block, "suspected_stall: running with no recent state/progress update") {
		t.Fatalf("stale-running worker not flagged before heartbeat refresh:\n%s", block)
	}

	// Real activity keeps the heartbeat fresh even though the state never
	// transitions away from Running; repeated tool/LLM round trips over many
	// minutes must not make the worker look stalled.
	for range 10 {
		sub.markActivity()
	}
	block = a.buildCoordinationSnapshotOverlay()
	if strings.Contains(block, "suspected_stall:") {
		t.Fatalf("busy running worker flagged as suspected_stall after heartbeat refresh:\n%s", block)
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || strings.TrimSpace(rec.SuspectedStallReason) != "" {
		t.Fatalf("task record marker not cleared for busy worker: %#v", rec)
	}
}

// TestRunningWorkerStallSweepAlertsOwnerOnceWithoutKill pins the owner-facing
// watchdog: a Running worker whose heartbeat is stale past the coordination
// stall threshold produces exactly one risk_alert mailbox plus the matching
// AgentNotifyEvent, the worker is not killed, and a later stall episode after
// renewed activity alerts again.
func TestRunningWorkerStallSweepAlertsOwnerOnceWithoutKill(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-stall-alert")
	sub.agentDefName = "worker"
	sub.semHeld = true
	sub.setState(SubAgentStateRunning, "working")
	a.syncTaskRecordFromSub(sub, "")
	a.mailboxDeliveryPaused.Store(true)

	// A healthy worker (fresh heartbeat) is not alerted.
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	if sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("healthy running worker produced a stall notification")
	}

	// Heartbeat goes quiet past the threshold; the periodic sweep must alert
	// the owner without cancelling the worker.
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	if !sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("owner did not receive the stall AgentNotifyEvent")
	}
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 1 {
		t.Fatalf("stall risk_alert count = %d, want exactly 1 for the first episode", got)
	}
	risk := findRiskAlertForTask(a, sub.taskID)
	if risk == nil || !strings.Contains(risk.Summary, "no recent state/progress update") {
		t.Fatalf("stall risk_alert = %#v, want the stall reason summary", risk)
	}
	if live := a.subAgentByID(sub.instanceID); live == nil {
		t.Fatal("stalled worker was killed; the watchdog must only notify")
	}
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.State != string(SubAgentStateRunning) {
		t.Fatalf("stalled worker task record = %#v, want still running (no auto-kill)", rec)
	}

	// Repeated sweeps while the same episode is unresolved do not spam.
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 1 {
		t.Fatalf("duplicate stall risk_alert count = %d, want 1 (one alert per episode)", got)
	}

	// Renewed activity clears the episode; a fresh quiet window alerts again.
	sub.markActivity()
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 2 {
		t.Fatalf("second-episode stall risk_alert count = %d, want 2", got)
	}
}

// TestLLMRequestSilenceWatchdogBoundsNeverReturningRequest pins the in-flight
// fallback: a request that produces nothing and never returns on its own is
// cancelled after the silence budget, gets exactly one bounded recovery
// attempt, and then surfaces an EventAgentError instead of parking the run
// loop on llmCh forever.
func TestLLMRequestSilenceWatchdogBoundsNeverReturningRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newSilentSubAgent(t, a, "adhoc-silent-request")
	sub.llmSilenceBudget = 40 * time.Millisecond
	sub.startRunLoop()

	if !sub.InjectUserMessage("work on the task") {
		t.Fatal("InjectUserMessage() rejected queued input")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case evt := <-a.eventCh:
			if evt.Type != EventAgentError || evt.SourceID != sub.instanceID {
				continue
			}
			err, ok := evt.Payload.(error)
			if !ok || !strings.Contains(err.Error(), "stalled silently") {
				t.Fatalf("silence watchdog error = %#v, want silent-stall failure", evt.Payload)
			}
			return
		case <-time.After(deadline.Sub(time.Now())):
			t.Fatal("silence watchdog never produced the bounded fallback error")
		}
	}
	t.Fatal("silence watchdog never produced the bounded fallback error")
}
