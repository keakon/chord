package agent

import (
	"strconv"
	"time"

	"github.com/keakon/chord/internal/message"
)

const (
	hostedAttemptInitial      = "initial"
	hostedAttemptRetry        = "retry"
	hostedAttemptFallback     = "fallback"
	hostedAttemptContinuation = "continuation"
)

type hostedWireAttempt struct {
	reason   string
	response *message.Response
	err      error
	elapsed  time.Duration
}

func hostedAttemptDiagnostics(attempt hostedWireAttempt) map[string]string {
	callCount := 0
	usageKnown := false
	if resp := attempt.response; resp != nil {
		usageKnown = resp.Usage != nil
		if resp.Hosted != nil {
			for _, call := range resp.Hosted.Calls {
				// A continuation can contain only the result for an earlier call.
				if call.Name != "" || len(call.Input) > 0 {
					callCount++
				}
			}
		}
	}
	return map[string]string{
		"request_reason":      attempt.reason,
		"model_requests":      "1",
		"observed_tool_calls": strconv.Itoa(callCount),
		"retry":               strconv.FormatBool(attempt.reason == hostedAttemptRetry),
		"continuation":        strconv.FormatBool(attempt.reason == hostedAttemptContinuation),
		"request_failed":      strconv.FormatBool(attempt.err != nil),
		"request_duration_ms": strconv.FormatInt(attempt.elapsed.Milliseconds(), 10),
		"usage_known":         strconv.FormatBool(usageKnown),
		"tool_fee_known":      "false",
	}
}
