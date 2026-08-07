package agent

import (
	"strings"
	"sync"
)

// cacheHitTracker maintains the exact token-weighted prompt-cache hit rate per
// running model ref for the current session. It is a lifetime session rate,
// not a recent/decaying rate; restoring a session restores the same evidence.
type cacheHitTracker struct {
	mu    sync.Mutex
	byRef map[string]*cacheHitWindow
}

// cacheHitWindow stores mutually-exclusive token totals.
type cacheHitWindow struct {
	hitTokens   int64
	totalTokens int64
}

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
	// InputTokens is the normalized full input. Clamp both sides so provider
	// inconsistencies cannot produce a hit rate above 100%.
	hit := int64(max(cacheReadTokens, 0))
	total := int64(inputTokens)
	if hit > total {
		hit = total
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.byRef[modelRef]
	if w == nil {
		w = &cacheHitWindow{}
		t.byRef[modelRef] = w
	}
	w.hitTokens += hit
	w.totalTokens += total
}

// HitRate returns the exact token-weighted hit rate for a ref.
func (t *cacheHitTracker) HitRate(modelRef string) (rate float64, ok bool) {
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	w := t.byRef[strings.TrimSpace(modelRef)]
	if w == nil || w.totalTokens <= 0 {
		return 0, false
	}
	rate = float64(w.hitTokens) / float64(w.totalTokens)
	// Keep the public routing signal valid even with provider inconsistencies
	// or floating-point accumulation at the boundary.
	return min(max(rate, 0), 1), true
}
