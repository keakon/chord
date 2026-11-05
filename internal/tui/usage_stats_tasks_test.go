package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

type taskUsageTestAgent struct {
	*sessionControlAgent

	mu      sync.Mutex
	report  *agent.TaskUsageReport
	err     error
	waitCtx bool
	calls   int
}

func (a *taskUsageTestAgent) LoadTaskUsageReport(ctx context.Context) (*agent.TaskUsageReport, error) {
	a.mu.Lock()
	a.calls++
	report, err, waitCtx := a.report, a.err, a.waitCtx
	a.mu.Unlock()
	if waitCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return report, err
}

func (a *taskUsageTestAgent) setResult(report *agent.TaskUsageReport, err error) {
	a.mu.Lock()
	a.report, a.err = report, err
	a.mu.Unlock()
}

func (a *taskUsageTestAgent) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func newTaskUsageTestModel(t *testing.T, sessionID string) (*Model, *taskUsageTestAgent) {
	t.Helper()
	base := &sessionControlAgent{sessionSummary: &agent.SessionSummary{ID: sessionID}}
	fake := &taskUsageTestAgent{sessionControlAgent: base}
	m := NewModel(fake)
	m.openUsageStats()
	m.usageStats.view = statsViewTasks
	return &m, fake
}

func runTaskUsageCmd(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("cmd = nil, want task usage load command")
	}
	msg, ok := cmd().(taskUsageLoadedMsg)
	if !ok {
		t.Fatalf("cmd() = %#v, want taskUsageLoadedMsg", msg)
	}
	return m.handleTasksUsageLoaded(msg)
}

func taskUsageLinesText(t *testing.T, m *Model, width int) string {
	t.Helper()
	return terminaltext.StripANSI(strings.Join(m.sessionTaskUsageLines(width), "\n"))
}

func ledgerStats(calls, input, output int64, cost float64) *analytics.AgentStats {
	return &analytics.AgentStats{
		LLMCalls:      calls,
		InputTokens:   input,
		OutputTokens:  output,
		EstimatedCost: cost,
	}
}

func TestUsageStatsTasksIsSessionOnly(t *testing.T) {
	m, _ := newTaskUsageTestModel(t, "session-a")
	m.usageStats.scope = statsScopeSession
	if !slices.Contains(m.currentUsageStatsViews(), statsViewTasks) {
		t.Fatal("session views must include Tasks")
	}
	m.usageStats.scope = statsScopeProject
	if slices.Contains(m.currentUsageStatsViews(), statsViewTasks) {
		t.Fatal("project views must not include Tasks")
	}

	m.usageStats.scope = statsScopeSession
	m.usageStats.view = statsViewTasks
	m.toggleUsageStatsScope()
	if m.usageStats.view != statsViewOverview {
		t.Fatalf("view = %v after switching to Project, want Overview", m.usageStats.view)
	}
}

func TestUsageStatsTasksUnavailableWithoutReporter(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.openUsageStats()
	m.usageStats.view = statsViewTasks
	if got := taskUsageLinesText(t, &m, 80); !strings.Contains(got, "Task usage unavailable.") {
		t.Fatalf("lines = %q, want unavailable state", got)
	}
}

