package llm

// sessionIDSetter is implemented by provider types that support setting a
// persistent session identifier for prompt caching.
type sessionIDSetter interface {
	SetSessionID(sid string)
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
