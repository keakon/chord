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
)

var diagnosticUsagePurposes = []string{
	UsagePurposeCompactionPolicy,
	UsagePurposeCompactionFailure,
	UsagePurposeOversizeRecovery,
	UsagePurposeContextProvenance,
	UsagePurposeCompactionLifecycle,
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