func TestUsageStatsTasksLoadsAndRendersGroups(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(&agent.TaskUsageReport{
		Total: analytics.AgentStats{LLMCalls: 6, InputTokens: 600, OutputTokens: 60, EstimatedCost: 0.65},
		Rows: []agent.TaskUsageRow{
			{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(2, 200, 20, 0.2)},
			{TaskID: "alpha", Kind: agent.TaskUsageKindTask, AgentDefName: "worker", TaskDesc: "first task", Ledger: ledgerStats(2, 200, 20, 0.3)},
			{TaskID: "bravo", Kind: agent.TaskUsageKindTask, AgentDefName: "worker", TaskDesc: "second task", Ledger: ledgerStats(1, 100, 10, 0.1)},
			{TaskID: "charlie", Kind: agent.TaskUsageKindTask, AgentDefName: "worker", TaskDesc: "still running"},
			{Kind: agent.TaskUsageKindUntracked, Ledger: ledgerStats(1, 100, 10, 0.05)},
			{TaskID: "delta", Kind: agent.TaskUsageKindTask, AgentDefName: "reviewer", TaskDesc: "external task", Backend: "claude-code", Reported: &agent.AgentUsage{
				Tokens: message.TokenUsage{InputTokens: 1000, OutputTokens: 100}, CostUSD: 0.07, CostKnown: true,
			}},
			{TaskID: "echo", Kind: agent.TaskUsageKindTask, AgentDefName: "reviewer", TaskDesc: "unknown cost", Backend: "claude-code", Reported: &agent.AgentUsage{
				Tokens: message.TokenUsage{InputTokens: 500, OutputTokens: 50},
			}},
			{TaskID: "foxtrot", Kind: agent.TaskUsageKindTask, AgentDefName: "reviewer", TaskDesc: "no usage yet", Backend: "claude-code"},
		},
	}, nil)

	cmd := m.enterTasksUsageView()
	if cmd == nil {
		t.Fatal("enterTasksUsageView() = nil, want load command")
	}
	if !m.usageStats.taskLoading {
		t.Fatal("taskLoading = false, want true after entering the view")
	}
	runTaskUsageCmd(t, m, cmd)
	if m.usageStats.taskLoading {
		t.Fatal("taskLoading = true after result, want false")
	}
	if m.usageStats.taskLoadErr != "" {
		t.Fatalf("taskLoadErr = %q, want empty", m.usageStats.taskLoadErr)
	}
	if m.usageStats.taskReport == nil {
		t.Fatal("taskReport = nil, want loaded report")
	}

	got := taskUsageLinesText(t, m, 110)
	for _, want := range []string{
		"Chord estimated usage",
		"External reported usage",
		"Main agent",
		"Untracked",
		"Total: 6 calls",
		"$0.07",
		"unknown",
		"claude-code",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("lines = %q, want %q", got, want)
		}
	}
	chordStart := strings.Index(got, "Chord estimated usage")
	externalStart := strings.Index(got, "External reported usage")
	if chordStart < 0 || externalStart < 0 || chordStart > externalStart {
		t.Fatalf("groups out of order: chord=%d external=%d", chordStart, externalStart)
	}
	mainIdx := strings.Index(got, "Main agent")
	alphaIdx := strings.Index(got, "alpha · worker")
	bravoIdx := strings.Index(got, "bravo · worker")
	nativeIdx := strings.Index(got, "charli · worker")
	untrackedIdx := strings.Index(got, "Untracked calls")
	if !(mainIdx < alphaIdx && alphaIdx < bravoIdx && bravoIdx < nativeIdx && nativeIdx < untrackedIdx) {
		t.Fatalf("chord order main=%d alpha=%d bravo=%d native=%d untracked=%d\n%s", mainIdx, alphaIdx, bravoIdx, nativeIdx, untrackedIdx, got)
	}
	deltaIdx := strings.Index(got, "delta · reviewer")
	echoIdx := strings.Index(got, "echo · reviewer")
	foxtrotIdx := strings.Index(got, "foxtro · reviewer")
	if !(externalStart < deltaIdx && deltaIdx < echoIdx && echoIdx < foxtrotIdx) {
		t.Fatalf("external order delta=%d echo=%d foxtrot=%d", deltaIdx, echoIdx, foxtrotIdx)
	}
}

