package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/logtest"
)

// seedMailboxLog gives the session a mailbox log that exists but has no row for
// the ids under test, so a spool reload finds the index ready and reports the
// id as genuinely absent instead of failing to open the file.
func seedMailboxLog(t *testing.T, a *MainAgent) {
	t.Helper()
	warmup := SubAgentMailboxMessage{MessageID: "warmup-row", AgentID: "worker-warmup", TaskID: "task-warmup"}
	if err := a.persistSubAgentMailboxMessage(warmup); err != nil {
		t.Fatalf("persist warmup mailbox row: %v", err)
	}
}

// writeMalformedMailboxLog makes every durable reload of the session fail with a
// decode error, standing in for a corrupt or unreadable mailbox row.
func writeMalformedMailboxLog(t *testing.T, a *MainAgent) {
	t.Helper()
	dir := filepath.Join(a.sessionDir, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir subagents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mailbox.jsonl"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("write malformed mailbox log: %v", err)
	}
}

func firstMailboxDrop(events []AgentEvent) (MailboxDeliveryDroppedEvent, bool) {
	for _, evt := range events {
		if drop, ok := evt.(MailboxDeliveryDroppedEvent); ok {
			return drop, true
		}
	}
	return MailboxDeliveryDroppedEvent{}, false
}

// TestMainInboxProgressRetryCountsOnlyAttemptedIDs pins that a load failure
// while claiming the progress FIFO does not consume a retry attempt for the
// ids the claim never reached: only the failed id advances toward the deferred
// set, and the untouched tail is requeued with its previous count in order.
func TestMainInboxProgressRetryCountsOnlyAttemptedIDs(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	writeMalformedMailboxLog(t, a)
	ids := []string{"progress-1", "progress-2", "progress-3"}
	a.subAgentInbox.progressPending = append([]string(nil), ids...)
	for _, id := range ids {
		a.subAgentInbox.progressPendingAgent[id] = "worker-" + id
		a.subAgentInbox.progressPendingTask[id] = "task-" + id
	}

	if got := a.takeMainInboxProgressSnapshots(); len(got) != 0 {
		t.Fatalf("takeMainInboxProgressSnapshots() = %#v, want none while every row fails to load", got)
	}

	if got := a.subAgentInbox.progressPendingAttempts["progress-1"]; got != 1 {
		t.Fatalf("progress-1 attempts = %d, want 1 (the id actually attempted)", got)
	}
	for _, id := range ids[1:] {
		if got := a.subAgentInbox.progressPendingAttempts[id]; got != 0 {
			t.Fatalf("%s attempts = %d, want 0 (the claim never reached it)", id, got)
		}
	}
	if got := a.subAgentInbox.progressPending; len(got) != 3 || got[0] != ids[0] || got[1] != ids[1] || got[2] != ids[2] {
		t.Fatalf("progressPending = %#v, want the original FIFO order preserved", got)
	}
}

func firstWarnToast(events []AgentEvent) (ToastEvent, bool) {
	for _, evt := range events {
		if toast, ok := evt.(ToastEvent); ok && toast.Level == "warn" {
			return toast, true
		}
	}
	return ToastEvent{}, false
}

func dueDeferredRetry(messageID, agentID string) deferredMailboxRetry {
	return deferredMailboxRetry{
		MessageID:     messageID,
		AgentID:       agentID,
		TaskID:        "task-" + messageID,
		Attempts:      1,
		FirstFailedAt: time.Now().Add(-time.Minute),
		NextAttemptAt: time.Now().Add(-time.Second),
	}
}

// withActiveTurn installs a minimal in-flight turn so the deferred-retry gate
// can be observed, and returns a restore func that clears it.
func withActiveTurn(t *testing.T, a *MainAgent) func() {
	t.Helper()
	a.turnMu.Lock()
	a.turn = &Turn{ID: 1, Ctx: context.Background()}
	a.turnMu.Unlock()
	return func() {
		a.turnMu.Lock()
		a.turn = nil
		a.turnMu.Unlock()
	}
}

