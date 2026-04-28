package analytics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestBuildProjectUsageReportAggregatesAllTimeAndRange(t *testing.T) {
	projectRoot := t.TempDir()
	sessionsDir := setupTestProjectSessions(t, projectRoot)
	session1Dir := filepath.Join(sessionsDir, "20260320-1")
	session2Dir := filepath.Join(sessionsDir, "20260320-2")

	recent := time.Now().In(time.Local).AddDate(0, 0, -1)
	older := time.Now().In(time.Local).AddDate(0, 0, -10)

	appendUsageEventAt(t, session1Dir, projectRoot, recent, "main", "provider-a/model-1", UsageSnapshot{
		InputTokens:  100,
		OutputTokens: 50,
	})
	appendUsageEventAt(t, session1Dir, projectRoot, recent, "worker-1", "provider-b/model-2", UsageSnapshot{
		InputTokens:  60,
		OutputTokens: 20,
	})
	appendUsageEventAt(t, session2Dir, projectRoot, older, "main", "provider-a/model-1", UsageSnapshot{
		InputTokens:  40,
		OutputTokens: 10,
	})

	allTime, err := BuildProjectUsageReport(projectRoot, StatsRangeAllTime)
	if err != nil {
		t.Fatalf("BuildProjectUsageReport(all_time): %v", err)
	}
	if allTime.SessionCount != 2 {
		t.Fatalf("SessionCount = %d, want 2", allTime.SessionCount)
	}
	if allTime.ActiveDays != 2 {
		t.Fatalf("ActiveDays = %d, want 2", allTime.ActiveDays)
	}
	if allTime.UsageTotal.LLMCalls != 3 {
		t.Fatalf("LLMCalls = %d, want 3", allTime.UsageTotal.LLMCalls)
	}
	if got := allTime.ByModelRef["provider-a/model-1"]; got == nil || got.LLMCalls != 2 {
		t.Fatalf("provider-a/model-1 = %+v, want 2 calls", got)
	}
	if got := allTime.ByModelRef["provider-b/model-2"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("provider-b/model-2 = %+v, want 1 call", got)
	}
	if got := allTime.ByAgent["main"]; got == nil || got.LLMCalls != 2 {
		t.Fatalf("main agent = %+v, want 2 calls", got)
	}
	if got := allTime.ByAgent["worker-1"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("worker-1 agent = %+v, want 1 call", got)
	}
	if len(allTime.ByDate) != 2 {
		t.Fatalf("len(ByDate) = %d, want 2", len(allTime.ByDate))
	}

	last7d, err := BuildProjectUsageReport(projectRoot, StatsRangeLast7D)
	if err != nil {
		t.Fatalf("BuildProjectUsageReport(last_7d): %v", err)
	}
	if last7d.SessionCount != 1 {
		t.Fatalf("SessionCount(last_7d) = %d, want 1", last7d.SessionCount)
	}
	if last7d.ActiveDays != 1 {
		t.Fatalf("ActiveDays(last_7d) = %d, want 1", last7d.ActiveDays)
	}
	if last7d.UsageTotal.LLMCalls != 2 {
		t.Fatalf("LLMCalls(last_7d) = %d, want 2", last7d.UsageTotal.LLMCalls)
	}
	if got := last7d.ByModelRef["provider-a/model-1"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("provider-a/model-1(last_7d) = %+v, want 1 call", got)
	}
	if got := last7d.ByModelRef["provider-b/model-2"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("provider-b/model-2(last_7d) = %+v, want 1 call", got)
	}
	if got := last7d.ByAgent["main"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("main agent(last_7d) = %+v, want 1 call", got)
	}
	if got := last7d.ByAgent["worker-1"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("worker-1 agent(last_7d) = %+v, want 1 call", got)
	}
	if len(last7d.ByDate) != 1 {
		t.Fatalf("len(ByDate last_7d) = %d, want 1", len(last7d.ByDate))
	}
}

func TestBuildProjectUsageReportRebuildsOldSummaryVersion(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := newTestProjectSessionDir(t, projectRoot, "20260320-1")
	recent := time.Now().In(time.Local)

	appendUsageEventAt(t, sessionDir, projectRoot, recent, "main", "provider-a/model-1", UsageSnapshot{
		InputTokens:  120,
		OutputTokens: 30,
	})

	summaryPath := filepath.Join(sessionDir, "usage-summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("ReadFile(usage-summary.json): %v", err)
	}
	var summary SessionUsageSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("Unmarshal(summary): %v", err)
	}
	summary.Version = 1
	summary.ByDateModelRef = nil
	summary.ByDateAgent = nil
	rewritten, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(summary): %v", err)
	}
	if err := os.WriteFile(summaryPath, rewritten, 0o600); err != nil {
		t.Fatalf("WriteFile(summary): %v", err)
	}

	report, err := BuildProjectUsageReport(projectRoot, StatsRangeLast7D)
	if err != nil {
		t.Fatalf("BuildProjectUsageReport(last_7d): %v", err)
	}
	if got := report.ByModelRef["provider-a/model-1"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("provider-a/model-1(last_7d) = %+v, want 1 call after rebuild", got)
	}
	if got := report.ByAgent["main"]; got == nil || got.LLMCalls != 1 {
		t.Fatalf("main agent(last_7d) = %+v, want 1 call after rebuild", got)
	}

	upgraded, err := readUsageSummaryFile(summaryPath)
	if err != nil {
		t.Fatalf("readUsageSummaryFile(upgraded): %v", err)
	}
	if upgraded.Version != usageSummaryVersion {
		t.Fatalf("summary version = %d, want %d", upgraded.Version, usageSummaryVersion)
	}
	if len(upgraded.ByDateModelRef) == 0 {
		t.Fatal("ByDateModelRef = empty, want rebuilt nested model data")
	}
	if len(upgraded.ByDateAgent) == 0 {
		t.Fatal("ByDateAgent = empty, want rebuilt nested agent data")
	}
}

func setupTestProjectSessions(t *testing.T, projectRoot string) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	t.Setenv("CHORD_STATE_DIR", stateDir)
	locator, err := config.DefaultPathLocator()
	if err != nil {
		t.Fatalf("DefaultPathLocator: %v", err)
	}
	pl, err := locator.EnsureProject(projectRoot)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	return pl.ProjectSessionsDir
}

func newTestProjectSessionDir(t *testing.T, projectRoot, sessionID string) string {
	t.Helper()
	return filepath.Join(setupTestProjectSessions(t, projectRoot), sessionID)
}

func appendUsageEventAt(t *testing.T, sessionDir, projectRoot string, occurredAt time.Time, agentID, modelRef string, raw UsageSnapshot) {
	t.Helper()

	ledger := NewUsageLedger(sessionDir, projectRoot)
	costCfg := &config.ModelCost{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75}
	billing := NormalizeBillingUsage(raw)
	if err := ledger.AppendEvent(UsageEvent{
		OccurredAt:       occurredAt,
		LocalDate:        occurredAt.Format("2006-01-02"),
		Timezone:         occurredAt.Location().String(),
		AgentID:          agentID,
		AgentKind:        "main",
		AgentName:        agentID,
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