func TestTaskUsageTableItemsGroupsSortAndFormat(t *testing.T) {
	report := &agent.TaskUsageReport{
		Rows: []agent.TaskUsageRow{
			{Kind: agent.TaskUsageKindUntracked, Ledger: ledgerStats(1, 10, 1, 0.01)},
			{TaskID: "worker-alpha-001", Kind: agent.TaskUsageKindTask, Ledger: ledgerStats(1, 10, 1, 0.02)},
			{TaskID: "worker-bravo-002", Kind: agent.TaskUsageKindTask, Ledger: ledgerStats(1, 10, 1, 0.05)},
			{TaskID: "native-hotel-003", Kind: agent.TaskUsageKindTask},
			{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(1, 10, 1, 0.1)},
			{TaskID: "cli-echo-004", Kind: agent.TaskUsageKindTask, Backend: "claude-code", Reported: &agent.AgentUsage{Tokens: message.TokenUsage{InputTokens: 10}, CostUSD: 0.07, CostKnown: true}},
			{TaskID: "cli-zulu-005", Kind: agent.TaskUsageKindTask, Backend: "claude-code", Reported: &agent.AgentUsage{CostKnown: true}},
			{TaskID: "cli-foxtrot-006", Kind: agent.TaskUsageKindTask, Backend: "claude-code", Reported: &agent.AgentUsage{Tokens: message.TokenUsage{InputTokens: 1000, CacheReadTokens: 900, CacheWriteTokens: 200}}},
			{TaskID: "cli-golf-007", Kind: agent.TaskUsageKindTask, Backend: "claude-code"},
		},
	}
	rows := buildTaskUsageTableItems(report)
	var chordIDs []string
	for _, item := range rows.chord {
		chordIDs = append(chordIDs, item.ID)
	}
	wantChord := []string{"", "worker-bravo-002", "worker-alpha-001", "native-hotel-003", ""}
	if !slices.Equal(chordIDs, wantChord) {
		t.Fatalf("chord order = %v, want %v", chordIDs, wantChord)
	}
	if !rows.hasUntracked {
		t.Fatal("hasUntracked = false, want true")
	}
	var externalIDs []string
	for _, item := range rows.external {
		externalIDs = append(externalIDs, item.ID)
	}
	wantExternal := []string{"cli-echo-004", "cli-zulu-005", "cli-foxtrot-006", "cli-golf-007"}
	if !slices.Equal(externalIDs, wantExternal) {
		t.Fatalf("external order = %v, want %v", externalIDs, wantExternal)
	}

	echo := rows.external[0]
	if echo.Cells[1] != "—" {
		t.Fatalf("external calls cell = %q, want dash", echo.Cells[1])
	}
	zulu := rows.external[1]
	if zulu.Cells[4] != "$0.00" {
		t.Fatalf("known zero cost cell = %q, want $0.00", zulu.Cells[4])
	}
	foxtrot := rows.external[2]
	if foxtrot.Cells[4] != "unknown" {
		t.Fatalf("unknown cost cell = %q, want unknown", foxtrot.Cells[4])
	}
	if foxtrot.Cells[2] != "1.2k" {
		t.Fatalf("external input cell = %q, want cache-read not double counted and cache-write included", foxtrot.Cells[2])
	}
	golf := rows.external[3]
	if golf.Cells[2] != "—" || golf.Cells[4] != "—" {
		t.Fatalf("unreported external cells = %v, want dashes", golf.Cells)
	}
	untracked := rows.chord[len(rows.chord)-1]
	if untracked.Label != "Untracked" {
		t.Fatalf("untracked label = %q", untracked.Label)
	}
}

func TestShortTaskUsageIDsDistinguishesPrefixes(t *testing.T) {
	rows := []agent.TaskUsageRow{
		{TaskID: "abcdef-alpha", Kind: agent.TaskUsageKindTask},
		{TaskID: "abcdef-bravo", Kind: agent.TaskUsageKindTask},
		{TaskID: "zzzzzz", Kind: agent.TaskUsageKindTask},
	}
	got := shortTaskUsageIDs(rows)
	if got["abcdef-alpha"] != "abcdef-a" || got["abcdef-bravo"] != "abcdef-b" {
		t.Fatalf("short IDs = %v, want 8-rune unique prefixes", got)
	}
	if got["zzzzzz"] != "zzzzzz" {
		t.Fatalf("short ID = %q, want the full short ID", got["zzzzzz"])
	}
}

