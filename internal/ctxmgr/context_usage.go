package ctxmgr

import "github.com/keakon/chord/internal/message"

// ContextPromptTokens separates window observations from multi-request billing.
func ContextPromptTokens(usage message.TokenUsage) (int, bool) {
	if usage.Context != nil {
		if !usage.Context.Known {
			return 0, false
		}
		return usage.Context.PromptTokens, true
	}
	return FullPromptTokens(usage), hasUsageReport(usage)
}
