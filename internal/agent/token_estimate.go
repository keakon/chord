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

// EstimateTokensForText exposes the usage-calibrated estimator for plain text
// (compact_context continuation-state budgeting). The text is wrapped in a
// single user message so the byte accounting and calibration ratio match every
// other message-based estimate; nil manager falls back to the plain bytes/3
// heuristic.
func (a *MainAgent) EstimateTokensForText(text string) int {
	if a == nil {
		return len(text) / 3
	}
	return estimateMessageTokens(a.ctxMgr, message.Message{Role: message.RoleUser, Content: text})
}