func TestDeferredMailboxDeliveryRecoversWhenRowBecomesVisible(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{dueDeferredRetry("progress-1", "worker-1")}
	msg := SubAgentMailboxMessage{MessageID: "progress-1", AgentID: "worker-1", TaskID: "task-progress-1", Kind: SubAgentMailboxKindProgress, Summary: "step 1"}
	if err := a.persistSubAgentMailboxMessage(msg); err != nil {
		t.Fatalf("persist progress row: %v", err)
	}
	drainAgentEvents(a.outputCh)

	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want the recovered id removed", a.subAgentInbox.deferredProgress)
	}
	if len(a.subAgentInbox.progressQueue) != 1 || a.subAgentInbox.progressQueue[0].MessageID != "progress-1" {
		t.Fatalf("progressQueue = %#v, want the recovered progress row requeued", a.subAgentInbox.progressQueue)
	}
	if !a.hasQueuedMailboxMessage("progress-1") {
		t.Fatal("hasQueuedMailboxMessage() = false after the deferred row recovered")
	}
	if !a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = false after the deferred row recovered into the progress FIFO")
	}
	if events := drainAgentEvents(a.outputCh); len(events) != 0 {
		t.Fatalf("events = %#v, want no drop reported for a recovered delivery", events)
	}
}

func TestDeferredMailboxDeliveryRestoresEnqueueOrder(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ids := []string{"progress-1", "progress-2", "progress-3"}
	for i, id := range ids {
		msg := SubAgentMailboxMessage{MessageID: id, AgentID: "worker-" + id, TaskID: "task-" + id, Kind: SubAgentMailboxKindProgress, Summary: "step"}
		if err := a.persistSubAgentMailboxMessage(msg); err != nil {
			t.Fatalf("persist %s: %v", id, err)
		}
		// Distinct first-failure times keep the deferred set's enqueue order
		// deterministic; all three are due now.
		a.subAgentInbox.deferredProgress = append(a.subAgentInbox.deferredProgress, deferredMailboxRetry{
			MessageID:     id,
			AgentID:       "worker-" + id,
			TaskID:        "task-" + id,
			Attempts:      1,
			FirstFailedAt: time.Now().Add(-time.Minute),
			NextAttemptAt: time.Now().Add(-time.Duration(3-i) * time.Second),
		})
	}

	a.retryDeferredMailboxDeliveries()

	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want every recovered id removed", a.subAgentInbox.deferredProgress)
	}
	queue := a.subAgentInbox.progressQueue
	if len(queue) != len(ids) {
		t.Fatalf("progressQueue = %#v, want the %d recovered rows", queue, len(ids))
	}
	for i, want := range ids {
		if queue[i].MessageID != want {
			t.Fatalf("progressQueue[%d] = %q, want %q (recovery must keep the enqueue order)", i, queue[i].MessageID, want)
		}
	}
}

func TestDeferredMailboxDeliveryReportsPermanentlyMissingRow(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{dueDeferredRetry("progress-missing", "worker-1")}
	drainAgentEvents(a.outputCh)

	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want the confirmed-missing id removed", a.subAgentInbox.deferredProgress)
	}
	events := drainAgentEvents(a.outputCh)
	drop, ok := firstMailboxDrop(events)
	if !ok || drop.MessageID != "progress-missing" {
		t.Fatalf("drop event = %#v (found=%v), want the missing message id", drop, ok)
	}
	toast, ok := firstWarnToast(events)
	if !ok || toast.Category != mailboxDeliveryDroppedToastCategory || toast.AgentID != "worker-1" {
		t.Fatalf("toast = %#v (found=%v), want a warn mailbox_delivery_dropped toast for the agent", toast, ok)
	}
	if !strings.Contains(toast.Message, "task-progress-missing") {
		t.Fatalf("toast = %q, want the known task id named", toast.Message)
	}
	lower := strings.ToLower(toast.Message)
	if strings.Contains(lower, "mailbox") || strings.Contains(lower, "replay") {
		t.Fatalf("toast = %q, must avoid the internal term and must not promise a replay", toast.Message)
	}
	if !strings.Contains(buf.String(), "progress-missing") {
		t.Fatalf("log = %q, want the dropped id named", buf.String())
	}
	if a.isSubAgentMailboxConsumed("progress-missing") {
		t.Fatal("the drop marked the message consumed; the durable row must be left for a later restore")
	}
}

