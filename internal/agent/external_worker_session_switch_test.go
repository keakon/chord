package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func createTestExternalWorker(t *testing.T, a *MainAgent) *externalWorker {
	t.Helper()
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Wait."})
	if err != nil {
		t.Fatalf("create external worker: %v", err)
	}
	worker, ok := a.subs.runtime(handle.AgentID).(*externalWorker)
	if !ok {
		t.Fatalf("runtime %s is not an external worker", handle.AgentID)
	}
	t.Cleanup(func() {
		worker.teardown()
		select {
		case <-worker.done:
		case <-time.After(10 * time.Second):
			t.Error("external worker did not stop during cleanup")
		}
	})
	return worker
}

func dispatchEventsUntil(t *testing.T, a *MainAgent, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the session switch")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, err := a.nextEvent(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		a.dispatch(event)
	}
}

// assertExternalTaskSettledAsCancelled reads the registry persisted by the
// source session: the switch installs a new session and clears the in-memory
// records, so staying in memory would hide whether settlement happened first.
func assertExternalTaskSettledAsCancelled(t *testing.T, a *MainAgent, sessionDir string, worker *externalWorker) {
	t.Helper()
	if a.subs.runtime(worker.instanceID) != nil {
		t.Fatal("external worker runtime survived the session switch")
	}
	records, err := loadDurableTaskRecords(sessionDir)
	if err != nil {
		t.Fatalf("load persisted task records: %v", err)
	}
	record := records[worker.taskID]
	if record == nil {
		t.Fatalf("external task record is missing from %s", sessionDir)
	}
	if record.State != string(SubAgentStateCancelled) {
		t.Fatalf("external task state = %q, want cancelled; record=%+v", record.State, record)
	}
	if !isTerminalSubAgentState(SubAgentState(record.State)) || !record.RuntimeParked {
		t.Fatalf("external task is not terminally parked: %+v", record)
	}
}

func TestSessionTakeoverStopsExternalWorkerBeforeSwitch(t *testing.T) {
	a := newReadyTestMainAgent(t)
	script := writeFakeClaude(t, "exec sleep 60\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	installTakeoverTestLock(t, a)
	path, id := createPausedTakeoverTarget(t, a, "paused-target")
	sourceDir := a.SessionDir()
	worker := createTestExternalWorker(t, a)

	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: context.Background(), sessionID: id.SessionID, identity: id, reply: reply})
	if a.SessionDir() == path {
		t.Fatal("takeover switched before the external worker settled")
	}
	dispatchEventsUntil(t, a, func() bool {
		select {
		case err := <-reply:
			if err != nil {
				t.Fatalf("takeover error: %v", err)
			}
			return true
		default:
			return false
		}
	})
	if a.SessionDir() != path || !a.sessionExecutionPaused() {
		t.Fatal("takeover did not activate the paused target")
	}
	assertExternalTaskSettledAsCancelled(t, a, sourceDir, worker)
	if a.admissionPaused.Load() {
		t.Fatal("takeover left admission paused")
	}
}

func TestSessionTakeoverRejectsUnprovenExternalTermination(t *testing.T) {
	a := newReadyTestMainAgent(t)
	// The fake agent exits by itself, so the worker finishes before the
	// takeover; the test then hides the proof that its process exited.
	script := writeFakeClaude(t, "exit 0\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	installTakeoverTestLock(t, a)
	oldPath := a.SessionDir()
	_, id := createPausedTakeoverTarget(t, a, "paused-target")
	worker := createTestExternalWorker(t, a)
	select {
	case <-worker.done:
	case <-time.After(10 * time.Second):
		t.Fatal("external worker did not finish")
	}
	worker.promptMu.Lock()
	worker.processEnded = false
	worker.promptMu.Unlock()

	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: context.Background(), sessionID: id.SessionID, identity: id, reply: reply})
	select {
	case err := <-reply:
		if err == nil {
			t.Fatal("takeover accepted an unproven external termination")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("takeover did not report the unproven termination")
	}
	if a.SessionDir() != oldPath || a.admissionPaused.Load() {
		t.Fatal("rejected takeover changed session state")
	}
}

func TestPlanExecutionStopsExternalWorkerBeforeSwitch(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := filepath.Join(projectRoot, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n- task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, projectRoot)
	script := writeFakeClaude(t, "exec sleep 60\n")
	a.agentConfigs = map[string]*config.AgentConfig{
		"builder": {Name: "builder", Mode: config.AgentModeMain, Description: "Builder role"},
		"external-worker": {
			Name:       "external-worker",
			Mode:       config.AgentModeSubAgent,
			Backend:    &config.BackendConfig{Kind: config.BackendKindClaudeCode, Command: script},
			Permission: parsePermissionNode(t, `"*": allow`),
		},
	}
	a.activeConfig = &config.AgentConfig{Name: "builder", Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: 2}}
	a.rebuildRuleset()
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	oldDir := a.SessionDir()
	worker := createTestExternalWorker(t, a)

	a.startPlanExecution(planPath, "builder")
	if a.SessionDir() != oldDir {
		t.Fatal("plan execution switched before the external worker settled")
	}
	dispatchEventsUntil(t, a, func() bool { return a.SessionDir() != oldDir })
	assertExternalTaskSettledAsCancelled(t, a, oldDir, worker)
}

