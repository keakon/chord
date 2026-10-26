// Package promptcache tracks local cache expectations and observed hit rates.
package promptcache

import (
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// expectationRecord fingerprints the last request actually sent to one
// running model ref. Provider prompt caches are independent per provider, so
// expectations are tracked per ref: the next request to the same ref should
// hit the cache for exactly the unchanged message prefix. Records are
// immutable once stored; Source keeps a shallow copy of the sent messages so
// the next request can reuse Shapes and Tokens for the field-equal prefix
// instead of re-hashing the whole conversation.
type expectationRecord struct {
	Source      []message.Message
	Shapes      []requestshape.Shape
	Tokens      []int
	ToolDefHash [sha256.Size]byte
	PromptHash  [sha256.Size]byte
	// SentAt is when the request was dispatched to the provider — the closest
	// local approximation of when the provider refreshed its cache entry
	// (prefill). Using the response-completion time instead would overstate
	// cache age by the whole streaming duration for both the warm-window
	// outcome classification and warm-cache routing.
	SentAt time.Time
	// ProviderInputTokens is the prompt size the provider reported for this
	// request, or 0 when its input-token semantics are unknown. The local
	// estimator attributes bytes to payload the provider prices differently
	// (encrypted reasoning blobs are the large case), so this number — not the
	// estimate — bounds what the next request could possibly find cached.
	ProviderInputTokens int
	// ObservedCacheRead records that this ref has ever reported a cache read.
	// An all-zero cache report then means a lost cache, not a provider that
	// never caches.
	ObservedCacheRead bool
}

// incrementalCacheExpectationShapes computes the shape and token-estimate
// slices for messages, reusing the previous record's entries for the leading
// run of messages that are field-equal to the previously sent request.
// requestshape.MessageEquivalent covers every field that
// requestshape.ShapeOf hashes and ctxmgr.EstimateMessageTokens reads,
// so equivalence guarantees identical shape and token values. In the
// append-only steady state only the new tail is hashed; when the whole request
// is unchanged the previous slices are returned as-is.
func incrementalCacheExpectationShapes(previous *expectationRecord, messages []message.Message) (shapes []requestshape.Shape, tokens []int, source []message.Message) {
	reusable := 0
	if previous != nil && len(previous.Source) == len(previous.Shapes) && len(previous.Source) == len(previous.Tokens) {
		reusable = requestshape.ReusablePrefixLen(previous.Source, messages)
		if reusable == len(messages) && len(previous.Source) == len(messages) {
			return previous.Shapes, previous.Tokens, previous.Source
		}
	}
	shapes = make([]requestshape.Shape, len(messages))
	tokens = make([]int, len(messages))
	if reusable > 0 {
		copy(shapes, previous.Shapes[:reusable])
		copy(tokens, previous.Tokens[:reusable])
	}
	for i := reusable; i < len(messages); i++ {
		shapes[i] = requestshape.ShapeOf(&messages[i])
		tokens[i] = ctxmgr.EstimateMessageTokens(messages[i])
	}
	return shapes, tokens, append([]message.Message(nil), messages...)
}

// Cache outcome classes attribute one request's cache result. Provider-side
// loss is never reported as definitive: a low cache read after the warm window
// or without any reported cache usage stays unknown.
const (
	cacheOutcomeLocalRewrite          = "local_rewrite"
	cacheOutcomeAppend                = "append"
	cacheOutcomeSuspectedProviderMiss = "suspected_provider_miss"
	cacheOutcomeUnknown               = "unknown"
)

// minAttributableCacheTokens is the smallest expected stable prefix worth
// attributing a miss to: providers do not cache prompts below roughly this
// size, so a smaller expectation cannot prove anything about provider behavior.
const minAttributableCacheTokens = 1024

// classifyCacheOutcome buckets a request into the four cache diagnostic
// classes: a proven chord-side rewrite, an append-only change with consistent
// cache reads, a stable local prefix whose provider cache reads fell far short
// (suspected provider or routing miss), or unknown when the evidence cannot
// separate those cases.
func classifyCacheOutcome(localRewriteReason string, expectedTokens int, sinceLastRequest time.Duration, usage *message.TokenUsage, cacheObserved bool) (outcome, reason string) {
	if localRewriteReason != "" {
		return cacheOutcomeLocalRewrite, localRewriteReason
	}
	if usage == nil {
		return cacheOutcomeUnknown, "no_cache_usage"
	}
	if usage.CacheReadTokens <= 0 && usage.CacheWriteTokens <= 0 && usage.CacheWrite1hTokens <= 0 && !cacheObserved {
		// All-zero cache fields cannot distinguish a provider without prompt
		// caching from a complete miss. Once this ref has reported a cache
		// read, though, a zero report is a lost cache rather than a provider
		// that never caches.
		return cacheOutcomeUnknown, "no_cache_usage"
	}
	if expectedTokens < minAttributableCacheTokens {
		return cacheOutcomeAppend, ""
	}
	// expectedTokens is a local estimate while CacheReadTokens is
	// provider-accurate, so require a shortfall beyond estimation error.
	if usage.CacheReadTokens*2 >= expectedTokens {
		return cacheOutcomeAppend, ""
	}
	if sinceLastRequest >= cacheWarmWindow {
		// The provider may have legitimately expired the entry; a miss after
		// the warm window is not evidence of provider misbehavior.
		return cacheOutcomeUnknown, "warm_window_elapsed"
	}
	return cacheOutcomeSuspectedProviderMiss, "low_cache_read"
}

// providerPromptTokens returns the provider-reported prompt size, or 0 when the
// provider does not state that input tokens already include cache reads: a
// provider reporting only the uncached remainder would otherwise cap the
// expectation far below the real prompt.
func providerPromptTokens(usage *message.TokenUsage) int {
	if usage == nil || usage.InputTokens <= 0 || !usage.InputSemanticsKnown || !usage.InputIncludesCacheRead {
		return 0
	}
	return usage.InputTokens
}

// observedCacheRead reports whether this ref has ever returned a cache read.
func observedCacheRead(previous *expectationRecord, usage *message.TokenUsage) bool {
	if previous != nil && previous.ObservedCacheRead {
		return true
	}
	return usage != nil && usage.CacheReadTokens > 0
}

// Request is the projected input evidence for a completed provider attempt.
// Messages are a borrowed immutable surface; the tracker keeps shallow copies
// and reuses prior shape/token slices when the prefix remains field-equal.
type Request struct {
	ModelRef    string
	Messages    []message.Message
	ToolDefHash [sha256.Size]byte
	SentAt      time.Time
	Usage       *message.TokenUsage
	// PromptHash reads the installed prompt snapshot after message fingerprinting
	// and immediately before publishing the expectation. It must be non-nil and
	// must not mutate this tracker; request execution remains with the caller.
	PromptHash func() [sha256.Size]byte
}

// Expectations owns the latest completed-request cache evidence per model ref.
// Its zero value is ready for use. It must not be copied after use.
type Expectations struct {
	mu    sync.Mutex
	byRef map[string]*expectationRecord
}

// Note compares and publishes one completed request. The caller owns protocol
// projection and chooses the settlement point; SentAt records original dispatch
// time. Fingerprinting runs outside the lock, preserving independent readers.
func (s *Expectations) Note(request Request) map[string]string {
	if s == nil || request.ModelRef == "" {
		return nil
	}
	modelRef, messages := request.ModelRef, request.Messages
	toolDefHash, sentAt, usage := request.ToolDefHash, request.SentAt, request.Usage
	s.mu.Lock()
	previous := s.byRef[modelRef]
	s.mu.Unlock()

	shapes, tokens, source := incrementalCacheExpectationShapes(previous, messages)
	totalTokens := 0
	for _, t := range tokens {
		totalTokens += t
	}
	record := &expectationRecord{
		Source:              source,
		Shapes:              shapes,
		Tokens:              tokens,
		ToolDefHash:         toolDefHash,
		PromptHash:          request.PromptHash(),
		SentAt:              sentAt,
		ProviderInputTokens: providerPromptTokens(usage),
		ObservedCacheRead:   observedCacheRead(previous, usage),
	}

	s.mu.Lock()
	if s.byRef == nil {
		s.byRef = make(map[string]*expectationRecord)
	}
	s.byRef[modelRef] = record
	s.mu.Unlock()

	diag := map[string]string{
		"cache_messages":   strconv.Itoa(len(messages)),
		"cache_est_tokens": strconv.Itoa(totalTokens),
	}
	if previous == nil {
		diag["cache_expected_tokens"] = "0"
		diag["cache_prefix_divergence"] = "0"
		diag["cache_first_request"] = "true"
		diag["cache_outcome"] = cacheOutcomeUnknown
		diag["cache_outcome_reason"] = "first_request"
		return diag
	}

	divergence := 0
	limit := min(len(previous.Shapes), len(shapes))
	for divergence < limit && previous.Shapes[divergence] == shapes[divergence] {
		divergence++
	}
	expected := 0
	for i := 0; i < divergence; i++ {
		expected += previous.Tokens[i]
	}
	// The provider cannot hold more of this ref than the prompt it actually
	// received last time, so that reported size caps the local estimate.
	if previous.ProviderInputTokens > 0 {
		expected = min(expected, previous.ProviderInputTokens)
	}
	toolDefChanged := previous.ToolDefHash != toolDefHash
	promptChanged := previous.PromptHash != record.PromptHash
	if toolDefChanged || promptChanged {
		// A tool-surface change invalidates the provider cache from position 0
		// regardless of message stability.
		expected = 0
	}
	diag["cache_expected_tokens"] = strconv.Itoa(expected)
	diag["cache_prefix_divergence"] = strconv.Itoa(divergence)
	diag["cache_prev_messages"] = strconv.Itoa(len(previous.Shapes))
	diag["cache_prev_gap_ms"] = strconv.FormatInt(sentAt.Sub(previous.SentAt).Milliseconds(), 10)
	if toolDefChanged {
		diag["cache_tooldef_changed"] = "true"
	}
	if promptChanged {
		diag["cache_system_prompt_changed"] = "true"
	}
	prefixRewrite := divergence < len(previous.Shapes) && divergence < len(shapes)
	if prefixRewrite {
		// The first differing position tells whether the mutation was an
		// append (tail growth, cheap) or an in-place rewrite (early message
		// changed, expensive: everything after it is re-billed at input price).
		diag["cache_divergence_kind"] = "rewrite"
	} else {
		diag["cache_divergence_kind"] = "append"
	}
	localRewriteReason := ""
	switch {
	case toolDefChanged:
		localRewriteReason = "tool_definitions_changed"
	case promptChanged:
		localRewriteReason = "system_prompt_changed"
	case prefixRewrite:
		localRewriteReason = "prefix_rewrite"
	}
	outcome, reason := classifyCacheOutcome(localRewriteReason, expected, sentAt.Sub(previous.SentAt), usage, record.ObservedCacheRead)
	diag["cache_outcome"] = outcome
	if reason != "" {
		diag["cache_outcome_reason"] = reason
	}
	return diag
}

// cacheWarmWindow approximates provider prompt-cache TTL: a ref called within
// this window very likely still holds our prefix.
const cacheWarmWindow = 10 * time.Minute

// Warm reports whether this ref was sent a request within the cache warm window.
func (s *Expectations) Warm(modelRef string, now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.byRef[modelRef]
	return record != nil && now.Sub(record.SentAt) < cacheWarmWindow
}

// Reset drops the prior session's request evidence.
func (s *Expectations) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.byRef = nil
	s.mu.Unlock()
}
