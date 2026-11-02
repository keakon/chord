package llm

import (
	"net/http"
	"sync"

	"github.com/keakon/chord/internal/message"
)

// sliceID identifies a slice by its backing array, for use as a cache guard.
//
// This is a valid identity only for slices that are frozen once built: every
// new request surface (a new turn, replay reinforcement, normalization for
// another pool target) allocates a fresh slice, so a matching (pointer, length)
// pair means the contents are still the ones a cached value was derived from.
// A path that mutates such a slice in place would defeat every guard built on
// this, and must rebuild the slice instead.
//
// The length must be compared alongside the pointer — a reslice shares the
// first element — and the caller must keep the returned pointer reachable for
// as long as it holds the cached value. Both callers here store it in the
// guard itself, which pins the backing array, so a freed array's address
// cannot be recycled into a false match. The body cache drops that reference
// as soon as the retry walk that created it ends (requestBodyReuse.release);
// holding it any longer would keep a whole conversation history alive in a
// provider instance shared by every client. (partDigestRef deliberately does
// the opposite with a weak pointer, because that memo must not pin every
// payload it has ever hashed.)
func sliceID[T any](s []T) *T {
	if len(s) == 0 {
		return nil
	}
	return &s[0]
}

// requestBodyIdentity captures the inputs a provider's marshaled body derives
// from, comparable by value. The message and tool slices are identified by
// sliceID. Most request tuning is deliberately not part of the identity, which
// is sound only while one (messages, tools) slice is converted under a single
// model and tuning: the pool's key-attempt loop reuses one target's slice while
// rotating only the auth key, and modelcompat.NormalizeForTarget builds a fresh
// slice per target. A future path that re-converts the same slice under a
// different model or tuning would reuse the wrong body, so it must rebuild the
// slice (or extend this identity) instead.
//
// sessionKey is the exception: it is tuning that lands inside the body itself
// (the Responses prompt_cache_key / client_metadata pair), and it can change
// between two requests that share a messages slice — the identity a client only
// learns after a swap would otherwise keep serving a body built before it was
// known.
type requestBodyIdentity struct {
	system     string
	maxTokens  int
	sessionKey string
	msgs       *message.Message
	msgsLen    int
	tools      *message.ToolDefinition
	toolsLen   int
}

func requestBodyIdentityFor(systemPrompt string, messages []message.Message, tools []message.ToolDefinition, maxTokens int, sessionKey string) requestBodyIdentity {
	return requestBodyIdentity{
		system:     systemPrompt,
		maxTokens:  maxTokens,
		sessionKey: sessionKey,
		msgs:       sliceID(messages),
		msgsLen:    len(messages),
		tools:      sliceID(tools),
		toolsLen:   len(tools),
	}
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

	// Compression memo for the cached body: compressBodyBytes is
	// deterministic in (body bytes, encoding), so a key-rotation retry burst
	// reuses the compressed bytes instead of recompressing a large body per
	// attempt. compBytes==nil with compValid=true caches a "send uncompressed"
	// outcome (compression failed or did not reduce size).
	compValid bool
	compEnc   string
	compBytes []byte
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
	r.compValid = false
	r.compBytes = nil
	return body, extra, nil
}

// release drops the cached body when it was built from the given messages
// slice, which unpins that slice's backing array (see sliceID).
//
// The match is deliberately scoped to the pinned fields instead of a whole
// requestBodyIdentity. Provider instances are shared across clients — the
// process caches one impl per provider name, so MainAgent, SubAgent and
// auxiliary calls hit the same bodyReuse — and a concurrent request may
// already have replaced this single entry. Clearing unconditionally would
// throw away that request's body in the middle of its key-attempt loop; the
// pointer match is what makes the release safe. It is also all a releaser can
// prove: the other identity fields are rewritten between the client and the
// provider (completeWithNativeTools trims tools and tuning, replay recovery
// rebuilds the request tuning, the Responses cache key is provider-derived),
// so reconstructing a full identity outside the provider would be a guess.
//
// msgs must come from sliceID, i.e. be the same value the guard holds: an
// empty slice is represented as nil so two empty surfaces compare equal.
func (r *requestBodyReuse) release(msgs *message.Message, msgsLen int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.valid || r.guard.msgs != msgs || r.guard.msgsLen != msgsLen {
		return
	}
	r.valid = false
	r.cached = nil
	r.extra = nil
	r.compValid = false
	r.compEnc = ""
	r.compBytes = nil
	// Zero the guard as well: it holds the only strong reference to the
	// messages backing array this cache keeps alive.
	r.guard = requestBodyIdentity{}
}

// BodyCacheReleaser is the optional capability the retry layer uses to drop a
// provider's cached request body once a whole retry walk is over. It stays out
// of Provider on purpose: only the providers that marshal a body cache one,
// and the retry layer must keep working with providers that do not (test
// doubles, and any future implementation that builds bodies per attempt).
//
// Wrappers that delegate Provider calls must forward this method too; an
// embedded Provider interface does not promote a capability it does not
// declare.
type BodyCacheReleaser interface {
	ReleaseBodyCache(msgs *message.Message, msgsLen int)
}

// bodyCachePin names one cache entry a target created, so the retry layer can
// release it when the walk ends. impl is the provider instance that built it;
// msgs/msgsLen identify the entry (see requestBodyReuse.release).
type bodyCachePin struct {
	impl    Provider
	msgs    *message.Message
	msgsLen int
}

// compressBody compresses the cached body for the configured encoding,
// memoizing the result across the target's key-attempt loop. When bodyBytes
// is not the currently cached body (identity changed), the compression runs
// uncached like compressRequestBody.
func (r *requestBodyReuse) compressBody(identity requestBodyIdentity, req *http.Request, bodyBytes []byte, encoding string) (*http.Request, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !(r.valid && r.guard == identity) {
		return compressRequestBody(req, bodyBytes, encoding)
	}
	if r.compValid && r.compEnc == encoding {
		if len(r.compBytes) == 0 {
			return req, bodyBytes
		}
		newReq, err := applyCompressedBody(req, r.compBytes, r.compEnc)
		if err != nil {
			// Keep serving this attempt uncompressed; the cached compressed
			// bytes stay valid for attempts that build their request cleanly.
			return req, bodyBytes
		}
		return newReq, r.compBytes
	}
	compressed, contentEncoding, ok := compressBodyBytes(bodyBytes, encoding)
	r.compValid = true
	r.compEnc = encoding
	if !ok {
		r.compBytes = nil
		return req, bodyBytes
	}
	r.compBytes = compressed
	newReq, err := applyCompressedBody(req, compressed, contentEncoding)
	if err != nil {
		return req, bodyBytes
	}
	return newReq, compressed
}