func TestDeferredMailboxDeliveryKeepsReadErrorUntilWindowExpires(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	a := newTestMainAgent(t, t.TempDir())
	writeMalformedMailboxLog(t, a)
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{dueDeferredRetry("progress-1", "worker-1")}
	drainAgentEvents(a.outputCh)

	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 1 {
		t.Fatalf("deferredProgress = %#v, want the read-error entry retained inside the window", a.subAgentInbox.deferredProgress)
	}
	retained := a.subAgentInbox.deferredProgress[0]
	if retained.Attempts != 2 || !retained.NextAttemptAt.After(time.Now()) {
		t.Fatalf("retained entry = %#v, want a bumped attempt count and a future cooldown", retained)
	}
	if events := drainAgentEvents(a.outputCh); len(events) != 0 {
		if drop, ok := firstMailboxDrop(events); ok {
			t.Fatalf("drop event = %#v, want no drop while the retry window is still open", drop)
		}
	}

	// Past the window the delivery is abandoned and reported instead of
	// retrying a row that never becomes readable.
	a.subAgentInbox.deferredProgress[0].FirstFailedAt = time.Now().Add(-mainInboxDeferredRetryWindow - time.Minute)
	a.subAgentInbox.deferredProgress[0].NextAttemptAt = time.Now().Add(-time.Second)
	drainAgentEvents(a.outputCh)
	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want the window-expired entry removed", a.subAgentInbox.deferredProgress)
	}
	drop, ok := firstMailboxDrop(drainAgentEvents(a.outputCh))
	if !ok || drop.MessageID != "progress-1" {
		t.Fatalf("drop event = %#v (found=%v), want the window-expired id reported", drop, ok)
	}
	if !strings.Contains(buf.String(), "progress-1") {
		t.Fatalf("log = %q, want the abandoned id named", buf.String())
	}
}

func TestDeferredMailboxDeliveryWaitsForIdleMain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	msg := SubAgentMailboxMessage{MessageID: "progress-1", AgentID: "worker-1", TaskID: "task-progress-1", Kind: SubAgentMailboxKindProgress, Summary: "step 1"}
	if err := a.persistSubAgentMailboxMessage(msg); err != nil {
		t.Fatalf("persist progress row: %v", err)
	}
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{dueDeferredRetry("progress-1", "worker-1")}
	drainAgentEvents(a.outputCh)

	restore := withActiveTurn(t, a)
	// Staging also runs at the mid-turn request boundary (the turn-continuation
	// staging), so neither entry point may retry a deferred delivery while a
	// turn is in flight: the retry only runs between turns.
	a.stageNextSubAgentMailboxBatch()
	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 1 {
		t.Fatalf("deferredProgress = %#v, want the retry held while a turn is active", a.subAgentInbox.deferredProgress)
	}
	if len(a.subAgentInbox.progressQueue) != 0 {
		t.Fatalf("progressQueue = %#v, want nothing requeued mid-turn", a.subAgentInbox.progressQueue)
	}
	if drop, ok := firstMailboxDrop(drainAgentEvents(a.outputCh)); ok {
		t.Fatalf("drop event = %#v, want no report while a turn is active", drop)
	}

	restore()
	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want the retry resumed once the turn ended", a.subAgentInbox.deferredProgress)
	}
	if len(a.subAgentInbox.progressQueue) != 1 || a.subAgentInbox.progressQueue[0].MessageID != "progress-1" {
		t.Fatalf("progressQueue = %#v, want the row delivered after the turn ended", a.subAgentInbox.progressQueue)
	}
}

func TestDrainOwnedSubAgentMailboxesReportsMissingUnconsumedRow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedMailboxLog(t, a)
	a.subAgentMailboxIDsMu.Lock()
	a.ownedMailboxSpool = map[string][]string{"owner-1": {"missing-owned"}}
	a.subAgentMailboxIDsMu.Unlock()
	drainAgentEvents(a.outputCh)

	if progressed := a.drainOwnedSubAgentMailboxes("owner-1"); progressed {
		t.Fatal("drainOwnedSubAgentMailboxes() = true for a missing row")
	}
	drop, ok := firstMailboxDrop(drainAgentEvents(a.outputCh))
	if !ok || drop.MessageID != "missing-owned" {
		t.Fatalf("drop event = %#v (found=%v), want the missing owned row reported", drop, ok)
	}
	a.subAgentMailboxIDsMu.Lock()
	remaining := len(a.ownedMailboxSpool["owner-1"])
	a.subAgentMailboxIDsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("owned spool holds %d entries, want the missing row dropped", remaining)
	}
}

