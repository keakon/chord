package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/privatefs"
)

func (a *MainAgent) loadSpooledMailbox(messageID string) (*SubAgentMailboxMessage, bool, error) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || a.isSubAgentMailboxConsumed(messageID) {
		return nil, false, nil
	}
	path := filepath.Join(a.sessionDir, "subagents", "mailbox.jsonl")
	if err := a.indexSpooledMailbox(path); err != nil {
		return nil, false, err
	}
	return a.reloadSpooledMailbox(path, messageID)
}

// reloadSpooledMailbox resolves messageID against the current spool index and,
// on a trusted hit, decodes the indexed byte span. Callers must refresh the
// index with indexSpooledMailbox first. The lookup and its ready/hit/miss
// decision are split out of loadSpooledMailbox so the stale-hit case — a
// rebuild that a concurrent append or rollback invalidated while it read the
// log, leaving an entry that can point at a span covering another record — is
// testable without reproducing the timing window.
func (a *MainAgent) reloadSpooledMailbox(path, messageID string) (*SubAgentMailboxMessage, bool, error) {
	a.subAgentMailboxIDsMu.Lock()
	location, ok := a.subAgentInbox.spoolIndex[messageID]
	ready := a.subAgentInbox.spoolIndexReady
	a.subAgentMailboxIDsMu.Unlock()
	if !ready {
		// The rebuild above could not republish — a persist or rollback landed
		// while it read the log — so the index may be missing this id or still
		// hold an entry that points at another row. Report the reload as failed
		// so the queueing callers keep the id queued for a retry; the next load
		// rebuilds over the current log. A stale index is never authoritative,
		// even when the id already appears in it.
		return nil, false, fmt.Errorf("spool mailbox index stale for message_id=%v: a concurrent append invalidated the rebuild", messageID)
	}
	if !ok {
		return nil, false, nil
	}
	msg, err := readSpooledMailboxAt(path, location)
	if err != nil {
		return nil, false, err
	}
	a.orchestrationMetrics.mailboxSpoolRehydrated.Add(1)
	return &msg, true, nil
}

// indexSpooledMailbox rebuilds the spool index after a write left it stale
// (spoolIndexReady false). The index is shared with the append path that keeps
// it fresh under subAgentMailboxIDsMu, so the log is read outside the lock and
// the freshly built map is published under it. Writes bump spoolWriteGen
// under the same lock after they touch the log; the rebuild snapshots the
// counter before reading and publishes only when it is unchanged, so a
// persist or rollback that landed mid-read keeps the index stale and the next
// load rebuilds over the current log instead of publishing an index that
// drops the appended entry (see publishSpooledMailboxIndex).
func (a *MainAgent) indexSpooledMailbox(path string) error {
	a.subAgentMailboxIDsMu.Lock()
	ready := a.subAgentInbox.spoolIndexReady
	writeGen := a.subAgentInbox.spoolWriteGen
	a.subAgentMailboxIDsMu.Unlock()
	if ready {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open spooled mailbox: %w", err)
	}
	built := make(map[string]mailboxSpoolLocation)
	dec := json.NewDecoder(f)
	var offset int64
	for {
		var msg SubAgentMailboxMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			_ = f.Close()
			return fmt.Errorf("decode spooled mailbox: %w", err)
		}
		messageID := strings.TrimSpace(msg.MessageID)
		if messageID != "" {
			if _, exists := built[messageID]; !exists {
				built[messageID] = mailboxSpoolLocation{offset: offset, length: dec.InputOffset() - offset}
			}
		}
		offset = dec.InputOffset()
	}
	_ = f.Close()
	a.publishSpooledMailboxIndex(built, writeGen)
	return nil
}

// publishSpooledMailboxIndex publishes a freshly built spool index when no
// other rebuild won in the meantime and the write generation still matches
// the snapshot taken before the log was read. When either condition fails the
// current index is left as-is and spoolIndexReady stays false, so the next
// load rebuilds over the current log: publishing a map that missed an append
// (or kept a rolled-back row) would let loadSpooledMailbox treat a queued id
// as not-found and drop it from the spool forever.
func (a *MainAgent) publishSpooledMailboxIndex(built map[string]mailboxSpoolLocation, writeGen uint64) {
	a.subAgentMailboxIDsMu.Lock()
	defer a.subAgentMailboxIDsMu.Unlock()
	if a.subAgentInbox.spoolIndexReady {
		// Another rebuild finished first; its index is at least as fresh.
		return
	}
	if a.subAgentInbox.spoolWriteGen != writeGen {
		// A persist appended or a rollback truncated the log while this index
		// was being built. Stay stale so the next load rebuilds over the
		// current log.
		return
	}
	a.subAgentInbox.spoolIndex = built
	a.subAgentInbox.spoolIndexReady = true
}

