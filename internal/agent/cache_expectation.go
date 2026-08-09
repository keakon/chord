package agent

import (
	"crypto/sha256"
	"strconv"
	"time"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// cacheExpectationRecord fingerprints the last request actually sent to one
// running model ref. Provider prompt caches are independent per provider, so
// expectations are tracked per ref: the next request to the same ref should
// hit the cache for exactly the unchanged message prefix. Records are
// immutable once stored; Source keeps a shallow copy of the sent messages so
// the next request can reuse Shapes and Tokens for the field-equal prefix
// instead of re-hashing the whole conversation.
type cacheExpectationRecord struct {
	Source      []message.Message
	Shapes      []stableReductionMessageShape
	Tokens      []int
	ToolDefHash [sha256.Size]byte
	PromptHash  [sha256.Size]byte
	// SentAt is when the request was dispatched to the provider — the closest
	// local approximation of when the provider refreshed its cache entry
	// (prefill). Using the response-completion time instead would overstate
	// cache age by the whole streaming duration for both the warm-window
	// outcome classification and refCacheWarm routing.
	SentAt time.Time
}

// incrementalCacheExpectationShapes computes the shape and token-estimate
// slices for messages, reusing the previous record's entries for the leading
// run of messages that are field-equal to the previously sent request.
// stableReductionMessageEquivalent covers every field that
// stableReductionMessageShapeOf hashes and ctxmgr.EstimateMessageTokens reads,
// so equivalence guarantees identical shape and token values. In the
// append-only steady state only the new tail is hashed; when the whole request
// is unchanged the previous slices are returned as-is.
func incrementalCacheExpectationShapes(previous *cacheExpectationRecord, messages []message.Message) (shapes []stableReductionMessageShape, tokens []int, source []message.Message) {
	reusable := 0
	if previous != nil && len(previous.Source) == len(previous.Shapes) && len(previous.Source) == len(previous.Tokens) {
		limit := min(len(previous.Source), len(messages))
		for reusable < limit && stableReductionMessageEquivalent(&previous.Source[reusable], &messages[reusable]) {
			reusable++
		}
		if reusable == len(messages) && len(previous.Source) == len(messages) {
			return previous.Shapes, previous.Tokens, previous.Source
		}
	}
	shapes = make([]stableReductionMessageShape, len(messages))
	tokens = make([]int, len(messages))
	if reusable > 0 {
		copy(shapes, previous.Shapes[:reusable])
		copy(tokens, previous.Tokens[:reusable])
	}
	for i := reusable; i < len(messages); i++ {
		shapes[i] = stableReductionMessageShapeOf(&messages[i])
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
func classifyCacheOutcome(localRewriteReason string, expectedTokens int, sinceLastRequest time.Duration, usage *message.TokenUsage) (outcome, reason string) {
	if localRewriteReason != "" {
		return cacheOutcomeLocalRewrite, localRewriteReason
	}
	if usage == nil || (usage.CacheReadTokens <= 0 && usage.CacheWriteTokens <= 0 && usage.CacheWrite1hTokens <= 0) {
		// All-zero cache fields cannot distinguish a provider without prompt
		// caching from a complete miss.
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

// noteCacheExpectation compares the outgoing request against the previous
// request sent to the same running model ref and returns usage diagnostics
// that attribute cache misses: if actual cache_read is far below
// cache_expected_tokens, the provider dropped a cache chord kept byte-stable;
// if cache_prefix_divergence is small, chord itself mutated an early message
// (e.g. context reduction) and the miss is self-inflicted. The provider-
// reported usage for the current response, when available, resolves the
// attribution into cache_outcome. It then records the current request as the
// new expectation for that ref.
//
// tailOverlayCount transient messages at the end of the request are excluded
// from the expectation: the cache boundary is placed before them, so their
// churn across requests is not a chord-side prefix rewrite.
func (a *MainAgent) noteCacheExpectation(modelRef string, messages []message.Message, tailOverlayCount int, toolDefHash [sha256.Size]byte, sentAt time.Time, usage *message.TokenUsage) map[string]string {
	if a == nil || modelRef == "" || len(messages) == 0 {
		return nil
	}
	if tailOverlayCount > 0 && tailOverlayCount < len(messages) {
		messages = messages[:len(messages)-tailOverlayCount]
	}
	a.cacheExpectMu.Lock()
	previous := a.cacheExpectations[modelRef]
	a.cacheExpectMu.Unlock()

	shapes, tokens, source := incrementalCacheExpectationShapes(previous, messages)
	totalTokens := 0
	for _, t := range tokens {
		totalTokens += t
	}
	record := &cacheExpectationRecord{
		Source:      source,
		Shapes:      shapes,
		Tokens:      tokens,
		ToolDefHash: toolDefHash,
		PromptHash:  a.systemPromptHash(),
		SentAt:      sentAt,
	}

	a.cacheExpectMu.Lock()
	if a.cacheExpectations == nil {
		a.cacheExpectations = make(map[string]*cacheExpectationRecord)
	}
	a.cacheExpectations[modelRef] = record
	a.cacheExpectMu.Unlock()

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
	outcome, reason := classifyCacheOutcome(localRewriteReason, expected, sentAt.Sub(previous.SentAt), usage)
	diag["cache_outcome"] = outcome
	if reason != "" {
		diag["cache_outcome_reason"] = reason
	}
	return diag
}

func (a *MainAgent) systemPromptHash() [sha256.Size]byte {
	if a == nil {
		return [sha256.Size]byte{}
	}
	a.llmMu.RLock()
	prompt := a.installedSysPrompt
	a.llmMu.RUnlock()
	return sha256.Sum256([]byte(prompt))
}

func (a *MainAgent) resetCacheRoutingState() {
	if a == nil {
		return
	}
	a.cacheExpectMu.Lock()
	a.cacheExpectations = nil
	a.cacheExpectMu.Unlock()
	if a.cacheHitTracker == nil {
		a.cacheHitTracker = newCacheHitTracker()
	} else {
		a.cacheHitTracker.Reset()
	}
}

func (a *MainAgent) restoreCacheHitStats(stats analytics.SessionStats) {
	if a == nil || a.cacheHitTracker == nil {
		return
	}
	for modelRef, modelStats := range stats.ByModel {
		if modelStats == nil {
			continue
		}
		fullInputTokens := analytics.FullInputTokens(modelStats.InputTokens, modelStats.CacheReadTokens, modelStats.CacheWriteTokens)
		if fullInputTokens <= 0 {
			continue
		}
		a.cacheHitTracker.Observe(modelRef, int(fullInputTokens), int(modelStats.CacheReadTokens))
	}
}

// observedCacheHit feeds every valid provider-reported usage record into the
// exact token-weighted cache hit-rate tracker used by cache-aware routing.
func (a *MainAgent) observedCacheHit(modelRef string, usage *message.TokenUsage) {
	if a == nil || modelRef == "" || usage == nil {
		return
	}
	billing := analytics.NormalizeBillingUsage(analytics.UsageSnapshotFromTokenUsage(*usage))
	fullInputTokens := analytics.FullInputTokens(billing.InputTokens, billing.CacheReadTokens, billing.CacheWriteTokens)
	if fullInputTokens <= 0 {
		return
	}
	if a.cacheHitTracker != nil {
		a.cacheHitTracker.Observe(modelRef, int(fullInputTokens), int(billing.CacheReadTokens))
	}
}

// cacheWarmWindow approximates provider prompt-cache TTL: a ref called within
// this window very likely still holds our prefix.
const cacheWarmWindow = 10 * time.Minute

// refCacheWarm reports whether a request was recently sent to modelRef, so
// its provider-side prompt cache is likely still populated.
func (a *MainAgent) refCacheWarm(modelRef string, now time.Time) bool {
	if a == nil {
		return false
	}
	a.cacheExpectMu.Lock()
	defer a.cacheExpectMu.Unlock()
	record := a.cacheExpectations[modelRef]
	return record != nil && now.Sub(record.SentAt) < cacheWarmWindow
}

// cacheAwareCandidateScore ranks interchangeable providers for the same model
// by expected effective input price: nominal price discounted by the share of
// tokens the provider is observed to actually serve from cache. Higher score
// is better (score is the negated effective price). A warm cache from a recent
// request raises the expected hit rate to at least 90% for the ranking.
func (a *MainAgent) cacheAwareCandidateScore(modelRef string) float64 {
	// Optimistic default keeps unobserved providers competitive so the tracker
	// gets samples; persistently bad caches lose their rank within a few calls.
	const defaultHitRate = 0.85
	rate, ok := float64(0), false
	if a != nil && a.cacheHitTracker != nil {
		rate, ok = a.cacheHitTracker.HitRate(modelRef)
	}
	if !ok {
		rate = defaultHitRate
	}
	if a.refCacheWarm(modelRef, time.Now()) && rate < 0.9 {
		rate = 0.9
	}
	inputPrice, cachePrice := 1.0, 0.1
	if cost := a.lookupModelCost(modelRef); cost != nil && cost.Input > 0 {
		inputPrice = cost.Input
		if cost.CacheRead > 0 {
			cachePrice = cost.CacheRead
		} else {
			cachePrice = inputPrice / 10
		}
	}
	effective := inputPrice*(1-rate) + cachePrice*rate
	return -effective
}
