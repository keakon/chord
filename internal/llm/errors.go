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

func apiErrorStructuredSignals(apiErr *APIError) []string {
	if apiErr == nil {
		return nil
	}
	signals := make([]string, 0, 6)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			signals = append(signals, value)
		}
	}
	add(apiErr.Code)
	add(apiErr.Type)
	msg := strings.TrimSpace(apiErr.Message)
	if msg == "" || !strings.HasPrefix(msg, "{") {
		return signals
	}
	var body struct {
		Code  string `json:"code"`
		Type  string `json:"type"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
		Detail struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"detail"`
	}
	if err := json.Unmarshal([]byte(msg), &body); err != nil {
		return signals
	}
	add(body.Code)
	add(body.Type)
	add(body.Error.Code)
	add(body.Error.Type)
	add(body.Detail.Code)
	add(body.Detail.Type)
	return signals
}

func apiErrorSignalContains(apiErr *APIError, needles ...string) bool {
	for _, signal := range apiErrorStructuredSignals(apiErr) {
		for _, needle := range needles {
			if strings.Contains(signal, needle) {
				return true
			}
		}
	}
	return false
}

// apiErrMessageContainsAny reports whether the normalized (lowercased) Message
// contains any of the given substrings.
func apiErrMessageContainsAny(apiErr *APIError, substrs ...string) bool {
	if apiErr == nil {
		return false
	}
	msg := strings.ToLower(apiErr.Message)
	for _, s := range substrs {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// apiErrMessageContains reports whether the normalized (lowercased) Message
// contains all of the given substrings (AND semantics).
func apiErrMessageContains(apiErr *APIError, substrs ...string) bool {
	if apiErr == nil {
		return false
	}
	msg := strings.ToLower(apiErr.Message)
	for _, s := range substrs {
		if !strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

func apiErrorSignalEquals(apiErr *APIError, values ...string) bool {
	for _, signal := range apiErrorStructuredSignals(apiErr) {
		for _, value := range values {
			if signal == value {
				return true
			}
		}
	}
	return false
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

// AllAttemptedCandidatesContextLengthExceededError indicates every provider/model target
// that was actually attempted in the current model-pool pass rejected the
// request as oversized. This is the signal callers may use for context
// compaction recovery; a lone context-length last error after mixed failures is
// not sufficient.
type AllAttemptedCandidatesContextLengthExceededError struct {
	Inner error
}

func (e *AllAttemptedCandidatesContextLengthExceededError) Error() string {
	if e == nil || e.Inner == nil {
		return "all attempted candidate models exceeded the current context"
	}
	return fmt.Sprintf("all attempted candidate models exceeded the current context: %v", e.Inner)
}

func (e *AllAttemptedCandidatesContextLengthExceededError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Inner
}

// IsAllAttemptedCandidatesContextLengthExceeded reports whether err is the precise
// model-pool exhaustion signal used for oversize-driven compaction recovery.
func IsAllAttemptedCandidatesContextLengthExceeded(err error) bool {
	_, ok := errors.AsType[*AllAttemptedCandidatesContextLengthExceededError](err)
	return ok
}

// IsContextLengthExceeded reports whether err indicates the input context
// exceeds the model's maximum context window. This is a strict classification
// that matches provider-specific error codes/messages; ordinary 400 errors
// must NOT be classified as oversize to avoid infinite retry loops.
func IsContextLengthExceeded(err error) bool {
	if IsAllAttemptedCandidatesContextLengthExceeded(err) {
		return true
	}
	if _, ok := errors.AsType[*ContextLengthExceededError](err); ok {
		return true
	}
	// Also check APIError for provider-specific signals
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil {
		return false
	}
	return classifyContextLengthExceeded(apiErr)
}

// classifyContextLengthExceeded checks APIError status code and message for
// oversize signals from various providers.
func classifyContextLengthExceeded(apiErr *APIError) bool {
	if apiErrorSignalEquals(apiErr, "context_length_exceeded", "context_window_exceeded", "input_too_long") {
		return true
	}

	switch apiErr.StatusCode {
	case 400:
		return apiErrMessageContainsAny(apiErr,
			"context_length_exceeded",
			"maximum context length",
			"exceeds the context window",
			"input is too long",
			"prompt is too long",
		) || apiErrMessageContains(apiErr, "too many tokens", "context")
	case 413:
		// HTTP 413 Payload Too Large — some proxies/gateways use this for context
		return true
	}

	return false
}

// shouldFallback determines whether the error warrants falling back to an
// alternative model once the current model's keys are exhausted.
// Ordering vs key rotation is implemented in client.completeStreamWithRetry
// (for example: invisible timeouts skip the current provider, while visible
// stream timeouts retry on the same key).
func shouldFallback(err error) bool {
	if IsContextLengthExceeded(err) {
		return true
	}

	// NoUsableKeysError: configured credentials exist but every key is permanently disabled.
	if _, ok := errors.AsType[*NoUsableKeysError](err); ok {
		return true
	}

	// AllKeysCoolingError: all keys for the current provider are cooling → fallback.
	if _, ok := errors.AsType[*AllKeysCoolingError](err); ok {
		return true
	}

	// EmptyTruncationError: model produced nothing despite a length stop → fallback.
	if _, ok := errors.AsType[*EmptyTruncationError](err); ok {
		return true
	}
	// EmptyResponseError: completed stream but semantically empty output → fallback.
	if _, ok := errors.AsType[*EmptyResponseError](err); ok {
		return true
	}

	// Network timeouts: always eligible to try another model. Exact routing
	// order (skip-provider vs same-key visible retry) is decided in client.go.
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return true
	}
	if isProviderUnreachable(err) {
		return true
	}

	// Classify by HTTP status code.
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		return false
	}

	switch apiErr.StatusCode {
	case 402:
		return true // Payment/quota exhausted → fallback
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
		// Provider-aware official-vs-compatible handling happens in the retry
		// loop. At the pure status-code layer, keep 400 fallback-eligible so
		// explicit request-shape/provider-protocol errors can still advance to
		// another configured model when appropriate.
		return true
	default:
		return apiErr.StatusCode >= 500 // Other 5xx → fallback
	}
}

// isProviderUnreachable reports whether err is a connection-level failure
// (dial timeout, connection refused, DNS resolution failure, etc.) indicating
// the entire provider host is unreachable. All models on that provider should
// be skipped.
func isProviderUnreachable(err error) bool {
	if opErr, ok := errors.AsType[*net.OpError](err); ok {
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
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
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
	_, ok := errors.AsType[*NoUsableKeysError](err)
	return ok
}

const (
	providerSkipReasonNoUsableKeys               = "no_usable_keys"
	providerSkipReasonConnectionEstablishment    = "connection_establishment_timeout"
	providerSkipReasonProviderUnreachable        = "provider_unreachable"
	providerSkipReasonTimeoutBeforeVisibleOutput = "timeout_before_visible_output"
	providerSkipReasonGeneric                    = "provider_skipped"
)

func providerSkipReason(err error) string {
	if _, ok := errors.AsType[*NoUsableKeysError](err); ok {
		return providerSkipReasonNoUsableKeys
	}
	if isConnectionEstablishmentTimeout(err) {
		return providerSkipReasonConnectionEstablishment
	}
	if isProviderUnreachable(err) {
		return providerSkipReasonProviderUnreachable
	}
	if isTimeoutLikeError(err) {
		return providerSkipReasonTimeoutBeforeVisibleOutput
	}
	return providerSkipReasonGeneric
}

// isRetriable determines whether the error can be retried on the same model
// by selecting another key. Timeouts are excluded from this path: invisible
// timeouts advance to the next provider/model, while visible stream timeouts
// are handled by the caller's same-key retry path. client.completeStreamWithRetry
// may still treat some errors as key-retriable even when isRetriable is false
// (currently 401/403).

func isRetriable(err error) bool {
	// Context length exceeded is a model/context-size signal, not a key-health or
	// transient stream failure. The current round may still try other models.
	if IsContextLengthExceeded(err) {
		return false
	}
	// User/system cancellation (e.g. Ctrl+C → turn cancel): never rotate keys or rounds.
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Timeouts do not rotate keys through this generic retriable path. Higher-level
	// retry routing skips the provider for pre-visible timeouts and retries the
	// same key after visible stream interruptions.
	if isTimeoutLikeError(err) {
		return false
	}
	// Connection-level errors (dial, DNS, etc.): not retriable, skip the provider.
	if isProviderUnreachable(err) {
		return false
	}
	// Other network errors (connection reset, EOF, etc.) are retriable with a different key.
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return true
	}

	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
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
	// HTTP 400 provider-specific official-vs-compatible handling is enforced by
	// the caller, because APIError alone does not know which transport semantics
	// the provider promises.
	if isRequestOrParamError(apiErr) {
		return false
	}

	switch apiErr.StatusCode {
	case 402, 429, 529:
		return true // Payment/quota exhausted / Rate limited / Overloaded → retry (respect Retry-After)
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
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil || apiErr.StatusCode != 429 {
		return false
	}
	return apiErrMessageContainsAny(apiErr, "too many concurrent requests", "concurrent requests for this model") ||
		apiErrMessageContains(apiErr, "concurrent", "requests", "model")
}

// isRequestOrParamError reports request/parameter failures that are not useful
// to retry with another key. For non-official/compatible APIs, HTTP 400 is not
// trusted as a request-shape signal by itself because many gateways collapse
// upstream overload/rate-limit/provider failures into 400. Therefore a 400 only
// classifies here when there is an explicit request/parameter signal in the
// structured code/type fields or in a small set of strong free-text markers.
// Official APIs keep their stricter "400 is terminal" behavior in the caller's
// provider-aware checks. For non-400 statuses, only explicit structured
// request/parameter code/type signals classify; request-shaped free text alone
// stays retriable.
func isRequestOrParamError(apiErr *APIError) bool {
	if apiErr == nil {
		return false
	}
	if apiErr.StatusCode == 400 {
		return hasExplicitRequestOrParamSignal(apiErr)
	}
	return apiErrorSignalContains(apiErr,
		"invalid_request",
		"invalid_parameter",
		"invalid_argument",
		"missing_required_parameter",
	)
}

func hasExplicitRequestOrParamSignal(apiErr *APIError) bool {
	if apiErr == nil {
		return false
	}
	if confirmedCodexUsageLimitError(apiErr) ||
		classifyContextLengthExceeded(apiErr) ||
		isCodexWSChainStateMismatch(apiErr) {
		return false
	}
	if apiErrorSignalContains(apiErr,
		"invalid_request",
		"invalid_parameter",
		"invalid_argument",
		"missing_required_parameter",
	) {
		return true
	}
	// Keep the text markers intentionally narrow. Compatible gateways often wrap
	// transient upstream/provider failures as HTTP 400, so broad text matching
	// would recreate the original false-terminal problem.
	return apiErrMessageContainsAny(apiErr,
		"missing required parameter",
		"unsupported parameter",
		"unknown field",
		"invalid parameter",
		"invalid argument",
	)
}

func hasTerminalNonRetriable400Signal(apiErr *APIError) bool {
	if hasExplicitRequestOrParamSignal(apiErr) {
		return true
	}
	return apiErrMessageContainsAny(apiErr,
		"invalid assistant message",
		"invalid tool schema",
		"reasoning_content",
		"content[].thinking",
		"must be set to true",
		"must be set to false",
	)
}

// isTerminalModelPoolFailureForProvider reports errors that should stop after
// the current model pool (current cursor-head entry + all remaining configured
// entries) is exhausted, rather than continuing full retry rounds forever.
//
// HTTP 400 is treated as terminal for official APIs (where the request shape
// is the source of truth) and for explicit request/parameter/model-
// incompatible gateway responses (where retrying will keep failing the same
// way). Other non-official 400s remain retryable across pool passes because
// compatible gateways often mis-map upstream overload/rate-limit/provider
// failures to 400.
func isTerminalModelPoolFailureForProvider(provider *ProviderConfig, err error) bool {
	if IsContextLengthExceeded(err) {
		return true
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr == nil {
		return false
	}
	if apiErr.StatusCode != 400 {
		return false
	}
	return providerUsesOfficialAPI(provider) || hasTerminalNonRetriable400Signal(apiErr)
}
