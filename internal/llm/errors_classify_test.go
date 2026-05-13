package llm

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
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
		t.Fatal("400 should not be retriable")
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
		tt := tt
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
	err := &APIError{StatusCode: 500, Message: "invalid_request_error: bad field"}
	if isRetriable(err) {
		t.Fatal("request-shaped 5xx message should not be retriable")
	}
}

func TestShouldFallback400ModelIncompatible(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: `{"detail":"Store must be set to false"}`}
	if !shouldFallback(err) {
		t.Fatal("model-incompatible 400 should be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("model-incompatible 400 should not rotate keys on same model")
	}
}

func TestShouldFallback400CodexRequiresStreaming(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: `{"detail":"Stream must be set to true"}`}
	if !shouldFallback(err) {
		t.Fatal("codex stream-required 400 should be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("codex stream-required 400 should not rotate keys on same model")
	}
	if !isTerminalModelPoolFailure(err) {
		t.Fatal("codex stream-required 400 should stop after model pool exhaustion")
	}
}

func TestShouldNotFallback400RequestShapeError(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "invalid_request_error: missing required parameter: input"}
	if shouldFallback(err) {
		t.Fatal("request-shape 400 should not be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("request-shape 400 should not be retriable")
	}
	if !isPermanentFailure(err) {
		t.Fatal("request-shape 400 should be permanent")
	}
}

func TestReasoningReplay400IsPermanentRequestShapeError(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "The `reasoning_content` in the thinking mode must be passed back to the API."}
	if shouldFallback(err) {
		t.Fatal("reasoning replay 400 should not be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("reasoning replay 400 should not be retriable")
	}
	if !isPermanentFailure(err) {
		t.Fatal("reasoning replay 400 should be permanent")
	}
}

func TestAnthropicThinkingReplay400IsPermanentRequestShapeError(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "The `content[].thinking` in the thinking mode must be passed back to the API."}
	if shouldFallback(err) {
		t.Fatal("anthropic thinking replay 400 should not be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("anthropic thinking replay 400 should not be retriable")
	}
	if !isPermanentFailure(err) {
		t.Fatal("anthropic thinking replay 400 should be permanent")
	}
}

func TestAssistantMessageShape400IsPermanentRequestShapeError(t *testing.T) {
	t.Parallel()
	err := &APIError{StatusCode: 400, Message: "Invalid assistant message: content or tool_calls must be set"}
	if shouldFallback(err) {
		t.Fatal("assistant-message-shape 400 should not be fallback-eligible")
	}
	if isRetriable(err) {
		t.Fatal("assistant-message-shape 400 should not be retriable")
	}
	if !isPermanentFailure(err) {
		t.Fatal("assistant-message-shape 400 should be permanent")
	}
}
