package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// MalformedArgsSentinel is the JSON object stored in tool call args when the
// LLM produces invalid JSON (unparseable streaming output). It is used as a
// sentinel value to detect and handle malformed tool calls gracefully.
const MalformedArgsSentinel = `{"error":"malformed tool call arguments from model"}`

// IsMalformedArgs reports whether raw JSON args contain the malformed sentinel.
// It checks both exact match (the sentinel itself) and the "error" field value.
func IsMalformedArgs(args json.RawMessage) bool {
	if len(args) == 0 {
		return false
	}
	if string(args) == MalformedArgsSentinel {
		return true
	}
	// Also catch any JSON object with an "error" key set to the sentinel message.
	var obj struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(args, &obj) == nil && obj.Error == "malformed tool call arguments from model" {
		return true
	}
	return false
}

// IsEmptyArgs reports whether raw JSON args are effectively empty — either
// zero-length, or just "{}". When a tool with required parameters receives
// empty args, it usually means the LLM's output was truncated before the
// argument JSON could be fully generated (the model produced valid but
// vacuous JSON rather than truly malformed output).
func IsEmptyArgs(args json.RawMessage) bool {
	trimmed := bytes.TrimSpace(args)
	return len(trimmed) == 0 || string(trimmed) == "{}" || string(trimmed) == "null"
}

// RequiredFields extracts the "required" array from a JSON-schema-like tool
// parameter map. It handles both []string (Go-defined tools) and []any
// (MCP/JSON-decoded schemas) to ensure consistent behavior across all tool
// types.
func RequiredFields(schema map[string]any) []string {
	v, ok := schema["required"]
	if !ok {
		return nil
	}
	// Go-defined tools: Parameters() returns []string directly.
	if ss, ok := v.([]string); ok {
		return ss
	}
	// MCP/JSON-decoded tools: json.Unmarshal produces []any.
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, elem := range arr {
		if s, ok := elem.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// APIError represents an error returned by the LLM provider's API.
type APIError struct {
	StatusCode int
	Message    string
	Code       string        // provider error code (e.g. "model_not_allowed")
	Type       string        // provider error type (e.g. "invalid_request_error")
	RetryAfter time.Duration // suggested retry wait time (parsed from Retry-After header)
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
}

// NoUsableKeysError indicates a provider has configured keys/credentials, but
// none of them are selectable because they were permanently disabled.
type NoUsableKeysError struct {
	Provider string
}

func (e *NoUsableKeysError) Error() string {
	if e == nil || e.Provider == "" {
		return "no usable API keys configured"
	}
	return fmt.Sprintf("no usable API keys configured for provider %q", e.Provider)
}

// AllKeysCoolingError indicates all API keys for a provider are in cooldown.
type AllKeysCoolingError struct {
	RetryAfter time.Duration
}

func (e *AllKeysCoolingError) Error() string {
	return fmt.Sprintf("all API keys cooling down, retry after %s", e.RetryAfter)
}

// EmptyTruncationError indicates the model returned a truncated response with
// no content, no tool calls, and no thinking blocks (stop_reason == "length").
// This typically means the model is unable to produce output given the current
// context and should be treated as a model-level failure eligible for fallback.
type EmptyTruncationError struct{}

func (e *EmptyTruncationError) Error() string {
	return "model returned empty truncated response (stop_reason=length with no content or tool calls)"
}

// EmptyResponseError indicates the model completed successfully (HTTP 200 and a
// terminal stop reason) but produced no content, no tool calls, and no
// thinking blocks. This is treated as a key/model-level failure so routing can
// rotate keys and then advance through the remaining model-pool entries.
type EmptyResponseError struct{}

func (e *EmptyResponseError) Error() string {
	return "model returned empty response (stop_reason=stop with no content or tool calls)"
}

// ContextLengthExceededError indicates the LLM provider rejected the request
// because the input context exceeds the model's maximum context window. This
// is distinct from other 400 errors: it signals that compaction or truncation
// is needed rather than a request formatting issue.
type ContextLengthExceededError struct {
	ProviderMessage string
}

func (e *ContextLengthExceededError) Error() string {
	return fmt.Sprintf("context length exceeded: %s", e.ProviderMessage)
}

// IsContextLengthExceeded reports whether err indicates the input context
// exceeds the model's maximum context window. This is a strict classification
// that matches provider-specific error codes/messages; ordinary 400 errors
// must NOT be classified as oversize to avoid infinite retry loops.
func IsContextLengthExceeded(err error) bool {
	var ctxErr *ContextLengthExceededError
	if errors.As(err, &ctxErr) {
		return true
	}
	// Also check APIError for provider-specific signals
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return false
	}
	return classifyContextLengthExceeded(apiErr)
}

// classifyContextLengthExceeded checks APIError status code and message for
// oversize signals from various providers.
func classifyContextLengthExceeded(apiErr *APIError) bool {
	msg := strings.ToLower(apiErr.Message)

	switch apiErr.StatusCode {
	case 400:
		// OpenAI: "context_length_exceeded" or "maximum context length"
		if strings.Contains(msg, "context_length_exceeded") {
			return true
		}
		if strings.Contains(msg, "maximum context length") {
			return true
		}
		// Anthropic: "prompt is too long"
		if strings.Contains(msg, "prompt is too long") {
			return true
		}
		// Generic: "too many tokens"
		if strings.Contains(msg, "too many tokens") && strings.Contains(msg, "context") {
			return true
		}
	case 413:
		// HTTP 413 Payload Too Large — some proxies/gateways use this for context
		return true
	}

	// Also check provider error code field
	code := strings.ToLower(apiErr.Code)
	return code == "context_length_exceeded"
}

// shouldFallback determines whether the error warrants falling back to an
// alternative model once the current model's keys are exhausted.
// See docs/ARCHITECTURE.md §14.3. Ordering vs key rotation is implemented in
// client.completeStreamWithRetry (e.g. isPerKeyTimeoutRetry for read timeouts).
func shouldFallback(err error) bool {
	// NoUsableKeysError: configured credentials exist but every key is permanently disabled.
	var noUsable *NoUsableKeysError
	if errors.As(err, &noUsable) {
		return true
	}

	// AllKeysCoolingError: all keys for the current provider are cooling → fallback.
	var cooling *AllKeysCoolingError
	if errors.As(err, &cooling) {
		return true
	}

	// EmptyTruncationError: model produced nothing despite a length stop → fallback.
	var emptyTrunc *EmptyTruncationError
	if errors.As(err, &emptyTrunc) {
		return true
	}
	// EmptyResponseError: completed stream but semantically empty output → fallback.
	var emptyResp *EmptyResponseError
	if errors.As(err, &emptyResp) {
		return true
	}

	// Network timeouts: always eligible to try another model after keys are exhausted
	// (per-key vs per-model ordering is decided in client.go using
	// isConnectionEstablishmentTimeout / isPerKeyTimeoutRetry).
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if isProviderUnreachable(err) {
		return true
	}

	// Classify by HTTP status code.
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}

	switch apiErr.StatusCode {
	case 429:
		return true // Rate limited → fallback
	case 529:
		return true // Overloaded → fallback
	case 413:
		return true // Context too long → fallback (different model may have different limits)
	case 401:
		return true // Invalid key → all keys may be invalid, try fallback model
	case 403:
		return true // Key permission denied → try fallback model
	case 400:
		// Some gateways return 400 for target-model transport incompatibility
		// (e.g. "Store must be set to false"). Those should move to the next
		// model immediately instead of getting stuck in round retries.
		return isModelIncompatible400(apiErr.Message)
	default:
		return apiErr.StatusCode >= 500 // Other 5xx → fallback
	}
}

