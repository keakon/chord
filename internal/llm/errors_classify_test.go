package llm

import "testing"

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
}
