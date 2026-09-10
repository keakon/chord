package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/logtest"
)

func TestPersistSubAgentMailboxMessageReportsOpenFailure(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	err := a.persistSubAgentMailboxMessage(SubAgentMailboxMessage{MessageID: "msg-1", TaskID: "task-1"})
	if err == nil {
		t.Fatal("persistSubAgentMailboxMessage() error = nil, want open failure")
	}
}

func TestMailboxMemoryBudgetSpoolsAndRehydratesCriticalMessages(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "first"}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "second"}
	a.enqueueSubAgentMailbox(first)
	a.enqueueSubAgentMailbox(second)

	if got := len(a.subAgentInbox.urgent); got != 1 {
		t.Fatalf("urgent memory messages = %d, want 1", got)
	}
	if got := a.subAgentInbox.spoolUrgent; len(got) != 1 || got[0] != second.MessageID {
		t.Fatalf("urgent spool = %#v, want [%q]", got, second.MessageID)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first dequeue = %#v", got)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("spooled dequeue = %#v", got)
	}
	stats := a.OrchestrationStats()
	if stats.MailboxSpoolQueued != 1 || stats.MailboxSpoolRehydrated != 1 {
		t.Fatalf("spool stats = %+v, want queued=1 rehydrated=1", stats)
	}
}

func TestSpooledMailboxReadFailureRetainsQueueForRetry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	a.enqueueSubAgentMailbox(first)
	a.enqueueSubAgentMailbox(second)
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first dequeue = %#v", got)
	}

	validSessionDir := a.sessionDir
	a.sessionDir = filepath.Join(t.TempDir(), "missing-session")
	if got := a.dequeueNextSubAgentMailbox(); got != nil {
		t.Fatalf("dequeue during read failure = %#v, want nil", got)
	}
	if got := a.subAgentInbox.spoolUrgent; len(got) != 1 || got[0] != second.MessageID {
		t.Fatalf("urgent spool after failure = %#v, want retained %q", got, second.MessageID)
	}

	a.sessionDir = validSessionDir
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("dequeue after recovery = %#v", got)
	}
}

func TestSpooledMailboxIndexRefreshesAfterAppend(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted}
	if err := a.persistSubAgentMailboxMessage(first); err != nil {
		t.Fatalf("persist first: %v", err)
	}
	a.subAgentInbox.spoolNormal = []string{first.MessageID}
	if got := a.dequeueSpooledSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first spooled dequeue = %#v", got)
	}
	if err := a.persistSubAgentMailboxMessage(second); err != nil {
		t.Fatalf("persist second: %v", err)
	}
	a.subAgentInbox.spoolNormal = []string{second.MessageID}
	if got := a.dequeueSpooledSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("second spooled dequeue = %#v", got)
	}
}

func TestPersistSubAgentMailboxMessageExtendsReadySpoolIndex(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	first := SubAgentMailboxMessage{MessageID: "msg-1", TaskID: "task-1", Summary: "first"}
	if err := a.persistSubAgentMailboxMessage(first); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	if err := a.indexSpooledMailbox(path); err != nil {
		t.Fatal(err)
	}
	second := SubAgentMailboxMessage{MessageID: "msg-2", TaskID: "task-2", Summary: "second"}
	if err := a.persistSubAgentMailboxMessage(second); err != nil {
		t.Fatal(err)
	}
	if !a.subAgentInbox.spoolIndexReady {
		t.Fatal("append invalidated a ready spool index")
	}
	location, ok := a.subAgentInbox.spoolIndex[second.MessageID]
	if !ok {
		t.Fatalf("spool index = %#v, want msg-2", a.subAgentInbox.spoolIndex)
	}
	loaded, err := readSpooledMailboxAt(path, location)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MessageID != second.MessageID || loaded.Summary != second.Summary {
		t.Fatalf("loaded message = %#v", loaded)
	}
}

