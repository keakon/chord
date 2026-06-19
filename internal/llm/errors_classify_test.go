package llm

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
)

func TestIsRetriable5xxRotatesKeysBeforeFallback(t *testing.T) {
	t.Parallel()
	// Regression: 522 was missing from the explicit list → isRetriable false →
	// completeStreamWithRetry jumped to the next model without trying other keys.
	for _, code := range []int{500, 501, 502, 503, 504, 522, 523, 529} {
		if !isRetriable(&APIError{StatusCode: code, Message: "upstream"}) {
			t.Fatalf("status %d: expected retriable (try next key)", code)
		}
		if !shouldFallback(&APIError{StatusCode: code, Message: "upstream"}) {
			t.Fatalf("status %d: expected fallback-eligible after keys exhausted", code)
		}
	}
}

func TestIsRetriable4xxNotRetriableExceptAuth(t *testing.T) {
	t.Parallel()
	if isRetriable(&APIError{StatusCode: 400, Message: "bad"}) {
		t.Fatal("400 should not be retriable at the status-only classification layer")
	}
	if isRetriable(&APIError{StatusCode: 413, Message: "too large"}) {
		t.Fatal("413 should not be retriable")
	}
	if !isRetriable(&APIError{StatusCode: 401, Message: "auth"}) {
		t.Fatal("401 should be retriable (next key)")
	}
	if !isRetriable(&APIError{StatusCode: 402, Message: "quota exhausted"}) {
		t.Fatal("402 quota exhaustion should be retriable (next key)")
	}
	if !shouldFallback(&APIError{StatusCode: 402, Message: "quota exhausted"}) {
		t.Fatal("402 quota exhaustion should be fallback-eligible")
	}
}

func TestIsConcurrentRequestLimit429(t *testing.T) {
	t.Parallel()
	if !isConcurrentRequestLimit429(&APIError{StatusCode: 429, Message: `{"error":"Too many concurrent requests for this model"}`}) {
		t.Fatal("concurrent-request 429 should be recognized")
	}
	if isConcurrentRequestLimit429(&APIError{StatusCode: 429, Message: "daily quota exceeded"}) {
		t.Fatal("quota-style 429 should not be treated as concurrent-request throttling")
	}
	if isConcurrentRequestLimit429(&APIError{StatusCode: 500, Message: "Too many concurrent requests for this model"}) {
		t.Fatal("non-429 errors should not be treated as concurrent-request throttling")
	}
}

func TestProviderSkipReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "no usable keys wins",
			err:  fmt.Errorf("wrapped: %w", &NoUsableKeysError{Provider: "demo"}),
			want: providerSkipReasonNoUsableKeys,
		},
		{
			name: "dial timeout is connection establishment",
			err:  &net.OpError{Op: "dial", Err: &net.DNSError{IsTimeout: true}},
			want: providerSkipReasonConnectionEstablishment,
		},
		{
			name: "tls handshake timeout is connection establishment",
			err:  fmt.Errorf("wrapped: %w", errors.New("TLS handshake timeout")),
			want: providerSkipReasonConnectionEstablishment,
		},
		{
			name: "connect failure is provider unreachable",
			err:  &net.OpError{Op: "connect", Err: errors.New("connection refused")},
			want: providerSkipReasonProviderUnreachable,
		},
		{
			name: "chunk timeout is timeout before visible output",
			err:  &ChunkTimeoutError{d: time.Second},
			want: providerSkipReasonTimeoutBeforeVisibleOutput,
		},
		{
			name: "generic non-timeout error",
			err:  errors.New("boom"),
			want: providerSkipReasonGeneric,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := providerSkipReason(tt.err); got != tt.want {
				t.Fatalf("providerSkipReason(%T: %v) = %q, want %q", tt.err, tt.err, got, tt.want)
			}
		})
	}
}

func TestShouldContinueRetryKeepsRetryingConcurrent429AfterDefaultCap(t *testing.T) {
	t.Parallel()
	concurrentErr := &APIError{StatusCode: 429, Message: `{"error":"Too many concurrent requests for this model"}`}
	if !shouldContinueRetry(DefaultStreamRetryRounds, DefaultStreamRetryRounds, concurrentErr) {
		t.Fatal("concurrent-request 429 should continue retrying past the default cap")
	}
	quotaErr := &APIError{StatusCode: 429, Message: "daily quota exceeded"}
	if shouldContinueRetry(DefaultStreamRetryRounds, DefaultStreamRetryRounds, quotaErr) {
		t.Fatal("ordinary 429 should still stop at the default retry cap")
	}
}

