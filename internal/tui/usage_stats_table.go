package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/tui/overlaylist"
)

func usageModelItems(models map[string]*analytics.ModelStats) []overlaylist.TableItem {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return lessModelStatsByUsage(names[i], names[j], models[names[i]], models[names[j]])
	})
	items := make([]overlaylist.TableItem, 0, len(names))
	for _, name := range names {
		stats := models[name]
		if stats == nil {
			continue
		}
		cells := []string{
			name,
			fmt.Sprintf("%d", stats.Calls),
			formatUsageTokens(analytics.FullInputTokens(stats.InputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)),
			formatUsageTokens(stats.OutputTokens),
			formatCost(stats.EstimatedCost),
		}
		cells = append(cells, usageSpeedCells(stats.ThroughputTokens, stats.ThroughputNs, stats.ThroughputSamples, stats.FirstOutputTotalNs, stats.FirstOutputSamples)...)
		items = append(items, overlaylist.TableItem{ID: name, Label: name, Cells: cells})
	}
	return items
}

func usageAgentItems(agents map[string]*analytics.AgentStats) []overlaylist.TableItem {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return lessAgentStatsByUsage(names[i], names[j], agents[names[i]], agents[names[j]])
	})
	items := make([]overlaylist.TableItem, 0, len(names))
	for _, name := range names {
		stats := agents[name]
		if stats == nil {
			continue
		}
		label := name
		if name == "main" {
			label = "main"
		}
		items = append(items, overlaylist.TableItem{
			ID: name, Label: label,
			Cells: []string{
				label,
				fmt.Sprintf("%d", stats.LLMCalls),
				formatUsageTokens(analytics.FullInputTokens(stats.InputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)),
				formatUsageTokens(stats.OutputTokens),
				formatCost(stats.EstimatedCost),
			},
		})
	}
	return items
}

// lessModelStatsByUsage orders rows for the Models table: Cost, Calls, Input, Output (all descending), then name.
func lessModelStatsByUsage(nameA, nameB string, a, b *analytics.ModelStats) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	if a.EstimatedCost != b.EstimatedCost {
		return a.EstimatedCost > b.EstimatedCost
	}
	if a.Calls != b.Calls {
		return a.Calls > b.Calls
	}
	inputA := analytics.FullInputTokens(a.InputTokens, a.CacheReadTokens, a.CacheWriteTokens)
	inputB := analytics.FullInputTokens(b.InputTokens, b.CacheReadTokens, b.CacheWriteTokens)
	if inputA != inputB {
		return inputA > inputB
	}
	if a.OutputTokens != b.OutputTokens {
		return a.OutputTokens > b.OutputTokens
	}
	return nameA < nameB
}

// lessAgentStatsByUsage matches lessModelStatsByUsage but uses AgentStats.LLMCalls.
func lessAgentStatsByUsage(nameA, nameB string, a, b *analytics.AgentStats) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	if a.EstimatedCost != b.EstimatedCost {
		return a.EstimatedCost > b.EstimatedCost
	}
	if a.LLMCalls != b.LLMCalls {
		return a.LLMCalls > b.LLMCalls
	}
	inputA := analytics.FullInputTokens(a.InputTokens, a.CacheReadTokens, a.CacheWriteTokens)
	inputB := analytics.FullInputTokens(b.InputTokens, b.CacheReadTokens, b.CacheWriteTokens)
	if inputA != inputB {
		return inputA > inputB
	}
	if a.OutputTokens != b.OutputTokens {
		return a.OutputTokens > b.OutputTokens
	}
	return nameA < nameB
}

// lessUsageAggregateByUsage orders project rollups (per-model-ref / per-agent / etc.) like model stats.
func lessUsageAggregateByUsage(keyA, keyB string, a, b *analytics.UsageAggregate) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	if a.TotalCost != b.TotalCost {
		return a.TotalCost > b.TotalCost
	}
	if a.LLMCalls != b.LLMCalls {
		return a.LLMCalls > b.LLMCalls
	}
	inputA := analytics.FullInputTokens(a.InputTokens, a.CacheReadTokens, a.CacheWriteTokens)
	inputB := analytics.FullInputTokens(b.InputTokens, b.CacheReadTokens, b.CacheWriteTokens)
	if inputA != inputB {
		return inputA > inputB
	}
	if a.OutputTokens != b.OutputTokens {
		return a.OutputTokens > b.OutputTokens
	}
	return keyA < keyB
}

