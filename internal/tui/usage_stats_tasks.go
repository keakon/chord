package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/tui/overlaylist"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

const (
	taskUsageShortIDMinRunes = 6
	taskUsageAgentNameRunes  = 20
	taskUsageDescRunes       = 60
)

type taskUsageLoadedMsg struct {
	seq     uint64
	session string
	report  *agent.TaskUsageReport
	err     error
}

func (m *Model) taskUsageReporter() agent.TaskUsageReporter {
	if m == nil || m.agent == nil {
		return nil
	}
	reporter, _ := m.agent.(agent.TaskUsageReporter)
	return reporter
}

// enterTasksUsageView starts a fresh query whenever the view is entered. A
// previous report for the same session stays visible while the refresh runs,
// which also makes re-entering Tasks the retry path after a failed query.
func (m *Model) enterTasksUsageView() tea.Cmd {
	if m.usageStats.scope != statsScopeSession || m.usageStats.view != statsViewTasks {
		return nil
	}
	if m.taskUsageReporter() == nil {
		return nil
	}
	if m.usageStats.taskReport != nil && m.usageStats.taskReportSession != m.currentSessionID() {
		m.usageStats.taskReport = nil
		m.usageStats.taskLoadErr = ""
		m.usageStats.taskStale = false
	}
	m.usageStats.taskDirty = true
	return m.startTasksUsageReport()
}

// noteTaskUsageDataChanged coalesces live usage updates into at most one
// in-flight query. A failed query is retried only by re-entering the view, so
// a broken archive cannot turn usage updates into a retry loop.
func (m *Model) noteTaskUsageDataChanged() tea.Cmd {
	if m == nil || m.mode != ModeUsageStats || m.usageStats.scope != statsScopeSession || m.usageStats.view != statsViewTasks {
		return nil
	}
	if m.taskUsageReporter() == nil || m.usageStats.taskLoadErr != "" {
		return nil
	}
	m.usageStats.taskDirty = true
	if m.usageStats.taskLoading {
		return nil
	}
	return m.startTasksUsageReport()
}

func (m *Model) startTasksUsageReport() tea.Cmd {
	reporter := m.taskUsageReporter()
	if reporter == nil {
		return nil
	}
	if m.usageStats.taskLoading {
		m.usageStats.taskDirty = true
		return nil
	}
	m.usageStats.taskSeq++
	seq := m.usageStats.taskSeq
	session := m.currentSessionID()
	ctx, cancel := context.WithCancel(context.Background())
	m.usageStats.taskCancel = cancel
	m.usageStats.taskLoading = true
	m.usageStats.taskDirty = false
	m.invalidateUsageStatsCache()
	return func() tea.Msg {
		report, err := reporter.LoadTaskUsageReport(ctx)
		cancel()
		return taskUsageLoadedMsg{seq: seq, session: session, report: report, err: err}
	}
}

func (m *Model) cancelTasksUsageLoad() {
	if m.usageStats.taskCancel != nil {
		m.usageStats.taskCancel()
		m.usageStats.taskCancel = nil
	}
	m.usageStats.taskSeq++
	m.usageStats.taskLoading = false
}

func (m *Model) resetTasksUsageState() {
	m.cancelTasksUsageLoad()
	m.usageStats.taskReport = nil
	m.usageStats.taskReportSession = ""
	m.usageStats.taskLoadErr = ""
	m.usageStats.taskStale = false
	m.usageStats.taskDirty = false
	m.invalidateUsageStatsCache()
}