func TestDrainOwnedSubAgentMailboxesSkipsConsumedMissingRow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedMailboxLog(t, a)
	if err := a.markSubAgentMailboxConsumed("consumed-owned"); err != nil {
		t.Fatalf("mark consumed: %v", err)
	}
	a.subAgentMailboxIDsMu.Lock()
	a.ownedMailboxSpool = map[string][]string{"owner-1": {"consumed-owned"}}
	a.subAgentMailboxIDsMu.Unlock()
	drainAgentEvents(a.outputCh)

	a.drainOwnedSubAgentMailboxes("owner-1")
	if drop, ok := firstMailboxDrop(drainAgentEvents(a.outputCh)); ok {
		t.Fatalf("drop event = %#v, want no drop for an already-consumed row", drop)
	}
}

func TestStageMailboxBatchReportsMissingUnconsumedSpoolRow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedMailboxLog(t, a)
	a.subAgentInbox.spoolNormal = []string{"missing-main"}
	drainAgentEvents(a.outputCh)

	if staged := a.stageNextSubAgentMailboxBatch(); staged {
		t.Fatal("stageNextSubAgentMailboxBatch() = true for a missing spool row")
	}
	drop, ok := firstMailboxDrop(drainAgentEvents(a.outputCh))
	if !ok || drop.MessageID != "missing-main" {
		t.Fatalf("drop event = %#v (found=%v), want the missing main-inbox row reported", drop, ok)
	}
	if len(a.subAgentInbox.spoolNormal) != 0 {
		t.Fatalf("spoolNormal = %#v, want the missing row dropped", a.subAgentInbox.spoolNormal)
	}
}

func TestStageMailboxBatchSkipsConsumedMissingSpoolRow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	seedMailboxLog(t, a)
	if err := a.markSubAgentMailboxConsumed("consumed-main"); err != nil {
		t.Fatalf("mark consumed: %v", err)
	}
	a.subAgentInbox.spoolNormal = []string{"consumed-main"}
	drainAgentEvents(a.outputCh)

	a.stageNextSubAgentMailboxBatch()
	if drop, ok := firstMailboxDrop(drainAgentEvents(a.outputCh)); ok {
		t.Fatalf("drop event = %#v, want no drop for an already-consumed row", drop)
	}
	if len(a.subAgentInbox.spoolNormal) != 0 {
		t.Fatalf("spoolNormal = %#v, want the consumed row dropped", a.subAgentInbox.spoolNormal)
	}
}

func TestRemoveSubAgentMailboxStateFiltersDeferredRetries(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{
		{MessageID: "progress-1", AgentID: "worker-1", NextAttemptAt: time.Now().Add(time.Minute)},
		{MessageID: "progress-2", AgentID: "worker-2", NextAttemptAt: time.Now().Add(time.Minute)},
	}

	a.removeSubAgentMailboxState("worker-1")

	if len(a.subAgentInbox.deferredProgress) != 1 || a.subAgentInbox.deferredProgress[0].MessageID != "progress-2" {
		t.Fatalf("deferredProgress = %#v, want only the closing agent's entry removed", a.subAgentInbox.deferredProgress)
	}
}

func TestResetSubAgentMailboxRuntimeClearsDeferredRetries(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{{MessageID: "progress-1", AgentID: "worker-1", NextAttemptAt: time.Now().Add(time.Minute)}}

	a.resetSubAgentMailboxRuntime()

	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want the session reset to clear the deferred retry set", a.subAgentInbox.deferredProgress)
	}
	if a.hasDeferredMailboxDeliveries() {
		t.Fatal("hasDeferredMailboxDeliveries() = true after the session reset")
	}
}

func TestHasSubAgentLifecycleSweepCandidatesCoversDeferredDeliveries(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalIdle.Store(true)
	if a.hasSubAgentLifecycleSweepCandidates() {
		t.Fatal("hasSubAgentLifecycleSweepCandidates() = true for a globally idle main with no deferred delivery")
	}
	a.subAgentInbox.deferredProgress = []deferredMailboxRetry{{MessageID: "progress-1", AgentID: "worker-1", NextAttemptAt: time.Now().Add(time.Minute)}}
	if !a.hasSubAgentLifecycleSweepCandidates() {
		t.Fatal("hasSubAgentLifecycleSweepCandidates() = false while a deferred delivery waits on its retry")
	}
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true for a deferred delivery; it must not block global idle")
	}
}
