package llm

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

// The release is scoped to the messages slice the entry pins: providers are
// shared by every client, so a request finishing must never clear an entry a
// concurrent request built from a different slice.
func TestRequestBodyReuseReleaseIsScopedToPinnedSlice(t *testing.T) {
	var reuse requestBodyReuse
	msgsA := []message.Message{{Role: message.RoleUser, Content: "a"}}
	msgsB := []message.Message{{Role: message.RoleUser, Content: "b"}}
	idB := requestBodyIdentityFor("sys", msgsB, nil, 128, "")
	const bodyB = `{"content":"b"}`

	if _, err := reuse.body(idB, func() ([]byte, error) { return []byte(bodyB), nil }); err != nil {
		t.Fatalf("seed body: %v", err)
	}

	// Releasing A must leave B's entry alone: both the slice and the length
	// are part of the pin.
	reuse.release(sliceID(msgsA), len(msgsA))
	reuse.release(sliceID(msgsB), len(msgsB)+1)
	if !reuse.valid {
		t.Fatal("releasing a different messages slice cleared the cache")
	}
	if got, err := reuse.body(idB, func() ([]byte, error) { return nil, errors.New("must reuse cached body") }); err != nil || string(got) != bodyB {
		t.Fatalf("cached body after unrelated release = %q, %v; want reuse", got, err)
	}

	// The owning release drops the entry, its compression memo and the guard
	// that pins the messages array.
	reuse.compValid, reuse.compEnc, reuse.compBytes = true, config.RequestCompressionGzip, []byte("compressed")
	reuse.release(sliceID(msgsB), len(msgsB))
	if reuse.valid || reuse.compValid {
		t.Fatalf("release left valid=%v compValid=%v, want both cleared", reuse.valid, reuse.compValid)
	}
	if reuse.guard != (requestBodyIdentity{}) {
		t.Fatal("release kept the guard, so the messages array stays pinned")
	}
	if len(reuse.cached) != 0 || len(reuse.compBytes) != 0 {
		t.Fatalf("release left cached=%d compBytes=%d bytes, want both empty", len(reuse.cached), len(reuse.compBytes))
	}

	// Rebuilding after the release produces the identical bytes.
	builds := 0
	got, err := reuse.body(idB, func() ([]byte, error) {
		builds++
		return []byte(bodyB), nil
	})
	if err != nil {
		t.Fatalf("rebuild body: %v", err)
	}
	if builds != 1 {
		t.Fatalf("build ran %d times after release, want 1", builds)
	}
	if !bytes.Equal(got, []byte(bodyB)) {
		t.Fatalf("rebuilt body = %q, want %q", got, bodyB)
	}
	if len(reuse.cached) != len(bodyB) {
		t.Fatalf("cached body = %d bytes after rebuild, want %d", len(reuse.cached), len(bodyB))
	}
}

// A Responses-shaped request is forwarded to the embedded provider, which
// keeps its own entry; releasing only the outer one would leave the large
// forwarded body (and its messages pin) behind.
func TestOpenAIProviderReleaseClearsForwardedResponsesCache(t *testing.T) {
	cfg := NewProviderConfig("sample", config.ProviderConfig{
		Type:   config.ProviderTypeChatCompletions,
		APIURL: "https://example.invalid/v1/chat/completions",
	}, []string{"test-key"})
	impl, err := NewOpenAIProvider(cfg, "")
	if err != nil {
		t.Fatalf("NewOpenAIProvider: %v", err)
	}
	if impl.responsesProvider == nil {
		t.Fatal("OpenAIProvider has no embedded responses provider")
	}

	msgs := []message.Message{{Role: message.RoleUser, Content: "hi"}}
	identity := requestBodyIdentityFor("sys", msgs, nil, 128, "")
	for name, reuse := range map[string]*requestBodyReuse{
		"chat":      &impl.bodyReuse,
		"responses": &impl.responsesProvider.bodyReuse,
	} {
		if _, err := reuse.body(identity, func() ([]byte, error) { return []byte(`{"model":"m"}`), nil }); err != nil {
			t.Fatalf("seed %s body: %v", name, err)
		}
	}

	impl.ReleaseBodyCache(sliceID(msgs), len(msgs))
	if impl.bodyReuse.valid {
		t.Fatal("chat-completions body cache survived the release")
	}
	if impl.responsesProvider.bodyReuse.valid {
		t.Fatal("forwarded responses body cache survived the release")
	}
	if len(impl.bodyReuse.cached) != 0 || len(impl.responsesProvider.bodyReuse.cached) != 0 {
		t.Fatalf("cached bodies survived the release: chat=%d responses=%d",
			len(impl.bodyReuse.cached), len(impl.responsesProvider.bodyReuse.cached))
	}
}

