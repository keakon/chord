package llm

import (
	"errors"
	"net/http"

	"github.com/keakon/chord/internal/message"
)

func nativeRequestOutcome(dispatch *RequestDispatch, err error) message.NativeRequestOutcome {
	if err == nil {
		return message.NativeRequestCompleted
	}
	if dispatch.NotSent() {
		return message.NativeRequestNotSent
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Origin != APIErrorOriginHTTPResponse {
		return message.NativeRequestUnknown
	}
	if apiErrorSignalContains(apiErr, "upstream", "server_error", "internal_error", "timeout", "connection") ||
		apiErrMessageContainsAny(apiErr, "upstream", "timed out", "timeout", "connection reset", "connection closed") {
		return message.NativeRequestUnknown
	}
	// Require a structured refusal before any stream. Status alone, an
	// upstream-failure wrapper, and errors inside a stream prove no such thing.
	rejected := false
	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		rejected = apiErrorSignalEquals(apiErr, "invalid_request_error", invalidResponsesRequestCode, "invalid_parameter", "invalid_argument", "missing_required_parameter", "context_length_exceeded")
	case http.StatusUnauthorized:
		rejected = apiErrorSignalEquals(apiErr, "authentication_error", "invalid_api_key")
	case http.StatusForbidden:
		rejected = apiErrorSignalEquals(apiErr, "permission_error", "permission_denied")
	case http.StatusTooManyRequests:
		rejected = apiErrorSignalEquals(apiErr, "rate_limit_error", "rate_limit_exceeded", "insufficient_quota")
	}
	if rejected {
		return message.NativeRequestRejected
	}
	return message.NativeRequestUnknown
}
