package llm

// DisplayModelRef captures the request target, or the next request's cursor
// entry when no request is active. The boolean distinguishes an active request
// target from a cursor preview so callers can preview pending pool selections.
func (c *Client) DisplayModelRef() (ref string, active bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.attemptModelRef != "" {
		return c.attemptModelRef, true
	}
	return c.nextRequestModelRefLocked(), false
}

func (c *Client) nextRequestModelRefLocked() string {
	if c.poolCursor > 0 && c.poolCursor <= len(c.fallbackModels) {
		return modelRefWithVariant(c.fallbackModels[c.poolCursor-1])
	}
	ref := providerModelRef(c.provider, c.modelID)
	if c.activeVariant != "" && ref != "" {
		ref += "@" + c.activeVariant
	}
	return ref
}