// isProviderUnreachable reports whether err is a connection-level failure
// (dial timeout, connection refused, DNS resolution failure, etc.) indicating
// the entire provider host is unreachable. All models on that provider should
// be skipped.
func isProviderUnreachable(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial" || opErr.Op == "connect"
	}
	return false
}

func errorChainContainsAll(err error, parts ...string) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		msg := strings.ToLower(e.Error())
		matched := true
		for _, part := range parts {
			if !strings.Contains(msg, strings.ToLower(part)) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func isTimeoutLikeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errorChainContainsAll(err, "timeout")
}

// isConnectionEstablishmentTimeout reports a net.Error timeout while the client
// is still establishing connectivity (TCP dial/connect, or TLS handshake).
// All keys for this provider hit the same endpoint, so remaining keys should
// be skipped and the model pool should advance to another provider if possible.
func isConnectionEstablishmentTimeout(err error) bool {
	if !isTimeoutLikeError(err) {
		return false
	}
	if isProviderUnreachable(err) {
		return true
	}
	return errorChainContainsAll(err, "tls", "handshake")
}

// skipRemainingModelsOnProvider reports whether the error means this provider's
// endpoint or credential pool is unusable for every key (not a per-credential issue).
func skipRemainingModelsOnProvider(err error) bool {
	if isProviderUnreachable(err) {
		return true
	}
	if isConnectionEstablishmentTimeout(err) {
		return true
	}
	var noUsable *NoUsableKeysError
	return errors.As(err, &noUsable)
}