func TestTaskUsageRowLabelSanitizesAndTruncates(t *testing.T) {
	row := agent.TaskUsageRow{
		TaskID:       "task-000123",
		Kind:         agent.TaskUsageKindTask,
		AgentDefName: "worker",
		TaskDesc:     "line one\nline two\tend",
	}
	label := taskUsageRowLabel(row, shortTaskUsageIDs([]agent.TaskUsageRow{row}), false)
	if label != "task-0 · worker · line one line two end" {
		t.Fatalf("label = %q", label)
	}
	long := row
	long.TaskDesc = strings.Repeat("x", 200)
	label = taskUsageRowLabel(long, shortTaskUsageIDs([]agent.TaskUsageRow{long}), false)
	if !strings.HasPrefix(label, "task-0 · worker · ") || len([]rune(label)) > len("task-0 · worker · ")+taskUsageDescRunes+3 {
		t.Fatalf("long label = %q", label)
	}
}

func TestUsageStatsTasksCoalescesRefresh(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(&agent.TaskUsageReport{Rows: []agent.TaskUsageRow{
		{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(1, 10, 1, 0.1)},
	}}, nil)

	cmd := m.enterTasksUsageView()
	if cmd == nil || !m.usageStats.taskLoading {
		t.Fatal("initial load not started")
	}
	if got := m.noteTaskUsageDataChanged(); got != nil {
		t.Fatal("noteTaskUsageDataChanged() returned a command while loading, want coalesced dirty flag")
	}
	if !m.usageStats.taskDirty {
		t.Fatal("taskDirty = false, want true")
	}
	next := runTaskUsageCmd(t, m, cmd)
	if fake.callCount() != 1 {
		t.Fatalf("calls = %d, want 1", fake.callCount())
	}
	if next == nil {
		t.Fatal("completion with dirty data must start one follow-up query")
	}
	runTaskUsageCmd(t, m, next)
	if fake.callCount() != 2 {
		t.Fatalf("calls = %d, want 2 after coalesced refresh", fake.callCount())
	}
	if m.usageStats.taskDirty {
		t.Fatal("taskDirty = true after refresh, want false")
	}
}

func TestUsageStatsTasksErrorKeepsPreviousReportAndDoesNotRetry(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(&agent.TaskUsageReport{Rows: []agent.TaskUsageRow{
		{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(1, 10, 1, 0.1)},
	}}, nil)
	runTaskUsageCmd(t, m, m.enterTasksUsageView())
	if m.usageStats.taskReport == nil {
		t.Fatal("initial report not loaded")
	}

	fake.setResult(nil, errors.New("decode task archive: bad record"))
	runTaskUsageCmd(t, m, m.noteTaskUsageDataChanged())
	if m.usageStats.taskReport == nil {
		t.Fatal("previous report dropped after refresh failure, want it kept")
	}
	if !m.usageStats.taskStale || m.usageStats.taskLoadErr == "" {
		t.Fatalf("stale=%v err=%q, want stale error state", m.usageStats.taskStale, m.usageStats.taskLoadErr)
	}
	got := taskUsageLinesText(t, m, 100)
	if !strings.Contains(got, "Refresh failed; showing the previous report") {
		t.Fatalf("lines = %q, want stale marker", got)
	}
	if strings.Contains(got, "(no task usage)") {
		t.Fatalf("lines = %q, error state must not render an empty table", got)
	}

	callsBefore := fake.callCount()
	if cmd := m.noteTaskUsageDataChanged(); cmd != nil {
		t.Fatal("data change after error started an automatic retry")
	}
	if fake.callCount() != callsBefore {
		t.Fatalf("calls = %d, want %d", fake.callCount(), callsBefore)
	}

	if cmd := m.enterTasksUsageView(); cmd == nil {
		t.Fatal("re-entering Tasks must retry the failed query")
	}
}

