package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

// HostedFailureKind describes the action appropriate after a hosted request.
type HostedFailureKind string

const (
	HostedFailureRateLimited HostedFailureKind = "rate_limited"
	HostedFailureQuota       HostedFailureKind = "quota_exhausted"
	HostedFailureUnavailable HostedFailureKind = "upstream_unavailable"
	HostedFailureTransport   HostedFailureKind = "transport_failed"
	HostedFailureDeclaration HostedFailureKind = "declaration_rejected"
	HostedFailureNotObserved HostedFailureKind = "call_not_observed"
	HostedFailureCooling     HostedFailureKind = "target_cooling"
	HostedFailureAuth        HostedFailureKind = "authentication_failed"
	HostedFailureExecution   HostedFailureKind = "execution_failed"
	HostedFailureRetryBudget HostedFailureKind = "retry_budget_exhausted"
)

// HostedCallNotObservedError means this response did not demonstrate execution;
// it does not establish that the endpoint lacks support for the declaration.
type HostedCallNotObservedError struct{ Tool string }

func (e *HostedCallNotObservedError) Error() string {
	return "no " + e.Tool + " call was observed: the model did not run it or the endpoint ignored the hosted declaration"
}

// HostedTargetCoolingError means no request was sent because this target is
// cooling after missing executions or already has a recovery probe in flight.
type HostedTargetCoolingError struct {
	Tool          string
	Until         time.Time
	ProbeInFlight bool
}

func (e *HostedTargetCoolingError) Error() string {
	if e.ProbeInFlight {
		return e.Tool + " target already has a recovery probe in flight; no request was sent"
	}
	return e.Tool + " target is cooling until " + e.Until.UTC().Format(time.RFC3339) + "; no request was sent"
}

// ClassifyHostedFailure retains the original cause when key cooldown masks the
// wire error. Only known transient classes are eligible for another round.
func ClassifyHostedFailure(err error) HostedFailureKind {
	if _, ok := errors.AsType[*HostedTargetCoolingError](err); ok {
		return HostedFailureCooling
	}
	if admission, ok := errors.AsType[*HostedAdmissionError](err); ok {
		if admission.RetryBudget {
			return HostedFailureRetryBudget
		}
		return HostedFailureRateLimited
	}
	if cooling, ok := errors.AsType[*AllKeysCoolingError](err); ok {
		if cooling.cause != nil && cooling.cause.Err != nil {
			return ClassifyHostedFailure(cooling.cause.Err)
		}
		return HostedFailureRateLimited
	}
	if _, ok := errors.AsType[*HostedCallNotObservedError](err); ok {
		return HostedFailureNotObserved
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		if apiErrorSignalEquals(apiErr, "invalid_grant", "invalid_client") || apiErrMessageContainsAny(apiErr, "token endpoint http 400: invalid_grant", "token endpoint http 400: invalid_client") {
			return HostedFailureAuth
		}
		if apiErr.StatusCode == 402 || isGlobalQuotaExhausted(apiErr) || apiErrorSignalContains(apiErr, "insufficient_quota", "quota_exceeded", "usage_limit_reached", "quota_exhausted", "insufficient_balance", "credit_balance_too_low") || apiErrMessageContainsAny(apiErr, "insufficient balance", "insufficient credits", "quota exhausted") {
			return HostedFailureQuota
		}
		switch {
		case apiErr.StatusCode == 429:
			return HostedFailureRateLimited
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			return HostedFailureAuth
		case apiErr.StatusCode >= 500 && apiErr.StatusCode < 600:
			return HostedFailureUnavailable
		case apiErr.StatusCode == 424 && (apiErrorSignalContains(apiErr, "service_unavailable") || apiErrMessageContainsAny(apiErr, "service is currently unavailable", "service unavailable")):
			return HostedFailureUnavailable
		case (apiErr.StatusCode == 400 || apiErr.StatusCode == 422) && !apiErr.isStreamEvent():
			detail := strings.ToLower(apiErr.Code + " " + apiErr.Message)
			for _, signal := range []string{"tool_choice", "tool choice", "forced tool", "force tool", "hosted declaration", "unsupported tool", "tool type"} {
				if strings.Contains(detail, signal) {
					return HostedFailureDeclaration
				}
			}
		}
		if apiErr.isStreamEvent() && hasUpstreamFailureSignal(apiErr) {
			return HostedFailureUnavailable
		}
		return HostedFailureExecution
	}
	if _, ok := errors.AsType[net.Error](err); ok || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return HostedFailureTransport
	}
	return HostedFailureExecution
}

// HostedFailureAllowsRetryRound identifies transient failures eligible for another pool round.
func HostedFailureAllowsRetryRound(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch ClassifyHostedFailure(err) {
	case HostedFailureRateLimited, HostedFailureUnavailable, HostedFailureTransport:
		return true
	default:
		return false
	}
}
