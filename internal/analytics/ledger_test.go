package analytics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
		InputTokens:      1000,
		OutputTokens:     200,
		CacheReadTokens:  300,
		CacheWriteTokens: 20,
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
		Cost:             CalculateUsageCost(costCfg, billing, config.ServiceTierStandard),
		PricingSnapshot:  PricingSnapshotFromCost(costCfg, billing, config.ServiceTierStandard),
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
	if summary.UsageTotal.InputTokens != billing.InputTokens {
		t.Fatalf("InputTokens = %d, want uncached bucket %d", summary.UsageTotal.InputTokens, billing.InputTokens)
	}
	if summary.UsageTotal.ProviderTotalTokens != 1500 {
		t.Fatalf("ProviderTotalTokens = %d, want provider input/output total 1500", summary.UsageTotal.ProviderTotalTokens)
	}
	if summary.UsageTotal.BillingTotalTokens != 1520 {
		t.Fatalf("BillingTotalTokens = %d, want billing total 1520", summary.UsageTotal.BillingTotalTokens)
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

func TestDiagnosticEventsAdvanceCursorWithoutCountingAsCalls(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")

	raw := UsageSnapshot{InputTokens: 100, OutputTokens: 50}
	real := UsageEvent{
		AgentID:         "main",
		Purpose:         "chat",
		RunningModelRef: "provider-a/model-1",
		UsageRaw:        raw,
		BillingUsage:    NormalizeBillingUsage(raw),
	}
	diagnostic := UsageEvent{
		AgentID:         "main",
		Purpose:         UsagePurposeCompactionLifecycle,
		RunningModelRef: "provider-a/model-1",
		Diagnostic:      map[string]string{"stage": "applied"},
	}
	for _, evt := range []UsageEvent{real, diagnostic} {
		if err := ledger.AppendEvent(evt); err != nil {
			t.Fatalf("AppendEvent(%s): %v", evt.Purpose, err)
		}
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2 (diagnostic events keep the freshness cursor moving)", summary.EventCount)
	}
	if summary.UsageTotal.LLMCalls != 1 {
		t.Fatalf("UsageTotal.LLMCalls = %d, want 1 (diagnostic events are not calls)", summary.UsageTotal.LLMCalls)
	}
	if _, ok := summary.ByPurpose[UsagePurposeCompactionLifecycle]; ok {
		t.Fatal("diagnostic purpose must not appear in aggregated ByPurpose buckets")
	}

	stats, eventCount, err := ledger.BuildSessionStats()
	if err != nil {
		t.Fatalf("BuildSessionStats: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("scanned eventCount = %d, want 2", eventCount)
	}
	if stats.LLMCalls != 1 {
		t.Fatalf("stats.LLMCalls = %d, want 1", stats.LLMCalls)
	}
	if agent := stats.ByAgent["main"]; agent == nil || agent.LLMCalls != 1 {
		t.Fatalf("ByAgent[main] = %+v, want exactly 1 call", agent)
	}
}

func TestIsDiagnosticUsagePurpose(t *testing.T) {
	for _, purpose := range []string{
		UsagePurposeCompactionPolicy,
		UsagePurposeCompactionPolicy + "/loop_frozen",
		UsagePurposeCompactionFailure,
		UsagePurposeOversizeRecovery + "/retry",
		UsagePurposeContextProvenance,
		UsagePurposeCompactionLifecycle,
	} {
		if !IsDiagnosticUsagePurpose(purpose) {
			t.Fatalf("IsDiagnosticUsagePurpose(%q) = false, want true", purpose)
		}
	}
	for _, purpose := range []string{"", "chat", "compaction", "task", "context_compactionx"} {
		if IsDiagnosticUsagePurpose(purpose) {
			t.Fatalf("IsDiagnosticUsagePurpose(%q) = true, want false", purpose)
		}
	}
}

func TestWalltimeTailDoesNotAdvanceSummary(t *testing.T) {
	// Walltime bookkeeping events live in usage.jsonl but never enter
	// usage-summary.json. Appending a tail of walltime segments must not
	// make the cached summary look stale, which would force a full rebuild
	// on the next real event.
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		Purpose:          "chat",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         UsageSnapshot{InputTokens: 100, OutputTokens: 50},
		BillingUsage:     NormalizeBillingUsage(UsageSnapshot{InputTokens: 100, OutputTokens: 50}),
	}); err != nil {
		t.Fatalf("AppendEvent(usage): %v", err)
	}
	walltime := func(purpose string, ns int64) {
		if err := ledger.AppendEvent(UsageEvent{
			AgentID:    "main",
			Purpose:    purpose,
			Diagnostic: map[string]string{"ns": strconv.FormatInt(ns, 10)},
		}); err != nil {
			t.Fatalf("AppendEvent(%s): %v", purpose, err)
		}
	}
	walltime(WalltimePurposeTool, int64(2*time.Second))
	walltime(WalltimePurposeUserWait, int64(1*time.Second))

	loaded, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary: %v", err)
	}
	if loaded.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1 (walltime events excluded from summary)", loaded.EventCount)
	}
	if loaded.UsageTotal.LLMCalls != 1 {
		t.Fatalf("LLMCalls = %d, want 1", loaded.UsageTotal.LLMCalls)
	}
}

