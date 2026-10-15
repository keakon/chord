package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/toolname"
)

func contextRenderKey(o *contextOverlayState, w, h int, theme string) string {
	return fmt.Sprintf("%v/%d/%t/%t/%s/%s/%s/%d/%d/%d/%s/%d/%d/%d/%d/%d",
		o.view.Snapshot.ID, o.view.Snapshot.Revision, o.view.Available, o.view.Historical,
		o.view.AgentName, o.view.UnavailableReason, o.target, o.page, w, h, theme,
		o.view.WindowLimit, o.view.Snapshot.WindowLimit, o.view.SessionCalls, o.view.SessionCacheRead, o.view.SessionCacheWrite)
}

// contextWindowLimit picks the denominator for window shares. A snapshot
// carries the usable budget its request was dispatched against, so a historical
// request keeps the window it was actually measured against instead of being
// restated against whatever model is focused now. Zero means no usable budget,
// including a model whose configured headroom exhausts its input allowance.
func contextWindowLimit(v agent.ContextDiagnosticsView) int64 {
	if v.Available {
		return v.Snapshot.WindowLimit
	}
	return int64(v.WindowLimit)
}

// contextWindowShare renders a local or reported token count against the same
// usable input budget the sidebar gauge divides by, so the two surfaces can be
// read against one denominator. It stays empty when the focused target has no
// window (a parked or settled task), because a share of nothing is not a
// smaller share.
func contextWindowShare(tokens, limit int64) string {
	if limit <= 0 || tokens <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %s of the %s window", formatPercent(float64(tokens)/float64(limit)), formatTokens(int(limit)))
}

func contextDiagnosticLines(v agent.ContextDiagnosticsView, page, width int) []string {
	lines := []string{"1 Composition   2 Reduction   3 Cache", ""}
	if !v.Available {
		if v.UnavailableReason == "unsupported" {
			lines = append(lines, "This connection provides no context diagnostics")
		} else {
			lines = append(lines, "No dispatched request has been observed for this task yet")
		}
		lines = append(lines, contextSessionScopeNote(v)...)
		return contextWrapDiagnosticLines(lines, width)
	}
	s := v.Snapshot
	title := fmt.Sprintf("Latest request · %s · %s · %s · %s", v.AgentName, s.ModelRef, s.StartedAt.Local().Format("15:04:05"), contextDiagnosticLabel(s.State))
	if v.Historical {
		title += " · historical"
	}
	lines = append(lines, title, "")
	switch page {
	case 0:
		lines = append(lines, contextCompositionLines(v, s)...)
	case 1:
		lines = append(lines, contextReductionLines(s)...)
	case 2:
		lines = append(lines, contextCacheLines(s)...)
	}
	if page == 0 || page == 2 {
		lines = append(lines, "")
		lines = append(lines, contextSuggestionLines(v, s)...)
	}
	if len(s.Limitations) > 0 {
		lines = append(lines, "", "Limits:")
		for _, l := range s.Limitations {
			lines = append(lines, "  "+contextDiagnosticLabel(l))
		}
	}
	lines = append(lines, contextSessionScopeNote(v)...)
	return contextWrapDiagnosticLines(lines, width)
}

