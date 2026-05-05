package analytics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

func TestUsageLedgerAppendEventWritesSummary(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.SetFirstUserMessage("sample first request"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}

	costCfg := &config.ModelCost{
		Input:     3.0,
		Output:    15.0,
		CacheRead: 0.3,
	}
	raw := UsageSnapshot{
		InputTokens:     1000,
		OutputTokens:    200,
		CacheReadTokens: 300,
	}
	billing := NormalizeBillingUsage(raw)
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		AgentKind:        "main",
		AgentName:        "builder",
		Purpose:          "chat",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         raw,
		BillingUsage:     billing,
		Cost:             CalculateUsageCost(costCfg, billing),
		PricingSnapshot:  PricingSnapshotFromCost(costCfg),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1", summary.EventCount)
	}
	if summary.LastEventID == "" {
		t.Fatal("LastEventID is empty")
	}
	if summary.FirstUserMessage != "sample first request" {
		t.Fatalf("FirstUserMessage = %q", summary.FirstUserMessage)
	}
	if summary.UsageTotal.InputTokens != raw.InputTokens {
		t.Fatalf("InputTokens = %d, want %d", summary.UsageTotal.InputTokens, raw.InputTokens)
	}
	if summary.ByModelRef["provider-a/model-1"] == nil {
		t.Fatal("missing by_model_ref entry")
	}
	if summary.ByProvider["provider-a"] == nil {
		t.Fatal("missing by_provider entry")
	}

	onDiskBytes, err := os.ReadFile(filepath.Join(dir, "usage-summary.json"))
	if err != nil {
		t.Fatalf("ReadFile(usage-summary.json): %v", err)
	}
	if len(onDiskBytes) > 0 && onDiskBytes[0] != '{' {
		t.Fatalf("usage-summary.json should be compact JSON, starts with %q", onDiskBytes[:1])
	}
	var onDisk SessionUsageSummary
	if err := json.Unmarshal(onDiskBytes, &onDisk); err != nil {
		t.Fatalf("Unmarshal(usage-summary.json): %v", err)
	}
	if onDisk.LastEventID != summary.LastEventID {
		t.Fatalf("on-disk LastEventID = %q, want %q", onDisk.LastEventID, summary.LastEventID)
	}
}