func TestBuildSessionEvidenceFoldsWalltimeInOnePass(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	raw := UsageSnapshot{InputTokens: 100, OutputTokens: 40}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		Purpose:          "chat",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         raw,
		BillingUsage:     NormalizeBillingUsage(raw),
	}); err != nil {
		t.Fatalf("AppendEvent(usage): %v", err)
	}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:    "main",
		Purpose:    WalltimePurposeTool,
		Diagnostic: map[string]string{"ns": strconv.FormatInt(int64(3*time.Second), 10)},
	}); err != nil {
		t.Fatalf("AppendEvent(walltime): %v", err)
	}

	stats, count, refs, walltime, err := ledger.BuildSessionEvidence()
	if err != nil {
		t.Fatalf("BuildSessionEvidence: %v", err)
	}
	if count != 2 {
		t.Fatalf("eventCount = %d, want 2 (usage + walltime)", count)
	}
	if stats.LLMCalls != 1 {
		t.Fatalf("LLMCalls = %d, want 1 (walltime is not a call)", stats.LLMCalls)
	}
	if refs["main"].Running != "provider-a/model-1" {
		t.Fatalf("refs[main] = %+v", refs["main"])
	}
	if got := walltime["main"].Tool; got != 3*time.Second {
		t.Fatalf("walltime[main].Tool = %v, want 3s", got)
	}
}

// A digit string too long for the range is corruption, not a duration: it must
// be dropped rather than folded in, and its valid siblings must still count.
func TestBuildSessionEvidenceFoldsCompactionLifecycle(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")

	if err := ledger.AppendEvent(UsageEvent{
		AgentID:    "main",
		Purpose:    UsagePurposeCompactionLifecycle,
		Diagnostic: map[string]string{"stage": "applied", "trigger": "model_driven"},
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:    "main",
		Purpose:    UsagePurposeCompactionLifecycle,
		Diagnostic: map[string]string{"stage": "skipped"},
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	stats, eventCount, _, _, err := ledger.BuildSessionEvidence()
	if err != nil {
		t.Fatalf("BuildSessionEvidence: %v", err)
	}
	if stats.LLMCalls != 0 {
		t.Fatalf("LLMCalls = %d, want 0 (lifecycle events are not calls)", stats.LLMCalls)
	}
	if eventCount != 2 {
		t.Fatalf("scanned eventCount = %d, want 2", eventCount)
	}
	if stats.CompactionLifecycle["applied/model_driven"] != 1 || stats.CompactionLifecycle["skipped"] != 1 {
		t.Fatalf("CompactionLifecycle = %v, want applied/model_driven=1 skipped=1", stats.CompactionLifecycle)
	}
}

func TestBuildSessionEvidenceDropsOutOfRangeWalltimeNs(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:    "main",
		Purpose:    WalltimePurposeTool,
		Diagnostic: map[string]string{WalltimeDiagnosticNsKey: "99999999999999999999"},
	}); err != nil {
		t.Fatalf("AppendEvent(corrupt walltime): %v", err)
	}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:    "main",
		Purpose:    WalltimePurposeTool,
		Diagnostic: map[string]string{WalltimeDiagnosticNsKey: strconv.FormatInt(int64(2*time.Second), 10)},
	}); err != nil {
		t.Fatalf("AppendEvent(walltime): %v", err)
	}

	_, _, _, walltime, err := ledger.BuildSessionEvidence()
	if err != nil {
		t.Fatalf("BuildSessionEvidence: %v", err)
	}
	if got := walltime["main"].Tool; got != 2*time.Second {
		t.Fatalf("walltime[main].Tool = %v, want 2s (out-of-range ns dropped)", got)
	}
}