func TestUsageStatsTasksPartialReportIsMarkedIncomplete(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(&agent.TaskUsageReport{Rows: []agent.TaskUsageRow{
		{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(1, 10, 1, 0.1)},
	}}, errors.New("decode task archive: bad record"))
	runTaskUsageCmd(t, m, m.enterTasksUsageView())
	if m.usageStats.taskReport == nil {
		t.Fatal("partial report not kept")
	}
	if m.usageStats.taskStale {
		t.Fatal("partial report marked stale, want fresh-but-incomplete")
	}
	got := taskUsageLinesText(t, m, 100)
	if !strings.Contains(got, "External task history is incomplete") {
		t.Fatalf("lines = %q, want incomplete marker", got)
	}
	if strings.Contains(got, "(no task usage)") {
		t.Fatalf("lines = %q, want the partial report rendered", got)
	}
}

func TestUsageStatsTasksFailureWithoutReportShowsError(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(nil, errors.New("stat task archive: permission denied"))
	runTaskUsageCmd(t, m, m.enterTasksUsageView())
	got := taskUsageLinesText(t, m, 100)
	if !strings.Contains(got, "Failed to load task usage") || !strings.Contains(got, "Leave and re-enter Tasks to retry.") {
		t.Fatalf("lines = %q, want explicit error state", got)
	}
	if strings.Contains(got, "(no task usage)") {
		t.Fatalf("lines = %q, error state must not render an empty table", got)
	}
}

func TestUsageStatsTasksEmptyReportStates(t *testing.T) {
	for _, state := range []string{"complete", "partial", "stale"} {
		t.Run(state, func(t *testing.T) {
			m, fake := newTaskUsageTestModel(t, "session-a")
			report := &agent.TaskUsageReport{}
			fake.setResult(report, nil)
			if state == "stale" {
				runTaskUsageCmd(t, m, m.enterTasksUsageView())
				fake.setResult(nil, errors.New("archive unavailable"))
			} else if state == "partial" {
				fake.setResult(report, errors.New("archive unavailable"))
			}
			runTaskUsageCmd(t, m, m.enterTasksUsageView())
			got := taskUsageLinesText(t, m, 100)
			if state == "complete" {
				if !strings.Contains(got, "(no task usage)") {
					t.Fatalf("lines = %q, want the successful empty state", got)
				}
				if strings.Contains(got, "retry") {
					t.Fatalf("lines = %q, successful report must not ask for a retry", got)
				}
				return
			}
			if strings.Contains(got, "(no task usage)") {
				t.Fatalf("lines = %q, incomplete history must not claim no usage", got)
			}
			want := "External task history is incomplete"
			if state == "stale" {
				want = "Refresh failed; showing the previous report"
			}
			if !strings.Contains(got, want) || !strings.Contains(got, "Leave and re-enter Tasks to retry.") {
				t.Fatalf("lines = %q, want %q and retry instructions", got, want)
			}
		})
	}
}

func TestUsageStatsTasksDropsResultsFromAnotherSession(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(&agent.TaskUsageReport{Rows: []agent.TaskUsageRow{
		{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(1, 10, 1, 0.1)},
	}}, nil)
	cmd := m.enterTasksUsageView()
	if cmd == nil {
		t.Fatal("load not started")
	}
	m.resetTasksUsageState()
	fake.sessionControlAgent.sessionSummary = &agent.SessionSummary{ID: "session-b"}

	msg := cmd().(taskUsageLoadedMsg)
	if msg.session != "session-a" {
		t.Fatalf("request session = %q, want session-a", msg.session)
	}
	if next := m.handleTasksUsageLoaded(msg); next != nil {
		t.Fatal("late result started another query")
	}
	if m.usageStats.taskReport != nil {
		t.Fatal("late result from another session became the visible report")
	}
}

