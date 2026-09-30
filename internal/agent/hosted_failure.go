package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/llm"
)

type hostedTargetFailure struct {
	target string
	cause  error
}

type hostedAllTargetsFailedError struct {
	tool     string
	failures []hostedTargetFailure
}

func (e *hostedAllTargetsFailedError) Error() string {
	details := make([]string, 0, len(e.failures))
	actions := make([]string, 0, len(e.failures))
	seen := make(map[llm.HostedFailureKind]bool)
	for _, failure := range e.failures {
		kind := llm.ClassifyHostedFailure(failure.cause)
		detail, action := hostedFailureAdvice(kind)
		details = append(details, fmt.Sprintf("%s [%s]: %s", failure.target, kind, detail))
		if !seen[kind] {
			actions = append(actions, action)
			seen[kind] = true
		}
	}
	return fmt.Sprintf("%s could not run: all %d capable target(s) failed: %s. %s", e.tool, len(e.failures), strings.Join(details, "; "), strings.Join(actions, " "))
}

func (e *hostedAllTargetsFailedError) Unwrap() []error {
	causes := make([]error, 0, len(e.failures))
	for _, failure := range e.failures {
		causes = append(causes, failure.cause)
	}
	return causes
}

func hostedFailureAdvice(kind llm.HostedFailureKind) (string, string) {
	switch kind {
	case llm.HostedFailureRateLimited:
		return "request capacity or rate limit reached", "Retry later with fewer parallel requests; configure orchestration.provider_max_active_requests for the provider's quota."
	case llm.HostedFailureQuota:
		return "account quota or balance exhausted", "Use a target with available quota or restore the account's quota before retrying."
	case llm.HostedFailureUnavailable:
		return "upstream service unavailable", "Retry later or use another available target."
	case llm.HostedFailureTransport:
		return "request transport failed", "Check connectivity and retry later."
	case llm.HostedFailureDeclaration:
		return "hosted declaration rejected", "Check the endpoint's declaration support and the compat.hosted_tools entry."
	case llm.HostedFailureNotObserved:
		return "no hosted call was observed in this response", "Try another capable target; verify that the endpoint accepts and executes the declaration enabled by compat.hosted_tools."
	case llm.HostedFailureAuth:
		return "authentication or permission rejected", "Check the target's credentials and account permissions."
	default:
		return "hosted execution failed", "Inspect the request diagnostics before retrying or changing the request."
	}
}

func newHostedAllTargetsFailedError(tool string, failures []hostedTargetFailure) error {
	for _, failure := range failures {
		log.Warnf("hosted target failed tool=%v target=%v kind=%v error=%v", tool, failure.target, llm.ClassifyHostedFailure(failure.cause), failure.cause)
	}
	return &hostedAllTargetsFailedError{tool: tool, failures: failures}
}
