package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

func addTaskUsageReportEvent(t *testing.T, a *MainAgent, agentID, taskID string, input, output int64, cost float64) {
	t.Helper()
	a.usageTracker.AddUsageEvent(analytics.UsageEvent{
		AgentID:         agentID,
		TaskID:          taskID,
		Purpose:         analytics.UsagePurposeChat,
		RunningModelRef: "sample/model-1",
		UsageRaw:        analytics.UsageSnapshot{InputTokens: input, OutputTokens: output},
		BillingUsage:    analytics.BillingUsage{InputTokens: input, OutputTokens: output, BillingTotalTokens: input + output},
		Cost:            analytics.UsageCost{TotalCost: cost},
	})
}

func taskUsageReportRow(t *testing.T, report *TaskUsageReport, taskID string) TaskUsageRow {
	t.Helper()
	for _, row := range report.Rows {
		if row.TaskID == taskID {
			return row
		}
	}
	t.Fatalf("task usage row %q not found in %#v", taskID, report.Rows)
	return TaskUsageRow{}
}

func externalTaskRecord(taskID string, attempt uint64, usage *AgentUsage) *DurableTaskRecord {
	return &DurableTaskRecord{
		TaskID:            taskID,
		State:             string(SubAgentStateCompleted),
		SettlementDurable: true,
		Attempt:           attempt,
		ExternalSession:   &ExternalTaskSession{Backend: config.BackendKindClaudeCode, SessionID: "session-" + taskID},
		Usage:             usage,
	}
}