func TestLoadSessionUsageSummaryRejectsUnknownSummaryFields(t *testing.T) {
	dir := t.TempDir()
	original := `{"unknown_field":true,"session_id":"old","usage_total":{"llm_calls":3,"input_tokens":9}}`
	if err := os.WriteFile(filepath.Join(dir, "usage-summary.json"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSessionUsageSummary(dir); err == nil {
		t.Fatal("expected unknown summary field error")
	}
	data, err := os.ReadFile(filepath.Join(dir, "usage-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("unknown-field summary was rewritten: %s", data)
	}
}

func TestLoadSessionUsageSummaryKeepsCurrentSummaryWhenLedgerIsMissing(t *testing.T) {
	dir := t.TempDir()
	original := `{"session_id":"current","last_event_id":"event-1","event_count":1,"usage_total":{"llm_calls":1,"input_tokens":9}}`
	if err := os.WriteFile(filepath.Join(dir, "usage-summary.json"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageTotal.InputTokens != 9 || got.EventCount != 1 {
		t.Fatalf("summary = %+v, want existing aggregate", got.UsageTotal)
	}
	data, err := os.ReadFile(filepath.Join(dir, "usage-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("current summary was rewritten: %s", data)
	}
}

func TestUsageLedgerPreservesExistingSessionFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"usage.jsonl", "usage-summary.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.AppendEvent(UsageEvent{AgentID: "main"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// The pre-existing session dir keeps its permissions and the append-only
	// usage.jsonl is opened in place, so both keep their modes. The summary is
	// rewritten via a freshly created temp file + rename, so it carries the
	// private file mode (session-scoped runtime data, unchanged from the
	// previous behavior).
	assertAnalyticsMode(t, dir, 0o755)
	assertAnalyticsMode(t, filepath.Join(dir, "usage.jsonl"), 0o644)
	assertAnalyticsMode(t, filepath.Join(dir, "usage-summary.json"), 0o600)
}

func assertAnalyticsMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
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

// An in-place tail edit can drop the session's only user prompt. The preserved
// original names that removed message, so it must be dropped with it; session
// lists prefer the original over the current preview, and a stale original
// would keep advertising a prompt the transcript no longer contains.
func TestRewriteFirstUserMessageEmptyContentClearsPreservedOriginal(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.SetFirstUserMessage("removed first request"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}
	if err := ledger.RewriteFirstUserMessage(""); err != nil {
		t.Fatalf("RewriteFirstUserMessage(empty): %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage != "" {
		t.Fatalf("FirstUserMessage = %q, want empty", summary.FirstUserMessage)
	}
	if summary.OriginalFirstUserMessage != "" {
		t.Fatalf("OriginalFirstUserMessage = %q, want empty", summary.OriginalFirstUserMessage)
	}
	if got := ledger.OriginalFirstUserMessage(); got != "" {
		t.Fatalf("OriginalFirstUserMessage() = %q, want empty", got)
	}

	// The next submitted prompt seeds both previews again.
	if err := ledger.SetFirstUserMessage("corrected request"); err != nil {
		t.Fatalf("SetFirstUserMessage(corrected): %v", err)
	}
	summary, err = ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage != "corrected request" {
		t.Fatalf("FirstUserMessage = %q, want corrected request", summary.FirstUserMessage)
	}
	if summary.OriginalFirstUserMessage != "corrected request" {
		t.Fatalf("OriginalFirstUserMessage = %q, want corrected request", summary.OriginalFirstUserMessage)
	}
}

func TestSetFirstUserMessageAdoptsExistingSummary(t *testing.T) {
	dir := t.TempDir()
	seed := &SessionUsageSummary{
		SessionID:                filepath.Base(dir),
		FirstUserMessage:         "original first request",
		OriginalFirstUserMessage: "original first request",
		Status:                   "active",
		ByProvider:               make(map[string]*UsageAggregate),
		ByModelRef:               make(map[string]*UsageAggregate),
		ByAgent:                  make(map[string]*UsageAggregate),
		ByPurpose:                make(map[string]*UsageAggregate),
		ByDate:                   make(map[string]*UsageAggregate),
		ByDateModelRef:           make(map[string]map[string]*UsageAggregate),
		ByDateAgent:              make(map[string]map[string]*UsageAggregate),
	}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("Marshal(seed): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-summary.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(usage-summary.json): %v", err)
	}

	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.SetFirstUserMessage("continue"); err != nil {
		t.Fatalf("SetFirstUserMessage: %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage != "original first request" {
		t.Fatalf("FirstUserMessage = %q, want %q", summary.FirstUserMessage, "original first request")
	}
	if summary.OriginalFirstUserMessage != "original first request" {
		t.Fatalf("OriginalFirstUserMessage = %q, want %q", summary.OriginalFirstUserMessage, "original first request")
	}
	if got := ledger.OriginalFirstUserMessage(); got != "original first request" {
		t.Fatalf("OriginalFirstUserMessage() = %q, want %q", got, "original first request")
	}
}

func TestSetFirstUserMessageDoesNotBackfillOriginalFromLaterInput(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	if err := ledger.SetFirstUserMessage("original first request"); err != nil {
		t.Fatalf("SetFirstUserMessage(original): %v", err)
	}
	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	summary.OriginalFirstUserMessage = ""
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("Marshal(summary): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-summary.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile(usage-summary.json): %v", err)
	}

	restoredLedger := NewUsageLedger(dir, "/tmp/project")
	if err := restoredLedger.SetFirstUserMessage("continue"); err != nil {
		t.Fatalf("SetFirstUserMessage(continue): %v", err)
	}

	restoredSummary, err := restoredLedger.Summary()
	if err != nil {
		t.Fatalf("Summary(restored): %v", err)
	}
	if restoredSummary.FirstUserMessage != "original first request" {
		t.Fatalf("FirstUserMessage = %q, want %q", restoredSummary.FirstUserMessage, "original first request")
	}
	if restoredSummary.OriginalFirstUserMessage != "original first request" {
		t.Fatalf("OriginalFirstUserMessage = %q, want %q", restoredSummary.OriginalFirstUserMessage, "original first request")
	}
}

func TestRewriteFirstUserMessageWithOriginalForCompactionSeedsHintWhenNothingKnown(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	// Note: SetFirstUserMessage is intentionally NOT called — this mirrors
	// the brand-new-session case where the ledger has no cached original first
	// user message yet. main.jsonl is also absent.
	if err := ledger.RewriteFirstUserMessageWithOriginalForCompaction(
		"[Context Summary]\n## Goal\n…",
		"hello world", // hint captured by the caller before main.jsonl rewrite
	); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginalForCompaction: %v", err)
	}
	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage == "" {
		t.Fatal("FirstUserMessage is empty")
	}
	if !summary.FirstUserMessageIsCompactionSummary {
		t.Fatal("FirstUserMessageIsCompactionSummary = false, want true")
	}
	if summary.OriginalFirstUserMessage != "hello world" {
		t.Fatalf("OriginalFirstUserMessage = %q, want %q", summary.OriginalFirstUserMessage, "hello world")
	}
}

// writeCompactedMainLog writes a main.jsonl whose head is a compaction
// checkpoint followed by a mid-session prompt — the transcript shape a scan for
// the first user-authored message cannot read the original request out of.
func writeCompactedMainLog(t *testing.T, dir string) {
	t.Helper()
	messages := []message.Message{
		{Role: message.RoleUser, Content: "[Context Summary]\n## Goal\n- carry on", IsCompactionSummary: true},
		{Role: message.RoleAssistant, Content: "ack"},
		{Role: message.RoleUser, Content: "mid-session prompt"},
	}
	var payload []byte
	for _, msg := range messages {
		encoded, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), payload, 0o644); err != nil {
		t.Fatalf("WriteFile(main.jsonl): %v", err)
	}
}

// writeStaleSummaryAndEvent seeds a cached summary and one ledger event that
// lands after it, which is what makes the next open rebuild the summary.
func writeStaleSummaryAndEvent(t *testing.T, dir string, seed SessionUsageSummary) {
	t.Helper()
	seedBytes, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("Marshal(seed): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-summary.json"), seedBytes, 0o600); err != nil {
		t.Fatalf("WriteFile(usage-summary.json): %v", err)
	}
	event := `{"event_id":"event-1","session_id":"session-1","agent_id":"main","purpose":"chat","running_model_ref":"provider-a/model-1"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(event), 0o600); err != nil {
		t.Fatalf("WriteFile(usage.jsonl): %v", err)
	}
}

// A rebuild replays usage.jsonl, which records token/cost aggregates only — it
// carries no first-user metadata. That metadata must therefore be carried over
// from the summary being replaced; re-deriving it from the transcript instead
// loses the original request as soon as the session has been compacted, because
// the transcript then starts with a checkpoint and a scan for the first
// user-authored message names a mid-session prompt.
func TestRebuildSummaryLockedCarriesFirstUserMetadataOver(t *testing.T) {
	dir := t.TempDir()
	writeCompactedMainLog(t, dir)
	writeStaleSummaryAndEvent(t, dir, SessionUsageSummary{
		SessionID:                           filepath.Base(dir),
		LastEventID:                         "stale-event",
		FirstUserMessage:                    "the checkpoint preview",
		FirstUserMessageIsCompactionSummary: true,
		OriginalFirstUserMessage:            "original first request",
		Status:                              "active",
	})

	rebuilt, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary: %v", err)
	}
	if rebuilt.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1: the summary must have been rebuilt", rebuilt.EventCount)
	}
	if rebuilt.OriginalFirstUserMessage != "original first request" {
		t.Fatalf("OriginalFirstUserMessage = %q, want the carried original request", rebuilt.OriginalFirstUserMessage)
	}
	if !rebuilt.FirstUserMessageIsCompactionSummary {
		t.Fatal("FirstUserMessageIsCompactionSummary = false, want the carried true")
	}
	if rebuilt.FirstUserMessage != "the checkpoint preview" {
		t.Fatalf("FirstUserMessage = %q, want the carried checkpoint preview", rebuilt.FirstUserMessage)
	}
}

// When nothing carries the first-user metadata — no summary at all, or one that
// predates the field — the transcript scan is the only source left for the
// preview. Its result must not be promoted to the original request: on a
// compacted history it names a mid-session prompt, and an original request is
// sticky, because session lists prefer it and every later checkpoint copies it
// forward as its "Original request:" anchor.
func TestRebuildSummaryLockedDoesNotPromoteScannedPreviewToOriginal(t *testing.T) {
	dir := t.TempDir()
	writeCompactedMainLog(t, dir)
	event := `{"event_id":"event-1","session_id":"session-1","agent_id":"main","purpose":"chat","running_model_ref":"provider-a/model-1"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(event), 0o600); err != nil {
		t.Fatalf("WriteFile(usage.jsonl): %v", err)
	}

	rebuilt, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary: %v", err)
	}
	// The preview still falls back to the scan — session lists need something —
	// but the original request stays empty so the agent layer can recover the
	// real one from the checkpoint's anchors instead of inheriting a
	// mid-session prompt forever.
	if rebuilt.FirstUserMessage != "mid-session prompt" {
		t.Fatalf("FirstUserMessage = %q, want the transcript-scan fallback", rebuilt.FirstUserMessage)
	}
	if rebuilt.OriginalFirstUserMessage != "" {
		t.Fatalf("OriginalFirstUserMessage = %q, want empty: a scanned preview is not the original request", rebuilt.OriginalFirstUserMessage)
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

func TestFirstUserMessageLockedSkipsSyntheticUserMessages(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.jsonl")
	messages := []message.Message{
		{Role: message.RoleUser, Content: "mailbox", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1"}},
		{Role: message.RoleUser, Content: "loop", Kind: message.KindLoopNotice},
		{Role: message.RoleUser, Content: "real user message"},
	}
	var payload []byte
	for _, msg := range messages {
		encoded, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		payload = append(payload, encoded...)
		payload = append(payload, '\n')
	}
	if err := os.WriteFile(mainPath, payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ledger := NewUsageLedger(dir, "/tmp/project")
	if got := ledger.firstUserMessageLocked(); got != "real user message" {
		t.Fatalf("firstUserMessageLocked = %q, want real user message", got)
	}
}

// An in-place tail edit rewrites a transcript whose head may already be a
// compaction checkpoint. The ledger cannot read the original request out of
// such a transcript — its first user-authored message is a mid-session prompt —
// so with no hint it must leave the original unknown rather than freeze that
// prompt as the session's original request.
func TestRewriteFirstUserMessageDoesNotScanPastCheckpointForOriginal(t *testing.T) {
	dir := t.TempDir()
	writeCompactedMainLog(t, dir)
	ledger := NewUsageLedger(dir, "/tmp/project")

	if err := ledger.RewriteFirstUserMessage("mid-session prompt"); err != nil {
		t.Fatalf("RewriteFirstUserMessage: %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.FirstUserMessage != "mid-session prompt" {
		t.Fatalf("FirstUserMessage = %q, want the rewritten preview", summary.FirstUserMessage)
	}
	if summary.OriginalFirstUserMessage != "" {
		t.Fatalf("OriginalFirstUserMessage = %q, want empty: a post-checkpoint prompt is not the original request", summary.OriginalFirstUserMessage)
	}
}

// The same rewrite with a caller hint seeds the original request from it: that
// is the path the in-place tail edit takes, passing the checkpoint's anchors.
func TestRewriteFirstUserMessageWithOriginalSeedsHintForCompactedTranscript(t *testing.T) {
	dir := t.TempDir()
	writeCompactedMainLog(t, dir)
	ledger := NewUsageLedger(dir, "/tmp/project")

	if err := ledger.RewriteFirstUserMessageWithOriginal("mid-session prompt", "REAL original request"); err != nil {
		t.Fatalf("RewriteFirstUserMessageWithOriginal: %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != "REAL original request" {
		t.Fatalf("OriginalFirstUserMessage = %q, want the caller hint", summary.OriginalFirstUserMessage)
	}
	if summary.FirstUserMessageIsCompactionSummary {
		t.Fatal("FirstUserMessageIsCompactionSummary = true, want false: the preview is a real prompt")
	}
}

// The transcript scan stays available for a history that still starts with a
// real prompt: there the first user-authored message *is* the original request.
func TestRewriteFirstUserMessageScansUncompactedHeadForOriginal(t *testing.T) {
	dir := t.TempDir()
	payload := []byte(`{"role":"user","content":"real first prompt"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), payload, 0o644); err != nil {
		t.Fatalf("WriteFile(main.jsonl): %v", err)
	}
	ledger := NewUsageLedger(dir, "/tmp/project")

	if err := ledger.RewriteFirstUserMessage("real first prompt"); err != nil {
		t.Fatalf("RewriteFirstUserMessage: %v", err)
	}

	summary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.OriginalFirstUserMessage != "real first prompt" {
		t.Fatalf("OriginalFirstUserMessage = %q, want the scanned head", summary.OriginalFirstUserMessage)
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
		Cost:             CalculateUsageCost(costCfg, NormalizeBillingUsage(firstRaw), config.ServiceTierStandard),
		PricingSnapshot:  PricingSnapshotFromCost(costCfg, NormalizeBillingUsage(firstRaw), config.ServiceTierStandard),
	}); err != nil {
		t.Fatalf("AppendEvent(first): %v", err)
	}
	staleSummary, err := ledger.Summary()
	if err != nil {
		t.Fatalf("Summary(first): %v", err)
	}

	secondEvent := UsageEvent{
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
		Cost:             CalculateUsageCost(costCfg, NormalizeBillingUsage(UsageSnapshot{InputTokens: 200, OutputTokens: 80}), config.ServiceTierStandard),
		PricingSnapshot:  PricingSnapshotFromCost(costCfg, NormalizeBillingUsage(UsageSnapshot{InputTokens: 200, OutputTokens: 80}), config.ServiceTierStandard),
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

// A rebuild replays every ledger line, including walltime bookkeeping events.
// The freshness scan that decides whether a rebuild is needed skips those
// events, so the rebuilt cursor must skip them too; otherwise a ledger whose
// tail is a walltime segment rebuilds and rewrites the summary on every open.
func TestLoadSessionUsageSummaryTailWalltimeEventKeepsSummaryFresh(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	raw := UsageSnapshot{InputTokens: 100, OutputTokens: 50}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		Purpose:          "chat",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         raw,
	}); err != nil {
		t.Fatalf("AppendEvent(chat): %v", err)
	}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:    "main",
		Purpose:    WalltimePurposeTool,
		Diagnostic: map[string]string{WalltimeDiagnosticNsKey: "1500000000"},
	}); err != nil {
		t.Fatalf("AppendEvent(walltime): %v", err)
	}

	// Force a rebuild: the tail of usage.jsonl is now a walltime event.
	if err := os.Remove(filepath.Join(dir, "usage-summary.json")); err != nil {
		t.Fatalf("Remove(usage-summary.json): %v", err)
	}
	rebuilt, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary(rebuild): %v", err)
	}
	if rebuilt.EventCount != 1 {
		t.Fatalf("rebuilt EventCount = %d, want 1 (walltime events are not counted)", rebuilt.EventCount)
	}

	summaryPath := filepath.Join(dir, "usage-summary.json")
	before, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("ReadFile(before): %v", err)
	}
	if err := os.Chtimes(summaryPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	stat, err := os.Stat(summaryPath)
	if err != nil {
		t.Fatalf("Stat(before): %v", err)
	}

	// A second open must adopt the cached summary instead of rebuilding it.
	again, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary(second): %v", err)
	}
	if again.LastEventID != rebuilt.LastEventID || again.EventCount != rebuilt.EventCount {
		t.Fatalf("second open = %+v, want same cursor as %+v", again, rebuilt)
	}
	after, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("ReadFile(after): %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("summary content changed on a read-only open")
	}
	stat2, err := os.Stat(summaryPath)
	if err != nil {
		t.Fatalf("Stat(after): %v", err)
	}
	if !stat2.ModTime().Equal(stat.ModTime()) {
		t.Fatalf("summary was rewritten on a read-only open (mtime %v -> %v)", stat.ModTime(), stat2.ModTime())
	}
}

func TestLoadSessionUsageSummaryRebuildsWhenSummarySchemaIsInvalid(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	raw := UsageSnapshot{InputTokens: 100, OutputTokens: 50}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         raw,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "usage-summary.json"), []byte(`{"version":2,"session_id":"stale"}`), 0o600); err != nil {
		t.Fatalf("WriteFile(usage-summary.json): %v", err)
	}

	rebuilt, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary: %v", err)
	}
	if rebuilt.EventCount != 1 || rebuilt.UsageTotal.InputTokens != raw.InputTokens {
		t.Fatalf("rebuilt summary = %+v, want one current-format event", rebuilt)
	}
	data, err := os.ReadFile(filepath.Join(dir, "usage-summary.json"))
	if err != nil {
		t.Fatalf("ReadFile(usage-summary.json): %v", err)
	}
	if bytes.Contains(data, []byte(`"version"`)) {
		t.Fatalf("rebuilt summary retained obsolete version metadata: %s", data)
	}
}

func TestLoadSessionUsageSummaryToleratesLedgerLinesFromOtherVersions(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	raw := UsageSnapshot{InputTokens: 100, OutputTokens: 50}
	if err := ledger.AppendEvent(UsageEvent{
		AgentID:          "main",
		SelectedModelRef: "provider-a/model-1",
		RunningModelRef:  "provider-a/model-1",
		UsageRaw:         raw,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// A v0.7.2-style line (removed "version" field plus a hypothetical future
	// field) must still count; an undecodable line must be skipped without
	// aborting the scan; and a foreign-schema line that decodes into all
	// defaults (no event_id) must neither count as a zero-usage event nor
	// become the last event — as the tail line its empty ID would otherwise
	// match a stale summary's empty LastEventID and freeze freshness.
	legacyLines := `{"version":1,"event_id":"legacy-1","session_id":"s","occurred_at":"2026-07-03T08:57:37+08:00","agent_id":"main","running_model_ref":"provider-a/model-1","usage_raw":{"llm_calls":1,"input_tokens":30,"output_tokens":5},"billing_usage":{"llm_calls":1,"input_tokens":30,"output_tokens":5},"cost":{},"pricing_snapshot":{},"future_field":{"x":1}}
{"event_id":42}
{"id":"foreign-1","note":"written by a future chord version"}
`
	f, err := os.OpenFile(filepath.Join(dir, "usage.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(usage.jsonl): %v", err)
	}
	if _, err := f.WriteString(legacyLines); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(usage.jsonl): %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "usage-summary.json")); err != nil {
		t.Fatalf("Remove(usage-summary.json): %v", err)
	}

	rebuilt, err := LoadSessionUsageSummary(dir)
	if err != nil {
		t.Fatalf("LoadSessionUsageSummary: %v", err)
	}
	if rebuilt.EventCount != 2 {
		t.Fatalf("EventCount = %d, want 2 (current + legacy line, undecodable line skipped)", rebuilt.EventCount)
	}
	if got, want := rebuilt.UsageTotal.InputTokens, raw.InputTokens+30; got != want {
		t.Fatalf("InputTokens = %d, want %d", got, want)
	}
	if rebuilt.LastEventID != "legacy-1" {
		t.Fatalf("LastEventID = %q, want legacy-1", rebuilt.LastEventID)
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
			Cost:             CalculateUsageCost(costCfg, billing, config.ServiceTierStandard),
			PricingSnapshot:  PricingSnapshotFromCost(costCfg, billing, config.ServiceTierStandard),
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

func TestUsageLedgerBuildSessionStatsReturnsLatestAgentModelRefs(t *testing.T) {
	dir := t.TempDir()
	ledger := NewUsageLedger(dir, "/tmp/project")
	for _, evt := range []UsageEvent{
		{AgentID: "worker-1", SelectedModelRef: "provider/old", RunningModelRef: "provider/old"},
		{AgentID: "worker-1", SelectedModelRef: "provider/new", RunningModelRef: "provider/fallback"},
	} {
		if err := ledger.AppendEvent(evt); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	_, eventCount, refs, err := ledger.BuildSessionStatsWithAgentModelRefs()
	if err != nil {
		t.Fatalf("BuildSessionStatsWithAgentModelRefs: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("eventCount = %d, want 2", eventCount)
	}
	if got := refs["worker-1"]; got.Selected != "provider/new" || got.Running != "provider/fallback" {
		t.Fatalf("latest refs = %#v", got)
	}
}
