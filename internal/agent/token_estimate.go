package agent

import (
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// Token estimation routes through the usage-calibrated estimator when the
// caller has a context manager (nil-safe fallback to the plain bytes/3
// heuristic otherwise). All context-pressure decisions — SubAgent recovery
// headroom, compaction input budgeting, reduction stats — share one accounting
// convention so a raised retention threshold or a model switch is measured the
// same way everywhere.
func estimateMessagesTokens(mgr *ctxmgr.Manager, messages []message.Message) int {
	if mgr == nil {
		return ctxmgr.EstimateMessagesTokens(messages)
	}
	return mgr.EstimateMessagesTokensCalibrated(messages)
}

func estimateMessageTokens(mgr *ctxmgr.Manager, msg message.Message) int {
	return estimateMessagesTokens(mgr, []message.Message{msg})
}

// estimateBytesForTokens inverts the token estimate so byte budgets derived
// from remaining-token budgets follow the same accounting convention.
func estimateBytesForTokens(mgr *ctxmgr.Manager, tokens int) int {
	if mgr == nil {
		return tokens * 3
	}
	return mgr.EstimateBytesForTokensCalibrated(tokens)
}
