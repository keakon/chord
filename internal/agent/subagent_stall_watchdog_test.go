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

// taskHasStallResolvedAlert reports whether the task's mailbox state carries
// the stall_resolved risk_alert that closes a previously alerted stall
// episode (distinct from the still-active stall alert itself).
func taskHasStallResolvedAlert(a *MainAgent, taskID string) bool {
	for _, msg := range a.subAgentInbox.urgent {
		if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID && msg.Subtype == SubAgentStallResolvedSubtype {
			return true
		}
	}
	for _, msg := range a.subAgentInbox.normal {
		if msg.Kind == SubAgentMailboxKindRiskAlert && strings.TrimSpace(msg.TaskID) == taskID && msg.Subtype == SubAgentStallResolvedSubtype {
			return true
		}
	}
	return false
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
	// refresh: the coordination snapshot must suspect a stall. Snapshot
	// formatting is read-only — stall markers are refreshed at the
	// request-dispatch boundary (buildTurnOverlayMessages) — so the test runs
	// that same refresh before rendering.
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.updateSubAgentStallMarkers()
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
	a.updateSubAgentStallMarkers()
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

	// Renewed activity closes the episode with a stall_resolved alert; a
	// fresh quiet window then alerts again as a new episode.
	sub.markActivity()
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 2 {
		t.Fatalf("risk_alert count after recovery = %d, want 2 (stall alert + resolution)", got)
	}
	if !taskHasStallResolvedAlert(a, sub.taskID) {
		t.Fatalf("recovery did not produce the stall_resolved alert: %#v", a.subAgentInbox.urgent)
	}
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 3 {
		t.Fatalf("second-episode stall risk_alert count = %d, want 3 (alert + resolution + new alert)", got)
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
		case <-time.After(time.Until(deadline)):
			t.Fatal("silence watchdog never produced the bounded fallback error")
		}
	}
	t.Fatal("silence watchdog never produced the bounded fallback error")
}

// TestSubAgentToolHeartbeatRefreshesWhileRunningAndStops pins the long-tool
// heartbeat: runActivityHeartbeat refreshes the worker activity clock on a
// short interval while running (a single tool can run past the stall
// threshold), and stops refreshing once stop is called.
func TestSubAgentToolHeartbeatRefreshesWhileRunningAndStops(t *testing.T) {
	sub := &SubAgent{}
	stop := sub.runActivityHeartbeat(context.Background(), 10*time.Millisecond)
	time.Sleep(45 * time.Millisecond)
	// Read the activity clock through the locked accessor: the heartbeat
	// goroutine writes it under the runtime state lock.
	if elapsed := time.Since(sub.StateChangedAt()); elapsed > 35*time.Millisecond {
		t.Fatalf("activity heartbeat was not refreshed periodically while running: last refresh %v ago", elapsed)
	}
	// stop joins the heartbeat goroutine, so no final in-flight refresh can
	// land after it returns.
	stop()
	baseline := sub.StateChangedAt()
	time.Sleep(25 * time.Millisecond)
	if !sub.StateChangedAt().Equal(baseline) {
		t.Fatalf("activity heartbeat kept refreshing after stop")
	}
}

