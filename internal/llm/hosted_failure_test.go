package llm

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
)

func TestClassifyHostedFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		kind  HostedFailureKind
		retry bool
	}{
		{"rate", &APIError{StatusCode: 429}, HostedFailureRateLimited, true},
		{"quota", &APIError{StatusCode: 429, Code: "insufficient_quota"}, HostedFailureQuota, false},
		{"payment", &APIError{StatusCode: 402}, HostedFailureQuota, false},
		{"upstream", &APIError{StatusCode: 503}, HostedFailureUnavailable, true},
		{"dependency", &APIError{StatusCode: 424, Message: "service is currently unavailable"}, HostedFailureUnavailable, true},
		{"other_dependency", &APIError{StatusCode: 424, Message: "required resource missing"}, HostedFailureExecution, false},
		{"declaration", &APIError{StatusCode: 400, Message: "unsupported tool type"}, HostedFailureDeclaration, false},
		{"event", &APIError{StatusCode: 400, Origin: APIErrorOriginSSEEvent, Message: "unsupported tool type"}, HostedFailureExecution, false},
		{"auth", &APIError{StatusCode: 401}, HostedFailureAuth, false},
		{"no_call", &HostedCallNotObservedError{Tool: "sample_tool"}, HostedFailureNotObserved, false},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: io.EOF}, HostedFailureTransport, true},
		{"truncated", io.ErrUnexpectedEOF, HostedFailureTransport, true},
		{"cancel", context.Canceled, HostedFailureExecution, false},
		{"cooling_quota", &AllKeysCoolingError{cause: &keyCooldownCause{Err: &APIError{StatusCode: 429, Code: "usage_limit_reached"}}}, HostedFailureQuota, false},
		{"cooling", &AllKeysCoolingError{}, HostedFailureRateLimited, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("request failed: %w", tc.err)
			if got := ClassifyHostedFailure(err); got != tc.kind {
				t.Fatalf("kind=%s want=%s", got, tc.kind)
			}
			if got := hostedFailureAllowsRetryRound(err); got != tc.retry {
				t.Fatalf("retry=%t want=%t", got, tc.retry)
			}
		})
	}
}