func TestLoadTaskUsageReportJoinsLedgerLiveAndArchiveSources(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	addTaskUsageReportEvent(t, a, identity.MainAgentID, "", 100, 10, 0.1)
	addTaskUsageReportEvent(t, a, "worker-native", "task-native", 200, 20, 0.2)
	addTaskUsageReportEvent(t, a, "worker-old", "", 50, 5, 0.05)
	addTaskUsageReportEvent(t, a, "worker-arch-native", "task-arch-native", 70, 7, 0.07)

	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-native": {
			TaskID: "task-native", AgentDefName: "builder", TaskDesc: "native work",
			State:             string(SubAgentStateCompleted),
			SettlementDurable: true,
			Usage:             &AgentUsage{Tokens: message.TokenUsage{InputTokens: 9999}},
		},
		"task-idle": {
			TaskID: "task-idle", AgentDefName: "builder", TaskDesc: "still running",
			State: string(SubAgentStateRunning),
		},
		"task-empty": {
			TaskID: "task-empty", State: string(SubAgentStateCompleted), SettlementDurable: true,
		},
		"task-ext": {
			TaskID: "task-ext", AgentDefName: "claude", TaskDesc: "external work",
			State: string(SubAgentStateRunning), LatestInstanceID: "worker-ext-2", Attempt: 2,
			Usage:           &AgentUsage{Tokens: message.TokenUsage{InputTokens: 300}, CostUSD: 0.3, CostKnown: true},
			ExternalSession: &ExternalTaskSession{Backend: config.BackendKindClaudeCode, SessionID: "session-ext"},
		},
		"task-current": {
			TaskID: "task-current", State: string(SubAgentStateCompleted), SettlementDurable: true, Attempt: 2,
			Usage:           &AgentUsage{Tokens: message.TokenUsage{InputTokens: 250}, CostUSD: 0.25, CostKnown: true},
			ExternalSession: &ExternalTaskSession{Backend: config.BackendKindClaudeCode, SessionID: "session-current"},
		},
	})

	live := &externalWorker{
		instanceID:   "worker-ext-2",
		taskID:       "task-ext",
		agentDefName: "claude",
		taskDesc:     "external work",
		usage:        newExternalUsageTracker(),
	}
	live.usage.seed(AgentUsage{Tokens: message.TokenUsage{InputTokens: 400}, CostUSD: 0.4, CostKnown: true})
	liveOnly := &externalWorker{
		instanceID:   "worker-live-1",
		taskID:       "task-live",
		agentDefName: "claude",
		taskDesc:     "live only",
		usage:        newExternalUsageTracker(),
	}
	liveOnly.usage.seed(AgentUsage{Tokens: message.TokenUsage{InputTokens: 500}})
	a.subs.mu.Lock()
	a.subs.subAgents[live.instanceID] = live
	a.subs.subAgents[liveOnly.instanceID] = liveOnly
	a.subs.mu.Unlock()

	usage := func(input int, cost float64) *AgentUsage {
		return &AgentUsage{Tokens: message.TokenUsage{InputTokens: input}, CostUSD: cost, CostKnown: true}
	}
	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{
		externalTaskRecord("task-current", 1, usage(900, 0.9)),
		externalTaskRecord("task-arch", 1, usage(100, 0.1)),
		externalTaskRecord("task-arch", 2, usage(150, 0.15)),
		externalTaskRecord("task-arch", 2, usage(160, 0.16)),
		{TaskID: "task-arch-native", AgentDefName: "builder", TaskDesc: "archived native", State: string(SubAgentStateCompleted), SettlementDurable: true},
		{TaskID: "task-arch-empty", State: string(SubAgentStateCompleted), SettlementDurable: true},
	}); err != nil {
		t.Fatalf("append task archive: %v", err)
	}

	report, err := a.LoadTaskUsageReport(context.Background())
	if err != nil {
		t.Fatalf("LoadTaskUsageReport: %v", err)
	}
	if report.Total.LLMCalls != 4 || report.Total.InputTokens != 420 || !almostEqualFloat(report.Total.EstimatedCost, 0.42) {
		t.Fatalf("report total = %+v, want 4 calls / 420 input / 0.42", report.Total)
	}

	mainRow := taskUsageReportRow(t, report, "")
	if mainRow.Kind != TaskUsageKindMain || mainRow.Ledger == nil || mainRow.Ledger.LLMCalls != 1 {
		t.Fatalf("main row = %#v", mainRow)
	}
	var untracked *TaskUsageRow
	for i := range report.Rows {
		if report.Rows[i].Kind == TaskUsageKindUntracked {
			untracked = &report.Rows[i]
		}
	}
	if untracked == nil || untracked.Ledger == nil || untracked.Ledger.LLMCalls != 1 || untracked.Ledger.InputTokens != 50 {
		t.Fatalf("untracked row = %#v", untracked)
	}

	native := taskUsageReportRow(t, report, "task-native")
	if native.Ledger == nil || native.Ledger.LLMCalls != 1 || native.Reported != nil {
		t.Fatalf("native row = %#v, want ledger only", native)
	}
	idle := taskUsageReportRow(t, report, "task-idle")
	if idle.Ledger != nil || idle.Reported != nil || idle.TaskDesc != "still running" {
		t.Fatalf("idle row = %#v, want metadata with no observation", idle)
	}
	for _, row := range report.Rows {
		if row.TaskID == "task-empty" || row.TaskID == "task-arch-empty" {
			t.Fatalf("terminal task without usage should be omitted: %#v", row)
		}
	}

	externalRow := taskUsageReportRow(t, report, "task-ext")
	if externalRow.Reported == nil || externalRow.Reported.Tokens.InputTokens != 400 || !almostEqualFloat(externalRow.Reported.CostUSD, 0.4) {
		t.Fatalf("external row = %#v, want live cumulative report to win", externalRow)
	}
	currentRow := taskUsageReportRow(t, report, "task-current")
	if currentRow.Reported == nil || currentRow.Reported.Tokens.InputTokens != 250 {
		t.Fatalf("current record row = %#v, want current record over archive", currentRow)
	}
	archivedRow := taskUsageReportRow(t, report, "task-arch")
	if archivedRow.Reported == nil || archivedRow.Reported.Tokens.InputTokens != 160 {
		t.Fatalf("archived row = %#v, want latest attempt/revision/archive entry", archivedRow)
	}
	archivedNative := taskUsageReportRow(t, report, "task-arch-native")
	if archivedNative.Ledger == nil || archivedNative.Ledger.LLMCalls != 1 || archivedNative.Reported != nil || archivedNative.TaskDesc != "archived native" {
		t.Fatalf("archived native row = %#v", archivedNative)
	}
	liveRow := taskUsageReportRow(t, report, "task-live")
	if liveRow.Reported == nil || liveRow.Reported.Tokens.InputTokens != 500 || liveRow.Reported.CostKnown {
		t.Fatalf("live row = %#v, want reported tokens with unknown cost", liveRow)
	}
}

