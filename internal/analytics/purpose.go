package analytics

import "strings"

// Diagnostic usage purposes mark zero-usage bookkeeping events (compaction
// lifecycle and provenance, failure classification, oversize recovery). They
// are appended to usage.jsonl for inspection like every other event, but they
// are not LLM calls: aggregated stats skip them so "Calls" counts real
// requests only. Membership is decided by purpose, never by zero usage — a
// gateway with stream usage reporting disabled legitimately returns zero
// usage for real calls.
const (
	UsagePurposeCompactionPolicy    = "compaction_policy"
	UsagePurposeCompactionFailure   = "compaction_failure"
	UsagePurposeOversizeRecovery    = "oversize_recovery"
	UsagePurposeContextProvenance   = "context_provenance"
	UsagePurposeCompactionLifecycle = "context_compaction"
	// UsagePurposeContextPressureReminder and UsagePurposeCompactionWarning
	// record the context-pressure reminder and usage-driven externalization
	// warning deliveries (zero-usage bookkeeping). The reminder is sticky
	// (delivered_first / delivered_repeat per compaction window, optimization
	// 2.9); the warning stays one-shot per auto-compact generation
	// (plain "delivered").
	UsagePurposeContextPressureReminder = "context_pressure_reminder"
	UsagePurposeCompactionWarning       = "compaction_warning"
	// UsagePurposeCompactionGrace records the threshold grace-period lifecycle
	// (started / delivered_first / delivered_repeat / expired / hard_ceiling /
	// model_driven_settled) so telemetry can tell whether the grace bought a
	// model-driven reset or only delayed the safety net. The delivery stages
	// are sticky (optimization 2.9): the notice attaches on every deferred
	// request while the grace is active.
	UsagePurposeCompactionGrace = "compaction_grace"
)

var diagnosticUsagePurposes = []string{
	UsagePurposeCompactionPolicy,
	UsagePurposeCompactionFailure,
	UsagePurposeOversizeRecovery,
	UsagePurposeContextProvenance,
	UsagePurposeCompactionLifecycle,
	UsagePurposeContextPressureReminder,
	UsagePurposeCompactionWarning,
	UsagePurposeCompactionGrace,
	// Wall-clock time bookkeeping events (TIME sidebar section) are zero-usage
	// segments; they must stay out of token/cost aggregates and the Calls count.
	WalltimePurposeModel,
	WalltimePurposeCompaction,
	WalltimePurposeTool,
	WalltimePurposeCooldown,
	WalltimePurposeUserWait,
}

// IsDiagnosticUsagePurpose reports whether purpose identifies a diagnostic
// event. Purposes may carry a "/<detail>" suffix (compaction_policy/<detail>,
// oversize_recovery/<action>).
func IsDiagnosticUsagePurpose(purpose string) bool {
	for _, base := range diagnosticUsagePurposes {
		if purpose == base || strings.HasPrefix(purpose, base+"/") {
			return true
		}
	}
	return false
}