// isPerKeyTimeoutRetry reports whether err is a timeout after connectivity is
// established (e.g. waiting on response bytes) where another API key may help.
// Connection-establishment timeouts are excluded; those skip straight to the
// next model/provider.
func isPerKeyTimeoutRetry(err error) bool {
	if !isTimeoutLikeError(err) {
		return false
	}
	return !isConnectionEstablishmentTimeout(err)
}

// isRetriable determines whether the error can be retried on the same model
// by selecting another key (same-key exponential backoff is not used for
// timeouts). client.completeStreamWithRetry may treat some errors as key-
// retriable even when isRetriable is false (401/403, isPerKeyTimeoutRetry).
// See docs/ARCHITECTURE.md §14.3.
func isRetriable(err error) bool {
	// User/system cancellation (e.g. Ctrl+C → turn cancel): never rotate keys or rounds.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Timeouts: not retriable in the sense of exponential backoff on the same key;
	// client.go may still rotate keys for response-phase timeouts.
	if isTimeoutLikeError(err) {
		return false
	}
	// Connection-level errors (dial, DNS, etc.): not retriable, skip the provider.
	if isProviderUnreachable(err) {
		return false
	}
	// Other network errors (connection reset, EOF, etc.) are retriable with a different key.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return true // Unknown errors default to retriable.
	}

	// 401/403 are always per-key; try next key regardless of message (e.g. "plan not supported").
	switch apiErr.StatusCode {
	case 401:
		return true // Invalid key → cooldown this key and retry with next key
	case 403:
		return true // Key permission denied or plan not supported → cooldown and try next key
	}

	// Request/parameter errors: never retry (wrong body, missing param, etc.).
	// Some proxies may return 5xx for bad request; treat by message to avoid long retry loops.
	if isRequestOrParamError(apiErr.Message) {
		return false
	}

	switch apiErr.StatusCode {
	case 429, 529:
		return true // Rate limited / Overloaded → retry (respect Retry-After)
	default:
		// Any other 5xx (500, 502, 522, 523, Cloudflare edges, etc.): prefer rotating
		// to another key on the same model before advancing to the next model in the
		// fallback pool. Quota headers showing ~100% must not block keys (see
		// ratelimit.SnapshotBlocksKeyAt); only real API failures drive rotation.
		if apiErr.StatusCode >= 500 && apiErr.StatusCode < 600 {
			return true
		}
		return false // 4xx etc. are not retriable
	}
}

// isConcurrentRequestLimit429 reports whether err is a 429 caused by provider-
// side request concurrency limits rather than an explicit quota/reset window.
// These errors are transient capacity gates and should continue round retries
// beyond the ordinary default-attempt cap.
func isConcurrentRequestLimit429(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.StatusCode != 429 {
		return false
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "too many concurrent requests") ||
		strings.Contains(msg, "concurrent requests for this model") ||
		(strings.Contains(msg, "concurrent") && strings.Contains(msg, "requests") && strings.Contains(msg, "model"))
}

// isPermanentFailure reports whether err is unrecoverable and should not be retried
// on any key or fallback model. Only malformed requests (400 with bad params) qualify;
// 401/403 are key-level errors that should be retried with another key or fallback model.
func isPermanentFailure(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case 400:
		return isRequestOrParamError(apiErr.Message)
	}
	return false
}

// isRequestOrParamError returns true if the API message indicates a client/request
// error (e.g. missing parameter, invalid request). Such errors are not retriable.
func isRequestOrParamError(message string) bool {
	msg := strings.ToLower(message)
	return strings.Contains(msg, "missing required parameter") ||
		strings.Contains(msg, "invalid_request_error") ||
		strings.Contains(msg, "invalid parameter") ||
		strings.Contains(msg, "invalid request")
}

// isModelIncompatible400 reports 400-class errors that indicate the current
// model/provider transport cannot serve this request shape, so routing should
// try the next model in the pool rather than repeating rounds on the same one.
func isModelIncompatible400(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "store must be set to false") ||
		strings.Contains(msg, "stream must be set to true") ||
		strings.Contains(msg, "not supported for this model") ||
		strings.Contains(msg, "model does not support") ||
		strings.Contains(msg, "unsupported parameter") ||
		strings.Contains(msg, "unsupported value")
}

// isTerminalModelPoolFailure reports errors that should stop after the current
// model pool (current cursor-head entry + all remaining configured entries) is exhausted, rather than
// continuing full retry rounds forever.
func isTerminalModelPoolFailure(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return false
	}
	return apiErr.StatusCode == 400 && isModelIncompatible400(apiErr.Message)
}
