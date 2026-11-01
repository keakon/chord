package analytics

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeLedgerFixture appends n aggregate events to a fresh ledger in dir so
// benchmarks measure reads over a realistic pre-existing session ledger.
func writeLedgerFixture(b *testing.B, dir string, n int) {
	b.Helper()
	ledger := NewUsageLedger(dir, "")
	for i := range n {
		event := sampleUsageEvent(i)
		if err := ledger.AppendEvent(event); err != nil {
			b.Fatal(err)
		}
	}
	if err := ledger.Close(); err != nil {
		b.Fatal(err)
	}
}

func sampleUsageEvent(i int) UsageEvent {
	return UsageEvent{
		EventID:          fmt.Sprintf("evt-%06d", i),
		SessionID:        "bench-session",
		OccurredAt:       time.Now(),
		LocalDate:        "2026-10-31",
		Timezone:         "UTC",
		AgentID:          "main",
		Purpose:          "chat",
		SelectedModelRef: "sample/test-model",
		Provider:         "sample",
		ModelID:          "test-model",
		UsageRaw:         UsageSnapshot{InputTokens: 10, OutputTokens: 120, ReasoningTokens: 30},
		BillingUsage:     BillingUsage{InputTokens: 900, OutputTokens: 120, CacheReadTokens: 4096, BillingTotalTokens: 5116},
		Cost:             UsageCost{Currency: "USD", TotalCost: 0.0042},
	}
}

// BenchmarkUsageLedgerFirstAppendPrimed measures the first append a restored
// session pays with the evidence-scan cursor primed: ensureSummaryLocked
// decides freshness from the cursor instead of re-reading usage.jsonl. The
// evidence scan itself and the closing fsync stay outside the timer — both
// sides pay them.
func BenchmarkUsageLedgerFirstAppendPrimed(b *testing.B) {
	const eventCount = 2000
	template := b.TempDir()
	writeLedgerFixture(b, template, eventCount)

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		b.StopTimer()
		ledger := freshFixtureLedger(b, template, i)
		_, _, _, _, lastEventID, err := ledger.BuildSessionEvidence()
		if err != nil {
			b.Fatal(err)
		}
		ledger.PrimeSummaryCursor(lastEventID)
		b.StartTimer()

		if err := ledger.AppendEvent(sampleUsageEvent(eventCount + i)); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := ledger.Close(); err != nil {
			b.Fatal(err)
		}
		// b.Loop fails if the next call sees a stopped timer.
		b.StartTimer()
	}
}

// BenchmarkUsageLedgerFirstAppendUnprimed is the pre-priming fallback: the
// first append re-reads the whole ledger to decide summary freshness.
func BenchmarkUsageLedgerFirstAppendUnprimed(b *testing.B) {
	const eventCount = 2000
	template := b.TempDir()
	writeLedgerFixture(b, template, eventCount)

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		b.StopTimer()
		ledger := freshFixtureLedger(b, template, i)
		b.StartTimer()

		if err := ledger.AppendEvent(sampleUsageEvent(eventCount + i)); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := ledger.Close(); err != nil {
			b.Fatal(err)
		}
		// b.Loop fails if the next call sees a stopped timer.
		b.StartTimer()
	}
}

func freshFixtureLedger(b *testing.B, template string, i int) *UsageLedger {
	b.Helper()
	dir := filepath.Join(b.TempDir(), fmt.Sprintf("session-%d", i))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	for _, name := range []string{"usage.jsonl", SessionUsageSummaryFileName} {
		data, err := os.ReadFile(filepath.Join(template, name))
		if err != nil {
			if !os.IsNotExist(err) {
				b.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	return NewUsageLedger(dir, "")
}

// BenchmarkUsageLedgerAppendSteady measures the per-event cost of appending to
// an already-open ledger, including the usage-summary.json rewrite each
// aggregate event triggers.
func BenchmarkUsageLedgerAppendSteady(b *testing.B) {
	dir := b.TempDir()
	writeLedgerFixture(b, dir, 100)
	ledger := NewUsageLedger(dir, "")
	b.Cleanup(func() { _ = ledger.Close() })

	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if err := ledger.AppendEvent(sampleUsageEvent(1000 + i)); err != nil {
			b.Fatal(err)
		}
	}
}