func TestHandoffApproveRejectsUnprovenExternalTermination(t *testing.T) {
	projectRoot := t.TempDir()
	planPath := writePlanFile(t, projectRoot)
	a := newTestMainAgent(t, projectRoot)
	prepareExecutableHandoffAgent(t, a)
	script := writeFakeClaude(t, "exit 0\n")
	a.agentConfigs["external-worker"] = &config.AgentConfig{
		Name:       "external-worker",
		Mode:       config.AgentModeSubAgent,
		Backend:    &config.BackendConfig{Kind: config.BackendKindClaudeCode, Command: script},
		Permission: parsePermissionNode(t, `"*": allow`),
	}
	a.activeConfig = &config.AgentConfig{Name: "planner", Mode: config.AgentModeMain, Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: 2}}
	a.rebuildRuleset()
	setupHandoffTurn(t, a, planPath)
	worker := createTestExternalWorker(t, a)
	select {
	case <-worker.done:
	case <-time.After(10 * time.Second):
		t.Fatal("external worker did not finish")
	}
	worker.promptMu.Lock()
	worker.processEnded = false
	worker.promptMu.Unlock()

	sessionDir := a.SessionDir()
	requestID := a.pendingHandoff.RequestID
	a.handleHandoffResolveEvent(Event{Type: EventHandoffResolve, Payload: &handoffResolvePayload{
		RequestID: requestID,
		Action:    handoffResolveApprove,
		AgentName: "builder",
	}})
	if a.pendingHandoff != nil {
		t.Fatalf("pendingHandoff = %#v, want cleared after the rejected execution switch", a.pendingHandoff)
	}
	if a.SessionDir() != sessionDir {
		t.Fatal("handoff switched sessions over an unproven external termination")
	}
	if a.admissionPaused.Load() {
		t.Fatal("rejected handoff left admission paused")
	}
}

func TestSessionPauseRejectsLiveExternalWorker(t *testing.T) {
	a := newReadyTestMainAgent(t)
	script := writeFakeClaude(t, "exec sleep 60\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	lock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	a.SetSessionLock(lock)
	startMainAgentLoopForTest(t, a)
	worker := createTestExternalWorker(t, a)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = a.BeginSessionPause(ctx, SessionPauseRequest{Identity: testPauseIdentity(a)})
	if err == nil {
		t.Fatal("pause accepted a live external worker")
	}
	if pauseErr, ok := errors.AsType[*recovery.SessionPauseError](err); !ok || pauseErr.Code != "target_not_ready" {
		t.Fatalf("pause error = %v, want target_not_ready", err)
	}
	select {
	case <-worker.done:
		t.Fatal("pause request stopped the external worker")
	default:
	}
	if a.subs.runtime(worker.instanceID) == nil {
		t.Fatal("rejected pause removed the live external worker")
	}
	a.pause.inputMu.Lock()
	inputClosed := a.pause.inputClosed
	a.pause.inputMu.Unlock()
	if inputClosed {
		t.Fatal("rejected pause closed input admission")
	}
}

// TestAbandonedStartedExternalWorkerStillSettles pins the delegation × switch
// race: a worker that reached abandonSubAgentsForSessionSwitch started and
// still running (a delegation admitted concurrently with the switch) must
// still settle and release its runtime slot through the close event, which
// carries the worker after the registry lookup no longer finds it.
func TestAbandonedStartedExternalWorkerStillSettles(t *testing.T) {
	a := newReadyTestMainAgent(t)
	script := writeFakeClaude(t, "exec sleep 60\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	sourceDir := a.SessionDir()
	worker := createTestExternalWorker(t, a)
	if !worker.slotHeld() {
		t.Fatal("external worker does not hold a runtime slot")
	}

	a.abandonSubAgentsForSessionSwitch()

	dispatchEventsUntil(t, a, func() bool {
		return !worker.slotHeld() && a.subs.runtime(worker.instanceID) == nil
	})
	assertExternalTaskSettledAsCancelled(t, a, sourceDir, worker)
}

// TestCloseAfterSessionSwitchReleasesSlotWithoutSettling pins the epoch guard:
// a close that arrives after another switch replaced the session must release
// the runtime slot instead of leaking it, and must not settle the old task
// into the new session's in-memory registry.
func TestCloseAfterSessionSwitchReleasesSlotWithoutSettling(t *testing.T) {
	a := newReadyTestMainAgent(t)
	script := writeFakeClaude(t, "exit 0\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "finish before the switch",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	worker, ok := a.subs.runtime(handle.AgentID).(*externalWorker)
	if !ok || worker == nil {
		t.Fatalf("runtime is not an externalWorker")
	}
	select {
	case <-worker.done:
	case <-time.After(10 * time.Second):
		t.Fatal("external worker did not finish")
	}
	if !worker.slotHeld() {
		t.Fatal("external worker does not hold a runtime slot before the close")
	}
	// Simulate the session that owned the worker having been replaced.
	worker.sessionEpoch++

	a.closeExternalWorker(worker, SubAgentStateCancelled, "terminated on session switch", "terminated on session switch", nil, 0, false)

	if worker.slotHeld() {
		t.Fatal("runtime slot leaked for a worker closed after its session was replaced")
	}
	if record := a.taskRecordByTaskID(handle.TaskID); record != nil && isTerminalSubAgentState(SubAgentState(record.State)) {
		t.Fatalf("replaced-session close settled the old task in the new session: %+v", record)
	}
}