func readSpooledMailboxAt(path string, location mailboxSpoolLocation) (SubAgentMailboxMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return SubAgentMailboxMessage{}, fmt.Errorf("open spooled mailbox message: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(location.offset, io.SeekStart); err != nil {
		return SubAgentMailboxMessage{}, fmt.Errorf("seek spooled mailbox message: %w", err)
	}
	var msg SubAgentMailboxMessage
	if err := json.NewDecoder(io.LimitReader(f, location.length)).Decode(&msg); err != nil {
		return SubAgentMailboxMessage{}, fmt.Errorf("decode spooled mailbox message: %w", err)
	}
	return msg, nil
}

func (a *MainAgent) dequeueSpooledSubAgentMailbox() *SubAgentMailboxMessage {
	return a.dequeueSpooledMailboxQueue(&a.subAgentInbox.spoolNormal)
}

// dequeueSpooledMailboxQueue claims one queued spool id at a time under
// subAgentMailboxIDsMu and reloads the message from the mailbox log outside
// the lock. A missing record that was already consumed is skipped; one whose
// index is ready but whose row really is gone is reported as a dropped
// delivery instead of being skipped silently. A failed reload puts the id back
// at the front so the next call retries it.
func (a *MainAgent) dequeueSpooledMailboxQueue(queue *[]string) *SubAgentMailboxMessage {
	for {
		a.subAgentMailboxIDsMu.Lock()
		if len(*queue) == 0 {
			a.subAgentMailboxIDsMu.Unlock()
			return nil
		}
		id := strings.TrimSpace((*queue)[0])
		*queue = (*queue)[1:]
		a.subAgentMailboxIDsMu.Unlock()
		msg, found, err := a.loadSpooledMailbox(id)
		if err != nil {
			log.Warnf("failed to reload spooled SubAgent mailbox message message_id=%v error=%v", id, err)
			a.subAgentMailboxIDsMu.Lock()
			*queue = append([]string{id}, (*queue)...)
			a.subAgentMailboxIDsMu.Unlock()
			return nil
		}
		if !found {
			if a.isSubAgentMailboxConsumed(id) {
				log.Debugf("skipping consumed spooled SubAgent mailbox message message_id=%v", id)
				continue
			}
			a.reportSubAgentMailboxDropped(id, "", "", "spooled main-inbox mailbox row is missing and not consumed")
			continue
		}
		return msg
	}
}

func (a *MainAgent) persistSubAgentMailboxMessage(msg SubAgentMailboxMessage) error {
	_, err := a.persistSubAgentMailboxMessageWithOffset(msg)
	return err
}

// persistSubAgentMailboxMessageWithOffset appends msg to the session's
// mailbox.jsonl and returns the byte offset the appended line starts at (-1
// when nothing was appended or the offset could not be determined), so a
// caller that decides the message must not survive can roll the line back
// (see rollbackSubAgentMailboxMessage) exactly like the guarded settlement
// journal rollback (see task_terminal.go). mailbox.jsonl is written only on
// the event-loop goroutine (or, at startup, before the loop runs), so the
// append and a later rollback are synchronous; a rollback must still run
// before any other append extends the file.
func (a *MainAgent) persistSubAgentMailboxMessageWithOffset(msg SubAgentMailboxMessage) (int64, error) {
	sessionDir := strings.TrimSpace(a.sessionDir)
	if sessionDir == "" {
		return -1, nil
	}
	path := filepath.Join(sessionDir, "subagents", "mailbox.jsonl")
	startOffset, endOffset, err := a.appendSubAgentMailboxLog(sessionDir, path, msg)
	if err != nil {
		a.subAgentMailboxIDsMu.Lock()
		// The failed write may still have extended the file; treat the log as
		// touched so an in-flight index rebuild does not publish over it.
		a.subAgentInbox.spoolWriteGen++
		a.subAgentInbox.spoolIndexReady = false
		a.subAgentMailboxIDsMu.Unlock()
		return -1, err
	}
	messageID := strings.TrimSpace(msg.MessageID)
	a.subAgentMailboxIDsMu.Lock()
	// The append is durable now; any index rebuild that started before it
	// must not publish a map built from the shorter log.
	a.subAgentInbox.spoolWriteGen++
	if a.subAgentInbox.spoolIndexReady && startOffset >= 0 && endOffset > startOffset && messageID != "" {
		if _, exists := a.subAgentInbox.spoolIndex[messageID]; !exists {
			a.subAgentInbox.spoolIndex[messageID] = mailboxSpoolLocation{offset: startOffset, length: endOffset - startOffset}
		}
	} else {
		a.subAgentInbox.spoolIndexReady = false
	}
	a.subAgentMailboxIDsMu.Unlock()
	return startOffset, nil
}

