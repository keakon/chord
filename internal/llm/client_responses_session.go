package llm

// routingInvalidator is implemented by provider types that need to drop
// transport-level incremental state when routing changes mid-request.
type routingInvalidator interface {
	InvalidateRouting(reason string)
}

// SetSessionID records the persistent session identifier used for
// provider-side prompt-cache routing. The key travels per-request inside
// RequestTuning.SessionKey: provider impls are shared across agents
// (MainAgent and SubAgents may resolve to the same ResponsesProvider), so
// storing it on the impl would let one agent's requests inherit another's
// cache identity.
func (c *Client) SetSessionID(sid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionKey = sid
}

// SessionKey returns the per-Client session identity set via SetSessionID.
func (c *Client) SessionKey() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionKey
}
