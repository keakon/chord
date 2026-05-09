package llm

import (
	"time"

	"github.com/keakon/chord/internal/ratelimit"
)

// KeyStats returns healthy key-pool stats for the provider resolved from the last
// CompleteStream outcome (lastCallStatus.RunningModelRef). Prefer KeyStatsForRef
// when the UI source of truth is the agent's RunningModelRef (see MainAgent.KeyStats).
func (c *Client) KeyStats() (healthy, total int) {
	c.mu.RLock()
	ref := c.lastCallStatus.RunningModelRef
	c.mu.RUnlock()
	return c.KeyStatsForRef(ref)
}

// KeyStatsForRef returns healthy key-pool stats for the provider that owns the given
// "provider/model" ref within this client (cursor-head entry or remaining
// model-pool entries). Empty ref resolves to the current cursor-head entry.
func (c *Client) KeyStatsForRef(ref string) (healthy, total int) {
	c.mu.RLock()
	prov := c.providerForModelRefLocked(ref)
	c.mu.RUnlock()
	if prov == nil {
		return 0, 0
	}
	return prov.HealthyKeyCount()
}

// ConfirmedKeyStatsForRef returns (healthy, total) keys for the provider owning ref.
// It is retained as a semantic alias for the sidebar's healthy key count.
func (c *Client) ConfirmedKeyStatsForRef(ref string) (confirmed, total int) {
	return c.KeyStatsForRef(ref)
}

// KeyPoolNextTransitionForRef returns the next key-pool transition time for the
// provider that owns ref within the effective model pool.
func (c *Client) KeyPoolNextTransitionForRef(ref string) time.Duration {
	c.mu.RLock()
	prov := c.providerForModelRefLocked(ref)
	c.mu.RUnlock()
	if prov == nil {
		return 0
	}
	return prov.KeyPoolNextTransition()
}

func (c *Client) CurrentRateLimitSnapshotForRef(ref string) *ratelimit.KeyRateLimitSnapshot {
	c.mu.RLock()
	prov := c.providerForModelRefLocked(ref)
	c.mu.RUnlock()
	if prov == nil {
		return nil
	}

	now := time.Now()
	inline := prov.CurrentInlineRateLimitSnapshot()
	polled := prov.CurrentPolledRateLimitSnapshot()

	// Inline snapshots are preferred when fresh because they are request/key scoped.
	// However, when a Codex WebSocket stream stops emitting codex.rate_limits frames,
	// the inline snapshot may stay non-expired for the whole window (e.g. 15m) while
	// usage continues to change. In that case, prefer a newer /wham/usage (polled)
	// snapshot once the inline data is stale.
	if inline != nil && !ratelimit.SnapshotExpiredAt(inline, now) {
		if polled != nil && prov.IsCodexOAuthTransport() {
			const staleAfter = time.Minute
			if !inline.CapturedAt.IsZero() && now.Sub(inline.CapturedAt) >= staleAfter && polled.CapturedAt.After(inline.CapturedAt) {
				return polled
			}
		}
		return inline
	}
	if polled != nil {
		return polled
	}
	if inline != nil {
		return inline
	}
	return prov.CurrentKeySnapshot()
}
