package llm

import (
	"errors"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestNativeRequestOutcomeEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dispatch RequestDispatch
		err      error
		want     message.NativeRequestOutcome
	}{
		{"success", RequestDispatch{prepared: true, sent: true}, nil, message.NativeRequestCompleted},
		{"local validation", RequestDispatch{prepared: true}, errors.New("invalid schema"), message.NativeRequestNotSent},
		{"untracked provider", RequestDispatch{}, errors.New("failed"), message.NativeRequestUnknown},
		{"network", RequestDispatch{prepared: true, sent: true}, errors.New("connection closed"), message.NativeRequestUnknown},
		{"parameter refusal", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 400, Origin: APIErrorOriginHTTPResponse, Type: "invalid_request_error"}, message.NativeRequestRejected},
		{"rate limit", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 429, Origin: APIErrorOriginHTTPResponse, Type: "rate_limit_error"}, message.NativeRequestRejected},
		{"authentication", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 401, Origin: APIErrorOriginHTTPResponse, Code: "invalid_api_key"}, message.NativeRequestRejected},
		{"permission", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 403, Origin: APIErrorOriginHTTPResponse, Type: "permission_error"}, message.NativeRequestRejected},
		{"stream refusal", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 400, Origin: APIErrorOriginSSEEvent, Type: "invalid_request_error"}, message.NativeRequestUnknown},
		{"unstructured refusal", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 400, Origin: APIErrorOriginHTTPResponse, Message: "upstream failed"}, message.NativeRequestUnknown},
		{"server error", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 502, Origin: APIErrorOriginHTTPResponse, Type: "invalid_request_error"}, message.NativeRequestUnknown},
		{"unknown origin", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 400, Type: "invalid_request_error"}, message.NativeRequestUnknown},
		{"upstream wrapper", RequestDispatch{prepared: true, sent: true}, &APIError{StatusCode: 400, Origin: APIErrorOriginHTTPResponse, Type: "invalid_request_error", Code: "upstream_error"}, message.NativeRequestUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeRequestOutcome(&tc.dispatch, tc.err); got != tc.want {
				t.Fatalf("outcome=%s, want %s", got, tc.want)
			}
		})
	}
}