func (m *Model) handleTasksUsageLoaded(msg taskUsageLoadedMsg) tea.Cmd {
	if m.mode != ModeUsageStats || m.usageStats.scope != statsScopeSession || m.usageStats.view != statsViewTasks {
		return nil
	}
	if msg.seq != m.usageStats.taskSeq {
		return nil
	}
	m.usageStats.taskLoading = false
	m.usageStats.taskCancel = nil
	if msg.session != m.currentSessionID() {
		// The session changed without a panel reset (for example a restore that
		// did not pass through SessionSwitchStartedEvent). Never surface another
		// session's report, but honor a refresh that was requested meanwhile.
		if m.usageStats.taskDirty {
			return m.startTasksUsageReport()
		}
		return nil
	}
	if errors.Is(msg.err, context.Canceled) || errors.Is(msg.err, context.DeadlineExceeded) {
		return nil
	}
	if msg.err != nil {
		m.usageStats.taskDirty = false
		switch {
		case msg.report != nil:
			m.usageStats.taskReport = msg.report
			m.usageStats.taskReportSession = msg.session
			m.usageStats.taskStale = false
		case m.usageStats.taskReport != nil && m.usageStats.taskReportSession == msg.session:
			m.usageStats.taskStale = true
		default:
			m.usageStats.taskReport = nil
			m.usageStats.taskReportSession = ""
			m.usageStats.taskStale = false
		}
		m.usageStats.taskLoadErr = msg.err.Error()
		m.invalidateUsageStatsCache()
		return nil
	}
	m.usageStats.taskReport = msg.report
	m.usageStats.taskReportSession = msg.session
	m.usageStats.taskLoadErr = ""
	m.usageStats.taskStale = false
	m.invalidateUsageStatsCache()
	if m.usageStats.taskDirty {
		m.usageStats.taskDirty = false
		return m.startTasksUsageReport()
	}
	return nil
}

func (m *Model) sessionTaskUsageLines(width int) []string {
	title := currentStyles.DialogTitleStyle.Render("Session Tasks")
	if m.taskUsageReporter() == nil {
		return []string{title, currentStyles.DimStyle.Render("Task usage unavailable.")}
	}

	lines := []string{title}
	report := m.usageStats.taskReport
	errText := m.usageStats.taskLoadErr
	switch {
	case errText != "" && m.usageStats.taskStale:
		lines = append(lines, currentStyles.ErrorStyle.Render("Refresh failed; showing the previous report: "+errText))
	case errText != "" && report != nil:
		lines = append(lines, currentStyles.ErrorStyle.Render("External task history is incomplete: "+errText))
	case errText != "":
		lines = append(lines, currentStyles.ErrorStyle.Render("Failed to load task usage: "+errText))
	}
	if errText != "" {
		lines = append(lines, currentStyles.DimStyle.Render("Leave and re-enter Tasks to retry."))
		if report == nil {
			return lines
		}
	}
	if report == nil {
		lines = append(lines, currentStyles.DimStyle.Render("Loading task usage..."))
		return lines
	}
	if m.usageStats.taskLoading {
		lines = append(lines, currentStyles.DimStyle.Render("Refreshing..."))
	}

	rows := buildTaskUsageTableItems(report)
	if len(rows.chord) == 0 && len(rows.external) == 0 {
		if errText == "" {
			lines = append(lines, currentStyles.DimStyle.Render("(no task usage)"))
		}
		return lines
	}
	columns := []overlaylist.Column{
		{Title: "Task", Width: 0, Align: 0},
		{Title: "Calls", Width: 6, Align: 1},
		{Title: "Input", Width: 8, Align: 1},
		{Title: "Output", Width: 8, Align: 1},
		{Title: "Cost", Width: 8, Align: 1},
	}
	if len(rows.chord) > 0 {
		lines = append(lines, currentStyles.StatsTabLabelStyle.Render("Chord estimated usage"))
		lines = append(lines, renderUsageTable(columns, rows.chord, width)...)
		lines = append(lines, currentStyles.DimStyle.Render(fmt.Sprintf(
			"Total: %d calls · Input %s · Output %s · Estimated cost %s",
			report.Total.LLMCalls,
			formatUsageTokens(analytics.FullInputTokens(report.Total.InputTokens, report.Total.CacheReadTokens, report.Total.CacheWriteTokens)),
			formatUsageTokens(report.Total.OutputTokens),
			formatCost(report.Total.EstimatedCost),
		)))
		if rows.hasUntracked {
			lines = append(lines, currentStyles.DimStyle.Render("Untracked calls have no task ID; task rows cover only attributed calls."))
		}
	}
	if len(rows.external) > 0 {
		if len(rows.chord) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, currentStyles.StatsTabLabelStyle.Render("External reported usage"))
		lines = append(lines, renderUsageTable(columns, rows.external, width)...)
		lines = append(lines, currentStyles.DimStyle.Render("CLI cumulative reports may include nested agents; not included in Chord totals."))
	}
	return lines
}

