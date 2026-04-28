package tui

import (
	"testing"

	"github.com/keakon/chord/internal/analytics"
)

type usageStatsBenchAgent struct{ sessionControlAgent }

func (a *usageStatsBenchAgent) GetUsageStats() analytics.SessionStats {
	return analytics.SessionStats{
		LLMCalls:         42,
		InputTokens:      123456,
		OutputTokens:     654321,
		CacheReadTokens:  12000,
		CacheWriteTokens: 3400,
		ReasoningTokens:  5600,
		EstimatedCost:    12.34,
		ByModel: map[string]*analytics.ModelStats{
			"gpt-4.1": {Calls: 20, InputTokens: 60000, OutputTokens: 300000, EstimatedCost: 7.25},
			"gpt-5.5": {Calls: 12, InputTokens: 35000, OutputTokens: 200000, EstimatedCost: 3.10},
			"claude":  {Calls: 10, InputTokens: 28456, OutputTokens: 154321, EstimatedCost: 1.99},
		},
		ByAgent: map[string]*analytics.AgentStats{
			"main":     {LLMCalls: 18, InputTokens: 50000, OutputTokens: 250000, EstimatedCost: 5.60},
			"builder":  {LLMCalls: 14, InputTokens: 42000, OutputTokens: 220000, EstimatedCost: 4.10},
			"reviewer": {LLMCalls: 10, InputTokens: 31456, OutputTokens: 184321, EstimatedCost: 2.64},
		},
	}
}

func benchmarkModelForUsageStatsDialog() Model {
	m := benchmarkModelForView()
	m.agent = &usageStatsBenchAgent{}
	m.mode = ModeUsageStats
	m.usageStats = usageStatsState{
		prevMode: ModeInsert,
		scope:    statsScopeSession,
		view:     statsViewModels,
	}
	return m
}

func BenchmarkRenderUsageStatsDialogOpen(b *testing.B) {
	m := benchmarkModelForUsageStatsDialog()
	_ = m.renderUsageStatsDialog()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderUsageStatsDialog()
	}
}
