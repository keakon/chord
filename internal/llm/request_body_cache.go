package llm

import (
	"sync"

	"github.com/keakon/chord/internal/message"
)

// requestBodyIdentity captures the inputs a provider's marshaled body derives
// from, comparable by value. The message and tool slices are identified by
// their backing arrays: every new request surface (a new turn, replay
// reinforcement, normalization for another pool target) builds a fresh slice,
// so identity is a sufficient guard against reusing a body for changed
// inputs. Request tuning is deliberately not part of the identity, which is
// sound only while one (messages, tools) slice is converted under a single
// model and tuning: the pool's key-attempt loop reuses one target's slice while
// rotating only the auth key, and modelcompat.NormalizeForTarget builds a fresh
// slice per target. A future path that re-converts the same slice under a
// different model or tuning would reuse the wrong body, so it must rebuild the
// slice (or extend this identity) instead.
type requestBodyIdentity struct {
	system    string
	maxTokens int
	msgs      *message.Message
	msgsLen   int
	tools     *message.ToolDefinition
	toolsLen  int
}

func requestBodyIdentityFor(systemPrompt string, messages []message.Message, tools []message.ToolDefinition, maxTokens int) requestBodyIdentity {
	id := requestBodyIdentity{system: systemPrompt, maxTokens: maxTokens, msgsLen: len(messages), toolsLen: len(tools)}
	if len(messages) > 0 {
		id.msgs = &messages[0]
	}
	if len(tools) > 0 {
		id.tools = &tools[0]
	}
	return id
}

// requestBodyReuse is a single-entry cache for a provider's marshaled request
// body, shared across the key-attempt loop of one target: key rotation changes
// only the auth header, while rebuilding the body re-converts and re-marshals
// the full message history per attempt. A single entry is enough — a retry
// burst hits it, and an interleaved request with different inputs simply
// rebuilds (the status quo cost) without stale-reuse risk.
type requestBodyReuse struct {
	mu     sync.Mutex
	valid  bool
	guard  requestBodyIdentity
	cached []byte
	extra  any
}

// body returns the cached body for identical inputs, or builds and caches it.
func (r *requestBodyReuse) body(identity requestBodyIdentity, build func() ([]byte, error)) ([]byte, error) {
	cached, _, err := r.bodyWithExtra(identity, func() ([]byte, any, error) {
		data, err := build()
		return data, nil, err
	})
	return cached, err
}

// bodyWithExtra is body with an opaque per-build artifact cached and returned
// alongside the body — e.g. the converted input items a transport needs in
// struct form next to the marshaled bytes.
func (r *requestBodyReuse) bodyWithExtra(identity requestBodyIdentity, build func() ([]byte, any, error)) ([]byte, any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.valid && r.guard == identity {
		return r.cached, r.extra, nil
	}
	body, extra, err := build()
	if err != nil {
		return nil, nil, err
	}
	r.guard, r.cached, r.extra, r.valid = identity, body, extra, true
	return body, extra, nil
}