// TestStallSweepHoldsQuietWorkersWhileUserInteractionPending pins the
// user-interaction exemption: while the main agent waits on a user-facing
// confirm/question/handoff dialog it dispatches no new input, so a healthy
// Running worker goes quiet for as long as the user takes to answer. The sweep
// must treat the wait as activity — refreshing the heartbeat and closing any
// earlier stall episode — instead of raising AGENT BLOCKED, and must alert
// again once the dialog resolves and the worker stays silent past the
// threshold.
func TestStallSweepHoldsQuietWorkersWhileUserInteractionPending(t *testing.T) {
	cases := []struct {
		name string
		open func(a *MainAgent) (close func())
	}{
		{
			name: "confirm",
			open: func(a *MainAgent) (close func()) {
				req := "adhoc-user-confirm"
				a.interaction.registerConfirm(req, testWalltimeTarget("main"))
				return func() { a.interaction.unregisterConfirm(req) }
			},
		},
		{
			name: "question",
			open: func(a *MainAgent) (close func()) {
				req := "adhoc-user-question"
				a.interaction.registerQuestion(req, testWalltimeTarget("main"))
				return func() { a.interaction.unregisterQuestion(req) }
			},
		},
		{
			name: "handoff",
			open: func(a *MainAgent) (close func()) {
				req := "adhoc-user-handoff"
				a.interaction.openHandoff(req, testWalltimeTarget("main"))
				return func() { a.interaction.settleHandoff(req) }
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			sub := newControllableTestSubAgent(t, a, "adhoc-user-wait")
			sub.agentDefName = "worker"
			sub.semHeld = true
			sub.setState(SubAgentStateRunning, "working")
			a.syncTaskRecordFromSub(sub, "")
			a.mailboxDeliveryPaused.Store(true)
			closeWait := tc.open(a)
			defer closeWait()

			// The worker's heartbeat is stale past the threshold, but the
			// user dialog explains the silence: no stall alert, and the wait
			// refreshes the heartbeat instead.
			sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
			a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
			if sawRiskAlertNotify(a, sub.taskID) {
				t.Fatalf("quiet worker behind a user dialog produced a stall notification")
			}
			dispatchQueuedEvents(t, a)
			if got := countRiskAlertsForTask(a, sub.taskID); got != 0 {
				t.Fatalf("stall risk_alert count = %d, want 0 while a user dialog is pending", got)
			}
			if time.Since(sub.StateChangedAt()) > time.Minute {
				t.Fatalf("user wait did not refresh the worker heartbeat")
			}

			// The dialog resolves and the worker stays quiet past the
			// threshold: the next quiet window is a real stall episode and
			// alerts once.
			closeWait()
			sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
			a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
			if !sawRiskAlertNotify(a, sub.taskID) {
				t.Fatal("worker silent after the user dialog resolved did not produce a stall notification")
			}
			dispatchQueuedEvents(t, a)
			if got := countRiskAlertsForTask(a, sub.taskID); got != 1 {
				t.Fatalf("stall risk_alert count = %d, want 1 after the dialog resolved", got)
			}
		})
	}
}

// TestCoolingWaitHoldsLivenessChecksForSilentRequest pins the long-cooldown
// exemption: a worker whose request is sleeping out an API key cooldown is
// silent by design, so neither the in-flight silence watchdog nor the
// owner-facing stall sweep may treat it as wedged while the reported recovery
// instant is still ahead — and both must resume judging it once the wait ends.
func TestCoolingWaitHoldsLivenessChecksForSilentRequest(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-cooling-wait")
	sub.agentDefName = "worker"
	sub.semHeld = true
	sub.setState(SubAgentStateRunning, "working")
	a.syncTaskRecordFromSub(sub, "")
	a.mailboxDeliveryPaused.Store(true)

	// A request is in flight and its heartbeat is already stale past both
	// thresholds: without the cooling record this is a stall on both paths.
	sub.llmSilenceBudget = time.Minute
	sub.llmRequestInFlight.Store(true)
	sub.turn = &Turn{ID: 1}
	stale := time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	sub.runtimeState.stateChangedAt = stale

	if _, armed := sub.llmSilenceWatchdogDeadline(); !armed {
		t.Fatal("silence watchdog not armed for an in-flight request")
	}
	if got := runningSubAgentStallReason(sub, time.Now()); got == "" {
		t.Fatal("stale in-flight worker not flagged before the cooling wait is recorded")
	}

	// The client reports a cooling wait that runs far past the silence budget
	// (a real quota reset can be hours out). Both checks must stand down.
	coolingUntil := time.Now().Add(3 * time.Hour)
	sub.noteLLMCoolingWait(coolingUntil)
	// noteLLMCoolingWait is the only writer here; markActivity (which clears
	// the record) must not have run, so the heartbeat stays stale on purpose.
	sub.runtimeState.stateChangedAt = stale
	deadline, armed := sub.llmSilenceWatchdogDeadline()
	if !armed {
		t.Fatal("silence watchdog disarmed during a cooling wait; it must stay armed with a later deadline")
	}
	if !deadline.After(coolingUntil) {
		t.Fatalf("silence deadline = %v, want after the cooling recovery instant %v", deadline, coolingUntil)
	}
	if sub.handleLLMSilenceIfDue() {
		t.Fatal("silence watchdog escalated a request that is sleeping out a key cooldown")
	}
	if got := runningSubAgentStallReason(sub, time.Now()); got != "" {
		t.Fatalf("stall reason during a cooling wait = %q, want empty", got)
	}
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 0 {
		t.Fatalf("stall risk_alert count during a cooling wait = %d, want 0", got)
	}

	// Once the recovery instant has passed, the grace window expires and both
	// checks judge the worker normally again.
	sub.noteLLMCoolingWait(time.Now().Add(-time.Second))
	sub.runtimeState.stateChangedAt = stale
	if !sub.llmCoolingWaitDeadline().IsZero() {
		t.Fatal("a past cooling deadline must not keep the grace window open")
	}
	if got := runningSubAgentStallReason(sub, time.Now()); got == "" {
		t.Fatal("stall detection did not resume after the cooling wait ended")
	}

	// Real progress also drops the record immediately: a request that streams
	// after cooling must not carry the grace window into a later silence.
	sub.noteLLMCoolingWait(time.Now().Add(time.Hour))
	sub.markActivity()
	if !sub.llmCoolingWaitDeadline().IsZero() {
		t.Fatal("real activity must clear the recorded cooling wait")
	}
}

