package agent

import (
	"strings"
	"sync"
)

// cacheHitTracker maintains a rolling observed prompt-cache hit rate per
// running model ref. Provider gateways differ wildly in cache quality (same
// client behavior measured 40%–94% across providers), so observed hit rate is
// a routing signal: it converts a provider's nominal token price into an
// effective price.
type cacheHitTracker struct {
	mu    sync.Mutex
	byRef map[string]*cacheHitWindow
}

// cacheHitWindow is an exponentially-weighted average of per-call hit ratios,
// weighted by input size so one huge request counts more than many small ones.
type cacheHitWindow struct {
	weightedHit   float64
	weightedTotal float64
	observations  int
}

// cacheHitDecay keeps roughly the last ~20 large calls influential, so a
// provider that fixes (or degrades) its cache routing is re-scored within a
// single long session.
const cacheHitDecay = 0.95

func newCacheHitTracker() *cacheHitTracker {
	return &cacheHitTracker{byRef: make(map[string]*cacheHitWindow)}
}

func (t *cacheHitTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.byRef = make(map[string]*cacheHitWindow)
	t.mu.Unlock()
}

func (t *cacheHitTracker) Observe(modelRef string, inputTokens, cacheReadTokens int) {
	if t == nil || inputTokens <= 0 {
		return
	}
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return
	}
	hit := float64(cacheReadTokens)
	if hit > float64(inputTokens) {
		hit = float64(inputTokens)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.byRef[modelRef]
	if w == nil {
		w = &cacheHitWindow{}
		t.byRef[modelRef] = w
	}
	w.weightedHit = w.weightedHit*cacheHitDecay + hit
	w.weightedTotal = w.weightedTotal*cacheHitDecay + float64(inputTokens)
	w.observations++
}

// HitRate returns the observed rolling hit rate for a ref. ok is false until
// enough large calls have been seen to trust the estimate; callers should use
// an optimistic default in that case so new providers are not penalized.
func (t *cacheHitTracker) HitRate(modelRef string) (rate float64, ok bool) {
	const minObservations = 3
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.byRef[strings.TrimSpace(modelRef)]
	if w == nil || w.observations < minObservations || w.weightedTotal <= 0 {
		return 0, false
	}
	return w.weightedHit / w.weightedTotal, true
}
