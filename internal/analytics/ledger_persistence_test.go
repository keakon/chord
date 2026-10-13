package analytics

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLedgerRetryWritesPreservesFailedEventsAndOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.jsonl")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ledger := NewUsageLedger(dir, dir)
	for _, id := range []string{"event-1", "event-2"} {
		if err := ledger.AppendEvent(UsageEvent{EventID: id, Purpose: "main"}); err == nil {
			t.Fatal("append succeeded into directory")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Sync(); err == nil {
		t.Fatal("sync discarded previous failure")
	}
	ledger.Seal()
	if err := ledger.RetryWrites(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Sync(); err != nil {
		t.Fatal(err)
	}
	var events []UsageEvent
	if err := scanUsageEvents(path, func(event UsageEvent) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventID != "event-1" || events[1].EventID != "event-2" {
		t.Fatalf("events=%+v", events)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerSummaryRetryDoesNotAppendEventTwice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SessionUsageSummaryFileName)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ledger := NewUsageLedger(dir, dir)
	if err := ledger.AppendEvent(UsageEvent{EventID: "event-1", Purpose: "main"}); err == nil {
		t.Fatal("summary write succeeded into directory")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ledger.Seal()
	if err := ledger.RetryWrites(); err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var summary SessionUsageSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.EventCount != 1 {
		t.Fatalf("event count=%d", summary.EventCount)
	}
	calls := 0
	scanUsageEvents(filepath.Join(dir, "usage.jsonl"), func(UsageEvent) { calls++ })
	if calls != 1 {
		t.Fatalf("ledger appended %d copies", calls)
	}
}

func TestLedgerUnknownWriteDoesNotBlindRetry(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, dir)
	errUnknown := errors.New("partial write")
	ledger.failures = []ledgerFailure{{event: UsageEvent{EventID: "event-1"}, err: errUnknown, retrySafe: false}}
	if err := ledger.RetryWrites(); !errors.Is(err, errUnknown) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "usage.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("unknown outcome was retried: %v", err)
	}
}

func TestLedgerSealRejectsLateWriterAndCloseReportsPriorFailure(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, dir)
	ledger.Seal()
	if err := ledger.AppendEvent(UsageEvent{EventID: "event-1"}); err == nil {
		t.Fatal("late writer accepted")
	}
	if err := ledger.Close(); err == nil {
		t.Fatal("close discarded failed write")
	}
	if _, err := os.Stat(filepath.Join(dir, "usage.jsonl")); !os.IsNotExist(err) {
		t.Fatal("late write changed ledger")
	}
}