type taskUsageTableRows struct {
	chord        []overlaylist.TableItem
	external     []overlaylist.TableItem
	hasUntracked bool
}

func buildTaskUsageTableItems(report *agent.TaskUsageReport) taskUsageTableRows {
	if report == nil {
		return taskUsageTableRows{}
	}
	shortIDs := shortTaskUsageIDs(report.Rows)
	chordRows := make([]agent.TaskUsageRow, 0, len(report.Rows))
	externalRows := make([]agent.TaskUsageRow, 0, len(report.Rows))
	for _, row := range report.Rows {
		if row.Ledger != nil || (row.Reported == nil && row.Backend == "") {
			chordRows = append(chordRows, row)
		}
		if row.Reported != nil || row.Backend != "" {
			externalRows = append(externalRows, row)
		}
	}
	sort.Slice(chordRows, func(i, j int) bool {
		return lessChordTaskUsageRow(chordRows[i], chordRows[j])
	})
	sort.Slice(externalRows, func(i, j int) bool {
		return lessExternalTaskUsageRow(externalRows[i], externalRows[j])
	})

	out := taskUsageTableRows{
		chord:    make([]overlaylist.TableItem, 0, len(chordRows)),
		external: make([]overlaylist.TableItem, 0, len(externalRows)),
	}
	for _, row := range chordRows {
		if row.Kind == agent.TaskUsageKindUntracked {
			out.hasUntracked = true
		}
		label := taskUsageRowLabel(row, shortIDs, false)
		out.chord = append(out.chord, overlaylist.TableItem{
			ID: row.TaskID, Label: label,
			Cells: ledgerTaskUsageCells(row, label),
		})
	}
	for _, row := range externalRows {
		label := taskUsageRowLabel(row, shortIDs, true)
		out.external = append(out.external, overlaylist.TableItem{
			ID: row.TaskID, Label: label,
			Cells: externalTaskUsageCells(row, label),
		})
	}
	return out
}

func lessChordTaskUsageRow(a, b agent.TaskUsageRow) bool {
	rankA, rankB := taskUsageKindRank(a.Kind), taskUsageKindRank(b.Kind)
	if rankA != rankB {
		return rankA < rankB
	}
	if rankA != 1 {
		return false
	}
	if (a.Ledger == nil) != (b.Ledger == nil) {
		return a.Ledger != nil
	}
	if a.Ledger != nil && a.Ledger.EstimatedCost != b.Ledger.EstimatedCost {
		return a.Ledger.EstimatedCost > b.Ledger.EstimatedCost
	}
	return a.TaskID < b.TaskID
}

func lessExternalTaskUsageRow(a, b agent.TaskUsageRow) bool {
	knownA, costA := externalTaskUsageCost(a)
	knownB, costB := externalTaskUsageCost(b)
	if knownA != knownB {
		return knownA
	}
	if knownA && costA != costB {
		return costA > costB
	}
	return a.TaskID < b.TaskID
}

func externalTaskUsageCost(row agent.TaskUsageRow) (bool, float64) {
	if row.Reported == nil || !row.Reported.CostKnown {
		return false, 0
	}
	return true, row.Reported.CostUSD
}

