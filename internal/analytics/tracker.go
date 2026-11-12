package analytics

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

// ModelStats holds per-model aggregated statistics. InputTokens,
// CacheReadTokens, and CacheWriteTokens are mutually-exclusive input buckets.
type ModelStats struct {
	Calls            int64   `json:"calls"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
	// Throughput samples fold successful chat requests with client-measured
	// timing. Session-wide model stats include only main-agent samples;
	// per-agent and per-task model stats include their own samples.
	// ThroughputTokens is the output token sum over those samples,
	// independent of OutputTokens, which includes every purpose within
	// the aggregate's scope. tok/s = ThroughputTokens ÷ ThroughputNs is a duration-weighted
	// rate; persistence and restore fold sums, never rates.
	ThroughputTokens  int64 `json:"throughput_tokens,omitzero"`
	ThroughputNs      int64 `json:"throughput_ns,omitzero"`
	ThroughputSamples int64 `json:"throughput_samples,omitzero"`
	// FirstOutputTotalNs is the sum of first-visible-output latencies over
	// FirstOutputSamples eligible samples. Eligibility does not require
	// usable usage counts, so the two sample counts can legitimately differ.
	FirstOutputTotalNs int64 `json:"first_output_total_ns,omitzero"`
	FirstOutputSamples int64 `json:"first_output_samples,omitzero"`
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

// SessionStats holds session-wide aggregated statistics. InputTokens is the
// uncached input bucket; full prompt input is Input + CacheRead + CacheWrite.
// ByTask and Untracked split non-main calls into mutually-exclusive billing
// partitions: a call carrying a task identity lands in ByTask, every other
// non-main call lands in Untracked, and main calls stay in ByAgent only.
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
	ByTask           map[string]*AgentStats `json:"by_task,omitempty"`
	Untracked        *AgentStats            `json:"untracked,omitzero"`
	// CompactionLifecycle counts context-compaction lifecycle diagnostic
	// events keyed by "stage/trigger" (e.g. "applied/model_driven",
	// "skipped/model_driven"). It powers the /stats compaction-frequency
	// view without polluting the LLM-call accounting.
	CompactionLifecycle map[string]int64 `json:"compaction_lifecycle,omitempty"`
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
			ByTask:  make(map[string]*AgentStats),
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
		InputTokens:         s.InputTokens,
		OutputTokens:        s.OutputTokens,
		CacheReadTokens:     s.CacheReadTokens,
		CacheWriteTokens:    s.CacheWriteTokens,
		ReasoningTokens:     s.ReasoningTokens,
		LLMCalls:            s.LLMCalls,
		EstimatedCost:       s.EstimatedCost,
		ByModel:             cloneModelStatsMap(s.ByModel),
		ByAgent:             cloneAgentStatsMap(s.ByAgent),
		ByTask:              cloneAgentStatsMap(s.ByTask),
		Untracked:           cloneAgentStats(s.Untracked),
		CompactionLifecycle: cloneStringInt64Map(s.CompactionLifecycle),
	}
}

// Record records token usage and cost from one LLM API call.
//
//   - model is the model key string (preferably "provider/model").
//   - cost is the per-token pricing from config (may be nil if not configured).
//   - usage is the raw token usage from the API response.
func (t *UsageTracker) Record(model string, cost *config.ModelCost, usage message.TokenUsage) {
	t.RecordForAgent(identity.MainAgentID, model, cost, usage)
}

// RecordForAgent records token usage and cost from one LLM API call, grouped
// under both the session total and the specified agent ID.
func (t *UsageTracker) RecordForAgent(agentID, model string, cost *config.ModelCost, usage message.TokenUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.applyRecordLocked(agentID, model, cost, usage)
}

// usageContribution is one billed LLM call, already normalized into
// mutually-exclusive billing buckets with its recorded cost. Live recording
// and ledger replay both fold this shape so their totals cannot drift.
type usageContribution struct {
	agentID string
	taskID  string
	model   string
	purpose string
	raw     UsageSnapshot
	billing BillingUsage
	cost    float64
	// requestNs and firstOutputNs carry the client-measured per-attempt
	// timing from the event; zero marks "not measured" (failed attempts and
	// pre-timing historical events).
	requestNs     int64
	firstOutputNs int64
}

// isSpeedSampleEligible reports whether the contribution may fold into the
// throughput and first-output columns. Auxiliary calls are excluded even
// when they use the same model as a conversation.
func (c usageContribution) isSpeedSampleEligible() bool {
	return c.purpose == UsagePurposeChat
}

// isThroughputSample reports eligibility plus a usable numerator: output
// tokens must be present alongside the request wall clock.
func (c usageContribution) isThroughputSample() bool {
	return c.isSpeedSampleEligible() && c.requestNs > 0 && c.raw.OutputTokens > 0
}

// isFirstOutputSample reports eligibility for the first-visible-output
// latency column. It does not require usable usage counts, so throughput
// and latency sample counts can legitimately differ.
func (c usageContribution) isFirstOutputSample() bool {
	return c.isSpeedSampleEligible() && c.firstOutputNs > 0
}

// eventContribution projects a persisted usage event onto the shared
// aggregation shape. The event's recorded BillingUsage and Cost are used as-is:
// live tracking, replay, and the session summary must all report what was
// billed at event time instead of re-pricing with the current config.
func eventContribution(event UsageEvent) usageContribution {
	return usageContribution{
		agentID:       event.AgentID,
		taskID:        event.TaskID,
		model:         usageModelKey(event),
		purpose:       event.Purpose,
		raw:           event.UsageRaw,
		billing:       event.BillingUsage,
		cost:          event.Cost.TotalCost,
		requestNs:     event.RequestNs,
		firstOutputNs: event.FirstOutputNs,
	}
}

func (t *UsageTracker) applyRecordLocked(agentID, model string, cost *config.ModelCost, usage message.TokenUsage) {
	raw := UsageSnapshotFromTokenUsage(usage)
	billing := NormalizeBillingUsage(raw)
	t.stats.applyUsage(usageContribution{
		agentID: agentID,
		model:   model,
		raw:     raw,
		billing: billing,
		cost:    CalculateUsageCost(cost, billing, config.ServiceTierStandard).TotalCost,
	})
}

// applyUsage folds one call into the session total, per-model and per-agent
// views, and exactly one billing partition. Both live recording and ledger
// replay route through it so their totals cannot drift.
func (s *SessionStats) applyUsage(contribution usageContribution) {
	billing := contribution.billing
	raw := contribution.raw
	cost := contribution.cost

	s.InputTokens += billing.InputTokens
	s.OutputTokens += raw.OutputTokens
	s.CacheReadTokens += billing.CacheReadTokens
	s.CacheWriteTokens += billing.CacheWriteTokens
	s.ReasoningTokens += raw.ReasoningTokens
	s.LLMCalls++
	s.EstimatedCost += cost
	agentID := normalizeAgentID(contribution.agentID)
	addModelUsage(s.ByModel, contribution, agentID == identity.MainAgentID)
	addAgentUsage(s.ByAgent, agentID, contribution)

	// Every non-main call lands in exactly one billing partition. The task ID
	// is a runtime fact, never inferred from AgentKind: a historical record
	// without that optional field must still be visible as untracked rather
	// than silently escaping every partition.
	if agentID == identity.MainAgentID {
		return
	}
	if taskID := strings.TrimSpace(contribution.taskID); taskID != "" {
		if s.ByTask == nil {
			s.ByTask = make(map[string]*AgentStats)
		}
		addAgentUsage(s.ByTask, taskID, contribution)
		return
	}
	if s.Untracked == nil {
		s.Untracked = &AgentStats{ByModel: make(map[string]*ModelStats)}
	}
	addAgentUsageStats(s.Untracked, contribution)
}

func addModelUsage(group map[string]*ModelStats, contribution usageContribution, includeSpeed bool) {
	if group == nil {
		return
	}
	ms, ok := group[contribution.model]
	if !ok {
		ms = &ModelStats{}
		group[contribution.model] = ms
	}
	ms.Calls++
	ms.InputTokens += contribution.billing.InputTokens
	ms.OutputTokens += contribution.raw.OutputTokens
	ms.CacheReadTokens += contribution.billing.CacheReadTokens
	ms.CacheWriteTokens += contribution.billing.CacheWriteTokens
	ms.ReasoningTokens += contribution.raw.ReasoningTokens
	ms.EstimatedCost += contribution.cost
	if includeSpeed && contribution.isThroughputSample() {
		ms.ThroughputTokens += contribution.raw.OutputTokens
		ms.ThroughputNs += contribution.requestNs
		ms.ThroughputSamples++
	}
	if includeSpeed && contribution.isFirstOutputSample() {
		ms.FirstOutputTotalNs += contribution.firstOutputNs
		ms.FirstOutputSamples++
	}
}

func addAgentUsage(group map[string]*AgentStats, key string, contribution usageContribution) {
	if group == nil {
		return
	}
	as, ok := group[key]
	if !ok {
		as = &AgentStats{ByModel: make(map[string]*ModelStats)}
		group[key] = as
	}
	addAgentUsageStats(as, contribution)
}

func addAgentUsageStats(as *AgentStats, contribution usageContribution) {
	if as == nil {
		return
	}
	if as.ByModel == nil {
		as.ByModel = make(map[string]*ModelStats)
	}
	as.InputTokens += contribution.billing.InputTokens
	as.OutputTokens += contribution.raw.OutputTokens
	as.CacheReadTokens += contribution.billing.CacheReadTokens
	as.CacheWriteTokens += contribution.billing.CacheWriteTokens
	as.ReasoningTokens += contribution.raw.ReasoningTokens
	as.LLMCalls++
	as.EstimatedCost += contribution.cost
	addModelUsage(as.ByModel, contribution, true)
}

// AddUsageEvent applies one persisted usage event to the in-memory tracker.
func (t *UsageTracker) AddUsageEvent(event UsageEvent) {
	// Diagnostic events are bookkeeping, not LLM calls; runtime stats skip
	// them entirely so the TUI "Calls" count reflects real requests. The one
	// exception is the compaction-lifecycle purpose, which accumulates in a
	// dedicated bucket so /stats can report compaction frequency (e.g.
	// "applied/model_driven") without touching the LLM-call accounting.
	if IsDiagnosticUsagePurpose(event.Purpose) {
		if event.Purpose == UsagePurposeCompactionLifecycle {
			t.mu.Lock()
			if t.stats.CompactionLifecycle == nil {
				t.stats.CompactionLifecycle = make(map[string]int64)
			}
			t.stats.CompactionLifecycle[compactionLifecycleKey(event)]++
			t.mu.Unlock()
		}
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.stats.applyUsage(eventContribution(event))
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
	snapshot.ByTask = cloneAgentStatsMap(t.stats.ByTask)
	snapshot.Untracked = cloneAgentStats(t.stats.Untracked)
	snapshot.CompactionLifecycle = cloneStringInt64Map(t.stats.CompactionLifecycle)
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

	if len(stats.CompactionLifecycle) > 0 {
		sb.WriteString("\nContext Compaction (lifecycle events)\n")
		sb.WriteString("---------------------------------------\n")
		keys := make([]string, 0, len(stats.CompactionLifecycle))
		for key := range stats.CompactionLifecycle {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			sb.WriteString(fmt.Sprintf("  %-28s %d\n", key, stats.CompactionLifecycle[key]))
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
		out[k] = cloneAgentStats(v)
	}
	return out
}

func cloneAgentStats(in *AgentStats) *AgentStats {
	if in == nil {
		return nil
	}
	cp := *in
	cp.ByModel = cloneModelStatsMap(in.ByModel)
	return &cp
}

// cloneStringInt64Map returns a fresh copy of a string→count map, keeping the
// shared snapshot contract of SessionStats and RestoreStats: callers may
// mutate the returned map without affecting the tracker's internal state.
func cloneStringInt64Map(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return make(map[string]int64)
	}
	out := make(map[string]int64, len(in))
	maps.Copy(out, in)
	return out
}

// compactionLifecycleKey renders the diagnostic bucket key ("stage" or
// "stage/trigger") for a compaction-lifecycle event. The in-memory tracker
// (AddUsageEvent) and the ledger re-scan (UsageTrackerFromLedger) share it so
// the two accumulation paths can never disagree about the key shape.
func compactionLifecycleKey(event UsageEvent) string {
	stage := strings.TrimSpace(event.Diagnostic["stage"])
	trigger := strings.TrimSpace(event.Diagnostic["trigger"])
	key := stage
	if trigger != "" {
		key = stage + "/" + trigger
	}
	if key == "" {
		key = "unknown"
	}
	return key
}

func normalizeAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return identity.MainAgentID
	}
	return agentID
}