func contextCompositionLines(v agent.ContextDiagnosticsView, s contextdiag.Snapshot) []string {
	var lines []string
	total := contextCategoryTotal(s)
	unknown := 0
	for _, c := range s.Categories {
		unknown += c.Estimate.UnknownItems
	}
	lines = append(lines, fmt.Sprintf("Estimated input: ~%d tokens (%s)%s",
		total, contextDiagnosticLabel(s.Completeness), contextWindowShare(total, contextWindowLimit(v))))
	if s.Usage.State == contextdiag.Missing {
		lines = append(lines, "This response has not reported usage yet")
	} else {
		lines = append(lines, fmt.Sprintf("Input reported for this response: %d tokens (%s)%s",
			s.Usage.FullInput, contextDiagnosticLabel(s.Usage.State), contextWindowShare(s.Usage.FullInput, contextWindowLimit(v))))
	}
	if unknown > 0 {
		lines = append(lines, fmt.Sprintf("%d attachments or other fragments are not estimated", unknown))
	}
	for _, c := range s.Categories {
		if c.Estimate.Tokens == 0 && c.Estimate.UnknownItems == 0 {
			continue
		}
		row := fmt.Sprintf("%s: ~%d tokens", contextDiagnosticLabel(c.Kind), c.Estimate.Tokens)
		if c.Estimate.Tokens == 0 && c.Estimate.UnknownItems > 0 {
			row = contextDiagnosticLabel(c.Kind) + ": not estimated"
		}
		// The denominator here is the estimated total, not the context window:
		// these rows answer "what is this request made of", while the window
		// share above answers "how full is the window".
		if total > 0 && c.Estimate.Tokens > 0 {
			row += fmt.Sprintf(" · %.1f%% of the estimate", float64(c.Estimate.Tokens)*100/float64(total))
		}
		if c.Estimate.UnknownItems > 0 {
			row += fmt.Sprintf(" · %d unknown", c.Estimate.UnknownItems)
		}
		lines = append(lines, "", row)
		for _, source := range c.Sources {
			amount := fmt.Sprintf("~%d tokens", source.Estimate.Tokens)
			if source.Estimate.UnknownItems > 0 {
				if source.Estimate.Tokens == 0 {
					amount = "not estimated"
				}
				amount += fmt.Sprintf(" · %d unknown", source.Estimate.UnknownItems)
			}
			lines = append(lines, fmt.Sprintf("  %s/%s: %s · %d fragments", contextDiagnosticLabel(source.Kind), source.Label, amount, source.Count))
		}
		if c.Other.Tokens > 0 || c.Other.UnknownItems > 0 {
			lines = append(lines, fmt.Sprintf("  other: ~%d tokens · %d unknown", c.Other.Tokens, c.Other.UnknownItems))
		}
	}
	lines = append(lines, "", "Scope: the local input after protocol normalization (provider_input), estimated at UTF-8 bytes / 3. Adapter and server processing can change the real composition; protocol wrapping overhead is not included.")
	return lines
}

func contextReductionLines(s contextdiag.Snapshot) []string {
	var lines []string
	r := s.Reduction
	switch r.State {
	case contextdiag.Unavailable:
		lines = append(lines, "This request has no request-level reduction observation. Durable context compaction is not counted on this page.")
	case contextdiag.Disabled:
		lines = append(lines, "Request-level reduction is disabled.")
	default:
		lines = append(lines, fmt.Sprintf("Request input surface: ~%d → %d tokens", r.Before, r.After))
		if r.Before >= r.After {
			lines = append(lines, fmt.Sprintf("Net reduction ~%d tokens", r.Before-r.After))
		} else {
			lines = append(lines, fmt.Sprintf("Net increase ~%d tokens", r.After-r.Before))
		}
		lines = append(lines, "Both sides use the same frozen calibration; that range differs from the composition page.")
	}
	if r.Protected {
		lines = append(lines, "Some results were kept to preserve usable evidence.")
	}
	if r.ReusedStable {
		lines = append(lines, "Reused the previously stable reduced input surface.")
	}
	for _, reason := range r.Reasons {
		lines = append(lines, contextDiagnosticLabel(reason))
	}
	for _, rule := range r.Rules {
		lines = append(lines, fmt.Sprintf("%s · %s: %d items, ~%d tokens saved by this rule", rule.Tool, contextDiagnosticLabel(rule.Reason), rule.Count, rule.SavedEstimate))
	}
	if len(r.Rules) > 0 {
		lines = append(lines, "Per-rule savings are not additive.")
	}
	return lines
}

