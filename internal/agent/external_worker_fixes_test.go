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

	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// newTestExternalRuntime registers a bare externalWorker as a live runtime so
// the shared lifecycle sweep can be exercised without launching a process.
func newTestExternalRuntime(t *testing.T, a *MainAgent, taskID string) *externalWorker {
	t.Helper()
	w := &externalWorker{
		instanceID:    "external-runtime-1",
		taskID:        taskID,
		parent:        a,
		semHeld:       true,
		toolPreWrites: make(map[string]externalToolPreWrite),
	}
	w.setState(SubAgentStateRunning, "running")
	a.subs.mu.Lock()
	a.subs.subAgents[w.instanceID] = w
	a.subs.mu.Unlock()
	t.Cleanup(func() {
		a.subs.mu.Lock()
		delete(a.subs.subAgents, w.instanceID)
		a.subs.mu.Unlock()
	})
	a.setTaskRecords(map[string]*DurableTaskRecord{
		taskID: {
			TaskID:           taskID,
			AgentDefName:     "external-worker",
			Attempt:          1,
			State:            string(SubAgentStateRunning),
			LatestInstanceID: w.instanceID,
			Depth:            1,
		},
	})
	return w
}

// TestExternalRuntimeSharesStallWatchdog pins M2: the Running-worker stall scan
// covers any live runtime, not just *SubAgent, so a silent external coding-agent
// worker raises the same owner alert (once per episode) as a SubAgent.
func TestExternalRuntimeSharesStallWatchdog(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	w := newTestExternalRuntime(t, a, "task-external-stall")
	a.mailboxDeliveryPaused.Store(true)
	w.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)

	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, "task-external-stall"); got != 1 {
		t.Fatalf("stall risk_alert count for external runtime = %d, want exactly 1", got)
	}

	// A second sweep during the same episode must not spam the owner.
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if got := countRiskAlertsForTask(a, "task-external-stall"); got != 1 {
		t.Fatalf("duplicate stall risk_alert count = %d, want 1 (one alert per episode)", got)
	}

	// Renewed activity closes the episode with a stall_resolved alert.
	w.markActivity()
	a.dispatch(Event{Type: EventSubAgentLifecycleSweep})
	dispatchQueuedEvents(t, a)
	if !taskHasStallResolvedAlert(a, "task-external-stall") {
		t.Fatalf("external runtime recovery did not produce the stall_resolved alert: %#v", a.subAgentInbox.urgent)
	}
}

// TestRunningRuntimeStallReasonExternalProbe pins the shared predicate used by
// both the sweep and the coordination snapshot for an external runtime.
func TestRunningRuntimeStallReasonExternalProbe(t *testing.T) {
	w := &externalWorker{}
	w.setState(SubAgentStateRunning, "running")

	// No held slot: not coordination-tracked work.
	if got := runningRuntimeStallReason(w, time.Now()); got != "" {
		t.Fatalf("stall reason without a held slot = %q, want empty", got)
	}
	w.semHeld = true

	// Fresh heartbeat: healthy.
	w.runtimeState.stateChangedAt = time.Now()
	if got := runningRuntimeStallReason(w, time.Now()); got != "" {
		t.Fatalf("stall reason for a fresh runtime = %q, want empty", got)
	}

	// Stale heartbeat: suspected stall.
	w.runtimeState.stateChangedAt = time.Now().Add(-coordinationSnapshotStallAfter - time.Minute)
	if got := runningRuntimeStallReason(w, time.Now()); got == "" {
		t.Fatal("stale external runtime not flagged as a suspected stall")
	}
}

// TestUserCancelStopsExternalWorker pins H1's second half: the user cancel must
// reach an external worker (whose ctx is not the turn's) and settle it
// cancelled by the user through the shared close handler.
func TestUserCancelStopsExternalWorker(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"44444444-4444-4444-8444-444444444444"}'
while IFS= read -r line; do
  sleep 60
done
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "wait until the user cancels",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if !a.interruptSubAgentTurnsForUserCancel() {
		t.Fatal("interruptSubAgentTurnsForUserCancel = false, want the live external worker reported")
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCancelled) {
		t.Fatalf("task state = %q, want cancelled (closed_reason=%q)", record.State, record.ClosedReason)
	}
	if record.ClosedReason != "stopped by user" {
		t.Fatalf("ClosedReason = %q, want \"stopped by user\"", record.ClosedReason)
	}
}