// usageRefAggregateItemsByUsage renders project rollup rows. The speed tail
// columns only exist on the Models table, so callers pass includeSpeed
// accordingly.
func usageRefAggregateItemsByUsage(groups map[string]*analytics.UsageAggregate, labelFn func(string) string, includeSpeed bool) []overlaylist.TableItem {
	keys := make([]string, 0, len(groups))
	for key, agg := range groups {
		if agg == nil {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return lessUsageAggregateByUsage(keys[i], keys[j], groups[keys[i]], groups[keys[j]])
	})
	items := make([]overlaylist.TableItem, 0, len(keys))
	for _, key := range keys {
		agg := groups[key]
		if agg == nil {
			continue
		}
		label := key
		if labelFn != nil {
			label = labelFn(key)
		}
		cells := []string{
			label,
			fmt.Sprintf("%d", agg.LLMCalls),
			formatUsageTokens(analytics.FullInputTokens(agg.InputTokens, agg.CacheReadTokens, agg.CacheWriteTokens)),
			formatUsageTokens(agg.OutputTokens),
			formatCost(agg.TotalCost),
		}
		if includeSpeed {
			cells = append(cells, usageSpeedCells(agg.ThroughputTokens, agg.ThroughputNs, agg.ThroughputSamples, agg.FirstOutputTotalNs, agg.FirstOutputSamples)...)
		}
		items = append(items, overlaylist.TableItem{ID: key, Label: label, Cells: cells})
	}
	return items
}

// usageSpeedColumns returns the tail columns shared by the session and
// project Models tables: duration-weighted output throughput, average
// first-visible-output latency, and the throughput sample count.
func usageSpeedColumns() []overlaylist.Column {
	return []overlaylist.Column{
		{Title: "tok/s", Width: 6, Align: 1},
		{Title: "TTFT", Width: 6, Align: 1},
		{Title: "Samples", Width: 8, Align: 1},
	}
}

// formatUsageThroughput renders the duration-weighted rate output ÷ request
// wall time: integers from ten tokens/s up, one decimal below, and "—"
// without an eligible sample.
func formatUsageThroughput(tokens, requestNs int64) string {
	if tokens <= 0 || requestNs <= 0 {
		return "—"
	}
	tps := float64(tokens) / (float64(requestNs) / float64(time.Second))
	if tps < 0 {
		return "—"
	}
	if tps >= 10 {
		return fmt.Sprintf("%.0f", tps)
	}
	return fmt.Sprintf("%.1f", tps)
}

// formatUsageFirstOutput renders the average first-visible-output latency:
// whole milliseconds below one second, one-decimal seconds below a minute,
// then compact minutes/seconds; "—" without an eligible sample.
func formatUsageFirstOutput(totalNs, samples int64) string {
	if samples <= 0 || totalNs <= 0 {
		return "—"
	}
	d := time.Duration(totalNs / samples)
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// usageSpeedCells renders the shared speed tail cells for one Models row.
func usageSpeedCells(throughputTokens, throughputNs, throughputSamples, firstOutputTotalNs, firstOutputSamples int64) []string {
	return []string{
		formatUsageThroughput(throughputTokens, throughputNs),
		formatUsageFirstOutput(firstOutputTotalNs, firstOutputSamples),
		fmt.Sprintf("%d", throughputSamples),
	}
}

func parseUsageDateKey(key string) (time.Time, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", key, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// usageDateAggregateItems sorts by calendar date descending (newest first); non-YYYY-MM-DD keys sort after valid dates, then lexicographically descending.
func usageDateAggregateItems(groups map[string]*analytics.UsageAggregate, labelFn func(string) string) []overlaylist.TableItem {
	keys := make([]string, 0, len(groups))
	for key, agg := range groups {
		if agg == nil {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ti, oki := parseUsageDateKey(keys[i])
		tj, okj := parseUsageDateKey(keys[j])
		switch {
		case oki && okj:
			return ti.After(tj)
		case oki && !okj:
			return true
		case !oki && okj:
			return false
		default:
			return keys[i] > keys[j]
		}
	})
	items := make([]overlaylist.TableItem, 0, len(keys))
	for _, key := range keys {
		agg := groups[key]
		if agg == nil {
			continue
		}
		label := key
		if labelFn != nil {
			label = labelFn(key)
		}
		items = append(items, overlaylist.TableItem{
			ID: key, Label: label,
			Cells: []string{
				label,
				fmt.Sprintf("%d", agg.LLMCalls),
				formatUsageTokens(analytics.FullInputTokens(agg.InputTokens, agg.CacheReadTokens, agg.CacheWriteTokens)),
				formatUsageTokens(agg.OutputTokens),
				formatCost(agg.TotalCost),
			},
		})
	}
	return items
}