func taskUsageKindRank(kind string) int {
	switch kind {
	case agent.TaskUsageKindMain:
		return 0
	case agent.TaskUsageKindUntracked:
		return 2
	default:
		return 1
	}
}

func ledgerTaskUsageCells(row agent.TaskUsageRow, label string) []string {
	if row.Ledger == nil {
		return []string{label, "—", "—", "—", "—"}
	}
	return []string{
		label,
		fmt.Sprintf("%d", row.Ledger.LLMCalls),
		formatUsageTokens(analytics.FullInputTokens(row.Ledger.InputTokens, row.Ledger.CacheReadTokens, row.Ledger.CacheWriteTokens)),
		formatUsageTokens(row.Ledger.OutputTokens),
		formatCost(row.Ledger.EstimatedCost),
	}
}

func externalTaskUsageCells(row agent.TaskUsageRow, label string) []string {
	cells := []string{label, "—", "—", "—", "—"}
	reported := row.Reported
	if reported == nil {
		return cells
	}
	// AgentUsage.Tokens.InputTokens already includes cache reads. Normalize it
	// into the ledger's mutually exclusive buckets before summing input-side
	// tokens, or cache reads would be counted twice.
	billing := analytics.NormalizeBillingUsage(analytics.UsageSnapshotFromTokenUsage(reported.Tokens))
	cells[2] = formatUsageTokens(analytics.FullInputTokens(billing.InputTokens, billing.CacheReadTokens, billing.CacheWriteTokens))
	cells[3] = formatUsageTokens(billing.OutputTokens)
	if reported.CostKnown {
		cells[4] = formatCost(reported.CostUSD)
	} else {
		cells[4] = "unknown"
	}
	return cells
}

func taskUsageRowLabel(row agent.TaskUsageRow, shortIDs map[string]string, external bool) string {
	switch row.Kind {
	case agent.TaskUsageKindMain:
		return "Main agent"
	case agent.TaskUsageKindUntracked:
		return "Untracked"
	}
	taskID := strings.TrimSpace(row.TaskID)
	shortID := shortIDs[taskID]
	if shortID == "" {
		shortID = taskUsageLabelPart(taskID, 24)
	}
	parts := []string{shortID}
	if part := taskUsageLabelPart(row.AgentDefName, taskUsageAgentNameRunes); part != "" {
		parts = append(parts, part)
	}
	if part := taskUsageLabelPart(row.TaskDesc, taskUsageDescRunes); part != "" {
		parts = append(parts, part)
	}
	if external {
		if backend := taskUsageLabelPart(row.Backend, taskUsageAgentNameRunes); backend != "" {
			parts = append(parts, "("+backend+")")
		}
	}
	return strings.Join(parts, " · ")
}

func taskUsageLabelPart(s string, limit int) string {
	s = strings.Join(strings.Fields(terminaltext.Sanitize(s)), " ")
	if s == "" {
		return ""
	}
	return terminaltext.TruncateOneLine(s, limit)
}

// shortTaskUsageIDs gives every task row the shortest unique prefix of its
// TaskID (at least taskUsageShortIDMinRunes runes), so similar worker labels
// stay distinguishable after the table truncates the description.
func shortTaskUsageIDs(rows []agent.TaskUsageRow) map[string]string {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	maxLen := 0
	for _, row := range rows {
		if row.Kind != agent.TaskUsageKindTask {
			continue
		}
		id := strings.TrimSpace(row.TaskID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		maxLen = max(maxLen, len([]rune(id)))
	}
	sort.Strings(ids)
	length := taskUsageShortIDMinRunes
	for {
		used := make(map[string]struct{}, len(ids))
		unique := true
		for _, id := range ids {
			prefix := prefixRunes(id, length)
			if _, exists := used[prefix]; exists {
				unique = false
				break
			}
			used[prefix] = struct{}{}
		}
		if unique || length >= maxLen {
			break
		}
		length++
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[id] = prefixRunes(id, length)
	}
	return out
}

func prefixRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
