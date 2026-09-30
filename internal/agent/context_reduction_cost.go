package agent

import (
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/config"
)

const (
	// The horizon is a bounded policy assumption, not a prediction derived
	// from past request counts. A queued checkpoint ends this reuse period.
	reductionFlushHorizonRequests = 30
	cacheMissPenaltyRatioFallback = 9
	// With the horizon bounded at 30 and savings no larger than the cached
	// tail, this coefficient prevents speculative rewrites for free reads.
	cacheMissPenaltyRatioCeiling = 200
)

// boundaryCacheMissPenaltyRatio derives the write-minus-read penalty relative
// to cache reads. TTL selects the write price actually used by the request.
func boundaryCacheMissPenaltyRatio(cost *config.ModelCost, billableInputTokens int64, tier config.ServiceTier, ttl string) (float64, bool) {
	if cost == nil {
		return 0, false
	}
	resolved := cost.ResolvePricing(billableInputTokens, tier)
	if resolved.Input <= 0 {
		return 0, false
	}
	cacheWrite := resolved.CacheWrite
	if ttl == "1h" {
		cacheWrite = resolved.CacheWrite1h
	}
	if resolved.CacheRead <= 0 {
		return cacheMissPenaltyRatioCeiling, true
	}
	return max(0, (cacheWrite-resolved.CacheRead)/resolved.CacheRead), true
}

func (a *MainAgent) boundaryFlushPenaltyRatio(modelSnapshot llmModelContinuitySnapshot, billableInputTokens int) float64 {
	client, _, _, _ := a.llmSnapshot()
	mode, ttl := client.PromptCacheSettingsForModelRef(modelSnapshot.CurrentModel)
	if mode == "off" {
		return 0
	}
	if ratio, ok := boundaryCacheMissPenaltyRatio(a.lookupModelCost(modelSnapshot.CurrentModel), int64(billableInputTokens), client.EffectiveServiceTierForModelRef(modelSnapshot.CurrentModel), ttl); ok {
		return ratio
	}
	log.Debugf("context reduction: boundary flush pricing unavailable model=%q; falling back to penalty ratio %v", modelSnapshot.CurrentModel, cacheMissPenaltyRatioFallback)
	return cacheMissPenaltyRatioFallback
}

func (a *MainAgent) reductionFlushHorizon() int {
	if a.autoCompactRequested.Load() || a.IsCompactionRunning() {
		return 0
	}
	return reductionFlushHorizonRequests
}