// appendSubAgentMailboxLog appends msg to the session mailbox log and returns
// the byte span the appended record occupies. The write runs under
// spoolAppendMu so the log size before and after it bounds exactly this record
// even when another goroutine appends concurrently; otherwise the recorded
// start would be a stale size and the recorded length would cover the
// interleaved rows, and a reload of this id would decode a different message.
func (a *MainAgent) appendSubAgentMailboxLog(sessionDir, path string, msg SubAgentMailboxMessage) (int64, int64, error) {
	a.spoolAppendMu.Lock()
	defer a.spoolAppendMu.Unlock()
	f, err := privatefs.OpenFile(sessionDir, path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return -1, -1, fmt.Errorf("open mailbox log: %w", err)
	}
	startOffset := int64(-1)
	if info, statErr := f.Stat(); statErr == nil {
		startOffset = info.Size()
	}
	if err := json.NewEncoder(f).Encode(msg); err != nil {
		_ = f.Close()
		return startOffset, -1, fmt.Errorf("append mailbox message: %w", err)
	}
	endOffset := int64(-1)
	if info, statErr := f.Stat(); statErr == nil {
		endOffset = info.Size()
	}
	if err := f.Close(); err != nil {
		return startOffset, endOffset, fmt.Errorf("close mailbox log: %w", err)
	}
	return startOffset, endOffset, nil
}

// rollbackSubAgentMailboxMessage truncates the mailbox.jsonl entry that
// persistSubAgentMailboxMessageWithOffset appended at offset, mirroring the
// guarded-settlement journal rollback (truncateTaskSettlementJournal in
// task_settlement.go). It withdraws a mailbox message (a WaitingMain expiry
// alert) whose guarded terminal settle backed off, so neither this process
// nor a restore after a crash can deliver an expiry that never happened. Both
// the append and the rollback run synchronously on the event-loop goroutine —
// the sole writer of the log — so the file still ends at the appended line.
// The message's idempotency mark is dropped with the line and its spool-index
// entry (the append was at the tail, so nothing indexed after it can exist)
// is removed.
func (a *MainAgent) rollbackSubAgentMailboxMessage(msg *SubAgentMailboxMessage, offset int64) {
	if msg == nil || offset < 0 {
		return
	}
	sessionDir := strings.TrimSpace(a.sessionDir)
	messageID := strings.TrimSpace(msg.MessageID)
	if sessionDir == "" || messageID == "" {
		return
	}
	a.spoolAppendMu.Lock()
	err := truncateSubAgentMailboxLog(sessionDir, offset)
	a.spoolAppendMu.Unlock()
	if err != nil {
		log.Errorf("failed to roll back mailbox message message_id=%v offset=%v error=%v (restore may replay it)", messageID, offset, err)
		return
	}
	a.subAgentMailboxIDsMu.Lock()
	// The truncation is durable now; any index rebuild that read the longer
	// log must not publish a map that resurrects the withdrawn row.
	a.subAgentInbox.spoolWriteGen++
	delete(a.subAgentMailboxIDs, messageID)
	if location, ok := a.subAgentInbox.spoolIndex[messageID]; ok && location.offset >= offset {
		delete(a.subAgentInbox.spoolIndex, messageID)
	}
	a.subAgentMailboxIDsMu.Unlock()
}

func truncateSubAgentMailboxLog(sessionDir string, size int64) error {
	if strings.TrimSpace(sessionDir) == "" || size < 0 {
		return nil
	}
	path := filepath.Join(sessionDir, "subagents", "mailbox.jsonl")
	f, err := privatefs.OpenFile(sessionDir, path, os.O_RDWR)
	if err != nil {
		return fmt.Errorf("open mailbox log for rollback: %w", err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return fmt.Errorf("truncate mailbox log: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync mailbox log: %w", err)
	}
	return f.Close()
}

func (a *MainAgent) loadDurableMailboxMessage(messageID string) (*SubAgentMailboxMessage, bool, error) {
	// Reuse the shared spool index instead of rescanning mailbox.jsonl from the
	// start on every call: loadSpooledMailbox skips consumed records, rebuilds
	// the index only when a write left it stale, and reads exactly the record's
	// own line. A missing mailbox log keeps the historical not-found result (it
	// holds no row to deliver), while a genuine read failure stays an error the
	// delivery retry keeps honoring.
	msg, found, err := a.loadSpooledMailbox(messageID)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return msg, found, err
}