func TestUsageStatsTasksCloseCancelsInFlightQuery(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.mu.Lock()
	fake.waitCtx = true
	fake.mu.Unlock()
	cmd := m.enterTasksUsageView()
	if cmd == nil {
		t.Fatal("load not started")
	}
	m.closeUsageStats()
	msg, ok := cmd().(taskUsageLoadedMsg)
	if !ok {
		t.Fatalf("cmd() = %#v, want taskUsageLoadedMsg", msg)
	}
	if !errors.Is(msg.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", msg.err)
	}

	m.openUsageStats()
	m.usageStats.view = statsViewTasks
	if next := m.handleTasksUsageLoaded(msg); next != nil {
		t.Fatal("canceled result from the closed panel triggered a query")
	}
	if m.usageStats.taskReport != nil || m.usageStats.taskLoadErr != "" {
		t.Fatalf("report = %+v err = %q, canceled result must not surface", m.usageStats.taskReport, m.usageStats.taskLoadErr)
	}
}

func TestUsageStatsTasksReopenRejectsPreviousPanelResults(t *testing.T) {
	for _, outcome := range []string{"success", "error", "cancelled"} {
		for _, completed := range []bool{false, true} {
			order := "pending"
			if completed {
				order = "completed"
			}
			t.Run(outcome+"/"+order, func(t *testing.T) {
				m, fake := newTaskUsageTestModel(t, "session-a")
				oldReport := &agent.TaskUsageReport{Total: analytics.AgentStats{LLMCalls: 1}}
				switch outcome {
				case "success":
					fake.setResult(oldReport, nil)
				case "error":
					fake.setResult(oldReport, errors.New("archive unavailable"))
				case "cancelled":
					fake.setResult(nil, context.Canceled)
				}
				oldCmd := m.enterTasksUsageView()
				if oldCmd == nil {
					t.Fatal("previous panel query did not start")
				}
				queued := oldCmd().(taskUsageLoadedMsg)
				m.closeUsageStats()
				m.openUsageStats()
				m.usageStats.view = statsViewTasks
				newReport := &agent.TaskUsageReport{Total: analytics.AgentStats{LLMCalls: 2}}
				fake.setResult(newReport, nil)
				newCmd := m.enterTasksUsageView()
				if newCmd == nil {
					t.Fatal("reopened panel query did not start")
				}
				t.Cleanup(m.cancelTasksUsageLoad)
				if completed {
					runTaskUsageCmd(t, m, newCmd)
				}

				if next := m.handleTasksUsageLoaded(queued); next != nil {
					t.Fatal("previous panel result started another query")
				}
				if m.usageStats.taskLoadErr != "" || m.usageStats.taskStale {
					t.Fatalf("previous panel result changed the current error state: %q", m.usageStats.taskLoadErr)
				}
				if !completed {
					if m.usageStats.taskReport != nil || !m.usageStats.taskLoading || m.usageStats.taskCancel == nil {
						t.Fatal("previous panel result changed the pending query or its cancellation handle")
					}
					runTaskUsageCmd(t, m, newCmd)
				}
				if m.usageStats.taskReport != newReport || m.usageStats.taskLoading {
					t.Fatal("current report was lost or the current query did not settle")
				}
			})
		}
	}
}

func TestUsageStatsTasksResultInvalidatesRenderCache(t *testing.T) {
	m, fake := newTaskUsageTestModel(t, "session-a")
	fake.setResult(&agent.TaskUsageReport{Rows: []agent.TaskUsageRow{
		{Kind: agent.TaskUsageKindMain, Ledger: ledgerStats(1, 10, 1, 0.1)},
	}}, nil)
	versionBefore := m.usageStats.renderVersion
	runTaskUsageCmd(t, m, m.enterTasksUsageView())
	if m.usageStats.renderVersion == versionBefore {
		t.Fatal("render version not bumped after the report arrived")
	}
	_ = m.usageStatsLines(80)
	if m.usageStats.linesCacheLines == nil || m.usageStats.linesCacheVer != m.usageStats.renderVersion {
		t.Fatal("stats lines not cached after rendering")
	}
	callsBefore := fake.callCount()
	m.invalidateUsageStatsCache()
	if m.usageStats.taskDirty {
		t.Fatal("render-only invalidation marked task usage dirty")
	}
	if fake.callCount() != callsBefore {
		t.Fatalf("render-only invalidation queried task usage: calls = %d, want %d", fake.callCount(), callsBefore)
	}
}