func contextCacheLines(s contextdiag.Snapshot) []string {
	var lines []string
	c := s.Cache
	lines = append(lines, "Local comparison: "+contextDiagnosticLabel(c.BaselineState))
	if len(c.LocalChanges) > 0 {
		names := make([]string, len(c.LocalChanges))
		for i, k := range c.LocalChanges {
			names[i] = contextDiagnosticLabel(k)
		}
		lines = append(lines, "Local changes: "+strings.Join(names, ", "))
	}
	lines = append(lines, "Message prefix: "+contextDiagnosticLabel(c.PrefixState))
	if c.ExpectedUsable {
		lines = append(lines, fmt.Sprintf("Local common message prefix: ~%d tokens (a hit is not guaranteed)", c.ExpectedPrefix))
	}
	// Everything below describes this one request. The sidebar's Cache R /
	// Cache W rows accumulate every call the task has made, so the two are
	// never the same quantity; the scope note at the foot says so explicitly.
	switch c.Explanation {
	case contextdiag.ReadObserved:
		lines = append(lines, fmt.Sprintf("Service reported %d cache-read tokens for this request", s.Usage.CacheRead))
	case contextdiag.WriteOnly:
		lines = append(lines, fmt.Sprintf("Service reported %d cache-write tokens and no cache read for this request", s.Usage.CacheWrite))
	default:
		lines = append(lines, "No cache read was confirmed for this request, and the reason is unknown")
	}
	lines = append(lines, "", "Local changes and service cache observations are shown separately; together they do not explain every cache or billing difference. Incremental transport does not mean earlier history stops occupying context.")
	return lines
}

// contextSessionScopeNote names the denominator a reader is most likely to
// conflate: this panel reports a single request, while the sidebar's cache
// rows accumulate the whole task. It stays silent when nothing has been
// recorded yet, so an untouched session is not annotated with zeroes. The
// wording also covers the unavailable state, where no request is on screen.
func contextSessionScopeNote(v agent.ContextDiagnosticsView) []string {
	if v.SessionCalls <= 0 && v.SessionCacheRead <= 0 && v.SessionCacheWrite <= 0 {
		return nil
	}
	note := fmt.Sprintf("This panel reports one request at a time. The sidebar's Cache R / Cache W rows are cumulative across all %s calls this task has made.", formatTokens(int(v.SessionCalls)))
	if v.SessionCacheRead > 0 {
		note += fmt.Sprintf(" Cumulative cache read so far: %s tokens.", formatUsageTokens(v.SessionCacheRead))
	}
	return []string{"", note}
}

// contextSuggestion is one actionable finding. rank leads the order (the
// near-capacity finding heads the list), then severity, then savingsTokens.
type contextSuggestion struct {
	rank          int
	severity      string // "warning" / "info"
	title         string
	detail        string
	savingsTokens int64
}

// contextSuggestionRankLead marks the finding that states the window is nearly
// full; every category finding that explains why ranks behind it.
const (
	contextSuggestionRankLead    = 0
	contextSuggestionRankSupport = 1
)

// These thresholds give every share both a relative floor and an absolute one,
// so a large context window cannot make a merely-large category look urgent.
const (
	contextSuggestionLargeCategoryPercent = 15
	contextSuggestionLargeCategoryTokens  = 10_000
	contextSuggestionNearCapacityPercent  = 80
	contextSuggestionMemoryPercent        = 5
	contextSuggestionMemoryTokens         = 5_000
)

func contextSuggestionLines(v agent.ContextDiagnosticsView, s contextdiag.Snapshot) []string {
	if contextWindowLimit(v) <= 0 {
		// Without a window there is nothing to be a share of, so a "none"
		// line would claim a small share of a window that does not exist.
		return []string{"Suggestions: unavailable — this target has no context window to measure against."}
	}
	suggestions := contextSuggestions(v, s)
	if len(suggestions) == 0 {
		return []string{"Suggestions: none. The available data does not meet any suggestion threshold."}
	}
	lines := []string{"Suggestions:"}
	for _, sug := range suggestions {
		lines = append(lines, fmt.Sprintf("  [%s] %s", sug.severity, sug.title))
		lines = append(lines, fmt.Sprintf("      %s", sug.detail))
	}
	return lines
}

func contextCategoryTokens(s contextdiag.Snapshot, kind string) int64 {
	for _, c := range s.Categories {
		if c.Kind == kind {
			return c.Estimate.Tokens
		}
	}
	return 0
}

func contextCategoryTotal(s contextdiag.Snapshot) int64 {
	var total int64
	for _, c := range s.Categories {
		total += c.Estimate.Tokens
	}
	return total
}

