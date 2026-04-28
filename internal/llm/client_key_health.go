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
	if snap := prov.CurrentInlineRateLimitSnapshot(); snap != nil {
		return snap
	}
	if snap := prov.CurrentPolledRateLimitSnapshot(); snap != nil {
		return snap
	}
	return prov.CurrentKeySnapshot()
}