func TestMailboxPersistenceFailureDefersCriticalDeliveryUntilRetrySucceeds(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	validSessionDir := a.sessionDir
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot
	msg := SubAgentMailboxMessage{
		AgentID:  "worker-1",
		TaskID:   "task-1",
		Kind:     SubAgentMailboxKindCompleted,
		Priority: SubAgentMailboxPriorityUrgent,
		Summary:  "done",
	}

	a.enqueueSubAgentMailbox(msg)
	if len(a.subAgentInbox.urgent) != 1 || !a.subAgentInbox.urgent[0].persistPending {
		t.Fatalf("urgent inbox = %#v, want pending durable message", a.subAgentInbox.urgent)
	}
	if a.stageNextSubAgentMailboxBatch() {
		t.Fatal("mailbox became deliverable while persistence still failed")
	}
	if len(a.subAgentInbox.urgent) != 1 {
		t.Fatalf("urgent inbox length = %d, want retained message", len(a.subAgentInbox.urgent))
	}

	a.sessionDir = validSessionDir
	if !a.stageNextSubAgentMailboxBatch() {
		t.Fatal("mailbox did not become deliverable after persistence recovered")
	}
	if a.activeSubAgentMailbox == nil || a.activeSubAgentMailbox.persistPending {
		t.Fatalf("active mailbox = %#v, want durable message", a.activeSubAgentMailbox)
	}
}

func TestMailboxAckFailureDoesNotMarkMessageConsumed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if err := a.markSubAgentMailboxConsumed("msg-1"); err == nil {
		t.Fatal("markSubAgentMailboxConsumed() error = nil, want open failure")
	}
	if a.isSubAgentMailboxConsumed("msg-1") {
		t.Fatal("mailbox was marked consumed despite ack persistence failure")
	}
}

func TestMailboxAckFailureRequeuesActiveMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot
	a.activeSubAgentMailboxes = []*SubAgentMailboxMessage{{
		MessageID: "msg-1",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Priority:  SubAgentMailboxPriorityUrgent,
	}}
	a.activeSubAgentMailbox = a.activeSubAgentMailboxes[0]
	a.activeSubAgentMailboxAck = true

	a.setIdleAndDrainPending()

	if len(a.subAgentInbox.urgent) != 1 || a.subAgentInbox.urgent[0].MessageID != "msg-1" {
		t.Fatalf("urgent inbox = %#v, want failed ack mailbox requeued", a.subAgentInbox.urgent)
	}
	if a.isSubAgentMailboxConsumed("msg-1") {
		t.Fatal("mailbox was marked consumed despite failed ack")
	}
}

func TestNotifyAckFailureRequeuesOutstandingMailbox(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateWaitingMain, "need decision")
	msg := SubAgentMailboxMessage{
		MessageID: "msg-1",
		AgentID:   sub.instanceID,
		TaskID:    sub.taskID,
		Kind:      SubAgentMailboxKindDecisionRequired,
		Priority:  SubAgentMailboxPriorityUrgent,
	}
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{msg}
	sub.setLastMailboxID(msg.MessageID)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue", "reply"); err == nil {
		t.Fatal("NotifySubAgent() error = nil, want ack persistence failure")
	}
	if len(a.subAgentInbox.urgent) != 1 || a.subAgentInbox.urgent[0].MessageID != msg.MessageID {
		t.Fatalf("urgent inbox = %#v, want outstanding mailbox restored", a.subAgentInbox.urgent)
	}
	if sub.State() != SubAgentStateWaitingMain {
		t.Fatalf("sub.State() = %q, want waiting_main", sub.State())
	}
	if held, _ := sub.slotState(); held {
		t.Fatal("worker retained slot after failed Notify")
	}
}

func TestNotifyDefersAckUntilPendingMailboxPersists(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	sub.setState(SubAgentStateWaitingMain, "need decision")
	msg := SubAgentMailboxMessage{
		MessageID:      "msg-1",
		AgentID:        sub.instanceID,
		TaskID:         sub.taskID,
		Kind:           SubAgentMailboxKindDecisionRequired,
		Priority:       SubAgentMailboxPriorityUrgent,
		persistPending: true,
	}
	a.subAgentInbox.urgent = []SubAgentMailboxMessage{msg}
	sub.setLastMailboxID(msg.MessageID)
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if _, err := a.NotifySubAgent(context.Background(), sub.taskID, "continue", "reply"); err == nil || !strings.Contains(err.Error(), "persist target mailbox") {
		t.Fatalf("NotifySubAgent error = %v, want pending mailbox persistence failure", err)
	}
	if a.isSubAgentMailboxConsumed(msg.MessageID) {
		t.Fatal("pending mailbox was acknowledged before message persistence")
	}
	if len(a.subAgentInbox.urgent) != 1 || !a.subAgentInbox.urgent[0].persistPending {
		t.Fatalf("urgent inbox = %#v, want pending mailbox retained", a.subAgentInbox.urgent)
	}
}