// TestCoolingWaitSurvivesProductionStreamReducerWiring pins the wiring order a
// SubAgent request actually goes through. The reducer installs its own
// emitActivity while it is being built — that closure is the only writer of the
// cooling record the liveness checks read — and the wall-clock recorder wires
// its accounting on afterwards. Wiring therefore has to wrap the hook rather
// than replace it: a replacement leaves every cooling wait unrecorded, so a
// worker sleeping out a real key cooldown is reported as wedged again.
//
// The construction order below mirrors SubAgent.handleLLM
// (newSubLLMStreamReducer -> startRequestAt -> wireStreamReducer -> Handle), so
// the test fails if the accounting hook ever stops chaining.
func TestCoolingWaitSurvivesProductionStreamReducerWiring(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-cooling-wiring")
	sub.agentDefName = "worker"
	sub.semHeld = true
	sub.setState(SubAgentStateRunning, "working")
	a.syncTaskRecordFromSub(sub, "")

	// The request is in flight with a stale heartbeat, so without the cooling
	// record both liveness checks would call this worker stalled.
	sub.llmSilenceBudget = time.Minute
	sub.llmRequestInFlight.Store(true)
	sub.turn = &Turn{ID: 1}
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)

	turn := &Turn{ID: 1}
	reducer := sub.newSubLLMStreamReducer(turn, func(string) {}, false, nil, 0)
	wallReq := a.walltime.startRequestAt(sub.instanceID, sub.agentDefName, turn.ID)
	if wallReq == nil {
		t.Fatal("walltime recorder not wired; the test cannot reproduce the production order")
	}
	wallReq.wireStreamReducer(reducer)
	t.Cleanup(wallReq.finish)

	coolingUntil := time.Now().Add(3 * time.Hour)
	reducer.Handle(message.StreamDelta{
		Type: message.StreamDeltaStatus,
		Status: &message.StatusDelta{
			Type:     message.StatusDeltaCooling,
			Detail:   "3h",
			Deadline: coolingUntil,
		},
	})

	got := sub.llmCoolingWaitDeadline()
	if got.IsZero() {
		t.Fatal("cooling wait not recorded through the production wiring order; the reducer's emitActivity hook was replaced instead of wrapped")
	}
	if !got.Equal(coolingUntil) {
		t.Fatalf("recorded cooling deadline = %v, want %v", got, coolingUntil)
	}
	if reason := runningSubAgentStallReason(sub, time.Now()); reason != "" {
		t.Fatalf("stall reason after a cooling status through production wiring = %q, want empty", reason)
	}
}

// TestStallSweepSkipsRunningWorkerWhoseTaskAlreadySettled pins the late-alert
// guard: a live runtime that still reports Running while its durable record
// already settled is a stale conflict survivor. The sweep must not raise a
// late AGENT BLOCKED for a task that already completed — the owner can no
// longer act on it, and the settlement owns the terminal truth.
func TestStallSweepSkipsRunningWorkerWhoseTaskAlreadySettled(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-stall-settled")
	sub.agentDefName = "worker"
	sub.semHeld = true
	sub.setState(SubAgentStateRunning, "working")
	a.syncTaskRecordFromSub(sub, "")
	a.subs.mu.Lock()
	if rec := a.subs.taskRecords[sub.taskID]; rec != nil {
		rec.State = string(SubAgentStateCompleted)
	}
	a.subs.mu.Unlock()
	a.mailboxDeliveryPaused.Store(true)
	sub.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, sub.taskID); got != 0 {
		t.Fatalf("late stall risk_alert count for a settled task = %d, want 0", got)
	}
	if sawRiskAlertNotify(a, sub.taskID) {
		t.Fatal("settled task produced a late stall AgentNotifyEvent")
	}
}
