package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

type responsesTurnStateContextKey struct{}

// ResponsesTurnState carries the sticky-routing token returned by a Responses
// backend for one turn. The token is scoped by both provider and credential so
// fallback cannot echo state to another endpoint or OAuth account.
type ResponsesTurnState struct {
	mu     sync.RWMutex
	values map[string]string
}

// NewResponsesTurnState creates empty state for one Responses turn.
func NewResponsesTurnState() *ResponsesTurnState {
	return &ResponsesTurnState{}
}

// WithResponsesTurnState attaches state to a request context. A nil state is
// ignored so callers can pass optional turn state without branching.
func WithResponsesTurnState(ctx context.Context, state *ResponsesTurnState) context.Context {
	if ctx == nil || state == nil {
		return ctx
	}
	return context.WithValue(ctx, responsesTurnStateContextKey{}, state)
}

// ResponsesTurnStateFromContext returns the turn-scoped state attached to ctx.
func ResponsesTurnStateFromContext(ctx context.Context) *ResponsesTurnState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(responsesTurnStateContextKey{}).(*ResponsesTurnState)
	return state
}

func (s *ResponsesTurnState) headerValue(identity string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.values[identity]
}

func (s *ResponsesTurnState) capture(value, identity string) {
	if s == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	s.mu.Lock()
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[identity] = value
	s.mu.Unlock()
}

func responsesTurnStateIdentity(provider *ProviderConfig, apiKey string) string {
	providerScope := ""
	if provider != nil {
		providerScope = strings.TrimSpace(provider.Name())
		if providerScope == "" {
			providerScope = strings.TrimSpace(provider.APIURL())
		}
		if info := provider.oauthInfoForKey(apiKey); info != nil {
			if accountID := strings.TrimSpace(info.AccountID); accountID != "" {
				return providerScope + "\x00account:" + accountID
			}
		}
	}
	return providerScope + "\x00key:" + apiKey
}

func applyResponsesTurnStateHeader(h http.Header, state *ResponsesTurnState, identity string) {
	if value := state.headerValue(identity); value != "" {
		h.Set(headerCodexTurnState, value)
	}
}

func captureResponsesTurnState(state *ResponsesTurnState, headers http.Header, identity string) {
	if state == nil {
		return
	}
	for name, values := range headers {
		if !strings.EqualFold(name, headerCodexTurnState) || len(values) == 0 {
			continue
		}
		state.capture(values[0], identity)
		return
	}
}

func captureResponsesTurnStateEvent(state *ResponsesTurnState, data []byte, identity string) {
	if state == nil || len(data) == 0 {
		return
	}
	var event struct {
		Headers map[string]json.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return
	}
	for name, raw := range event.Headers {
		if !strings.EqualFold(name, headerCodexTurnState) {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			state.capture(value, identity)
			return
		}
		var values []string
		if json.Unmarshal(raw, &values) == nil && len(values) > 0 {
			state.capture(values[0], identity)
			return
		}
	}
}
