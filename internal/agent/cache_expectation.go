package agent

import (
	"crypto/sha256"
	"strconv"
	"time"

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
	SentAt      time.Time
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

// noteCacheExpectation compares the outgoing request against the previous
// request sent to the same running model ref and returns usage diagnostics
// that let offline analysis attribute cache misses: if actual cache_read is
// far below cache_expected_tokens, the provider dropped a cache chord kept
// byte-stable; if cache_prefix_divergence is small, chord itself mutated an
// early message (e.g. context reduction) and the miss is self-inflicted.
// It then records the current request as the new expectation for that ref.
func (a *MainAgent) noteCacheExpectation(modelRef string, messages []message.Message, toolDefHash [sha256.Size]byte) map[string]string {
	if a == nil || modelRef == "" || len(messages) == 0 {
		return nil
	}
	a.cacheExpectMu.Lock()
	previous := a.cacheExpectations[modelRef]
	a.cacheExpectMu.Unlock()

	shapes, tokens, source := incrementalCacheExpectationShapes(previous, messages)
	totalTokens := 0
	for _, t := range tokens {
		totalTokens += t
	}
	now := time.Now()
	record := &cacheExpectationRecord{
		Source:      source,
		Shapes:      shapes,
		Tokens:      tokens,
		ToolDefHash: toolDefHash,
		PromptHash:  a.systemPromptHash(),
		SentAt:      now,
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
	diag["cache_prev_gap_ms"] = strconv.FormatInt(now.Sub(previous.SentAt).Milliseconds(), 10)
	if toolDefChanged {
		diag["cache_tooldef_changed"] = "true"
	}
	if promptChanged {
		diag["cache_system_prompt_changed"] = "true"
	}
	if divergence < len(previous.Shapes) && divergence < len(shapes) {
		// The first differing position tells whether the mutation was an
		// append (tail growth, cheap) or an in-place rewrite (early message
		// changed, expensive: everything after it is re-billed at input price).
		diag["cache_divergence_kind"] = "rewrite"
	} else {
		diag["cache_divergence_kind"] = "append"
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

// observedCacheHit feeds the actual provider-reported usage back into the
// per-ref rolling hit-rate tracker used by cache-aware model selection.
// Small requests are skipped: their hit ratio is dominated by the system
// prompt and carries no routing signal.
func (a *MainAgent) observedCacheHit(modelRef string, usage *message.TokenUsage) {
	const minObservedInputTokens = 30000
	if a == nil || modelRef == "" || usage == nil || usage.InputTokens < minObservedInputTokens {
		return
	}
	if a.cacheHitTracker != nil {
		a.cacheHitTracker.Observe(modelRef, usage.InputTokens, usage.CacheReadTokens)
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
