package analytics

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

type StatsRange string

const (
	StatsRangeAllTime StatsRange = "all_time"
	StatsRangeLast30D StatsRange = "last_30d"
	StatsRangeLast7D  StatsRange = "last_7d"
)

func (r StatsRange) normalize() StatsRange {
	switch r {
	case StatsRangeLast30D, StatsRangeLast7D:
		return r
	default:
		return StatsRangeAllTime
	}
}

func (r StatsRange) Label() string {
	switch r.normalize() {
	case StatsRangeLast30D:
		return "Last 30 days"
	case StatsRangeLast7D:
		return "Last 7 days"
	default:
		return "All time"
	}
}

func (r StatsRange) cutoffKey(now time.Time) (string, bool) {
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch r.normalize() {
	case StatsRangeLast30D:
		return today.AddDate(0, 0, -29).Format("2006-01-02"), true
	case StatsRangeLast7D:
		return today.AddDate(0, 0, -6).Format("2006-01-02"), true
	default:
		return "", false
	}
}

type ProjectUsageReport struct {
	ProjectPath  string
	Range        StatsRange
	SessionCount int
	ActiveDays   int
	FirstEventAt time.Time
	LastEventAt  time.Time
	UsageTotal   UsageAggregate
	ByModelRef   map[string]*UsageAggregate
	ByAgent      map[string]*UsageAggregate
	ByDate       map[string]*UsageAggregate
}

type filteredUsageSummary struct {
	UsageTotal UsageAggregate
	ByModelRef map[string]*UsageAggregate
	ByAgent    map[string]*UsageAggregate
	ByDate     map[string]*UsageAggregate
}

func BuildProjectUsageReport(projectRoot string, r StatsRange) (*ProjectUsageReport, error) {
	report := &ProjectUsageReport{
		ProjectPath: strings.TrimSpace(projectRoot),
		Range:       r.normalize(),
		ByModelRef:  make(map[string]*UsageAggregate),
		ByAgent:     make(map[string]*UsageAggregate),
		ByDate:      make(map[string]*UsageAggregate),
	}
	if report.ProjectPath == "" {
		return report, nil
	}

	locator, err := config.DefaultPathLocator()
	if err != nil {
		return nil, err
	}
	pl, err := locator.EnsureProject(report.ProjectPath)
	if err != nil {
		return nil, err
	}
	sessionsDir := pl.ProjectSessionsDir
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() > entries[j].Name()
	})

	cutoffKey, hasCutoff := report.Range.cutoffKey(time.Now())
	activeDates := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		sessionDir := sessionsDir + string(os.PathSeparator) + entry.Name()
		summary, err := LoadSessionUsageSummary(sessionDir)
		if err != nil {
			return nil, fmt.Errorf("load session usage summary %s: %w", entry.Name(), err)
		}
		filtered, firstDate, lastDate := sliceSummaryForRange(summary, report.Range, cutoffKey, hasCutoff)
		if filtered == nil {
			continue
		}

		report.SessionCount++
		mergeUsageAggregate(&report.UsageTotal, &filtered.UsageTotal)
		mergeUsageAggregateMap(report.ByModelRef, filtered.ByModelRef)
		mergeUsageAggregateMap(report.ByAgent, filtered.ByAgent)
		mergeUsageAggregateMap(report.ByDate, filtered.ByDate)
		for dateKey := range filtered.ByDate {
			activeDates[dateKey] = struct{}{}
		}
		reportFirst, reportLast := summaryTimeBounds(summary, report.Range, firstDate, lastDate)
		updateReportBounds(report, reportFirst, reportLast)
	}
	report.ActiveDays = len(activeDates)
	return report, nil
}