func TestIsRetriableRequestShapeMessageNotRetriableEvenOn5xx(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 500, Type: "invalid_request_error", Message: "bad field"}
	if isRetriable(err) {
		t.Fatal("request-shaped 5xx type should not be retriable")
	}
}

func TestIsRetriableRequestShapeMessageAloneDoesNotClassify(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 500, Message: "invalid_request_error: bad field"}
	if !isRetriable(err) {
		t.Fatal("request-shaped text without structured code/type should remain retriable")
	}
}

func TestShouldFallback400ModelIncompatible(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: `{"detail":"Store must be set to false"}`}
	if !shouldFallback(err) {
		t.Fatal("model-incompatible 400 should be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("model-incompatible 400 should not rotate keys on same model globally")
	}
}

func TestShouldFallback400CodexRequiresStreaming(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}
	if !shouldFallback(err) {
		t.Fatal("codex stream-required 400 should be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("codex stream-required 400 should not rotate keys on same model globally")
	}
	if !isTerminalModelPoolFailureForProvider(nil, err) {
		t.Fatal("codex stream-required 400 should stop after model pool exhaustion by default")
	}
}

func TestShouldFallback400RequestShapeError(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Code: "invalid_request_error", Message: "missing required parameter: input"}
	if !shouldFallback(err) {
		t.Fatal("request-shape 400 should be fallback-eligible for another model")
	}
	if isRetriable(err) {
		t.Fatal("request-shape 400 should not rotate keys on same model globally")
	}
	if !isTerminalModelPoolFailureForProvider(nil, err) {
		t.Fatal("request-shape 400 should stop after model pool exhaustion by default")
	}
}

func TestReasoningReplay400FallsBackToAnotherModel(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "The `reasoning_content` in the thinking mode must be passed back to the API."}
	if !shouldFallback(err) {
		t.Fatal("reasoning replay 400 should be fallback-eligible for another model")
	}
	if isRetriable(err) {
		t.Fatal("reasoning replay 400 should not rotate keys on same model globally")
	}
	if !isTerminalModelPoolFailureForProvider(nil, err) {
		t.Fatal("reasoning replay 400 should stop after model pool exhaustion by default")
	}
}

func TestAnthropicThinkingReplay400FallsBackToAnotherModel(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "The `content[].thinking` in the thinking mode must be passed back to the API."}
	if !shouldFallback(err) {
		t.Fatal("anthropic thinking replay 400 should be fallback-eligible for another model")
	}
	if isRetriable(err) {
		t.Fatal("anthropic thinking replay 400 should not rotate keys on same model globally")
	}
	if !isTerminalModelPoolFailureForProvider(nil, err) {
		t.Fatal("anthropic thinking replay 400 should stop after model pool exhaustion by default")
	}
}

func TestAssistantMessageShape400FallsBackToAnotherModel(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "Invalid assistant message: content or tool_calls must be set"}
	if !shouldFallback(err) {
		t.Fatal("assistant-message-shape 400 should be fallback-eligible for another model")
	}
	if isRetriable(err) {
		t.Fatal("assistant-message-shape 400 should not rotate keys on same model")
	}
	if !isTerminalModelPoolFailureForProvider(nil, err) {
		t.Fatal("assistant-message-shape 400 should stop after model pool exhaustion")
	}
}

func TestTerminalModelPoolFailureForProviderKeepsCompatible400Retryable(t *testing.T) {
	t.Parallel()
	compatible := NewProviderConfig("gateway", config.ProviderConfig{OfficialAPI: new(false)}, nil)
	err := &APIError{StatusCode: 400, Message: "Concurrency limit exceeded for user, please retry later"}
	if isTerminalModelPoolFailureForProvider(compatible, err) {
		t.Fatal("compatible gateway 400 should not stop after model pool exhaustion")
	}
}

