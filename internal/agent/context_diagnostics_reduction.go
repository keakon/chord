package agent

import (
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// Freeze both faces with one calibration. The result is request metadata only;
// it never feeds back into reduction or cache-aware routing decisions.
func (a *MainAgent) freezeDiagnosticReduction(before, after []message.Message, ratio float64) {
	if a == nil {
		return
	}
	budget := diagnosticReductionBudget{deadline: time.Now().Add(diagnosticReductionTimeBudget)}
	reason := budget.check(before)
	if reason == "" {
		reason = budget.check(after)
	}
	if reason != "" {
		a.contextDiagnostics.mu.Lock()
		a.contextDiagnostics.reduction = contextdiag.Reduction{State: contextdiag.Unavailable, Reasons: []string{reason}}
		a.contextDiagnostics.mu.Unlock()
		return
	}
	stats := a.currentMainContextReductionStats()
	r := contextdiag.Reduction{State: contextdiag.Observed, Estimator: "reduction_frozen_ratio", Ratio: ratio, Before: int64(ctxmgr.EstimateMessagesTokensWithRatio(before, ratio)), After: int64(ctxmgr.EstimateMessagesTokensWithRatio(after, ratio)), Protected: stats.Protected, ReusedStable: stats.ReusedStable}
	if time.Now().After(budget.deadline) {
		r = contextdiag.Reduction{State: contextdiag.Unavailable, Reasons: []string{diagnosticReductionScanIncomplete}}
		a.contextDiagnostics.mu.Lock()
		a.contextDiagnostics.reduction = r
		a.contextDiagnostics.mu.Unlock()
		return
	}
	if a.contextReductionPolicy().Disabled {
		r.State = contextdiag.Disabled
	}
	for _, reason := range []string{stats.ProtectReason, stats.ReuseReason} {
		if reason != "" {
			r.Reasons = append(r.Reasons, reason)
		}
	}
	for reason := range stats.SkippedByReason {
		r.Reasons = append(r.Reasons, reason)
	}
	slices.Sort(r.Reasons)
	keys := make([]string, 0, len(stats.ByToolAndRule))
	for key := range stats.ByToolAndRule {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		b := stats.ByToolAndRule[key]
		tool, reason := key, "other"
		if before, after, ok := strings.CutLast(key, "/"); ok {
			tool, reason = before, after
		}
		rule := contextdiag.Rule{Tool: contextdiag.DisplayLabel(tool), Reason: reason, Count: b.Messages, SavedEstimate: int64(b.TokensSaved)}
		if len(r.Rules) < 16 {
			r.Rules = append(r.Rules, rule)
		} else {
			if len(r.Rules) == 16 {
				r.Rules = append(r.Rules, contextdiag.Rule{Reason: "other"})
			}
			r.Rules[16].Count += rule.Count
			r.Rules[16].SavedEstimate += rule.SavedEstimate
		}
	}
	a.contextDiagnostics.mu.Lock()
	a.contextDiagnostics.reduction = r
	a.contextDiagnostics.mu.Unlock()
}
func (a *MainAgent) diagnosticReduction() contextdiag.Reduction {
	a.contextDiagnostics.mu.Lock()
	defer a.contextDiagnostics.mu.Unlock()
	return contextdiag.CloneReduction(a.contextDiagnostics.reduction)
}