func contextSuggestions(v agent.ContextDiagnosticsView, s contextdiag.Snapshot) []contextSuggestion {
	var out []contextSuggestion
	limit := contextWindowLimit(v)
	if limit <= 0 {
		return nil
	}

	// Near capacity is judged on the service-reported baseline when the response
	// carried usage, and on the local estimate otherwise.
	base := s.Usage.FullInput
	if base <= 0 {
		base = contextCategoryTotal(s)
	}
	if base > 0 {
		if ratio := float64(base) / float64(limit); ratio*100 >= contextSuggestionNearCapacityPercent {
			out = append(out, contextSuggestion{
				rank:     contextSuggestionRankLead,
				severity: "warning",
				title:    fmt.Sprintf("Context is %.0f%% full (%s of the %s window)", ratio*100, formatUsageTokens(base), formatTokens(int(limit))),
				detail:   "Automatic compaction summarizes older messages when it triggers. Run /compact to request compaction now.",
			})
		}
	}

	if toolResults := contextCategoryTokens(s, contextdiag.ToolResults); toolResults >= contextSuggestionLargeCategoryTokens {
		if ratio := float64(toolResults) / float64(limit) * 100; ratio >= contextSuggestionLargeCategoryPercent {
			out = append(out, contextToolResultSuggestion(toolResults, ratio))
		}
	}
	if conversation := contextCategoryTokens(s, contextdiag.Conversation); conversation >= contextSuggestionLargeCategoryTokens {
		if ratio := float64(conversation) / float64(limit) * 100; ratio >= contextSuggestionLargeCategoryPercent {
			out = append(out, contextSuggestion{
				rank:          contextSuggestionRankSupport,
				severity:      "info",
				title:         fmt.Sprintf("Conversation using %s tokens (%.0f%% of the window)", formatUsageTokens(conversation), ratio),
				detail:        "Older turns are the usual cause. /compact summarizes them, and request-level reduction already trims stale tool output before every request.",
				savingsTokens: conversation / 2,
			})
		}
	}
	if instructions := contextCategoryTokens(s, contextdiag.Instructions); instructions >= contextSuggestionMemoryTokens {
		if ratio := float64(instructions) / float64(limit) * 100; ratio >= contextSuggestionMemoryPercent {
			out = append(out, contextSuggestion{
				rank:          contextSuggestionRankSupport,
				severity:      "info",
				title:         fmt.Sprintf("Environment and instruction files using %s tokens (%.0f%% of the window)", formatUsageTokens(instructions), ratio),
				detail:        "Project instruction files are injected into every request. Trim the ones this task does not need.",
				savingsTokens: instructions / 2,
			})
		}
	}
	if tools := contextCategoryTokens(s, contextdiag.Tools); tools >= contextSuggestionLargeCategoryTokens {
		if ratio := float64(tools) / float64(limit) * 100; ratio >= contextSuggestionLargeCategoryPercent {
			out = append(out, contextSuggestion{
				rank:          contextSuggestionRankSupport,
				severity:      "info",
				title:         fmt.Sprintf("Tool definitions using %s tokens (%.0f%% of the window)", formatUsageTokens(tools), ratio),
				detail:        "Every request pays for the whole tool set, so narrowing tool availability for this task is the lever.",
				savingsTokens: tools / 2,
			})
		}
	}

	slices.SortStableFunc(out, func(a, b contextSuggestion) int {
		if a.rank != b.rank {
			if a.rank < b.rank {
				return -1
			}
			return 1
		}
		if a.severity != b.severity {
			if a.severity == "warning" {
				return -1
			}
			return 1
		}
		switch {
		case a.savingsTokens > b.savingsTokens:
			return -1
		case a.savingsTokens < b.savingsTokens:
			return 1
		}
		return strings.Compare(a.title, b.title)
	})
	return out
}