func TestLoadTaskUsageReportRefreshesArchiveProjection(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	usage := func(input int, cost float64) *AgentUsage {
		return &AgentUsage{Tokens: message.TokenUsage{InputTokens: input}, CostUSD: cost, CostKnown: true}
	}
	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{externalTaskRecord("task-arch", 1, usage(10, 0.01))}); err != nil {
		t.Fatalf("append task archive: %v", err)
	}
	first, err := a.LoadTaskUsageReport(context.Background())
	if err != nil {
		t.Fatalf("first LoadTaskUsageReport: %v", err)
	}
	if row := taskUsageReportRow(t, first, "task-arch"); row.Reported == nil || row.Reported.Tokens.InputTokens != 10 {
		t.Fatalf("first archived row = %#v", row)
	}

	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{
		externalTaskRecord("task-arch", 2, usage(20, 0.02)),
	}); err != nil {
		t.Fatalf("append task archive update: %v", err)
	}
	second, err := a.LoadTaskUsageReport(context.Background())
	if err != nil {
		t.Fatalf("second LoadTaskUsageReport: %v", err)
	}
	matches := 0
	for _, row := range second.Rows {
		if row.TaskID != "task-arch" {
			continue
		}
		matches++
		if row.Reported == nil || row.Reported.Tokens.InputTokens != 20 {
			t.Fatalf("second archived row = %#v, want refreshed attempt", row)
		}
	}
	if matches != 1 {
		t.Fatalf("archived task rows = %d, want 1", matches)
	}
}

func TestLoadTaskUsageReportReturnsPartialArchiveFailure(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	addTaskUsageReportEvent(t, a, "worker-a", "task-ledger", 100, 10, 0.1)
	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{
		externalTaskRecord("task-arch", 1, &AgentUsage{Tokens: message.TokenUsage{InputTokens: 20}, CostUSD: 0.02, CostKnown: true}),
	}); err != nil {
		t.Fatalf("append task archive: %v", err)
	}
	f, err := os.OpenFile(durableTaskArchivePath(a.sessionDir), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	if _, err := f.WriteString(`{"archived_at":`); err != nil {
		t.Fatalf("write corrupt archive tail: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	report, loadErr := a.LoadTaskUsageReport(context.Background())
	if loadErr == nil || !strings.Contains(loadErr.Error(), "decode task archive") {
		t.Fatalf("LoadTaskUsageReport error = %v, want decode failure", loadErr)
	}
	if report == nil {
		t.Fatal("expected partial report alongside the archive error")
	}
	if row := taskUsageReportRow(t, report, "task-arch"); row.Reported == nil || row.Reported.Tokens.InputTokens != 20 {
		t.Fatalf("partial archived row = %#v", row)
	}
	if row := taskUsageReportRow(t, report, "task-ledger"); row.Ledger == nil || row.Ledger.LLMCalls != 1 {
		t.Fatalf("partial ledger row = %#v", row)
	}
}

func TestLoadTaskUsageReportHonorsCancelledContext(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report, err := a.LoadTaskUsageReport(ctx); !errors.Is(err, context.Canceled) || report != nil {
		t.Fatalf("LoadTaskUsageReport = (%#v, %v), want cancelled", report, err)
	}
}

func TestLoadTaskUsageReportRejectsRegistryFromAnotherSession(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.subs.mu.Lock()
	a.subs.sessionEpoch++
	a.subs.mu.Unlock()
	if report, err := a.LoadTaskUsageReport(context.Background()); !errors.Is(err, errTaskUsageSessionChanging) || report != nil {
		t.Fatalf("LoadTaskUsageReport = (%#v, %v), want session-changing error", report, err)
	}
}

func TestArchiveRowNewerPrefersAttemptThenRevisionThenLaterEntry(t *testing.T) {
	base := taskUsageArchiveRow{TaskID: "task", Attempt: 2, LifecycleRevision: 3, order: 10}
	cases := []struct {
		name      string
		candidate taskUsageArchiveRow
		want      bool
	}{
		{name: "higher attempt", candidate: taskUsageArchiveRow{Attempt: 3, LifecycleRevision: 1, order: 1}, want: true},
		{name: "lower attempt", candidate: taskUsageArchiveRow{Attempt: 1, LifecycleRevision: 9, order: 99}, want: false},
		{name: "higher revision", candidate: taskUsageArchiveRow{Attempt: 2, LifecycleRevision: 4, order: 1}, want: true},
		{name: "lower revision", candidate: taskUsageArchiveRow{Attempt: 2, LifecycleRevision: 2, order: 99}, want: false},
		{name: "later duplicate", candidate: taskUsageArchiveRow{Attempt: 2, LifecycleRevision: 3, order: 11}, want: true},
		{name: "earlier duplicate", candidate: taskUsageArchiveRow{Attempt: 2, LifecycleRevision: 3, order: 9}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := archiveRowNewer(tc.candidate, base); got != tc.want {
				t.Fatalf("archiveRowNewer(%+v, %+v) = %v, want %v", tc.candidate, base, got, tc.want)
			}
		})
	}
}

// taskUsageArchiveMarkerForScan pins the whole archive file so
// scanTaskUsageArchive decodes every appended record.
func taskUsageArchiveMarkerForScan(t *testing.T, sessionDir string) taskUsageArchiveMarker {
	t.Helper()
	info, err := os.Stat(durableTaskArchivePath(sessionDir))
	if err != nil {
		t.Fatalf("stat task archive: %v", err)
	}
	return taskUsageArchiveMarker{
		exists:     true,
		sessionDir: sessionDir,
		size:       info.Size(),
		modTime:    info.ModTime(),
	}
}

func externalTaskRecordWithRevision(taskID string, attempt, revision uint64, usage *AgentUsage) *DurableTaskRecord {
	return &DurableTaskRecord{
		TaskID:            taskID,
		State:             string(SubAgentStateCompleted),
		SettlementDurable: true,
		Attempt:           attempt,
		LifecycleRevision: revision,
		ExternalSession:   &ExternalTaskSession{Backend: config.BackendKindClaudeCode, SessionID: "session-" + taskID},
		Usage:             usage,
	}
}

// A zero/nil usage is not an observation. The newest archived record must
// still win for lifecycle metadata, but it must not erase the latest positive
// cost already attributed to an external-only task (or the task row disappears
// from /stats entirely).
func TestTaskUsageArchiveKeepsPositiveCostWhenNewerRecordHasNilUsage(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	const taskID = "task-external-only"
	positive := &AgentUsage{
		Tokens:    message.TokenUsage{InputTokens: 1000, OutputTokens: 200},
		CostUSD:   0.42,
		CostKnown: true,
	}
	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{
		externalTaskRecordWithRevision(taskID, 1, 1, positive),
		externalTaskRecordWithRevision(taskID, 2, 2, nil),
	}); err != nil {
		t.Fatalf("append task archive: %v", err)
	}

	marker := taskUsageArchiveMarkerForScan(t, a.sessionDir)
	rows, err := scanTaskUsageArchive(context.Background(), a.sessionDir, marker)
	if err != nil {
		t.Fatalf("scanTaskUsageArchive: %v", err)
	}
	got, ok := rows[taskID]
	if !ok {
		t.Fatalf("scanTaskUsageArchive lost %q: %#v", taskID, rows)
	}
	if got.Reported == nil || !almostEqualFloat(got.Reported.CostUSD, 0.42) {
		t.Fatalf("scan kept row = %#v, want positive cost 0.42", got)
	}
	if got.Attempt != 2 || got.LifecycleRevision != 2 {
		t.Fatalf("scan row lifecycle metadata = %+v, want newest attempt/revision 2/2", got)
	}

	report := buildTaskUsageReport(analytics.SessionStats{}, nil, nil, rows)
	row := taskUsageReportRow(t, report, taskID)
	if row.Reported == nil || !almostEqualFloat(row.Reported.CostUSD, 0.42) {
		t.Fatalf("report row = %#v, want task row with cost 0.42", row)
	}
}