func TestRewriteFirstUserMessagePreservesOriginalFirstUserMessage(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.SetFirstUserMessage("original first request"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	if err := ledger.RewriteFirstUserMessage("updated first request"); err != nil {
		t.Fatalf("RewriteFirstUserMessage: %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage != "updated first request" {
		t.Fatalf("FirstUserMessage = %q", summary.FirstUserMessage)
	}
	if summary.OriginalFirstUserMessage != "original first request" {
		t.Fatalf("OriginalFirstUserMessage = %q", summary.OriginalFirstUserMessage)
	}
}

func TestRewriteFirstUserMessageWithOriginalSeedsHintWhenNothingKnown(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	// Note: SetFirstUserMessage is intentionally NOT called — this mirrors
	// the legacy / brand-new-session case where the ledger has no cached
	// original first user message yet. main.jsonl is also absent.
	if err := ledger.RewriteFirstUserMessageWithOriginal(
		"[Context Summary]\n## Goal\n…",
		"hello world", // hint captured by the caller before main.jsonl rewrite
	); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginal: %v", err)
	}
	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage == "" {
		t.Fatal("FirstUserMessage is empty")
	}
	if summary.OriginalFirstUserMessage != "hello world" {
		t.Fatalf("OriginalFirstUserMessage = %q, want %q", summary.OriginalFirstUserMessage, "hello world")
	}
}

func TestFirstUserMessageLockedSkipsCompactionSummary(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.jsonl")
	// Write a main.jsonl whose first user message is a compaction summary.
	// firstUserMessageLocked must skip it so the ledger never adopts the
	// summary as the original first user message.
	first := message.Message{
		Role:                "user",
		Content:             "[Context Summary]\n## Goal\n…",
		IsCompactionSummary: true,
	}
	second := message.Message{
		Role:    "user",
		Content: "real second user message",
	}
	enc := func(m message.Message) []byte {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return append(b, '\n')
	}
	payload := append(enc(first), enc(second)...)
	if err := os.WriteFile(mainPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ledger := NewUsageLedger(dir, "/tmp/project")
	got := ledger.firstUserMessageLocked()
	if got != "real second user message" {
		t.Fatalf("firstUserMessageLocked = %q, want %q", got, "real second user message")
	}
}

func TestLoadSessionUsageSummaryRebuildsWhenSummaryStale(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	costCfg := &config.ModelCost{Input: 3.0, Output: 15.0}

	firstRaw := UsageSnapshot{InputTokens: 100, OutputTokens: 50}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		AgentKind:        "main",
		AgentName:        "builder",
		Purpose:          "chat",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         firstRaw,
		BillingUsage:     NormalizeBillingUsage(firstRaw),
		Cost:             CalculateUsageCost(costCfg, NormalizeBillingUsage(firstRaw)),
		PricingSnapshot:  PricingSnapshotFromCost(costCfg),
	}); err != nil {
		t.Fatalf("AppendEvent(first): %v", err)
	}
	staleSummary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary(first): %v", err)
	}

	secondEvent := UsageEvent{
		Version:          usageEventVersion,
		EventID:          "event-2",
		SessionID:        filepath.Base(dir),
		ProjectID:        ProjectIDForPath("/tmp/project"),
		ProjectPath:      "/tmp/project",
		OccurredAt:       time.Now().UTC(),
		Timezone:         "UTC",
		LocalDate:        time.Now().UTC().Format("2006-01-02"),
		AgentID:          "worker-1",
		AgentKind:        "sub",
		AgentName:        "coder",
		Purpose:          "chat",
		SelectedModelRef: "provider-b/model-2",
		RunningModelRef:  "provider-b/model-2",
		Provider:         "provider-b",
		ModelID:          "model-2",
		UsageRaw:         UsageSnapshot{InputTokens: 200, OutputTokens: 80},
		BillingUsage:     NormalizeBillingUsage(UsageSnapshot{InputTokens: 200, OutputTokens: 80}),
		Cost:             CalculateUsageCost(costCfg, NormalizeBillingUsage(UsageSnapshot{InputTokens: 200, OutputTokens: 80})),
		PricingSnapshot:  PricingSnapshotFromCost(costCfg),
	}
	data, marshalErr := json.Marshal(secondEvent)
	if marshalErr != nil {
		t.Fatalf("Marshal(second): %v", marshalErr)
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "usage.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if openErr != nil {
		t.Fatalf("OpenFile(usage.jsonl): %v", openErr)
	}
	if _, writeErr := f.Write(append(data, '\n')); writeErr != nil {
		f.Close()
		t.Fatalf("Write(second): %v", writeErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		t.Fatalf("Close(usage.jsonl): %v", closeErr)
	}

	rebuilt, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary: %v", err)
	}
	if rebuilt.EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2", rebuilt.EventCount)
	}
	if rebuilt.LastEventID != secondEvent.EventID {
		t.Fatalf("LastEventID = %q, want %q", rebuilt.LastEventID, secondEvent.EventID)
	}
	if rebuilt.UsageTotal.InputTokens != firstRaw.InputTokens+secondEvent.UsageRaw.InputTokens {
		t.Fatalf("InputTokens = %d, want %d", rebuilt.UsageTotal.InputTokens, firstRaw.InputTokens+secondEvent.UsageRaw.InputTokens)
	}
	if rebuilt.LastEventID == staleSummary.LastEventID {
		t.Fatalf("summary was not rebuilt; LastEventID still %q", rebuilt.LastEventID)
	}
}

func TestUsageLedgerBuildSessionStatsUsesRunningModelRefs(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")

	appendEvent := func(agentID, agentKind, agentName, modelRef string, raw UsageSnapshot, costCfg *config.ModelCost) {
		t.Helper()
		billing := NormalizeBillingUsage(raw)
		if err := ledger.AppendEvent(UsageEvent{
			AgentID:          agentID,
			AgentKind:        agentKind,
			AgentName:        agentName,
			Purpose:          "chat",
			SelectedModelRef: modelRef,
			RunningModelRef:  modelRef,
			UsageRaw:         raw,
			BillingUsage:     billing,
			Cost:             CalculateUsageCost(costCfg, billing),
			PricingSnapshot:  PricingSnapshotFromCost(costCfg),
		}); err != nil {
			t.Fatalf("AppendEvent(%s): %v", modelRef, err)
		}
	}

	costCfg := &config.ModelCost{Input: 3.0, Output: 15.0}
	appendEvent("main", "main", "builder", "provider-a/model-1", UsageSnapshot{InputTokens: 100, OutputTokens: 40}, costCfg)
	appendEvent("worker-1", "sub", "coder", "provider-b/model-2", UsageSnapshot{InputTokens: 80, OutputTokens: 30}, costCfg)

	stats, eventCount, err := ledger.BuildSessionStats()
	if err != nil {
		t.Fatalf("BuildSessionStats: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("eventCount = %d, want 2", eventCount)
	}
	if stats.ByModel["provider-a/model-1"] == nil {
		t.Fatal("missing provider-a/model-1 ByModel entry")
	}
	if stats.ByModel["provider-b/model-2"] == nil {
		t.Fatal("missing provider-b/model-2 ByModel entry")
	}
	if stats.ByAgent["worker-1"] == nil {
		t.Fatal("missing worker-1 ByAgent entry")
	}
	if stats.ByAgent["worker-1"].ByModel["provider-b/model-2"] == nil {
		t.Fatal("missing worker-1 per-model entry")
	}
}