func sliceSummaryForRange(summary *SessionUsageSummary, r StatsRange, cutoffKey string, hasCutoff bool) (*filteredUsageSummary, string, string) {
	if summary == nil {
		return nil, "", ""
	}
	if r.normalize() == StatsRangeAllTime {
		if isUsageAggregateZero(&summary.UsageTotal) {
			return nil, "", ""
		}
		firstDate, lastDate := dateKeyBounds(summary.ByDate)
		return &filteredUsageSummary{
			UsageTotal: summary.UsageTotal,
			ByModelRef: cloneUsageAggregateMap(summary.ByModelRef),
			ByAgent:    cloneUsageAggregateMap(summary.ByAgent),
			ByDate:     cloneUsageAggregateMap(summary.ByDate),
		}, firstDate, lastDate
	}

	out := &filteredUsageSummary{
		ByModelRef: make(map[string]*UsageAggregate),
		ByAgent:    make(map[string]*UsageAggregate),
		ByDate:     make(map[string]*UsageAggregate),
	}
	var firstDate string
	var lastDate string
	for dateKey, total := range summary.ByDate {
		if total == nil || !dateKeyInRange(dateKey, cutoffKey, hasCutoff) {
			continue
		}
		mergeUsageAggregate(&out.UsageTotal, total)
		out.ByDate[dateKey] = cloneUsageAggregate(total)
		mergeUsageAggregateMap(out.ByModelRef, summary.ByDateModelRef[dateKey])
		mergeUsageAggregateMap(out.ByAgent, summary.ByDateAgent[dateKey])
		if firstDate == "" || dateKey < firstDate {
			firstDate = dateKey
		}
		if lastDate == "" || dateKey > lastDate {
			lastDate = dateKey
		}
	}
	if isUsageAggregateZero(&out.UsageTotal) {
		return nil, "", ""
	}
	return out, firstDate, lastDate
}

func dateKeyInRange(dateKey, cutoffKey string, hasCutoff bool) bool {
	dateKey = strings.TrimSpace(dateKey)
	if dateKey == "" {
		return false
	}
	if !hasCutoff {
		return true
	}
	return dateKey >= cutoffKey
}

func dateKeyBounds(byDate map[string]*UsageAggregate) (string, string) {
	if len(byDate) == 0 {
		return "", ""
	}
	keys := make([]string, 0, len(byDate))
	for key, agg := range byDate {
		if agg == nil {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return "", ""
	}
	sort.Strings(keys)
	return keys[0], keys[len(keys)-1]
}

func summaryTimeBounds(summary *SessionUsageSummary, r StatsRange, firstDate, lastDate string) (time.Time, time.Time) {
	if summary == nil {
		return time.Time{}, time.Time{}
	}
	if r.normalize() == StatsRangeAllTime {
		first := parseDateKey(firstDate)
		last := parseDateKey(lastDate)
		if !summary.CreatedAt.IsZero() {
			first = summary.CreatedAt
		}
		if !summary.LastUpdatedAt.IsZero() {
			last = summary.LastUpdatedAt
		}
		return first, last
	}
	return parseDateKey(firstDate), parseDateKey(lastDate)
}

func parseDateKey(dateKey string) time.Time {
	if strings.TrimSpace(dateKey) == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", dateKey)
	if err != nil {
		return time.Time{}
	}
	return t
}

func updateReportBounds(report *ProjectUsageReport, first, last time.Time) {
	if report == nil {
		return
	}
	if !first.IsZero() && (report.FirstEventAt.IsZero() || first.Before(report.FirstEventAt)) {
		report.FirstEventAt = first
	}
	if !last.IsZero() && (report.LastEventAt.IsZero() || last.After(report.LastEventAt)) {
		report.LastEventAt = last
	}
}

func mergeUsageAggregate(dst *UsageAggregate, src *UsageAggregate) {
	if dst == nil || src == nil {
		return
	}
	dst.LLMCalls += src.LLMCalls
	dst.InputTokens += src.InputTokens
	dst.OutputTokens += src.OutputTokens
	dst.CacheReadTokens += src.CacheReadTokens
	dst.CacheWriteTokens += src.CacheWriteTokens
	dst.ReasoningTokens += src.ReasoningTokens
	dst.ProviderTotalTokens += src.ProviderTotalTokens
	dst.BillingTotalTokens += src.BillingTotalTokens
	dst.TotalCost += src.TotalCost
}

func mergeUsageAggregateMap(dst map[string]*UsageAggregate, src map[string]*UsageAggregate) {
	if len(src) == 0 {
		return
	}
	for key, agg := range src {
		if agg == nil {
			continue
		}
		if dst[key] == nil {
			dst[key] = &UsageAggregate{}
		}
		mergeUsageAggregate(dst[key], agg)
	}
}

func cloneUsageAggregate(in *UsageAggregate) *UsageAggregate {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func isUsageAggregateZero(agg *UsageAggregate) bool {
	return agg == nil ||
		(agg.LLMCalls == 0 &&
			agg.InputTokens == 0 &&
			agg.OutputTokens == 0 &&
			agg.CacheReadTokens == 0 &&
			agg.CacheWriteTokens == 0 &&
			agg.ReasoningTokens == 0 &&
			agg.ProviderTotalTokens == 0 &&
			agg.BillingTotalTokens == 0 &&
			agg.TotalCost == 0)
}