func TestTerminalModelPoolFailureForProviderOfficial400StillStops(t *testing.T) {
	t.Parallel()
	official := NewProviderConfig("official", config.ProviderConfig{OfficialAPI: new(true)}, nil)
	err := &APIError{StatusCode: 400, Message: "Our servers are currently overloaded. Please try again later."}
	if !isTerminalModelPoolFailureForProvider(official, err) {
		t.Fatal("official API 400 should remain terminal even if the message looks transient")
	}
}

func TestIsAccountDeactivatedMessageFallback(t *testing.T) {
	t.Parallel()
	// Structured code/type signals.
	for _, apiErr := range []*APIError{
		{StatusCode: 401, Code: "account_deactivated"},
		{StatusCode: 402, Message: `{"detail":{"code":"deactivated_workspace"}}`},
	} {
		if !isAccountDeactivated(apiErr) {
			t.Fatalf("expected deactivated for %#v", apiErr)
		}
	}
	// Free-text fallback: gateways that omit a structured code/type must still
	// be recognized so a permanently disabled account is not retried in a loop.
	for _, msg := range []string{
		"Your account has been deactivated.",
		"This account has been disabled.",
	} {
		if !isAccountDeactivated(&APIError{StatusCode: 403, Message: msg}) {
			t.Fatalf("expected deactivated for plaintext %q", msg)
		}
	}
	if isAccountDeactivated(&APIError{StatusCode: 500, Message: "internal error"}) {
		t.Fatal("unrelated error should not classify as deactivated")
	}
	// A bare "disabled" substring (e.g. a disabled feature/tool) must not
	// permanently remove an otherwise-healthy key.
	for _, msg := range []string{
		"This feature is disabled for your current plan.",
		"Tool calling is disabled for this model.",
	} {
		if isAccountDeactivated(&APIError{StatusCode: 403, Message: msg}) {
			t.Fatalf("unrelated %q should not classify as deactivated", msg)
		}
	}
}

func TestIsAccountInvalidatedMessageFallback(t *testing.T) {
	t.Parallel()
	for _, apiErr := range []*APIError{
		{StatusCode: 401, Code: "account_invalidated"},
		{StatusCode: 401, Code: "token_revoked"},
	} {
		if !isAccountInvalidated(apiErr) {
			t.Fatalf("expected invalidated for %#v", apiErr)
		}
	}
	for _, msg := range []string{
		"Your authentication token has been invalidated.",
		"This token has been revoked.",
		"could not parse your authentication token",
	} {
		if !isAccountInvalidated(&APIError{StatusCode: 401, Message: msg}) {
			t.Fatalf("expected invalidated for plaintext %q", msg)
		}
	}
}

func TestIsRequestOrParamError400RequiresExplicitSignal(t *testing.T) {
	t.Parallel()
	// Non-official bare 400s are not trusted as request-shape failures by
	// default because compatible gateways often wrap transient upstream errors.
	if isRequestOrParamError(&APIError{StatusCode: 400, Message: "bad"}) {
		t.Fatal("bare 400 should not classify as a request/parameter error")
	}
	// Transient overloaded 400s from compatible gateways must stay retryable.
	if isRequestOrParamError(&APIError{StatusCode: 400, Message: "Our servers are currently overloaded. Please try again later."}) {
		t.Fatal("overloaded 400 should stay non-param and retryable on compatible gateways")
	}
	// Explicit structured and strong text signals still classify.
	if !isRequestOrParamError(&APIError{StatusCode: 400, Code: "invalid_request_error", Message: "bad field"}) {
		t.Fatal("structured invalid_request 400 should classify as a param error")
	}
	if !isRequestOrParamError(&APIError{StatusCode: 400, Message: "missing required parameter: input"}) {
		t.Fatal("explicit missing-parameter text should classify as a param error")
	}
	// Non-400 request-shaped free text alone must not classify (stays retriable).
	if isRequestOrParamError(&APIError{StatusCode: 500, Message: "invalid_request_error: bad field"}) {
		t.Fatal("non-400 request-shaped free text should not classify")
	}
	// Non-400 with a structured request signal classifies.
	if !isRequestOrParamError(&APIError{StatusCode: 500, Code: "invalid_request_error"}) {
		t.Fatal("structured invalid_request signal should classify at any status")
	}
}
