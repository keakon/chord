package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/keakon/chord/internal/llm"
)

const (
	usageDrivenCompactionFailureThreshold = 2
	usageDrivenCompactionSuppressTurns    = 3
	compactionPolicyAnalyticsPurpose      = "compaction_policy"
	compactionFailureAnalyticsPurpose     = "compaction_failure"
)

type autoCompactionFailureState struct {
	ConsecutiveFailures int
	SuppressedUntilTurn uint64
	LastFailureClass    string
	LastFailureReason   string
}

type compactionFailureClass string

var (
	errCompactionInputTooLargeAfterTruncation    = errors.New("compaction input too large even after truncation")
	errCompactionPromptExceedsReservedBudget     = errors.New("compaction prompt still exceeds reserved context budget")
	errCompactionContextLimitTooSmall            = errors.New("compaction context limit too small after reserving output")
	errCompactionNoModelAvailable                = errors.New("no model available for context compaction")
	errCompactionModelSwitchFactoryNotConfigured = errors.New("model switch factory is not configured")
	errCompactionSelectedModelFallbackAlsoFailed = errors.New("selected model fallback also failed")
)

const (
	compactionFailureTransient  compactionFailureClass = "transient"
	compactionFailureStructural compactionFailureClass = "structural"
	compactionFailureUnknown    compactionFailureClass = "unknown"
)

type compactionTrigger struct {
	Manual         bool
	UsageDriven    bool
	LengthRecovery bool
}

func (t compactionTrigger) needed() bool {
	return t.Manual || t.UsageDriven || t.LengthRecovery
}

func (t compactionTrigger) analyticsName() string {
	if t.LengthRecovery {
		return "length_recovery_driven"
	}
	if t.UsageDriven {
		return "usage_driven"
	}
	return "manual"
}

func (a *MainAgent) resetAutoCompactionFailureState() {
	a.autoCompactFailureState = autoCompactionFailureState{}
}

func (a *MainAgent) clearUsageDrivenAutoCompactRequest() {
	a.autoCompactRequested.Store(false)
}

func (a *MainAgent) usageDrivenAutoCompactCheckTurn() uint64 {
	if a.turn != nil {
		return a.turn.ID
	}
	return a.nextTurnID + 1
}

func (a *MainAgent) isUsageDrivenAutoCompactSuppressed() bool {
	until := a.autoCompactFailureState.SuppressedUntilTurn
	if until == 0 {
		return false
	}
	return a.usageDrivenAutoCompactCheckTurn() <= until
}

func (a *MainAgent) recordUsageDrivenCompactionFailureClassified(err error, class compactionFailureClass) {
	state := &a.autoCompactFailureState
	state.ConsecutiveFailures++
	state.LastFailureClass = string(class)
	state.LastFailureReason = shortCompactionFailureReason(err)
	if state.ConsecutiveFailures < usageDrivenCompactionFailureThreshold {
		return
	}

	newUntil := a.nextTurnID + usageDrivenCompactionSuppressTurns
	if newUntil < state.SuppressedUntilTurn {
		newUntil = state.SuppressedUntilTurn
	}
	state.SuppressedUntilTurn = newUntil
	a.clearUsageDrivenAutoCompactRequest()
	a.emitToTUI(ToastEvent{
		Message: "Automatic context compaction paused for 3 turns after repeated failures. Try /compact manually.",
		Level:   "warn",
	})
	a.recordCompactionPolicyAnalyticsEvent("breaker_trip")
}

func (a *MainAgent) compactionTriggerForMainLLM() compactionTrigger {
	trigger := compactionTrigger{}
	if a.autoCompactRequested.Load() && !a.isUsageDrivenAutoCompactSuppressed() {
		trigger.UsageDriven = true
	}
	return trigger
}

func (a *MainAgent) noteCompactionFailure(err error) compactionFailureClass {
	if err == nil {
		return compactionFailureUnknown
	}
	class := classifyCompactionFailure(err)
	if a.compactionState.trigger.UsageDriven {
		a.recordUsageDrivenCompactionFailureClassified(err, class)
	}
	return class
}

func classifyCompactionFailure(err error) compactionFailureClass {
	err = canonicalizeCompactionFailureError(err)
	if err == nil {
		return compactionFailureUnknown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return compactionFailureTransient
	}

	var cooling *llm.AllKeysCoolingError
	if errors.As(err, &cooling) {
		return compactionFailureTransient
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return compactionFailureTransient
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "connect") {
		return compactionFailureTransient
	}

	var apiErr *llm.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 429, 529:
			return compactionFailureTransient
		case 413:
			return compactionFailureStructural
		case 400:
			if isCompactionStructuralRequestError(apiErr.Message) {
				return compactionFailureStructural
			}
		default:
			if apiErr.StatusCode >= 500 {
				return compactionFailureTransient
			}
		}
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "compaction input too large even after truncation"),
		strings.Contains(msg, "compaction prompt still exceeds reserved context budget"),
		strings.Contains(msg, "compaction context limit too small after reserving output"),
		strings.Contains(msg, "no model available for context compaction"),
		strings.Contains(msg, "model switch factory is not configured"),
		strings.Contains(msg, "selected model fallback also failed"),
		isCompactionStructuralRequestError(msg):
		return compactionFailureStructural
	case strings.Contains(msg, "timeout"),
		strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"):
		return compactionFailureTransient
	default:
		return compactionFailureUnknown
	}
}

func canonicalizeCompactionFailureError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, errCompactionInputTooLargeAfterTruncation.Error()):
		return fmt.Errorf("%w", errCompactionInputTooLargeAfterTruncation)
	case strings.Contains(msg, errCompactionPromptExceedsReservedBudget.Error()):
		return fmt.Errorf("%w", errCompactionPromptExceedsReservedBudget)
	case strings.Contains(msg, errCompactionContextLimitTooSmall.Error()):
		return fmt.Errorf("%w", errCompactionContextLimitTooSmall)
	case strings.Contains(msg, errCompactionNoModelAvailable.Error()):
		return fmt.Errorf("%w", errCompactionNoModelAvailable)
	case strings.Contains(msg, errCompactionModelSwitchFactoryNotConfigured.Error()):
		return fmt.Errorf("%w", errCompactionModelSwitchFactoryNotConfigured)
	case strings.Contains(msg, errCompactionSelectedModelFallbackAlsoFailed.Error()):
		return fmt.Errorf("%w", errCompactionSelectedModelFallbackAlsoFailed)
	default:
		return err
	}
}

func isCompactionStructuralRequestError(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "missing required parameter") ||
		strings.Contains(lower, "invalid_request_error") ||
		strings.Contains(lower, "invalid parameter") ||
		strings.Contains(lower, "invalid request") ||
		strings.Contains(lower, "stream must be set to true") ||
		strings.Contains(lower, "unsupported parameter") ||
		strings.Contains(lower, "unsupported value") ||
		strings.Contains(lower, "not supported for this model") ||
		strings.Contains(lower, "model does not support") ||
		strings.Contains(lower, "store must be set to false")
}

func shortCompactionFailureReason(err error) string {
	if err == nil {
		return ""
	}
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		return ""
	}
	if line, _, ok := strings.Cut(reason, "\n"); ok {
		reason = strings.TrimSpace(line)
	}
	const maxReasonLen = 160
	if len(reason) <= maxReasonLen {
		return reason
	}
	return strings.TrimSpace(reason[:maxReasonLen-3]) + "..."
}