func TestMailboxSpoolPreservesFIFOAcrossMemoryRecovery(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	first := SubAgentMailboxMessage{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	second := SubAgentMailboxMessage{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	third := SubAgentMailboxMessage{MessageID: "msg-3", AgentID: "worker-3", TaskID: "task-3", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent}
	a.enqueueSubAgentMailbox(first)
	a.enqueueSubAgentMailbox(second)

	// Drain the in-memory prefix so budget frees up while msg-2 is still
	// spooled; a newer arrival must queue behind it instead of jumping ahead.
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != first.MessageID {
		t.Fatalf("first dequeue = %#v", got)
	}
	a.enqueueSubAgentMailbox(third)
	if got := len(a.subAgentInbox.urgent); got != 0 {
		t.Fatalf("urgent memory queue = %d entries, want 0 while older messages are spooled", got)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != second.MessageID {
		t.Fatalf("second dequeue = %#v, want spooled msg-2 before newer msg-3", got)
	}
	if got := a.dequeueNextSubAgentMailbox(); got == nil || got.MessageID != third.MessageID {
		t.Fatalf("third dequeue = %#v", got)
	}
}

func TestProgressMailboxRetainsEveryDurableUpdateWhenBudgetExhausted(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20
	a.mailboxDeliveryPaused.Store(true)

	progress := SubAgentMailboxMessage{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "step 1"}
	a.enqueueSubAgentMailbox(progress)
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-1" {
		t.Fatalf("initial progress = %#v", got)
	}

	// Every durable progress update remains queued even when the latest-status
	// map is still used by status rendering.
	update := SubAgentMailboxMessage{MessageID: "p-2", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "step 2"}
	a.enqueueSubAgentMailbox(update)
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-2" {
		t.Fatalf("replaced progress = %#v, want p-2", got)
	}

	// An update that no longer fits is kept in the durable-log fallback rather
	// than dropping it or replacing an earlier message.
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1
	oversized := SubAgentMailboxMessage{MessageID: "p-3", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: strings.Repeat("x", 256)}
	a.enqueueSubAgentMailbox(oversized)
	if got := a.subAgentInbox.progress["worker-1"]; got.MessageID != "p-3" {
		t.Fatalf("latest progress = %#v, want p-3", got)
	}
	if got := a.subAgentInbox.progressPending; len(got) != 2 || got[0] != "p-2" || got[1] != "p-3" {
		t.Fatalf("pending progress = %#v, want p-2,p-3", got)
	}
}

// TestTakeMainInboxProgressSnapshotsDeliversSpilledUpdateOnce pins the claim
// path when the memory budget is exhausted. The per-agent progress map is a
// latest-status view, not an owner of an undelivered update: a row that spilled
// to the durable fallback is still reachable through progressPending, so
// treating the map as a second delivery source re-emitted the same update the
// reload below already returned — twice in one batch. The same map row was
// never charged to the memory budget, so releasing it on the way out subtracted
// bytes that were never added and widened the budget for what was still queued.
func TestTakeMainInboxProgressSnapshotsDeliversSpilledUpdateOnce(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.mailboxDeliveryPaused.Store(true)
	// One in-memory slot: the urgent row takes it, so the progress row below
	// spills to the durable fallback and progressQueue stays empty.
	a.globalConfig.Orchestration.MailboxMemoryMessages = 1
	a.globalConfig.Orchestration.MailboxMemoryBytes = 1 << 20

	queued := SubAgentMailboxMessage{MessageID: "notice-1", AgentID: "worker-0", TaskID: "task-0", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "queued notice"}
	a.enqueueSubAgentMailbox(queued)
	progress := SubAgentMailboxMessage{MessageID: "progress-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "step 1"}
	a.enqueueSubAgentMailbox(progress)

	if got := a.subAgentInbox.progressPending; len(got) != 1 || got[0] != progress.MessageID {
		t.Fatalf("progressPending = %#v, want the spilled %q row", got, progress.MessageID)
	}
	if len(a.subAgentInbox.progressQueue) != 0 {
		t.Fatalf("progressQueue = %#v, want the progress row spilled to the durable fallback", a.subAgentInbox.progressQueue)
	}

	got := a.takeMainInboxProgressSnapshots()
	if len(got) != 1 || got[0].MessageID != progress.MessageID {
		t.Fatalf("progress snapshots = %#v, want exactly one %q row", got, progress.MessageID)
	}
	// The urgent row is the only charged message, so claiming the uncharged
	// spilled progress row must leave its charge intact.
	if len(a.subAgentInbox.urgent) != 1 {
		t.Fatalf("urgent queue = %#v, want the queued notice left in memory", a.subAgentInbox.urgent)
	}
	if want := mailboxMessageBytes(a.subAgentInbox.urgent[0]); a.subAgentInbox.memoryBytes != want {
		t.Fatalf("mailbox memory = %d bytes, want %d (the spilled progress row was never charged)", a.subAgentInbox.memoryBytes, want)
	}
	if len(a.subAgentInbox.progress) != 0 || len(a.subAgentInbox.progressPending) != 0 {
		t.Fatalf("progress map = %#v pending = %#v, want the claimed snapshot consumed", a.subAgentInbox.progress, a.subAgentInbox.progressPending)
	}
}

// TestConcurrentSpoolRebuildDoesNotDropQueuedMessage hammers the spool reload
// path from the two roles that race in production: producers append durable
// mailbox rows and queue their ids in the spool, a consumer dequeues and
// reloads each id from the mailbox log. Every spool enqueue invalidates the
// spool index, so each reload rebuilds it while producers keep appending; the
// rebuild detects the concurrent growth and stays stale. The P2 fix makes a
// lookup against that stale index report an error (so the consumer keeps the
// id queued for a retry) instead of reporting "not found" (which would drop
// the id after it was popped from the queue). The invariant checked here is
// that every successfully enqueued id is eventually reloaded and delivered —
// none is lost to the rebuild race.
func TestConcurrentSpoolRebuildDoesNotDropQueuedMessage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// The hammer only needs a writable mailbox log, so give it its own session
	// directory instead of relying on the agent's default and pre-create the
	// subagents directory the way every other mailbox test does. A zero-message
	// memory budget would force arrivals into the spool, but the producers here
	// enqueue directly anyway.
	a.sessionDir = filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	// Warm the mailbox log up synchronously: afterwards every producer append
	// opens an existing file (plain O_APPEND) and the consumer can always index
	// it, so the hammer exercises the append-during-rebuild race rather than
	// any first-creation bookkeeping.
	warmup := SubAgentMailboxMessage{
		MessageID: "warmup-0",
		AgentID:   "worker-1",
		TaskID:    "task-1",
		Kind:      SubAgentMailboxKindCompleted,
		Summary:   strings.Repeat("x", 256),
	}
	if err := a.persistSubAgentMailboxMessage(warmup); err != nil {
		t.Fatalf("warmup persist: %v", err)
	}
	if err := a.indexSpooledMailbox(filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")); err != nil {
		t.Fatalf("warmup index: %v", err)
	}

	const producers = 3
	const perProducer = 200

	var mu sync.Mutex
	enqueued := make(map[string]struct{})
	delivered := make(map[string]struct{})
	var persistErr error
	var producersWG sync.WaitGroup
	for g := range producers {
		producersWG.Add(1)
		go func(group int) {
			defer producersWG.Done()
			for i := range perProducer {
				id := fmt.Sprintf("spool-%d-%d", group, i)
				msg := SubAgentMailboxMessage{
					MessageID: id,
					AgentID:   "worker-1",
					TaskID:    "task-1",
					Kind:      SubAgentMailboxKindCompleted,
					Summary:   strings.Repeat("x", 256),
				}
				if err := a.persistSubAgentMailboxMessage(msg); err != nil {
					mu.Lock()
					if persistErr == nil {
						persistErr = err
					}
					mu.Unlock()
					return
				}
				a.subAgentMailboxIDsMu.Lock()
				a.spoolMailboxMessage(msg, false)
				a.subAgentMailboxIDsMu.Unlock()
				mu.Lock()
				enqueued[id] = struct{}{}
				mu.Unlock()
			}
		}(g)
	}
	done := make(chan struct{})
	var consumerWG sync.WaitGroup
	consumerWG.Go(func() {
		for {
			if msg := a.dequeueSpooledSubAgentMailbox(); msg != nil {
				mu.Lock()
				delivered[msg.MessageID] = struct{}{}
				mu.Unlock()
				continue
			}
			a.subAgentMailboxIDsMu.Lock()
			empty := len(a.subAgentInbox.spoolNormal) == 0
			a.subAgentMailboxIDsMu.Unlock()
			if !empty {
				continue
			}
			select {
			case <-done:
				return
			default:
				// Queue drained transiently while producers still enqueue;
				// yield so they can make progress.
				runtime.Gosched()
			}
		}
	})
	producersWG.Wait()
	close(done)
	consumerWG.Wait()

	mu.Lock()
	defer mu.Unlock()
	if persistErr != nil {
		t.Fatalf("producer persist failed: %v", persistErr)
	}
	a.subAgentMailboxIDsMu.Lock()
	remaining := append([]string(nil), a.subAgentInbox.spoolNormal...)
	a.subAgentMailboxIDsMu.Unlock()
	if len(delivered)+len(remaining) != len(enqueued) {
		t.Fatalf("spool accounting lost ids: enqueued=%d delivered=%d still-queued=%d", len(enqueued), len(delivered), len(remaining))
	}
	remained := make(map[string]struct{}, len(remaining))
	for _, id := range remaining {
		remained[id] = struct{}{}
	}
	for id := range enqueued {
		_, got := delivered[id]
		_, stillQueued := remained[id]
		if !got && !stillQueued {
			t.Fatalf("queued spool id %q was dropped by the reload race", id)
		}
		if got && stillQueued {
			t.Fatalf("queued spool id %q was both delivered and still queued", id)
		}
	}
}

// TestSpoolIndexPublishGateSkipsStaleGeneration locks in the spool index
// publish rule: a rebuild that snapshotted an older spoolWriteGen must not
// publish once a persist advanced the generation while it was reading the
// log, and an already-ready index is never overwritten by a later rebuild.
// Both halves of the guard are what keep a rebuild that raced an append from
// publishing an index that drops the appended id (which loadSpooledMailbox
// would then treat as not-found and the dequeue path would drop forever).
//
// The race is simulated rather than orchestrated: indexSpooledMailbox has no
// seam between reading the log and publishing, so this test snapshots the
// generation after the first persist, appends, and then attempts a publish
// carrying that stale snapshot — exactly the map a rebuild that read the log
// between the two appends would produce. The live append-during-rebuild race
// (no queued id ever dropped) is exercised end to end by
// TestConcurrentSpoolRebuildDoesNotDropQueuedMessage. Regression protection
// therefore depends on keeping this test aligned with the guard: dropping the
// generation comparison in publishSpooledMailboxIndex, or letting an append
// touch the log without bumping spoolWriteGen, fails the assertions below.
func TestSpoolIndexPublishGateSkipsStaleGeneration(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	msg := func(id string) SubAgentMailboxMessage {
		return SubAgentMailboxMessage{MessageID: id, TaskID: "task-1", Summary: id}
	}
	if err := a.persistSubAgentMailboxMessage(msg("msg-1")); err != nil {
		t.Fatalf("persist msg-1: %v", err)
	}
	a.subAgentMailboxIDsMu.Lock()
	writeGen := a.subAgentInbox.spoolWriteGen
	a.subAgentMailboxIDsMu.Unlock()
	if writeGen == 0 {
		t.Fatal("persist did not advance spoolWriteGen")
	}
	if err := a.persistSubAgentMailboxMessage(msg("msg-2")); err != nil {
		t.Fatalf("persist msg-2: %v", err)
	}

	// Rebuild started before msg-2's persist; its map covers only msg-1.
	stale := map[string]mailboxSpoolLocation{
		"msg-1": {offset: 0, length: 64},
	}
	a.publishSpooledMailboxIndex(stale, writeGen)
	a.subAgentMailboxIDsMu.Lock()
	ready := a.subAgentInbox.spoolIndexReady
	a.subAgentMailboxIDsMu.Unlock()
	if ready {
		t.Fatal("stale-generation index was published over a newer log")
	}

	// The next load-driven rebuild snapshots the current generation and
	// publishes an index that covers both messages.
	if err := a.indexSpooledMailbox(path); err != nil {
		t.Fatalf("indexSpooledMailbox: %v", err)
	}
	for _, id := range []string{"msg-1", "msg-2"} {
		loaded, found, err := a.loadSpooledMailbox(id)
		if err != nil || !found || loaded == nil || loaded.MessageID != id {
			t.Fatalf("load %s = (%#v, %v, %v), want found", id, loaded, found, err)
		}
	}

	// A rebuild that loses the publish race must not overwrite the winner.
	a.subAgentMailboxIDsMu.Lock()
	writeGen = a.subAgentInbox.spoolWriteGen
	a.subAgentMailboxIDsMu.Unlock()
	loser := map[string]mailboxSpoolLocation{
		"msg-1": {offset: 0, length: 64},
	}
	a.publishSpooledMailboxIndex(loser, writeGen)
	a.subAgentMailboxIDsMu.Lock()
	_, hasMsg2 := a.subAgentInbox.spoolIndex["msg-2"]
	a.subAgentMailboxIDsMu.Unlock()
	if !hasMsg2 {
		t.Fatal("a losing rebuild overwrote the published index")
	}
}

// TestPersistAndRollbackAdvanceSpoolWriteGeneration verifies the write-side
// bookkeeping the publish gate snapshots: every successful persist and every
// rollback advances spoolWriteGen exactly once under subAgentMailboxIDsMu, a
// quiescent rebuild does not advance it, and a rollback withdraws its row from
// both the log and the ready index so a later append self-registers at the
// truncated tail.
//
// These counts are the counter half of the publish gate above: publish only
// rejects a map whose snapshot generation is stale, so the exact bump-per-write
// asserted here is what staleness is measured against. A writer that touches
// mailbox.jsonl without advancing the generation is only safe while no index
// over that file is live — the restore-window log rewrite in
// compactSubAgentMailboxLogs is one such path; any new mutation of the log
// must advance spoolWriteGen exactly like the paths this test pins.
func TestPersistAndRollbackAdvanceSpoolWriteGeneration(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	if got := a.subAgentInbox.spoolWriteGen; got != 0 {
		t.Fatalf("initial spoolWriteGen = %d, want 0", got)
	}
	msg := SubAgentMailboxMessage{MessageID: "msg-1", TaskID: "task-1", Summary: "first"}
	offset, err := a.persistSubAgentMailboxMessageWithOffset(msg)
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if offset < 0 {
		t.Fatalf("persist offset = %d, want >= 0", offset)
	}
	if got := a.subAgentInbox.spoolWriteGen; got != 1 {
		t.Fatalf("spoolWriteGen after persist = %d, want 1", got)
	}
	if err := a.indexSpooledMailbox(path); err != nil {
		t.Fatalf("indexSpooledMailbox: %v", err)
	}
	if got := a.subAgentInbox.spoolWriteGen; got != 1 {
		t.Fatalf("spoolWriteGen after rebuild = %d, want 1", got)
	}

	a.rollbackSubAgentMailboxMessage(&msg, offset)
	if got := a.subAgentInbox.spoolWriteGen; got != 2 {
		t.Fatalf("spoolWriteGen after rollback = %d, want 2", got)
	}
	if loaded, found, err := a.loadSpooledMailbox("msg-1"); err != nil || found || loaded != nil {
		t.Fatalf("load rolled-back msg-1 = (%#v, %v, %v), want not found", loaded, found, err)
	}

	// The ready index survives the rollback; a fresh append self-registers at
	// the truncated tail instead of invalidating the index.
	second := SubAgentMailboxMessage{MessageID: "msg-2", TaskID: "task-1", Summary: "second"}
	if err := a.persistSubAgentMailboxMessage(second); err != nil {
		t.Fatalf("persist msg-2: %v", err)
	}
	if got := a.subAgentInbox.spoolWriteGen; got != 3 {
		t.Fatalf("spoolWriteGen after second persist = %d, want 3", got)
	}
	if loaded, found, err := a.loadSpooledMailbox("msg-2"); err != nil || !found || loaded == nil || loaded.Summary != "second" {
		t.Fatalf("load msg-2 = (%#v, %v, %v), want found", loaded, found, err)
	}
}

// TestMainInboxProgressRetryDefersPermanentlyMissingRow pins the two-stage
// retry on the durable progress fallback. A progress id whose row never appears
// in the mailbox log used to be requeued on every dispatch, so
// hasRunnableMailboxWork kept reporting pending progress (the main could never
// reach global idle) and each dispatch rescanned the whole log. After a bounded
// number of immediate reloads the id must move into the deferred retry set
// instead of being dropped silently, and only a later confirmed-missing reload
// reports the drop with a warning.
func TestMainInboxProgressRetryDefersPermanentlyMissingRow(t *testing.T) {
	var buf bytes.Buffer
	log.SetDefaultLogger(logtest.NewLogger(&buf, golog.DebugLevel))
	defer log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))

	a := newTestMainAgent(t, t.TempDir())
	const messageID = "progress-missing-1"
	const taskID = "task-missing-1"
	a.subAgentInbox.progressPending = []string{messageID}
	a.subAgentInbox.progressPendingAgent[messageID] = "worker-missing"
	a.subAgentInbox.progressPendingTask[messageID] = taskID

	// Stage 1: the id must survive exactly mainInboxProgressReloadMaxAttempts-1
	// immediate retries (a persistence race resolves on the next dispatch) and
	// then hand off to the deferred set instead of being requeued forever.
	deferredAfter := 0
	for take := 1; take <= 10; take++ {
		if got := a.takeMainInboxProgressSnapshots(); len(got) != 0 {
			t.Fatalf("take %d = %#v, want no snapshots while the durable row is missing", take, got)
		}
		if len(a.subAgentInbox.deferredProgress) > 0 {
			deferredAfter = take
			break
		}
		if got := a.subAgentInbox.progressPending; len(got) != 1 || got[0] != messageID {
			t.Fatalf("progressPending after take %d = %#v, want the missing id retried", take, got)
		}
	}
	if deferredAfter != mainInboxProgressReloadMaxAttempts {
		t.Fatalf("missing id deferred after %d take(s), want exactly mainInboxProgressReloadMaxAttempts=%d", deferredAfter, mainInboxProgressReloadMaxAttempts)
	}
	if len(a.subAgentInbox.progressPending) != 0 {
		t.Fatalf("progressPending = %#v, want the deferred id out of the immediate retry set", a.subAgentInbox.progressPending)
	}
	if _, ok := a.subAgentInbox.progressPendingAgent[messageID]; ok {
		t.Fatal("progressPendingAgent still holds the deferred id")
	}
	entry := a.subAgentInbox.deferredProgress[0]
	if entry.MessageID != messageID || entry.AgentID != "worker-missing" || entry.TaskID != taskID {
		t.Fatalf("deferred entry = %#v, want the message/agent/task identity preserved", entry)
	}
	if a.hasRunnableMailboxWork() {
		t.Fatal("hasRunnableMailboxWork() = true while the row is only deferred; the main must still reach global idle")
	}
	if !a.hasQueuedMailboxMessage(messageID) {
		t.Fatal("hasQueuedMailboxMessage() = false for a deferred id; a repeated event would double-queue it")
	}
	if !a.hasDeferredMailboxDeliveries() {
		t.Fatal("hasDeferredMailboxDeliveries() = false, want the sweep wake gate held open")
	}

	// Stage 2: the deferred attempt confirms the row is permanently missing
	// and reports the drop instead of retrying silently forever.
	drainAgentEvents(a.outputCh)
	a.subAgentInbox.deferredProgress[0].NextAttemptAt = time.Now().Add(-time.Second)
	a.retryDeferredMailboxDeliveries()
	if len(a.subAgentInbox.deferredProgress) != 0 {
		t.Fatalf("deferredProgress = %#v, want the confirmed-missing id removed", a.subAgentInbox.deferredProgress)
	}
	var dropped bool
	var toast bool
	for _, evt := range drainAgentEvents(a.outputCh) {
		switch e := evt.(type) {
		case MailboxDeliveryDroppedEvent:
			dropped = e.MessageID == messageID
		case ToastEvent:
			toast = e.Level == "warn" && e.Category == mailboxDeliveryDroppedToastCategory
		}
	}
	if !dropped {
		t.Fatal("no MailboxDeliveryDroppedEvent for the confirmed-missing progress row")
	}
	if !toast {
		t.Fatal("no warn toast for the confirmed-missing progress row")
	}
	if !strings.Contains(buf.String(), messageID) {
		t.Fatalf("log = %q, want a warning naming the dropped id %q", buf.String(), messageID)
	}
}

