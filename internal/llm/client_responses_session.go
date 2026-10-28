package llm

// routingInvalidator is implemented by provider types that need to drop
// transport-level incremental state when routing changes mid-request.
type routingInvalidator interface {
	InvalidateRouting(reason string)
}

// SessionIdentity is the local session identity a Client stamps onto its
// requests. Providers that need a wire-shaped client identity derive it from
// these fields; the local key itself never goes on the wire.
type SessionIdentity struct {
	// Key is Chord's session key: the session directory base, with a
	// ":sub:<instance>" suffix for a SubAgent.
	Key string
	// WindowNumber counts the durable context compactions already applied in
	// this session, so it only advances when the history is rewritten.
	WindowNumber uint64
	// SubAgent marks a delegated session, which the Codex client reports as a
	// distinct thread source from the user's own thread.
	SubAgent bool
}

// SetSessionIdentity records the session identity used for provider-side
// prompt-cache routing. The identity travels per-request inside RequestTuning:
// provider impls are shared across agents (MainAgent and SubAgents may resolve
// to the same ResponsesProvider), so storing it on the impl would let one
// agent's requests inherit another's cache identity.
func (c *Client) SetSessionIdentity(id SessionIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionIdentity = id
}

// SessionKey returns the per-Client local session key set via
// SetSessionIdentity.
func (c *Client) SessionKey() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionIdentity.Key
}
