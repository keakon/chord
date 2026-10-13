package agent

import (
	"time"

	"github.com/keakon/chord/internal/contextdiag"
	"github.com/keakon/chord/internal/message"
)

const (
	diagnosticReductionTimeBudget     = 20 * time.Millisecond
	diagnosticReductionScanIncomplete = "scan_incomplete"
	diagnosticReductionOpaqueNative   = "opaque_native"
)

type diagnosticReductionBudget struct {
	deadline  time.Time
	fragments int
}

// Both request faces share one budget. Native receipts require JSON parsing in
// the capacity estimator; diagnostic collection must not repeat that work.
// Their reduction remains unavailable while composition and usage stay visible.
func (b *diagnosticReductionBudget) check(messages []message.Message) string {
	if len(messages) > contextdiag.MaxMessages {
		return diagnosticReductionScanIncomplete
	}
	for _, m := range messages {
		if time.Now().After(b.deadline) {
			return diagnosticReductionScanIncomplete
		}
		if m.NativeTools != nil && len(m.NativeTools.Items) > 0 {
			return diagnosticReductionOpaqueNative
		}
		count := 1 + len(m.Parts) + len(m.ThinkingBlocks) + len(m.ToolCalls) + len(m.MCPTools) + len(m.ResponsesOutput) + len(m.GeminiParts)
		if count > contextdiag.MaxFragments-b.fragments {
			return diagnosticReductionScanIncomplete
		}
		b.fragments += count
		for _, item := range m.ResponsesOutput {
			if len(item.Summary) > contextdiag.MaxFragments-b.fragments {
				return diagnosticReductionScanIncomplete
			}
			b.fragments += len(item.Summary)
		}
	}
	return ""
}
