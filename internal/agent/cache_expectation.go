package agent

import (
	"crypto/sha256"
	"time"

	"github.com/keakon/chord/internal/agent/promptcache"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
)

// noteCacheExpectation compares the outgoing request against the previous
// request sent to the same running model ref and returns usage diagnostics
// that attribute cache misses: if actual cache_read is far below
// cache_expected_tokens, cache routing or retention may explain the shortfall;
// if cache_prefix_divergence is small, chord itself mutated an early message
// (e.g. context reduction) and the miss is self-inflicted. The provider-
// reported usage for the current response, when available, resolves the
// attribution into cache_outcome. It then records the current request as the
// new expectation for that ref.
//
// Transient tails are excluded only when the selected renderer has an explicit
// cache boundary before them. Other protocols compare the full sent request.
func (a *MainAgent) noteCacheExpectation(modelRef string, messages []message.Message, tailOverlayCount int, toolDefHash [sha256.Size]byte, sentAt time.Time, usage *message.TokenUsage) map[string]string {
	if a == nil || modelRef == "" || len(messages) == 0 {
		return nil
	}
	messages = a.llmClient.PromptCacheMessagesForModelRef(modelRef, messages, tailOverlayCount)
	return a.cacheExpectations.Note(promptcache.Request{
		ModelRef: modelRef, Messages: messages, ToolDefHash: toolDefHash,
		SentAt: sentAt, Usage: usage, PromptHash: a.systemPromptHash,
	})
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
	a.cacheExpectations.Reset()
	if a.cacheHitTracker == nil {
		a.cacheHitTracker = promptcache.NewHitTracker()
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
	if a != nil && a.cacheExpectations.Warm(modelRef, time.Now()) && rate < 0.9 {
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