// The fix must not disable newest-wins: a genuinely newer positive report
// still overrides an older positive report for the same task.
func TestTaskUsageArchiveNewerPositiveOverridesOlderPositive(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	const taskID = "task-external-retry"
	withUsage := func(input int, cost float64, attempt, revision uint64) *DurableTaskRecord {
		return externalTaskRecordWithRevision(taskID, attempt, revision, &AgentUsage{
			Tokens:    message.TokenUsage{InputTokens: input},
			CostUSD:   cost,
			CostKnown: true,
		})
	}
	if err := appendTaskArchive(a.sessionDir, []*DurableTaskRecord{
		withUsage(100, 0.10, 1, 1),
		withUsage(420, 0.42, 2, 2),
	}); err != nil {
		t.Fatalf("append task archive: %v", err)
	}

	marker := taskUsageArchiveMarkerForScan(t, a.sessionDir)
	rows, err := scanTaskUsageArchive(context.Background(), a.sessionDir, marker)
	if err != nil {
		t.Fatalf("scanTaskUsageArchive: %v", err)
	}
	got := rows[taskID]
	if got.Reported == nil || got.Reported.Tokens.InputTokens != 420 || !almostEqualFloat(got.Reported.CostUSD, 0.42) {
		t.Fatalf("scan row = %#v, want newer positive 420 tokens / 0.42", got)
	}

	report := buildTaskUsageReport(analytics.SessionStats{}, nil, nil, rows)
	row := taskUsageReportRow(t, report, taskID)
	if row.Reported == nil || !almostEqualFloat(row.Reported.CostUSD, 0.42) {
		t.Fatalf("report row = %#v, want newer positive 0.42", row)
	}
}

func almostEqualFloat(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}
