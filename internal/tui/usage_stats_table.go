package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keakon/chord/internal/analytics"
)

func usageModelItems(models map[string]*analytics.ModelStats) []OverlayTableItem {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return lessModelStatsByUsage(names[i], names[j], models[names[i]], models[names[j]])
	})
	items := make([]OverlayTableItem, 0, len(names))
	for _, name := range names {
		stats := models[name]
		if stats == nil {
			continue
		}
		items = append(items, OverlayTableItem{
			OverlayListItem: OverlayListItem{ID: name, Label: name},
			Cells: []string{
				name,
				fmt.Sprintf("%d", stats.Calls),
				formatUsageTokens(stats.InputTokens),
				formatUsageTokens(stats.OutputTokens),
				formatCost(stats.EstimatedCost),
			},
		})
	}
	return items
}

func usageAgentItems(agents map[string]*analytics.AgentStats) []OverlayTableItem {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return lessAgentStatsByUsage(names[i], names[j], agents[names[i]], agents[names[j]])
	})
	items := make([]OverlayTableItem, 0, len(names))
	for _, name := range names {
		stats := agents[name]
		if stats == nil {
			continue
		}
		label := name
		if name == "main" {
			label = "main"
		}
		items = append(items, OverlayTableItem{
			OverlayListItem: OverlayListItem{ID: name, Label: label},
			Cells: []string{
				label,
				fmt.Sprintf("%d", stats.LLMCalls),
				formatUsageTokens(stats.InputTokens),
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
	if a.InputTokens != b.InputTokens {
		return a.InputTokens > b.InputTokens
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
	if a.InputTokens != b.InputTokens {
		return a.InputTokens > b.InputTokens
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
	if a.InputTokens != b.InputTokens {
		return a.InputTokens > b.InputTokens
	}
	if a.OutputTokens != b.OutputTokens {
		return a.OutputTokens > b.OutputTokens
	}
	return keyA < keyB
}

func usageRefAggregateItemsByUsage(groups map[string]*analytics.UsageAggregate, labelFn func(string) string) []OverlayTableItem {
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
	items := make([]OverlayTableItem, 0, len(keys))
	for _, key := range keys {
		agg := groups[key]
		if agg == nil {
			continue
		}
		label := key
		if labelFn != nil {
			label = labelFn(key)
		}
		items = append(items, OverlayTableItem{
			OverlayListItem: OverlayListItem{ID: key, Label: label},
			Cells: []string{
				label,
				fmt.Sprintf("%d", agg.LLMCalls),
				formatUsageTokens(agg.InputTokens),
				formatUsageTokens(agg.OutputTokens),
				formatCost(agg.TotalCost),
			},
		})
	}
	return items
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
func usageDateAggregateItems(groups map[string]*analytics.UsageAggregate, labelFn func(string) string) []OverlayTableItem {
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
	items := make([]OverlayTableItem, 0, len(keys))
	for _, key := range keys {
		agg := groups[key]
		if agg == nil {
			continue
		}
		label := key
		if labelFn != nil {
			label = labelFn(key)
		}
		items = append(items, OverlayTableItem{
			OverlayListItem: OverlayListItem{ID: key, Label: label},
			Cells: []string{
				label,
				fmt.Sprintf("%d", agg.LLMCalls),
				formatUsageTokens(agg.InputTokens),
				formatUsageTokens(agg.OutputTokens),
				formatCost(agg.TotalCost),
			},
		})
	}
	return items
}
