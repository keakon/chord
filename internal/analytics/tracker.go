package analytics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// ModelStats holds per-model aggregated statistics.
// Input/Output are prompt and generated; Cache* are cache read/write; Reasoning is thinking output when reported separately.
type ModelStats struct {
	Calls            int64   `json:"calls"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// AgentStats holds per-agent aggregated statistics and a nested per-model
// breakdown so the TUI can render both perspectives from the same snapshot.
type AgentStats struct {
	InputTokens      int64                  `json:"input_tokens"`
	OutputTokens     int64                  `json:"output_tokens"`
	CacheReadTokens  int64                  `json:"cache_read_tokens"`
	CacheWriteTokens int64                  `json:"cache_write_tokens"`
	ReasoningTokens  int64                  `json:"reasoning_tokens"`
	LLMCalls         int64                  `json:"llm_calls"`
	EstimatedCost    float64                `json:"estimated_cost"`
	ByModel          map[string]*ModelStats `json:"by_model,omitempty"`
}

// SessionStats holds session-wide aggregated statistics.
type SessionStats struct {
	InputTokens      int64                  `json:"input_tokens"`
	OutputTokens     int64                  `json:"output_tokens"`
	CacheReadTokens  int64                  `json:"cache_read_tokens"`
	CacheWriteTokens int64                  `json:"cache_write_tokens"`
	ReasoningTokens  int64                  `json:"reasoning_tokens"`
	LLMCalls         int64                  `json:"llm_calls"`
	EstimatedCost    float64                `json:"estimated_cost"`
	ByModel          map[string]*ModelStats `json:"by_model"`
	ByAgent          map[string]*AgentStats `json:"by_agent,omitempty"`
}

// UsageTracker accumulates LLM call statistics for a session.
// All methods are goroutine-safe.
type UsageTracker struct {
	mu    sync.Mutex
	stats SessionStats
}

// NewUsageTracker creates a new empty tracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{
		stats: SessionStats{
			ByModel: make(map[string]*ModelStats),
			ByAgent: make(map[string]*AgentStats),
		},
	}
}

// RestoreStats resets the tracker to the given statistics. This is used when
// resuming a session so that the tracker reflects exactly that session's
// historical data (not the current session's data combined with the resumed
// session's data). Any calls made after RestoreStats will accumulate on top.
func (t *UsageTracker) RestoreStats(s SessionStats) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stats = SessionStats{
		InputTokens:      s.InputTokens,
		OutputTokens:     s.OutputTokens,
		CacheReadTokens:  s.CacheReadTokens,
		CacheWriteTokens: s.CacheWriteTokens,
		ReasoningTokens:  s.ReasoningTokens,
		LLMCalls:         s.LLMCalls,
		EstimatedCost:    s.EstimatedCost,
		ByModel:          cloneModelStatsMap(s.ByModel),
		ByAgent:          cloneAgentStatsMap(s.ByAgent),
	}
}

// Record records token usage and cost from one LLM API call.
//
//   - model is the model key string (preferably "provider/model").
//   - cost is the per-token pricing from config (may be nil if not configured).
//   - usage is the raw token usage from the API response.
func (t *UsageTracker) Record(model string, cost *config.ModelCost, usage message.TokenUsage) {
	t.RecordForAgent("main", model, cost, usage)
}

// RecordForAgent records token usage and cost from one LLM API call, grouped
// under both the session total and the specified agent ID.
func (t *UsageTracker) RecordForAgent(agentID, model string, cost *config.ModelCost, usage message.TokenUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.applyRecordLocked(agentID, model, cost, usage)
}

func (t *UsageTracker) applyRecordLocked(agentID, model string, cost *config.ModelCost, usage message.TokenUsage) {
	agentID = normalizeAgentID(agentID)

	t.stats.InputTokens += int64(usage.InputTokens)
	t.stats.OutputTokens += int64(usage.OutputTokens)
	t.stats.CacheReadTokens += int64(usage.CacheReadTokens)
	t.stats.CacheWriteTokens += int64(usage.CacheWriteTokens)
	t.stats.ReasoningTokens += int64(usage.ReasoningTokens)
	t.stats.LLMCalls++

	rawUsage := UsageSnapshotFromTokenUsage(usage)
	callCost := CalculateUsageCost(cost, NormalizeBillingUsage(rawUsage)).TotalCost
	t.stats.EstimatedCost += callCost

	ms, ok := t.stats.ByModel[model]
	if !ok {
		ms = &ModelStats{}
		t.stats.ByModel[model] = ms
	}
	ms.Calls++
	ms.InputTokens += int64(usage.InputTokens)
	ms.OutputTokens += int64(usage.OutputTokens)
	ms.CacheReadTokens += int64(usage.CacheReadTokens)
	ms.CacheWriteTokens += int64(usage.CacheWriteTokens)
	ms.ReasoningTokens += int64(usage.ReasoningTokens)
	ms.EstimatedCost += callCost

	as, ok := t.stats.ByAgent[agentID]
	if !ok {
		as = &AgentStats{
			ByModel: make(map[string]*ModelStats),
		}
		t.stats.ByAgent[agentID] = as
	}
	as.InputTokens += int64(usage.InputTokens)
	as.OutputTokens += int64(usage.OutputTokens)
	as.CacheReadTokens += int64(usage.CacheReadTokens)
	as.CacheWriteTokens += int64(usage.CacheWriteTokens)
	as.ReasoningTokens += int64(usage.ReasoningTokens)
	as.LLMCalls++
	as.EstimatedCost += callCost

	ams, ok := as.ByModel[model]
	if !ok {
		ams = &ModelStats{}
		as.ByModel[model] = ams
	}
	ams.Calls++
	ams.InputTokens += int64(usage.InputTokens)
	ams.OutputTokens += int64(usage.OutputTokens)
	ams.CacheReadTokens += int64(usage.CacheReadTokens)
	ams.CacheWriteTokens += int64(usage.CacheWriteTokens)
	ams.ReasoningTokens += int64(usage.ReasoningTokens)
	ams.EstimatedCost += callCost
}

// AddUsageEvent applies one persisted usage event to the in-memory tracker.
func (t *UsageTracker) AddUsageEvent(event UsageEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	raw := event.UsageRaw
	usage := message.TokenUsage{
		InputTokens:      int(raw.InputTokens),
		OutputTokens:     int(raw.OutputTokens),
		CacheReadTokens:  int(raw.CacheReadTokens),
		CacheWriteTokens: int(raw.CacheWriteTokens),
		ReasoningTokens:  int(raw.ReasoningTokens),
	}
	model := strings.TrimSpace(event.RunningModelRef)
	if model == "" {
		model = strings.TrimSpace(event.SelectedModelRef)
	}
	if model == "" {
		model = usageKeyOrUnknown(event.ModelID)
	}
	costCfg := &config.ModelCost{
		Input:      event.PricingSnapshot.InputPerMillion,
		Output:     event.PricingSnapshot.OutputPerMillion,
		CacheRead:  event.PricingSnapshot.CacheReadPerMillion,
		CacheWrite: event.PricingSnapshot.CacheWritePerMillion,
	}
	if event.PricingSnapshot.Source == "" && event.Cost.TotalCost == 0 {
		costCfg = nil
	}
	t.applyRecordLocked(event.AgentID, model, costCfg, usage)
}

// SessionStatsForAgent returns cumulative token/cost for a single agent ID
// (e.g. "main" or a SubAgent instance id), shaped like SessionStats for TUI
// sidebar display. Unknown agents yield zeroed stats with empty maps.
func (t *UsageTracker) SessionStatsForAgent(agentID string) SessionStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	agentID = normalizeAgentID(agentID)
	as, ok := t.stats.ByAgent[agentID]
	if !ok || as == nil {
		return SessionStats{
			ByModel: make(map[string]*ModelStats),
			ByAgent: make(map[string]*AgentStats),
		}
	}
	return SessionStats{
		InputTokens:      as.InputTokens,
		OutputTokens:     as.OutputTokens,
		CacheReadTokens:  as.CacheReadTokens,
		CacheWriteTokens: as.CacheWriteTokens,
		ReasoningTokens:  as.ReasoningTokens,
		LLMCalls:         as.LLMCalls,
		EstimatedCost:    as.EstimatedCost,
		ByModel:          cloneModelStatsMap(as.ByModel),
		ByAgent:          make(map[string]*AgentStats),
	}
}

// SessionStats returns a snapshot of current session statistics. The returned
// value is a deep copy; callers may mutate it freely without affecting the
// tracker's internal state.
func (t *UsageTracker) SessionStats() SessionStats {
	t.mu.Lock()
	defer t.mu.Unlock()

	snapshot := t.stats
	snapshot.ByModel = cloneModelStatsMap(t.stats.ByModel)
	snapshot.ByAgent = cloneAgentStatsMap(t.stats.ByAgent)
	return snapshot
}

// FormatStats returns a human-readable summary of usage statistics, suitable
// for display as the response to a /stats slash command.
func (t *UsageTracker) FormatStats() string {
	stats := t.SessionStats()

	var sb strings.Builder

	sb.WriteString("Session Usage Statistics\n")
	sb.WriteString("========================\n\n")

	sb.WriteString(fmt.Sprintf("Total LLM Calls:   %d\n", stats.LLMCalls))
	sb.WriteString(fmt.Sprintf("Input Tokens:      %s\n", formatTokenCount(stats.InputTokens)))
	sb.WriteString(fmt.Sprintf("Output Tokens:     %s\n", formatTokenCount(stats.OutputTokens)))
	if stats.CacheReadTokens > 0 {
		sb.WriteString(fmt.Sprintf("Cache Read:        %s\n", formatTokenCount(stats.CacheReadTokens)))
	}
	if stats.CacheWriteTokens > 0 {
		sb.WriteString(fmt.Sprintf("Cache Write:       %s\n", formatTokenCount(stats.CacheWriteTokens)))
	}
	if stats.ReasoningTokens > 0 {
		sb.WriteString(fmt.Sprintf("Reasoning:         %s\n", formatTokenCount(stats.ReasoningTokens)))
	}
	sb.WriteString(fmt.Sprintf("Estimated Cost:    %s\n", formatUSD(stats.EstimatedCost)))

	if len(stats.ByModel) > 0 {
		sb.WriteString("\nPer-Model Breakdown\n")
		sb.WriteString("-------------------\n")

		// Sort model names for deterministic output.
		models := make([]string, 0, len(stats.ByModel))
		for m := range stats.ByModel {
			models = append(models, m)
		}
		sort.Strings(models)

		for _, model := range models {
			ms := stats.ByModel[model]
			sb.WriteString(fmt.Sprintf("\n  %s\n", model))
			sb.WriteString(fmt.Sprintf("    Calls:       %d\n", ms.Calls))
			sb.WriteString(fmt.Sprintf("    Input:       %s\n", formatTokenCount(ms.InputTokens)))
			sb.WriteString(fmt.Sprintf("    Output:      %s\n", formatTokenCount(ms.OutputTokens)))
			if ms.CacheReadTokens > 0 {
				sb.WriteString(fmt.Sprintf("    Cache Read:  %s\n", formatTokenCount(ms.CacheReadTokens)))
			}
			if ms.CacheWriteTokens > 0 {
				sb.WriteString(fmt.Sprintf("    Cache Write: %s\n", formatTokenCount(ms.CacheWriteTokens)))
			}
			if ms.ReasoningTokens > 0 {
				sb.WriteString(fmt.Sprintf("    Reasoning:   %s\n", formatTokenCount(ms.ReasoningTokens)))
			}
			sb.WriteString(fmt.Sprintf("    Cost:        %s\n", formatUSD(ms.EstimatedCost)))
		}
	}

	return sb.String()
}

// formatTokenCount renders a token count in a human-friendly way.
func formatTokenCount(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return formatInt(n)
}

func formatInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append(parts, s[len(s)-3:])
		s = s[:len(s)-3]
	}
	parts = append(parts, s)
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}

// formatUSD renders a cost in USD with 4 decimal places.
func formatUSD(cost float64) string {
	abs := cost
	if abs < 0 {
		abs = -abs
	}
	if abs == 0 {
		return "$0.00"
	}
	if abs < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

func cloneModelStatsMap(in map[string]*ModelStats) map[string]*ModelStats {
	if len(in) == 0 {
		return make(map[string]*ModelStats)
	}
	out := make(map[string]*ModelStats, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		cp := *v
		out[k] = &cp
	}
	return out
}

func cloneAgentStatsMap(in map[string]*AgentStats) map[string]*AgentStats {
	if len(in) == 0 {
		return make(map[string]*AgentStats)
	}
	out := make(map[string]*AgentStats, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		cp := *v
		cp.ByModel = cloneModelStatsMap(v.ByModel)
		out[k] = &cp
	}
	return out
}

func normalizeAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "main"
	}
	return agentID
}