// contextToolResultSuggestion names the concrete habit behind a large
// tool-result share, so the finding is actionable rather than a restatement of
// the number. Attribution stays at the category level: the snapshot records
// which rules trimmed which tool, not which tool produced the surviving bulk.
func contextToolResultSuggestion(tokens int64, ratio float64) contextSuggestion {
	return contextSuggestion{
		rank:     contextSuggestionRankSupport,
		severity: "warning",
		title:    fmt.Sprintf("Tool results using %s tokens (%.0f%% of the window)", formatUsageTokens(tokens), ratio),
		detail: fmt.Sprintf(
			"%s output is the usual cause: pipe it through head, tail, or grep, and read a range instead of a whole file. Switch to the read, grep, glob, or web_fetch tool when one of them fits.",
			toolname.Shell),
		savingsTokens: tokens / 2,
	}
}

func contextWrapDiagnosticLines(lines []string, width int) []string {
	var wrapped []string
	for _, line := range lines {
		for part := range strings.SplitSeq(ansi.Hardwrap(line, width, true), "\n") {
			wrapped = append(wrapped, ansi.Truncate(part, width, ""))
		}
	}
	return wrapped
}

func contextDiagnosticLabel(key string) string {
	labels := map[string]string{
		contextdiag.Dispatched: "dispatched, awaiting response", contextdiag.Completed: "completed", contextdiag.Failed: "failed", contextdiag.Cancelled: "cancelled", contextdiag.Superseded: "superseded by a later attempt", contextdiag.Estimated: "estimated", contextdiag.Partial: "partial", contextdiag.Reported: "reported", contextdiag.Missing: "not reported", contextdiag.Unavailable: "unavailable",
		contextdiag.System: "system prompt", contextdiag.Tools: "tool definitions", contextdiag.Instructions: "environment and instructions", contextdiag.Conversation: "conversation", contextdiag.ToolResults: "tool results", contextdiag.Runtime: "runtime reminders", contextdiag.Attachments: "attachments", contextdiag.Messages: "messages", contextdiag.Tool: "tool", contextdiag.Skill: "skill",
		contextdiag.First: "no local baseline", contextdiag.Comparable: "comparable", contextdiag.DomainChanged: "request domain changed", contextdiag.Same: "unchanged", contextdiag.Append: "appended", contextdiag.Truncate: "truncated", contextdiag.Rewrite: "rewritten", contextdiag.Unknown: "unknown",
		"adapter_protocol_overhead_unestimated": "adapter and protocol wrapping overhead is not estimated", "upstream_route_unobservable": "upstream account routing is not observable", "projection_incomplete": "input projection is incomplete, so the message prefix cannot be compared", "scan_incomplete": "collection limit reached, some content was not scanned", "sources_incomplete": "too many sources, some were merged into other", "opaque_native": "native replay content cannot be estimated", "native_definition_unknown": "service-side tool definitions are opaque", "schema_unavailable": "tool schema is outside the estimation scope", "custom_provider": "custom provider, the message prefix cannot be compared", "server_context_reuse": "the service reuses context, so the message prefix cannot be compared", "request_overrides": "request overrides can change the real input", "local_state_in_projection": "the input carries local state", "source_unspecified": "some content has no finer source breakdown", "wrap_up_grace": "evidence is kept during the wrap-up phase", "below_incremental_min": "the added reduction benefit is small, so the stable input surface is kept", "no_previous_savings": "no reduction benefit was observed before",
		"recent_high_risk": "recent high-risk tool evidence is kept", "large_but_unreduced": "some large results are still kept in full", "frozen_reduced": "reuses already-reduced stable results", "deferred_for_cache": "reduction deferred to preserve a stable prefix", "deferred_for_review": "waiting for the next evidence check", "recalled_input_protect": "keeps evidence that was fetched again",
		"repeated_output": "repeated output", "tool_error": "tool errors", "confirmation": "confirmation results", "diagnostics": "diagnostics results", "diff": "file diffs", "read_like": "read results", "search_result": "search results", "numbered_source": "numbered sources", "json_blob": "structured results", "long_log": "long logs", "shell_success": "successful command output", "record_listing": "record listings", "skill_instruction": "skill instructions", "generic_stale": "stale output", "other": "other",
	}
	if label, ok := labels[key]; ok {
		return label
	}
	return key
}
