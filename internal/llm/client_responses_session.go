package llm

// responsesSessionResetter is implemented by provider types that maintain
// previous_response_id session state and support resetting it.
type responsesSessionResetter interface {
	ResetResponsesSession(reason string)
}

// sessionIDSetter is implemented by provider types that support setting a
// persistent session identifier for prompt caching.
type sessionIDSetter interface {
	SetSessionID(sid string)
}

// ResetResponsesSession clears any cached previous_response_id state owned by
// the current provider implementation (and fallback providers).
// Non-Responses providers are a no-op.
func (c *Client) ResetResponsesSession(reason string) {
	c.mu.RLock()
	primary := c.providerImpl
	fallbacks := c.fallbackModels
	c.mu.RUnlock()

	if r, ok := primary.(responsesSessionResetter); ok {
		r.ResetResponsesSession(reason)
	}
	for _, fb := range fallbacks {
		if r, ok := fb.ProviderImpl.(responsesSessionResetter); ok {
			r.ResetResponsesSession(reason)
		}
	}
}

// SetSessionID propagates the persistent session identifier to providers that
// support it (e.g. ResponsesProvider for OpenAI prompt caching).
func (c *Client) SetSessionID(sid string) {
	c.mu.RLock()
	primary := c.providerImpl
	fallbacks := c.fallbackModels
	c.mu.RUnlock()

	if s, ok := primary.(sessionIDSetter); ok {
		s.SetSessionID(sid)
	}
	for _, fb := range fallbacks {
		if s, ok := fb.ProviderImpl.(sessionIDSetter); ok {
			s.SetSessionID(sid)
		}
	}
}