// TestStageMailboxBatchRollbackRestoresProgressOrder pins the rollback order of
// a staged batch. requeueSubAgentMailboxInMemory pushes each message to the
// front of its queue, so a claimed batch must be requeued in reverse for the
// queue order to match the order the batch was taken in; requeueing forward
// handed the main the whole progress backlog reversed on every persistence
// retry.
func TestStageMailboxBatchRollbackRestoresProgressOrder(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	claimed := []SubAgentMailboxMessage{
		{MessageID: "p-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "one"},
		{MessageID: "p-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindProgress, Summary: "two"},
		{MessageID: "p-3", AgentID: "worker-3", TaskID: "task-3", Kind: SubAgentMailboxKindProgress, Summary: "three"},
	}
	for _, msg := range claimed {
		msg.persistPending = true
		a.subAgentInbox.progressQueue = append(a.subAgentInbox.progressQueue, msg)
		a.subAgentInbox.progressPendingAgent[msg.MessageID] = msg.AgentID
	}

	// A non-directory persist target makes the first progress snapshot fail its
	// durability check, forcing the whole claimed batch to roll back.
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a.sessionDir = blockedRoot

	if a.stageNextSubAgentMailboxBatch() {
		t.Fatal("stageNextSubAgentMailboxBatch() = true, want the failed persistence to roll the batch back")
	}
	got := a.subAgentInbox.progressQueue
	if len(got) != len(claimed) {
		t.Fatalf("progressQueue = %#v, want all %d claimed snapshots requeued", got, len(claimed))
	}
	for i, want := range claimed {
		if got[i].MessageID != want.MessageID {
			t.Fatalf("progressQueue[%d] = %q, want %q (claim order must survive the rollback)", i, got[i].MessageID, want.MessageID)
		}
	}
}

// TestLoadDurableMailboxMessageReusesSpoolIndex pins that the durable mailbox
// reload builds and keeps the shared spool index instead of scanning
// mailbox.jsonl from the start on every call: after an index invalidation the
// load must leave a ready index holding the reloaded row.
func TestLoadDurableMailboxMessageReusesSpoolIndex(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	msg := SubAgentMailboxMessage{MessageID: "progress-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindProgress, Summary: "step 1"}
	if err := a.persistSubAgentMailboxMessage(msg); err != nil {
		t.Fatalf("persist progress row: %v", err)
	}
	// Invalidate the index the persist self-registered, so the reload must
	// rebuild it from the log.
	a.subAgentMailboxIDsMu.Lock()
	a.subAgentInbox.spoolIndexReady = false
	a.subAgentMailboxIDsMu.Unlock()

	loaded, found, err := a.loadDurableMailboxMessage("progress-1")
	if err != nil || !found || loaded == nil || loaded.Summary != "step 1" {
		t.Fatalf("loadDurableMailboxMessage = (%#v, %v, %v), want the persisted row", loaded, found, err)
	}
	a.subAgentMailboxIDsMu.Lock()
	ready := a.subAgentInbox.spoolIndexReady
	_, indexed := a.subAgentInbox.spoolIndex["progress-1"]
	a.subAgentMailboxIDsMu.Unlock()
	if !ready {
		t.Fatal("loadDurableMailboxMessage left the spool index stale; it must reuse the indexed loader")
	}
	if !indexed {
		t.Fatal("loadDurableMailboxMessage did not index the reloaded row")
	}
	// A missing row still reports not-found against the ready index.
	if _, found, err := a.loadDurableMailboxMessage("absent-1"); err != nil || found {
		t.Fatalf("missing row load = (found=%v, err=%v), want not found", found, err)
	}
}