// TestExternalWorkerProgressThrottle pins M3: identical progress lines are
// dropped, and distinct lines within the interval accumulate until a burst
// release so the latest state stays visible under heavy tool traffic.
func TestExternalWorkerProgressThrottle(t *testing.T) {
	w := &externalWorker{}
	w.emitProgress("one")
	if w.lastProgress != "one" || w.progressItems != 0 {
		t.Fatalf("first progress = (%q, %d), want (one, 0)", w.lastProgress, w.progressItems)
	}

	w.emitProgress("one")
	if w.lastProgress != "one" || w.progressItems != 0 {
		t.Fatalf("duplicate progress = (%q, %d), want (one, 0) unchanged", w.lastProgress, w.progressItems)
	}

	for i := range externalProgressBurstItems - 1 {
		w.emitProgress(fmt.Sprintf("item-%d", i))
	}
	if w.progressItems != externalProgressBurstItems-1 || w.lastProgress != "one" {
		t.Fatalf("accumulated progress = (%q, %d), want (one, %d)", w.lastProgress, w.progressItems, externalProgressBurstItems-1)
	}
	w.emitProgress("item-final")
	if w.lastProgress != "item-final" || w.progressItems != 0 {
		t.Fatalf("burst release = (%q, %d), want (item-final, 0)", w.lastProgress, w.progressItems)
	}

	w.lastProgressAt = time.Now().Add(-externalProgressMinInterval - time.Second)
	w.emitProgress("later")
	if w.lastProgress != "later" || w.progressItems != 0 {
		t.Fatalf("interval release = (%q, %d), want (later, 0)", w.lastProgress, w.progressItems)
	}
}

// TestExternalToolDiffUsesAgentdiffLineNumbers pins L1: the external Edit card
// diff comes from the shared agentdiff helpers, so it carries the real unified
// hunk header and counts instead of a synthetic, line-number-off summary.
func TestExternalToolDiffUsesAgentdiffLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	w := &externalWorker{workDir: dir, toolPreWrites: make(map[string]externalToolPreWrite)}
	args, err := json.Marshal(map[string]string{"path": path, "old_string": "beta", "new_string": "BETA"})
	if err != nil {
		t.Fatalf("marshal edit args: %v", err)
	}
	w.captureToolPreWrite("tool-1", toolname.Edit, args)
	// The external CLI applies the change between tool_use and tool_result.
	if err := os.WriteFile(path, []byte("alpha\nBETA\ngamma\n"), 0o644); err != nil {
		t.Fatalf("apply edit: %v", err)
	}

	summary := w.buildToolDiff("tool-1")
	if !strings.Contains(summary.Text, "-beta") || !strings.Contains(summary.Text, "+BETA") {
		t.Fatalf("diff text = %q, want the beta→BETA hunk", summary.Text)
	}
	if !strings.Contains(summary.Text, "@@ -1,3 +1,3 @@") {
		t.Fatalf("diff text = %q, want the real hunk header with line numbers", summary.Text)
	}
	if summary.Added != 1 || summary.Removed != 1 {
		t.Fatalf("diff counts = (+%d, -%d), want (+1, -1)", summary.Added, summary.Removed)
	}
}

func TestExternalWorkerPersistsTerminalRecordWithoutEventLoop(t *testing.T) {
	// A shutdown that stops dispatching events must not leave a delivered
	// result recorded as running: restore-time normalization would then
	// misclassify the task as failed.
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"external done"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "finish without a dispatching event loop",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	runtime := a.subs.runtime(handle.AgentID)
	worker, ok := runtime.(*externalWorker)
	if !ok || worker == nil {
		t.Fatalf("runtime %T is not an externalWorker", runtime)
	}
	select {
	case <-worker.done:
	case <-time.After(10 * time.Second):
		t.Fatal("external worker did not finish")
	}

	records, err := loadDurableTaskRecords(a.SessionDir())
	if err != nil {
		t.Fatalf("load persisted task records: %v", err)
	}
	record := records[handle.TaskID]
	if record == nil {
		t.Fatalf("external task record is missing from %s", a.SessionDir())
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("persisted state = %q, want completed without the event loop; record=%+v", record.State, record)
	}
	if !record.RuntimeParked {
		t.Fatalf("persisted record is not parked: %+v", record)
	}
}
