package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/analytics"
)

func TestHandleUsageStatsKeyTabResetsScroll(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.openUsageStats()
	m.usageStats.scrollOffset = 5

	_ = m.handleUsageStatsKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))

	if m.usageStats.view != statsViewModels {
		t.Fatalf("view = %v, want %v", m.usageStats.view, statsViewModels)
	}
	if m.usageStats.scrollOffset != 0 {
		t.Fatalf("scrollOffset = %d, want 0", m.usageStats.scrollOffset)
	}
}

func TestHandleUsageStatsKeyScopeStartsProjectLoad(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	m.workingDir = t.TempDir()
	m.openUsageStats()
	m.usageStats.scrollOffset = 7

	cmd := m.handleUsageStatsKey(tea.KeyPressMsg(tea.Key{Text: "s"}))

	if m.usageStats.scope != statsScopeProject {
		t.Fatalf("scope = %v, want %v", m.usageStats.scope, statsScopeProject)
	}
	if m.usageStats.scrollOffset != 0 {
		t.Fatalf("scrollOffset = %d, want 0", m.usageStats.scrollOffset)
	}
	if !m.usageStats.projectLoading {
		t.Fatal("projectLoading = false, want true")
	}
	if cmd == nil {
		t.Fatal("cmd = nil, want project load command")
	}

	msg := cmd()
	if msg == nil {
		t.Fatal("cmd() = nil, want projectUsageLoadedMsg")
	}
	if _, updateCmd := m.Update(msg); updateCmd != nil {
		_ = updateCmd
	}
	if m.usageStats.projectLoading {
		t.Fatal("projectLoading = true after load, want false")
	}
	if m.usageStats.projectLoadErr != "" {
		t.Fatalf("projectLoadErr = %q, want empty", m.usageStats.projectLoadErr)
	}
	if m.usageStats.projectReport == nil {
		t.Fatal("projectReport = nil, want loaded report")
	}
}

func TestUsageStatsTabUsesActiveStyle(t *testing.T) {
	m := NewModel(&sessionControlAgent{})
	if got := m.renderUsageStatsTab("Project", true); got != StatsTabActiveStyle.Render("Project") {
		t.Fatalf("active tab render = %q, want active style %q", got, StatsTabActiveStyle.Render("Project"))
	}
	if got := m.renderUsageStatsTab("Project", false); got != StatsTabStyle.Render("Project") {
		t.Fatalf("inactive tab render = %q, want base style %q", got, StatsTabStyle.Render("Project"))
	}
}

func TestUsageModelItemsSortedByCostThenCalls(t *testing.T) {
	models := map[string]*analytics.ModelStats{
		"cheap":  {Calls: 100, InputTokens: 1, OutputTokens: 1, EstimatedCost: 0.01},
		"mid":    {Calls: 50, InputTokens: 999, OutputTokens: 1, EstimatedCost: 0.05},
		"top":    {Calls: 1, InputTokens: 1, OutputTokens: 999, EstimatedCost: 0.10},
		"tieB":   {Calls: 5, InputTokens: 100, OutputTokens: 1, EstimatedCost: 0.02},
		"tieA":   {Calls: 5, InputTokens: 200, OutputTokens: 1, EstimatedCost: 0.02},
		"tieOut": {Calls: 5, InputTokens: 200, OutputTokens: 2, EstimatedCost: 0.02},
	}
	items := usageModelItems(models)
	var got []string
	for _, it := range items {
		got = append(got, it.Label)
	}
	want := []string{"top", "mid", "tieOut", "tieA", "tieB", "cheap"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestUsageRefAggregateItemsByUsageSortedLikeSessionModels(t *testing.T) {
	groups := map[string]*analytics.UsageAggregate{
		"x": {LLMCalls: 1, InputTokens: 1, OutputTokens: 1, TotalCost: 0.01},
		"y": {LLMCalls: 10, InputTokens: 1, OutputTokens: 1, TotalCost: 0.02},
	}
	items := usageRefAggregateItemsByUsage(groups, func(k string) string { return k })
	if len(items) != 2 || items[0].Label != "y" || items[1].Label != "x" {
		t.Fatalf("items = %#v, want y before x", items)
	}
}

func TestUsageAgentItemsSortedLikeModels(t *testing.T) {
	agents := map[string]*analytics.AgentStats{
		"a": {LLMCalls: 1, InputTokens: 1, OutputTokens: 1, EstimatedCost: 0.01},
		"b": {LLMCalls: 10, InputTokens: 1, OutputTokens: 1, EstimatedCost: 0.02},
	}
	items := usageAgentItems(agents)
	if len(items) != 2 || items[0].Label != "b" || items[1].Label != "a" {
		t.Fatalf("items = %#v, want b before a", items)
	}
}

func TestUsageDateAggregateItemsNewestFirst(t *testing.T) {
	groups := map[string]*analytics.UsageAggregate{
		"2026-03-19": {LLMCalls: 1, TotalCost: 0.01},
		"2026-03-23": {LLMCalls: 1, TotalCost: 0.01},
		"2026-03-21": {LLMCalls: 1, TotalCost: 0.01},
	}
	items := usageDateAggregateItems(groups, func(k string) string { return k })
	if len(items) != 3 {
		t.Fatalf("len = %d", len(items))
	}
	if items[0].Label != "2026-03-23" || items[1].Label != "2026-03-21" || items[2].Label != "2026-03-19" {
		t.Fatalf("order = %v %v %v", items[0].Label, items[1].Label, items[2].Label)
	}
}
