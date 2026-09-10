package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// spooledTestRecord is one decoded mailbox.jsonl row together with the byte
// span it occupies. start/end are produced the same way indexSpooledMailbox
// derives its spans (json.Decoder.InputOffset), so a rebuilt index is expected
// to map each id to exactly this span.
type spooledTestRecord struct {
	msg   SubAgentMailboxMessage
	start int64
	end   int64
	raw   []byte
}

// scanSpoolLog decodes every record in the mailbox log and returns each with
// its byte span, in file order.
func scanSpoolLog(t *testing.T, path string) []spooledTestRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	var records []spooledTestRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	var offset int64
	for {
		var msg SubAgentMailboxMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode spool record at offset %d: %v", offset, err)
		}
		end := dec.InputOffset()
		records = append(records, spooledTestRecord{msg: msg, start: offset, end: end, raw: data[offset:end]})
		offset = end
	}
	return records
}

// decodeSingleRecord decodes exactly one mailbox record from raw and fails if
// raw holds a second record (which is what a span recorded too long, covering
// the next row, would look like).
func decodeSingleRecord(t *testing.T, label string, raw []byte) SubAgentMailboxMessage {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	var msg SubAgentMailboxMessage
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("%s: decode span %q: %v", label, raw, err)
	}
	if over := dec.Decode(&SubAgentMailboxMessage{}); over != io.EOF {
		t.Fatalf("%s: span %q holds more than one record (overrun = %v)", label, raw, over)
	}
	return msg
}