// cachingProvider seeds a real body cache on every attempt and counts the
// releases the retry layer performs, so a test can prove the pin is dropped
// only when the whole retry walk ends.
type cachingProvider struct {
	*recordingProvider
	bodyReuse requestBodyReuse
	releaseMu sync.Mutex
	releases  int
	// releasedPinned counts releases that actually found an entry to drop. It
	// is the positive control for "the cache is empty afterwards": without it a
	// cache that never cached anything would pass every assertion below.
	releasedPinned int
}

func (p *cachingProvider) CompleteStream(
	ctx context.Context,
	apiKey string,
	model string,
	systemPrompt string,
	messages []message.Message,
	tools []message.ToolDefinition,
	maxTokens int,
	tuning RequestTuning,
	cb StreamCallback,
) (*message.Response, error) {
	identity := requestBodyIdentityFor(systemPrompt, messages, tools, maxTokens, tuning.SessionKey)
	if _, err := p.bodyReuse.body(identity, func() ([]byte, error) {
		return []byte(`{"model":"cached"}`), nil
	}); err != nil {
		return nil, err
	}
	return p.recordingProvider.CompleteStream(ctx, apiKey, model, systemPrompt, messages, tools, maxTokens, tuning, cb)
}

func (p *cachingProvider) ReleaseBodyCache(msgs *message.Message, msgsLen int) {
	p.releaseMu.Lock()
	p.releases++
	if p.bodyReuse.valid && p.bodyReuse.guard.msgs == msgs && p.bodyReuse.guard.msgsLen == msgsLen {
		p.releasedPinned++
	}
	p.releaseMu.Unlock()
	p.bodyReuse.release(msgs, msgsLen)
}

func (p *cachingProvider) releaseCount() int {
	p.releaseMu.Lock()
	defer p.releaseMu.Unlock()
	return p.releases
}

func (p *cachingProvider) pinnedReleaseCount() int {
	p.releaseMu.Lock()
	defer p.releaseMu.Unlock()
	return p.releasedPinned
}

// Two targets are tried in turn; each leaves a cached body behind, and both
// entries must be released once the walk is over.
func TestCompleteStreamWithRetryReleasesEveryTargetBodyCache(t *testing.T) {
	primaryCfg := testProviderConfigWithKeys("strict", "strict-model", []string{"key-a"})
	fallbackCfg := testProviderConfigWithKeys("gateway", "fallback-model", []string{"key-b"})
	disableRetryDelayForTest(primaryCfg)
	disableRetryDelayForTest(fallbackCfg)

	primary := &cachingProvider{recordingProvider: &recordingProvider{}}
	primary.calls = []scriptedCall{{err: &APIError{StatusCode: 400, Message: "invalid_request_error: bad shape"}}}
	fallback := &cachingProvider{recordingProvider: &recordingProvider{}}
	fallback.calls = []scriptedCall{{err: &APIError{StatusCode: 400, Message: "invalid_request_error: bad shape"}}}

	client := NewClient(primaryCfg, primary, "strict-model", 512, "sys")
	resp, err := callCompleteStreamWithRetryForTest(
		client,
		context.Background(),
		primaryCfg,
		primary,
		"strict-model",
		512,
		RequestTuning{},
		"",
		[]message.Message{{Role: message.RoleUser, Content: "hi"}},
		nil,
		nil,
		true,
		[]FallbackModel{{
			ProviderConfig: fallbackCfg,
			ProviderImpl:   fallback,
			ModelID:        "fallback-model",
			MaxTokens:      512,
		}},
		1,
		&CallStatus{},
	)
	if err == nil {
		t.Fatalf("expected both targets to fail, got response %#v", resp)
	}
	for name, impl := range map[string]*cachingProvider{"primary": primary, "fallback": fallback} {
		if got := impl.CallCount(); got != 1 {
			t.Fatalf("%s calls = %d, want 1", name, got)
		}
		if impl.bodyReuse.valid {
			t.Fatalf("%s body cache survived the retry walk", name)
		}
		if got := impl.releaseCount(); got != 1 {
			t.Fatalf("%s release count = %d, want 1", name, got)
		}
		// Positive control: the release must have had a pinned entry to drop,
		// otherwise "not valid afterwards" would also hold for a provider that
		// never cached a body at all.
		if got := impl.pinnedReleaseCount(); got != 1 {
			t.Fatalf("%s released %d pinned entries, want 1", name, got)
		}
	}
}