// TestSpooledMailboxStaleIndexRejectsMisalignedEntry pins the ready guard
// reloadSpooledMailbox applies to a stale index: when spoolIndexReady is false
// — a persist or rollback invalidated the rebuild while it read the log — no
// entry may be trusted, even one already keyed by the requested id. The refusal
// comes from that !spoolIndexReady short-circuit, not from detecting that an
// entry's span covers another record, and it reports a stale error — neither a
// record nor a clean not-found — so the queueing callers keep the id queued.
//
// The decode check below keeps the poisoned entry honest: the span planted
// under the target id really does decode to the decoy record, so trusting the
// stale entry would hand the caller the decoy under the target id. That is the
// rationale for the guard, not a separate misalignment detection path.
//
// The state is assembled directly rather than by racing an append: in a single
// goroutine loadSpooledMailbox always refreshes the index to ready before it
// reads, so the stale branch is only reachable through the timing window a
// concurrent write opens. reloadSpooledMailbox is the exact lookup + decision
// loadSpooledMailbox runs after that refresh, so exercising it against the
// poisoned index tests the real guard without the races.
func TestSpooledMailboxStaleIndexRejectsMisalignedEntry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	target := SubAgentMailboxMessage{MessageID: "queued-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Summary: "target payload"}
	decoy := SubAgentMailboxMessage{MessageID: "other-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Summary: "decoy payload"}
	if err := a.persistSubAgentMailboxMessage(target); err != nil {
		t.Fatalf("persist target: %v", err)
	}
	if err := a.persistSubAgentMailboxMessage(decoy); err != nil {
		t.Fatalf("persist decoy: %v", err)
	}
	if err := a.indexSpooledMailbox(path); err != nil {
		t.Fatalf("indexSpooledMailbox: %v", err)
	}

	a.subAgentMailboxIDsMu.Lock()
	targetSpan := a.subAgentInbox.spoolIndex[target.MessageID]
	decoySpan := a.subAgentInbox.spoolIndex[decoy.MessageID]
	// The append race leaves the target id mapped to the decoy record's span
	// while a concurrent append keeps the rebuild unpublished.
	a.subAgentInbox.spoolIndex[target.MessageID] = decoySpan
	a.subAgentInbox.spoolIndexReady = false
	a.subAgentMailboxIDsMu.Unlock()

	// The poisoned span really does decode to another record, so trusting it
	// would hand the caller the decoy under the target id.
	if got, err := readSpooledMailboxAt(path, decoySpan); err != nil || got.MessageID != decoy.MessageID || got.Summary != decoy.Summary {
		t.Fatalf("decoy span = (%#v, %v), want the %q record", got, err, decoy.MessageID)
	}

	msg, found, err := a.reloadSpooledMailbox(path, target.MessageID)
	if err == nil {
		t.Fatalf("stale reload = (%#v, %v, nil), want a stale error", msg, found)
	}
	if msg != nil || found {
		t.Fatalf("stale reload returned a message: (%#v, %v, %v), want refusal", msg, found, err)
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale reload error = %v, want a stale-index error", err)
	}

	// A trusted miss is a clean not-found: it must not be reported as stale,
	// which is what the queueing callers key their retry-vs-drop choice on.
	a.subAgentMailboxIDsMu.Lock()
	a.subAgentInbox.spoolIndexReady = true
	a.subAgentMailboxIDsMu.Unlock()
	if msg, found, err := a.reloadSpooledMailbox(path, "never-persisted"); err != nil || found || msg != nil {
		t.Fatalf("trusted miss = (%#v, %v, %v), want clean not-found", msg, found, err)
	}

	// With a trusted index the target resolves to its own record, not the
	// decoy whose span was planted above.
	a.subAgentMailboxIDsMu.Lock()
	a.subAgentInbox.spoolIndex[target.MessageID] = targetSpan
	a.subAgentMailboxIDsMu.Unlock()
	msg, found, err = a.reloadSpooledMailbox(path, target.MessageID)
	if err != nil || !found || msg == nil || msg.MessageID != target.MessageID || msg.Summary != target.Summary {
		t.Fatalf("trusted reload = (%#v, %v, %v), want the target record", msg, found, err)
	}

	// The public load path, which rebuilds over the live log, also resolves the
	// target id to its own record.
	loaded, found, err := a.loadSpooledMailbox(target.MessageID)
	if err != nil || !found || loaded == nil || loaded.MessageID != target.MessageID || loaded.Summary != target.Summary {
		t.Fatalf("loadSpooledMailbox = (%#v, %v, %v), want the target record", loaded, found, err)
	}
}

// TestSpooledMailboxStaleReloadRefusedThenDequeueDeliversOnce pins two
// properties of a refused stale reload. First, a reload against a stale index
// refuses the hit and consumes no queue entry: the check calls
// reloadSpooledMailbox directly, so the queued id is untouched by construction
// rather than by requeue-on-error logic. Second, the real dequeue path delivers
// the queued id exactly once: it rebuilds the index to ready over the current
// log, resolves the target to its own record, drains the queue, and a second
// dequeue returns nil.
//
// It deliberately does not cover requeue-on-error (dequeueSpooledMailboxQueue
// putting a failed id back at the front): that needs a reload error reached
// through dequeue, and here dequeue rebuilds the index to ready before it
// reloads, so it never hits the stale branch.
// TestSpooledMailboxReadFailureRetainsQueueForRetry covers requeue-on-error
// with a plain read failure.
func TestSpooledMailboxStaleReloadRefusedThenDequeueDeliversOnce(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	target := SubAgentMailboxMessage{MessageID: "queued-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Summary: "target payload"}
	decoy := SubAgentMailboxMessage{MessageID: "other-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Summary: "decoy payload"}
	if err := a.persistSubAgentMailboxMessage(target); err != nil {
		t.Fatalf("persist target: %v", err)
	}
	if err := a.persistSubAgentMailboxMessage(decoy); err != nil {
		t.Fatalf("persist decoy: %v", err)
	}
	if err := a.indexSpooledMailbox(path); err != nil {
		t.Fatalf("indexSpooledMailbox: %v", err)
	}

	a.subAgentMailboxIDsMu.Lock()
	decoySpan := a.subAgentInbox.spoolIndex[decoy.MessageID]
	// The producer queued the target id, and the append race left the index
	// stale with the target mapped to another record's span.
	a.subAgentInbox.spoolNormal = append(a.subAgentInbox.spoolNormal, target.MessageID)
	a.subAgentInbox.spoolIndex[target.MessageID] = decoySpan
	a.subAgentInbox.spoolIndexReady = false
	a.subAgentMailboxIDsMu.Unlock()

	// This is the lookup decision the queue relies on: a stale index makes the
	// reload refuse the hit instead of returning the decoy or reporting the id
	// missing. The call is direct, so it consumes no queue entry — the check
	// below pins that, not the queue's requeue-on-error path.
	if msg, found, err := a.reloadSpooledMailbox(path, target.MessageID); err == nil || msg != nil || found {
		t.Fatalf("stale reload = (%#v, %v, %v), want a stale error with no message", msg, found, err)
	}
	a.subAgentMailboxIDsMu.Lock()
	queued := append([]string(nil), a.subAgentInbox.spoolNormal...)
	a.subAgentMailboxIDsMu.Unlock()
	if len(queued) != 1 || queued[0] != target.MessageID {
		t.Fatalf("spool queue = %#v, want the target id retained", queued)
	}

	// The next dequeue rebuilds the index over the current log and delivers the
	// target record exactly once.
	got := a.dequeueSpooledSubAgentMailbox()
	if got == nil || got.MessageID != target.MessageID || got.Summary != target.Summary {
		t.Fatalf("dequeue after rebuild = %#v, want the target record", got)
	}
	a.subAgentMailboxIDsMu.Lock()
	remaining := append([]string(nil), a.subAgentInbox.spoolNormal...)
	a.subAgentMailboxIDsMu.Unlock()
	if len(remaining) != 0 {
		t.Fatalf("spool queue = %#v, want drained after delivery", remaining)
	}
	if again := a.dequeueSpooledSubAgentMailbox(); again != nil {
		t.Fatalf("second dequeue = %#v, want nil (no duplicate delivery)", again)
	}
}

// TestSpooledMailboxIndexSpansDecodeEachRecordExactly pins the span invariant
// indexSpooledMailbox builds: every indexed id maps to a byte span that decodes
// to that exact record and to no other, and the spans tile the log with no gap
// or overlap. A reload returns whatever its span decodes, so this is the
// property that keeps a reload from returning another message.
func TestSpooledMailboxIndexSpansDecodeEachRecordExactly(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	messages := []SubAgentMailboxMessage{
		{MessageID: "msg-1", AgentID: "worker-1", TaskID: "task-1", Kind: SubAgentMailboxKindCompleted, Summary: "one"},
		{MessageID: "msg-2", AgentID: "worker-2", TaskID: "task-2", Kind: SubAgentMailboxKindProgress, Summary: strings.Repeat("two", 40)},
		{MessageID: "msg-3", AgentID: "worker-3", TaskID: "task-3", Kind: SubAgentMailboxKindCompleted, Priority: SubAgentMailboxPriorityUrgent, Summary: "three"},
		{MessageID: "msg-4", AgentID: "worker-4", TaskID: "task-4", Kind: SubAgentMailboxKindBackgroundResult, Summary: strings.Repeat("four", 7), Payload: "payload-4"},
	}
	for _, msg := range messages {
		if err := a.persistSubAgentMailboxMessage(msg); err != nil {
			t.Fatalf("persist %s: %v", msg.MessageID, err)
		}
	}
	if err := a.indexSpooledMailbox(path); err != nil {
		t.Fatalf("indexSpooledMailbox: %v", err)
	}

	records := scanSpoolLog(t, path)
	if len(records) != len(messages) {
		t.Fatalf("spool log has %d records, want %d", len(records), len(messages))
	}
	var wantOffset int64
	for i, rec := range records {
		if rec.start != wantOffset {
			t.Fatalf("record %d starts at %d, want %d: spans must tile the log with no gap or overlap", i, rec.start, wantOffset)
		}
		wantOffset = rec.end
	}
	byID := make(map[string]spooledTestRecord, len(records))
	for _, rec := range records {
		byID[rec.msg.MessageID] = rec
	}

	a.subAgentMailboxIDsMu.Lock()
	index := make(map[string]mailboxSpoolLocation, len(a.subAgentInbox.spoolIndex))
	for id, loc := range a.subAgentInbox.spoolIndex {
		index[id] = loc
	}
	ready := a.subAgentInbox.spoolIndexReady
	a.subAgentMailboxIDsMu.Unlock()
	if !ready {
		t.Fatal("index is not ready after a rebuild")
	}
	if len(index) != len(messages) {
		t.Fatalf("index has %d entries, want %d", len(index), len(messages))
	}
	for _, want := range messages {
		loc, ok := index[want.MessageID]
		if !ok {
			t.Fatalf("index missing %q", want.MessageID)
		}
		rec := byID[want.MessageID]
		if loc.offset != rec.start || loc.length != rec.end-rec.start {
			t.Fatalf("index span for %q = (offset=%d length=%d), want (offset=%d length=%d)", want.MessageID, loc.offset, loc.length, rec.start, rec.end-rec.start)
		}
		got, err := readSpooledMailboxAt(path, loc)
		if err != nil {
			t.Fatalf("read indexed span for %q: %v", want.MessageID, err)
		}
		if got.MessageID != want.MessageID || got.Summary != want.Summary || got.Payload != want.Payload {
			t.Fatalf("indexed span for %q decoded %#v, want its own record", want.MessageID, got)
		}
		if single := decodeSingleRecord(t, want.MessageID, rec.raw); single.MessageID != want.MessageID {
			t.Fatalf("span for %q decoded record %q", want.MessageID, single.MessageID)
		}
		loaded, found, err := a.loadSpooledMailbox(want.MessageID)
		if err != nil || !found || loaded == nil || loaded.MessageID != want.MessageID || loaded.Summary != want.Summary {
			t.Fatalf("loadSpooledMailbox(%q) = (%#v, %v, %v), want its own record", want.MessageID, loaded, found, err)
		}
	}
}

// TestConcurrentSpoolAppendsRecordExactSpans runs appendSubAgentMailboxLog from
// many goroutines at once and, once they join, checks the byte span each call
// reported: every reported span must decode to exactly the message written to
// it, and the reported spans must tile the log with no gap or overlap. Running
// the stat/encode/stat window under spoolAppendMu is what makes that hold;
// without it another writer's bytes can land between the two size reads and the
// recorded span decodes a different record — the flaw that let a reload return
// someone else's message under a requested id.
//
// The assertions are exact and deterministic; only which interleaving an
// unserialized writer would lose is not, so this is a regression guard for the
// serialization rather than a guaranteed-broken-without-it reproduction. It
// also runs clean under -race, and the single-record tiling is what a dropped
// mutex breaks.
func TestConcurrentSpoolAppendsRecordExactSpans(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	// Create the log up front so every concurrent append opens an existing
	// file: without this the writers also race the directory creation, which is
	// unrelated to the span this test checks.
	if _, _, err := a.appendSubAgentMailboxLog(a.sessionDir, path, SubAgentMailboxMessage{MessageID: "warmup", TaskID: "task-0", Summary: "warmup"}); err != nil {
		t.Fatalf("warmup append: %v", err)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate warmup record: %v", err)
	}

	const writers = 8
	const perWriter = 64

	type reportedSpan struct {
		id         string
		start, end int64
	}
	var (
		mu    sync.Mutex
		spans []reportedSpan
		fail  error
	)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				msg := SubAgentMailboxMessage{
					MessageID: fmt.Sprintf("w-%d-%d", w, i),
					AgentID:   fmt.Sprintf("worker-%d", w),
					TaskID:    fmt.Sprintf("task-%d", w),
					Kind:      SubAgentMailboxKindCompleted,
					Summary:   strings.Repeat("x", 32+i),
				}
				start, end, err := a.appendSubAgentMailboxLog(a.sessionDir, path, msg)
				if err != nil {
					mu.Lock()
					if fail == nil {
						fail = err
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				spans = append(spans, reportedSpan{id: msg.MessageID, start: start, end: end})
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	mu.Lock()
	collected := append([]reportedSpan(nil), spans...)
	err := fail
	mu.Unlock()
	if err != nil {
		t.Fatalf("concurrent append failed: %v", err)
	}
	if len(collected) != writers*perWriter {
		t.Fatalf("recorded %d spans, want %d", len(collected), writers*perWriter)
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile(%s): %v", path, readErr)
	}

	sort.Slice(collected, func(i, j int) bool { return collected[i].start < collected[j].start })
	seen := make(map[string]struct{}, len(collected))
	var wantStart int64
	for i, s := range collected {
		if _, dup := seen[s.id]; dup {
			t.Fatalf("message %q recorded more than once", s.id)
		}
		seen[s.id] = struct{}{}
		if s.start != wantStart {
			t.Fatalf("span %d (%s) starts at %d, want %d: a recorded span skipped or overlapped another record", i, s.id, s.start, wantStart)
		}
		if s.end <= s.start || s.end > int64(len(data)) {
			t.Fatalf("span for %q = (%d,%d), outside the %d-byte log", s.id, s.start, s.end, len(data))
		}
		single := decodeSingleRecord(t, s.id, data[s.start:s.end])
		if single.MessageID != s.id {
			t.Fatalf("span for %q decoded record %q", s.id, single.MessageID)
		}
		wantStart = s.end
	}
	if wantStart != int64(len(data)) {
		t.Fatalf("recorded spans cover %d bytes, want the whole %d-byte log", wantStart, len(data))
	}
}
